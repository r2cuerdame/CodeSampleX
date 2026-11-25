package domain

type BuildDiagnosticSeverity string

const (
	BuildDiagnosticWarning BuildDiagnosticSeverity = "warning"
	BuildDiagnosticInfo    BuildDiagnosticSeverity = "info"
)

type BuildDiagnosticKind string

const (
	BuildDiagnosticDeprecated BuildDiagnosticKind = "deprecated"
	BuildDiagnosticObsolete   BuildDiagnosticKind = "obsolete"
	BuildDiagnosticUnsafe     BuildDiagnosticKind = "unsafe"
	BuildDiagnosticOther      BuildDiagnosticKind = "other"
)

const MaxBuildDiagnostics = 64

type BuildDiagnostic struct {
	Tool         string                  `json:"tool"`
	ToolVersion  string                  `json:"toolVersion,omitempty"`
	Code         string                  `json:"code,omitempty"`
	Severity     BuildDiagnosticSeverity `json:"severity"`
	Kind         BuildDiagnosticKind     `json:"kind"`
	Stage        string                  `json:"stage,omitempty"`
	Message      string                  `json:"message"`
	File         string                  `json:"file,omitempty"`
	Line         int                     `json:"line,omitempty"`
	RawLogDigest string                  `json:"rawLogDigest"`
}
