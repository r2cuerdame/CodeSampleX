package sandbox

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

const (
	mavenResolverDir    = ".csx-vendor/maven-resolver"
	mavenResolverPOM    = mavenResolverDir + "/pom.xml"
	mavenResolverConfig = mavenResolverDir + "/settings.xml"
	// Fully qualifying and pinning the one plugin goal is part of the trust
	// boundary. `dependency:...` would consult prefix metadata and could pick a
	// different plugin release later.
	mavenDependencyPlugin = "org.apache.maven.plugins:maven-dependency-plugin:3.9.0"
)

// mavenCoordinate is the deliberately narrow Maven package shape supported by
// Public v1: one ordinary Central JAR identified by group/artifact and an exact,
// immutable release. Classifiers and non-JAR packaging need a richer purl
// model, so accepting them here would make the receipt say less than ran.
type mavenCoordinate struct {
	GroupID    string
	ArtifactID string
	Version    string
}

// prepareMavenResolver turns the manifest's exact Maven purls into a fresh,
// sanitized Maven project. The sample's pom.xml, .mvn directory, settings and
// build plugins never enter the network-enabled stage.
//
// Manifest packages are the lock: every runtime JAR, including companions that
// would usually be transitive, must be listed explicitly. The resolver uses
// excludeTransitive=true, so a dependency POM cannot silently widen or change
// the classpath in a later run.
func prepareMavenResolver(dir string, m domain.SampleManifest) error {
	coords, err := lockedMavenCoordinates(m)
	if err != nil {
		return err
	}
	vendorRoot := filepath.Join(dir, ".csx-vendor")
	root := filepath.Join(dir, filepath.FromSlash(mavenResolverDir))
	// Unlike a normal project resolver, all Maven inputs are regenerated.
	// Clearing the whole private vendor root prevents a locally authored tree
	// from planting a jar or remote marker that later looks resolver-produced.
	if err := os.RemoveAll(vendorRoot); err != nil {
		return fmt.Errorf("sandbox: clear generated Maven resolver: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("sandbox: create generated Maven resolver: %w", err)
	}

	pom, err := renderMavenResolverPOM(coords)
	if err != nil {
		return fmt.Errorf("sandbox: render generated Maven resolver: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(mavenResolverPOM)), pom, 0o644); err != nil {
		return fmt.Errorf("sandbox: write generated Maven resolver: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(mavenResolverConfig)), []byte(mavenSettings), 0o644); err != nil {
		return fmt.Errorf("sandbox: write generated Maven settings: %w", err)
	}
	return nil
}

func lockedMavenCoordinates(m domain.SampleManifest) ([]mavenCoordinate, error) {
	if strings.ToLower(strings.TrimSpace(m.Environment.Ecosystem)) != "maven" {
		return nil, fmt.Errorf("sandbox: Maven resolver requires ecosystem maven")
	}
	if len(m.Packages) == 0 {
		return nil, fmt.Errorf("sandbox: Maven manifest requires at least one exact package purl")
	}
	seen := make(map[string]bool, len(m.Packages))
	coords := make([]mavenCoordinate, 0, len(m.Packages))
	for _, raw := range m.Packages {
		p, err := domain.ParsePURL(raw)
		if err != nil || p.Ecosystem != "maven" || p.String() != raw {
			return nil, fmt.Errorf("sandbox: Maven manifest package %q is not a canonical Maven purl", raw)
		}
		if !domain.ConcreteResolvedVersion(p.Version) || strings.Contains(strings.ToUpper(p.Version), "SNAPSHOT") {
			return nil, fmt.Errorf("sandbox: Maven package %q must pin an immutable exact release", raw)
		}
		group, artifact, ok := strings.Cut(p.Name, "/")
		if !ok || strings.Contains(artifact, "/") || !validMavenGroup(group) || !validMavenPart(artifact) {
			return nil, fmt.Errorf("sandbox: Maven package %q must use groupId/artifactId", raw)
		}
		key := group + "/" + artifact
		if seen[key] {
			return nil, fmt.Errorf("sandbox: Maven manifest repeats %s; one locked version per artifact is required", key)
		}
		seen[key] = true
		coords = append(coords, mavenCoordinate{GroupID: group, ArtifactID: artifact, Version: p.Version})
	}
	sort.Slice(coords, func(i, j int) bool {
		if coords[i].GroupID != coords[j].GroupID {
			return coords[i].GroupID < coords[j].GroupID
		}
		return coords[i].ArtifactID < coords[j].ArtifactID
	})
	return coords, nil
}

func validMavenGroup(group string) bool {
	parts := strings.Split(group, ".")
	for _, part := range parts {
		if !validMavenPart(part) {
			return false
		}
	}
	return true
}

func validMavenPart(part string) bool {
	if part == "" || part == "." || part == ".." {
		return false
	}
	for _, r := range part {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

type resolverProject struct {
	XMLName      xml.Name             `xml:"project"`
	Xmlns        string               `xml:"xmlns,attr"`
	ModelVersion string               `xml:"modelVersion"`
	GroupID      string               `xml:"groupId"`
	ArtifactID   string               `xml:"artifactId"`
	Version      string               `xml:"version"`
	Repositories resolverRepositories `xml:"repositories"`
	PluginRepos  resolverPluginRepos  `xml:"pluginRepositories"`
	Dependencies []resolverDependency `xml:"dependencies>dependency"`
}

type resolverRepositories struct {
	Repository resolverRepository `xml:"repository"`
}

type resolverPluginRepos struct {
	Repository resolverRepository `xml:"pluginRepository"`
}

type resolverRepository struct {
	ID        string         `xml:"id"`
	URL       string         `xml:"url"`
	Releases  checksumPolicy `xml:"releases"`
	Snapshots enabledPolicy  `xml:"snapshots"`
}

type checksumPolicy struct {
	Enabled        bool   `xml:"enabled"`
	ChecksumPolicy string `xml:"checksumPolicy"`
}

type enabledPolicy struct {
	Enabled bool `xml:"enabled"`
}

type resolverDependency struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Type       string `xml:"type"`
}

func renderMavenResolverPOM(coords []mavenCoordinate) ([]byte, error) {
	deps := make([]resolverDependency, 0, len(coords))
	for _, c := range coords {
		deps = append(deps, resolverDependency{GroupID: c.GroupID, ArtifactID: c.ArtifactID, Version: c.Version, Type: "jar"})
	}
	repo := resolverRepository{
		ID: "central", URL: "https://repo.maven.apache.org/maven2",
		Releases:  checksumPolicy{Enabled: true, ChecksumPolicy: "fail"},
		Snapshots: enabledPolicy{Enabled: false},
	}
	project := resolverProject{
		Xmlns: "http://maven.apache.org/POM/4.0.0", ModelVersion: "4.0.0",
		GroupID: "dev.codesamplex.verifier", ArtifactID: "locked-classpath", Version: "1",
		Repositories: resolverRepositories{Repository: repo},
		PluginRepos:  resolverPluginRepos{Repository: repo},
		Dependencies: deps,
	}
	body, err := xml.MarshalIndent(project, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}

// The mirror closes a subtle escape hatch: dependency POMs may declare their
// own repositories even though they cannot execute code. Every such request is
// forced to Maven Central, so private coordinates and arbitrary hosts never see
// package names from a verifier.
const mavenSettings = `<?xml version="1.0" encoding="UTF-8"?>
<settings xmlns="http://maven.apache.org/SETTINGS/1.2.0">
  <interactiveMode>false</interactiveMode>
  <mirrors>
    <mirror>
      <id>csx-central</id>
      <name>CodeSampleX Maven Central only</name>
      <url>https://repo.maven.apache.org/maven2</url>
      <mirrorOf>*</mirrorOf>
    </mirror>
  </mirrors>
</settings>
`
