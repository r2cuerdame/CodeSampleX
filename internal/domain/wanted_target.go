package domain

import "strings"

// wantedTargetNames is the deliberately small public vocabulary that may be
// derived from an explicitly named environment descriptor and uploaded as a
// Wanted coordinate.  It is broader than libraries on purpose: engines, SDKs,
// operating systems and command-line tools all produce compatibility failures
// worth measuring. Arbitrary names are not eligible because they can contain a
// private SDK, executable or product name.
var wantedTargetNames = map[string]string{
	"unity":       "engine/unity",
	"unreal":      "engine/unreal",
	"godot":       "engine/godot",
	"android-sdk": "sdk/android",
	"ios-sdk":     "sdk/ios",
	"macos-sdk":   "sdk/macos",
	"windows-sdk": "sdk/windows",
	"jdk":         "sdk/jdk",
	"dotnet-sdk":  "sdk/dotnet",
	"xcode":       "sdk/xcode",
	"cuda":        "sdk/cuda",
	"vulkan":      "sdk/vulkan",

	// Operating-system families are targets only when the caller explicitly
	// names one. Ordinary library searches still carry the OS in the private,
	// coarse environment fingerprint and do not automatically file an OS Want.
	"windows": "os/windows",
	"ubuntu":  "os/ubuntu",
	"debian":  "os/debian",
	"rhel":    "os/rhel",
	"alpine":  "os/alpine",
	"macos":   "os/macos",

	// Built-in and widely used CLI tools. These are generic public targets,
	// not registry-library ecosystems. Exact versions remain mandatory so a
	// future system-cli verifier can compare real implementations rather than
	// the ambiguous word "shell".
	"bash":               "cli/bash",
	"busybox":            "cli/busybox",
	"coreutils":          "cli/coreutils",
	"powershell":         "cli/powershell",
	"windows-powershell": "cli/windows-powershell",
	"cmd":                "cli/cmd",
	"git":                "cli/git",
	"npm":                "cli/npm",
	"pnpm":               "cli/pnpm",
	"yarn":               "cli/yarn",
	"bun":                "cli/bun",
	"deno":               "cli/deno",
	"maven":              "cli/maven",
	"gradle":             "cli/gradle",
	"pip":                "cli/pip",
	"uv":                 "cli/uv",
	"cargo":              "cli/cargo",
	"gem":                "cli/gem",
	"bundler":            "cli/bundler",
	"composer":           "cli/composer",
	"mix":                "cli/mix",
	"dart":               "cli/dart",
	"curl":               "cli/curl",
	"jq":                 "cli/jq",
	"openssl":            "cli/openssl",
	"tar":                "cli/tar",
	"grep":               "cli/grep",
	"sed":                "cli/sed",
	"findutils":          "cli/findutils",
	"docker":             "cli/docker",
	"docker-compose":     "cli/docker-compose",
	"kubectl":            "cli/kubectl",
	"helm":               "cli/helm",
	"terraform":          "cli/terraform",
	"opentofu":           "cli/opentofu",
	"ffmpeg":             "cli/ffmpeg",
	"ripgrep":            "cli/ripgrep",
	"gh":                 "cli/gh",
}

var wantedTargetCoordinates = func() map[string]bool {
	out := make(map[string]bool, len(wantedTargetNames))
	for _, name := range wantedTargetNames {
		out[name] = true
	}
	return out
}()

// PublicTargetFromDescriptor converts a public, versioned target descriptor
// such as "unity@6000.0.24f1", "jdk@21" or "git@2.51.0" into a generic purl.
// generic namespace avoids pretending engines, SDKs, OSes and CLI tools are
// library-registry ecosystems while still fitting the privacy-reduced Wanted
// wire.
func PublicTargetFromDescriptor(value string) (PURL, bool) {
	value = strings.TrimSpace(value)
	i := strings.LastIndexByte(value, '@')
	if i <= 0 || i == len(value)-1 {
		return PURL{}, false
	}
	name := strings.ToLower(strings.TrimSpace(value[:i]))
	version := strings.TrimSpace(value[i+1:])
	coordinate, ok := wantedTargetNames[name]
	if !ok || !ConcreteResolvedVersion(version) {
		return PURL{}, false
	}
	return PURL{Ecosystem: "generic", Name: coordinate, Version: version}, true
}

// WantedTargetFromFramework keeps the original call surface used by clients
// that derive engine/SDK targets from Environment.Frameworks. New callers that
// name a CLI or OS explicitly should use PublicTargetFromDescriptor.
func WantedTargetFromFramework(value string) (PURL, bool) {
	return PublicTargetFromDescriptor(value)
}

// IsWantedTarget reports whether p is one of the fixed public engine/SDK
// coordinates. It is the publicness boundary for targets that do not live on
// a package registry; a syntactically valid arbitrary generic purl is false.
func IsWantedTarget(p PURL) bool {
	return p.Ecosystem == "generic" && wantedTargetCoordinates[p.Name] &&
		ConcreteResolvedVersion(p.Version)
}
