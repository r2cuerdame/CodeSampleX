package domain

import "strings"

// EnvironmentFingerprint is a schema-versioned sparse description of the
// environment an observation or verification happened in (goal.md §7.3).
// Dimensions meaningless for a language are simply omitted.
type EnvironmentFingerprint struct {
	SchemaVersion         int      `json:"schemaVersion"`
	Ecosystem             string   `json:"ecosystem"`
	OS                    string   `json:"os"`
	OSVersionBucket       string   `json:"osVersionBucket,omitempty"`
	Arch                  string   `json:"arch"`
	Runtime               string   `json:"runtime,omitempty"`
	RuntimeVersion        string   `json:"runtimeVersion,omitempty"`
	Language              string   `json:"language,omitempty"`
	LanguageVersion       string   `json:"languageVersion,omitempty"`
	PackageManager        string   `json:"packageManager,omitempty"`
	PackageManagerVersion string   `json:"packageManagerVersion,omitempty"`
	ModuleSystem          string   `json:"moduleSystem,omitempty"`
	Frameworks            []string `json:"frameworks,omitempty"`
}

// Hash returns the stable content id of the fingerprint.
func (e EnvironmentFingerprint) Hash() string {
	return SHA256Hex(MustCanonicalJSON(e))
}

// Bucketed returns a copy with version fields reduced to major.minor
// buckets, the privacy-lowering form used in web aggregates (goal.md §7.3).
func (e EnvironmentFingerprint) Bucketed() EnvironmentFingerprint {
	e.RuntimeVersion = versionBucket(e.RuntimeVersion)
	e.LanguageVersion = versionBucket(e.LanguageVersion)
	e.PackageManagerVersion = majorBucket(e.PackageManagerVersion)
	return e
}

// versionBucket trims "22.18.1" → "22.18"; keeps shorter forms as-is.
func versionBucket(v string) string {
	if v == "" {
		return ""
	}
	segs := strings.SplitN(v, ".", 3)
	if len(segs) >= 2 {
		return segs[0] + "." + segs[1]
	}
	return segs[0]
}

// majorBucket trims "10.4.1" → "10".
func majorBucket(v string) string {
	if v == "" {
		return ""
	}
	return strings.SplitN(v, ".", 2)[0]
}
