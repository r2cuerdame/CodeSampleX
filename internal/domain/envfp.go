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
	Compiler              string   `json:"compiler,omitempty"`
	CompilerVersion       string   `json:"compilerVersion,omitempty"`
	PackageManager        string   `json:"packageManager,omitempty"`
	PackageManagerVersion string   `json:"packageManagerVersion,omitempty"`
	ModuleSystem          string   `json:"moduleSystem,omitempty"`
	Frameworks            []string `json:"frameworks,omitempty"`

	// ExecutionContext is an independent compatibility axis: where the
	// observed stage actually executed. Open vocabulary — well-known values
	// are "node", "browser", "webview", "electron", "webworker",
	// "serviceworker", "bun", "deno"; new runtimes extend it without a
	// schema change. Build/test observations record the toolchain context
	// (a browser-targeting build still executes in node); browser-context
	// evidence only comes from stages that truly ran there.
	ExecutionContext string `json:"executionContext,omitempty"`
	// Browser dimensions, set only when ExecutionContext is a browser-like
	// runtime. Normalized locally — the raw User-Agent never leaves the
	// machine (fingerprinting is not the goal; API compatibility is).
	BrowserFamily string `json:"browserFamily,omitempty"` // chrome|edge|firefox|safari|chromium|android-webview|ios-wkwebview|electron
	BrowserMajor  string `json:"browserMajor,omitempty"`  // "140" — already a bucket
	Engine        string `json:"engine,omitempty"`        // chromium|gecko|webkit
	EngineVersion string `json:"engineVersion,omitempty"`
}

// ContextLabel renders the execution-context axis for display and
// aggregation row keys: "chrome 140", "node 22.18", "safari 19".
func (e EnvironmentFingerprint) ContextLabel() string {
	if e.BrowserFamily != "" {
		if e.BrowserMajor != "" {
			return e.BrowserFamily + " " + e.BrowserMajor
		}
		return e.BrowserFamily
	}
	ctx := e.ExecutionContext
	if ctx == "" {
		ctx = e.Runtime
	}
	if ctx == "" {
		return ""
	}
	if e.RuntimeVersion != "" {
		return ctx + " " + e.RuntimeVersion
	}
	return ctx
}

// Normalize fills derivable fields: a bare runtime implies its execution
// context, and browser fields imply engine family where unambiguous.
func (e EnvironmentFingerprint) Normalize() EnvironmentFingerprint {
	if e.ExecutionContext == "" {
		switch e.Runtime {
		case "node", "bun", "deno":
			e.ExecutionContext = e.Runtime
		}
	}
	if e.Engine == "" {
		switch e.BrowserFamily {
		case "chrome", "edge", "chromium", "android-webview", "electron":
			e.Engine = "chromium"
		case "firefox":
			e.Engine = "gecko"
		case "safari", "ios-wkwebview":
			e.Engine = "webkit"
		}
	}
	return e
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
	e.CompilerVersion = versionBucket(e.CompilerVersion)
	e.PackageManagerVersion = majorBucket(e.PackageManagerVersion)
	e.BrowserMajor = majorBucket(e.BrowserMajor)
	e.EngineVersion = majorBucket(e.EngineVersion)
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
