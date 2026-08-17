package domain

import "strings"

// wantedTargetNames is the deliberately small public vocabulary that may be
// derived from Environment.Frameworks and uploaded as a Wanted coordinate.
// Arbitrary framework names are not eligible: they can contain a private SDK
// or product name, while these names identify public toolchains only.
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
}

var wantedTargetCoordinates = func() map[string]bool {
	out := make(map[string]bool, len(wantedTargetNames))
	for _, name := range wantedTargetNames {
		out[name] = true
	}
	return out
}()

// WantedTargetFromFramework converts a public, versioned framework/toolchain
// descriptor such as "unity@6000.0.24f1" or "jdk@21" into a generic purl.
// The generic namespace avoids pretending engines and SDKs are registry
// ecosystems while still fitting the existing privacy-reduced Wanted wire.
func WantedTargetFromFramework(value string) (PURL, bool) {
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

// IsWantedTarget reports whether p is one of the fixed public engine/SDK
// coordinates. It is the publicness boundary for targets that do not live on
// a package registry; a syntactically valid arbitrary generic purl is false.
func IsWantedTarget(p PURL) bool {
	return p.Ecosystem == "generic" && wantedTargetCoordinates[p.Name] &&
		ConcreteResolvedVersion(p.Version)
}
