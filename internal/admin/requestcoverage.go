package admin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const topMissingAreaLimit = 20

type requestCoverageView struct {
	Available       bool
	WindowLabel     string
	TotalImpact     int64
	MappedImpact    int64
	MissingImpact   int64
	SearchAvailable bool
	Searches        int64
	Hits            int64
	Misses          int64
	HitRate         string
	Packages        []coveragePackageView
	Missing         []coverageRowView
}

type coveragePackageView struct {
	Key             string
	Label           string
	Impact          int64
	ShownImpact     int64
	OmittedImpact   int64
	OmittedChildren int64
	State           string
	X               float64
	Width           float64
	OmittedY        float64
	OmittedHeight   float64
	Versions        []coverageVersionView
}

type coverageVersionView struct {
	Version string
	Label   string
	Impact  int64
	X       float64
	Width   float64
	Y       float64
	Height  float64
	Nodes   []coverageRowView
}

type coverageRowView struct {
	Key                string
	PackageKey         string
	PackageLabel       string
	Version            string
	VersionLabel       string
	Symbol             string
	SymbolLabel        string
	Environment        string
	EnvironmentLabel   string
	Impact             int64
	State              string
	StateLabel         string
	Boundary           string
	BoundaryLabel      string
	LastDay            string
	FailureStage       string
	FailureFingerprint string
	FailureLabel       string
	X                  float64
	Y                  float64
	Width              float64
	Height             float64
}

func buildRequestCoverageView(raw serverstore.AdminRequestCoverage, week serverstore.AdminFlowWindow) requestCoverageView {
	view := requestCoverageView{
		Available:   len(raw.Nodes) > 0,
		WindowLabel: raw.WindowStart + " ~ " + raw.WindowEnd + " UTC",
		TotalImpact: raw.TotalImpact,
		Searches:    week.SearchTotal(),
		Hits:        week.Hits,
		Misses:      week.NoMatches,
	}
	if view.Searches > 0 {
		view.SearchAvailable = true
		view.HitRate = formatShare(view.Hits, view.Searches)
	}

	type packageBuild struct {
		view     coveragePackageView
		versions map[string]int
		miss     int64
		partial  int64
		hit      int64
	}
	packages := make([]*packageBuild, 0)
	packageIndex := map[string]int{}
	for _, node := range raw.Nodes {
		row := buildCoverageRow(node)
		if node.InTopMissing {
			view.MissingImpact += row.Impact
			view.Missing = append(view.Missing, row)
		}
		if !node.InMap {
			continue
		}
		pi, ok := packageIndex[row.PackageKey]
		if !ok {
			pi = len(packages)
			packageIndex[row.PackageKey] = pi
			packages = append(packages, &packageBuild{
				view:     coveragePackageView{Key: row.PackageKey, Label: row.PackageLabel, Impact: node.PackageImpact},
				versions: map[string]int{},
			})
			view.MappedImpact += node.PackageImpact
		}
		pkg := packages[pi]
		pkg.view.ShownImpact += row.Impact
		switch row.State {
		case string(serverstore.AdminCoverageHit):
			pkg.hit += row.Impact
		case string(serverstore.AdminCoveragePartial):
			pkg.partial += row.Impact
		default:
			pkg.miss += row.Impact
		}
		vi, ok := pkg.versions[row.Version]
		if !ok {
			vi = len(pkg.view.Versions)
			pkg.versions[row.Version] = vi
			pkg.view.Versions = append(pkg.view.Versions, coverageVersionView{
				Version: row.Version, Label: row.VersionLabel,
			})
		}
		version := &pkg.view.Versions[vi]
		version.Impact += row.Impact
		version.Nodes = append(version.Nodes, row)
	}
	for _, pkg := range packages {
		pkg.view.OmittedImpact = pkg.view.Impact - pkg.view.ShownImpact
		shownChildren := int64(0)
		for _, version := range pkg.view.Versions {
			shownChildren += int64(len(version.Nodes))
		}
		if len(pkg.view.Versions) > 0 {
			// PackageCoordinates is identical on every child. The raw type keeps
			// it there so a single bounded result set carries its own truncation.
			for _, node := range raw.Nodes {
				if node.Ecosystem+"/"+node.Name == pkg.view.Key {
					pkg.view.OmittedChildren = node.PackageCoordinates - shownChildren
					break
				}
			}
		}
		switch {
		case pkg.miss >= pkg.partial && pkg.miss >= pkg.hit:
			pkg.view.State = string(serverstore.AdminCoverageMiss)
		case pkg.partial >= pkg.hit:
			pkg.view.State = string(serverstore.AdminCoveragePartial)
		default:
			pkg.view.State = string(serverstore.AdminCoverageHit)
		}
		view.Packages = append(view.Packages, pkg.view)
	}
	view.Available = len(view.Packages) > 0
	layoutRequestCoverageMap(&view)

	sort.SliceStable(view.Missing, func(i, j int) bool {
		if view.Missing[i].Impact != view.Missing[j].Impact {
			return view.Missing[i].Impact > view.Missing[j].Impact
		}
		return view.Missing[i].Key < view.Missing[j].Key
	})
	if len(view.Missing) > topMissingAreaLimit {
		view.Missing = view.Missing[:topMissingAreaLimit]
	}
	return view
}

// layoutRequestCoverageMap assigns exact percentage rectangles. Packages run
// left-to-right, versions top-to-bottom within a package, and coordinates run
// left-to-right within a version. Consequently every leaf rectangle's area is
// its recent request impact divided by the total mapped package impact.
func layoutRequestCoverageMap(view *requestCoverageView) {
	if view.MappedImpact <= 0 {
		return
	}
	x := 0.0
	for pi := range view.Packages {
		pkg := &view.Packages[pi]
		pkg.X = x
		pkg.Width = 100 * float64(pkg.Impact) / float64(view.MappedImpact)
		y := 0.0
		for vi := range pkg.Versions {
			version := &pkg.Versions[vi]
			version.X = pkg.X
			version.Width = pkg.Width
			version.Y = y
			version.Height = 100 * float64(version.Impact) / float64(pkg.Impact)
			nodeX := pkg.X
			for ni := range version.Nodes {
				node := &version.Nodes[ni]
				node.X = nodeX
				node.Y = version.Y
				node.Width = pkg.Width * float64(node.Impact) / float64(version.Impact)
				node.Height = version.Height
				nodeX += node.Width
			}
			y += version.Height
		}
		pkg.OmittedY = y
		pkg.OmittedHeight = 100 * float64(pkg.OmittedImpact) / float64(pkg.Impact)
		x += pkg.Width
	}
}

func buildCoverageRow(node serverstore.AdminCoverageNode) coverageRowView {
	packageKey := node.Ecosystem + "/" + node.Name
	versionLabel := node.Version
	if versionLabel == "" {
		versionLabel = "모든 버전"
	}
	symbolLabel := node.Symbol
	if symbolLabel == "" {
		symbolLabel = "패키지 수준"
	}
	environmentLabel := node.TargetOS
	if environmentLabel == "" {
		environmentLabel = "환경 지정 없음"
	}
	boundaryLabel := coverageBoundaryLabel(node.Boundary, node.NearestEnvironment)
	failureLabel := "—"
	if node.FailureStage != "" || node.FailureFingerprint != "" {
		failureLabel = strings.TrimSpace(node.FailureStage + " · " + shortFingerprint(node.FailureFingerprint))
		failureLabel = strings.Trim(failureLabel, " ·")
	}
	key := strings.Join([]string{packageKey, node.Version, node.Symbol, node.TargetOS}, "|")
	return coverageRowView{
		Key: key, PackageKey: packageKey, PackageLabel: packageKey,
		Version: node.Version, VersionLabel: versionLabel,
		Symbol: node.Symbol, SymbolLabel: symbolLabel,
		Environment: node.TargetOS, EnvironmentLabel: environmentLabel,
		Impact: node.Impact, State: string(node.State), StateLabel: coverageStateLabel(node.State),
		Boundary: node.Boundary, BoundaryLabel: boundaryLabel, LastDay: node.LastDay,
		FailureStage: node.FailureStage, FailureFingerprint: node.FailureFingerprint,
		FailureLabel: failureLabel,
	}
}

func coverageStateLabel(state serverstore.AdminCoverageState) string {
	switch state {
	case serverstore.AdminCoverageHit:
		return "현재 HIT"
	case serverstore.AdminCoveragePartial:
		return "부분/약한 커버리지"
	default:
		return "미커버 MISS"
	}
}

func coverageBoundaryLabel(boundary, nearestEnvironment string) string {
	switch boundary {
	case "exact_pass":
		return "정확 좌표 CONTRACT PASS"
	case "environment_gap":
		if nearestEnvironment != "" {
			return fmt.Sprintf("다른 환경(%s)에만 PASS", nearestEnvironment)
		}
		return "다른 환경에만 PASS"
	case "fail_only":
		return "정확 경계 FAIL-only"
	case "symbol_gap":
		return "버전 근거 있음 · API 근거 없음"
	case "version_gap":
		return "패키지 근거 있음 · 요청 버전 없음"
	case "evidence_only":
		return "좌표 관측 있음 · CONTRACT PASS 없음"
	case "nearby_coverage":
		return "인접 버전/API 근거만 있음"
	default:
		return "패키지 근거 없음"
	}
}

func shortFingerprint(value string) string {
	const limit = 18
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
