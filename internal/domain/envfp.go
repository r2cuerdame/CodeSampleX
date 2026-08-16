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

	// Virtualization says where the toolchain ran relative to real
	// hardware: "container", "vm", "wsl", or empty for bare metal /
	// undetectable. A build inside a container is a different
	// compatibility population than the host that started it, and
	// recording both as plain "linux" makes the graph lie.
	Virtualization string `json:"virtualization,omitempty"`
	// ContainerRuntime names the engine when one is detectable:
	// docker | podman | containerd | kubernetes | lxc.
	ContainerRuntime string `json:"containerRuntime,omitempty"`
	// Libc separates musl (alpine) from glibc. This single dimension
	// explains a large share of "works on my machine" for anything with a
	// native module: prebuilt binaries routinely load on one and fail on
	// the other while every other dimension looks identical.
	Libc string `json:"libc,omitempty"`
	// LibcVersion is the glibc version, when the libc is glibc: "2.35".
	//
	// musl versus glibc is the first thing that stops a native module
	// loading on Linux, and the glibc VERSION is the second. Prebuilt
	// wheels and node binaries target a floor — manylinux2014 is glibc
	// 2.17, manylinux_2_28 is 2.28 — and a binary built against 2.35 fails
	// on 2.31 with "GLIBC_2.34 not found". Reporting only the family made a
	// sample verified on Ubuntu 24.04 an EXACT match for a caller on
	// CentOS 7, which is precisely the case this project exists to catch.
	//
	// It is a property of the platform, like the OS itself, and identifies
	// nobody. Empty on musl and on non-Linux.
	LibcVersion string `json:"libcVersion,omitempty"`
	// Distro is the /etc/os-release ID: "ubuntu", "debian", "alpine".
	//
	// osVersionBucket carries only the version NUMBER, so Ubuntu 22 and
	// any other distribution's 22 were the same bucket. The number means
	// nothing without the name beside it.
	Distro string `json:"distro,omitempty"`
	// CI marks an automated runner. CI fleets are clones of each other, so
	// this is recorded to let aggregation discount them rather than treat
	// them as many independent developer environments (goal.md §16.5).
	CI bool `json:"ci,omitempty"`
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

// placeholderValues are the ways a caller says "I could not determine this".
// They must become absence, because the search grader compares a dimension
// only when both sides declare it — absence is unknown, not a difference.
// Compared as a literal string instead, "unknown" made a caller that had
// simply not detected its package manager look like a caller using a
// DIFFERENT one, which downgraded every otherwise-exact match and produced
// the advice "use unknown equivalents of lockfile commands".
//
// "none" is deliberately not here. For Virtualization, ContainerRuntime and
// Libc it is a real answer — bare metal, no container, statically linked —
// and erasing it would throw away a fact rather than a non-answer.
var placeholderValues = map[string]bool{
	"unknown": true, "unspecified": true, "undefined": true,
	"n/a": true, "na": true, "null": true, "nil": true,
	"?": true, "-": true, "": true,
}

func clearPlaceholder(v string) string {
	if placeholderValues[strings.ToLower(strings.TrimSpace(v))] {
		return ""
	}
	return v
}

// Normalize fills derivable fields: a bare runtime implies its execution
// context, and browser fields imply engine family where unambiguous. It
// also erases placeholder values, so an undetected dimension is absent
// rather than present-and-different.
func (e EnvironmentFingerprint) Normalize() EnvironmentFingerprint {
	for _, f := range []*string{
		&e.OS, &e.OSVersionBucket, &e.Arch,
		&e.Runtime, &e.RuntimeVersion,
		&e.Language, &e.LanguageVersion,
		&e.Compiler, &e.CompilerVersion,
		&e.ModuleSystem, &e.PackageManager, &e.PackageManagerVersion,
		&e.ExecutionContext, &e.BrowserFamily, &e.BrowserMajor,
		&e.Engine, &e.EngineVersion,
		&e.Virtualization, &e.ContainerRuntime, &e.Libc, &e.LibcVersion, &e.Distro,
	} {
		*f = clearPlaceholder(*f)
	}
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
