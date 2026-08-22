package registry

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// MavenHasJar reports whether a maven coordinate publishes a compiled
// artifact, and whether the answer is known at all.
//
// A BOM or a parent POM publishes only a pom: it declares versions for other
// modules and contains no classes, so there is no symbol a contract could
// call and no sample anyone could write. Measured on Maven Central,
// guava-parent, kotlin-gradle-plugins-bom and junit-bom all answer 404 for
// their jar and 200 for their pom.
//
// The name cannot decide this. "-bom" and "-parent" are conventions authors
// choose, not a generated marker like Gradle's ".gradle.plugin", and a false
// exclusion loses real authoring work permanently and silently. So it asks.
//
// The second return is the honesty: a network failure, an unparseable
// coordinate or a registry that answers neither way is "not known", and the
// caller must treat that as "carry on" rather than as "no jar".
func (c *Checker) MavenHasJar(ctx context.Context, p domain.PURL) (hasJar, known bool) {
	if !strings.EqualFold(p.Ecosystem, "maven") ||
		!ValidPackageName("maven", p.Name) || !domain.ConcreteResolvedVersion(p.Version) {
		return false, false
	}
	u := c.mavenJarURL(p)
	if u == "" {
		return false, false
	}
	switch status := c.requestStatus(ctx, http.MethodHead, u); {
	case status == http.StatusOK:
		return true, true
	case status == http.StatusNotFound || status == http.StatusGone:
		return false, true
	default:
		// 403, 5xx, a timeout, anything else: we did not learn the answer.
		return false, false
	}
}

func (c *Checker) mavenJarURL(p domain.PURL) string {
	base, _ := c.baseURL("maven")
	group, artifact, ok := strings.Cut(p.Name, "/")
	if !ok || group == "" || artifact == "" || strings.Contains(artifact, "/") {
		return ""
	}
	groupPath := strings.ReplaceAll(group, ".", "/")
	return base + "/" + groupPath + "/" + url.PathEscape(artifact) + "/" +
		url.PathEscape(p.Version) + "/" + url.PathEscape(artifact+"-"+p.Version+".jar")
}
