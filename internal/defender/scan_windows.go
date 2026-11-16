//go:build windows

package defender

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	fallbackVendor = `Windows Defender`
	scannerName    = "MpCmdRun.exe"
	definitionsKey = `SOFTWARE\Microsoft\Windows Defender\Signature Updates`
)

var (
	noThreatsLine = regexp.MustCompile(`(?i)found no threats`)
	threatsLine   = regexp.MustCompile(`(?i)found\s+\d+\s+threats?`)
	threatName    = regexp.MustCompile(`(?im)^\s*Threat\s*:\s*(\S.*?)\s*$`)
	detectedList  = regexp.MustCompile(`(?i)LIST OF DETECTED THREATS`)
)

func Scan(ctx context.Context, paths ...string) ([]Verdict, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	scanner, err := locateScanner()
	if err != nil {
		return nil, err
	}
	definitions := definitionVersion()
	out := make([]Verdict, 0, len(paths))
	for _, p := range paths {
		v, err := scanOne(ctx, scanner, p, definitions)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func scanOne(ctx context.Context, scanner, path, definitions string) (Verdict, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Verdict{}, fmt.Errorf("defender: resolve %s: %w", path, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Verdict{}, fmt.Errorf("defender: scan target %s: %w", abs, err)
	}
	if !fi.Mode().IsRegular() {
		return Verdict{}, fmt.Errorf("defender: scan target %s is not a regular file", abs)
	}

	cmd := exec.CommandContext(ctx, scanner, "-Scan", "-ScanType", "3", "-File", abs, "-DisableRemediation")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	cmd.Env = ensureWindowsEnv(os.Environ())
	raw, runErr := cmd.CombinedOutput()
	text := string(raw)

	v := Verdict{Path: abs, Definitions: definitions}
	switch {
	case noThreatsLine.MatchString(text):
		return v, nil
	case threatsLine.MatchString(text) || detectedList.MatchString(text):
		v.Flagged = true
		v.Threats = parseThreats(text)
		return v, nil
	}
	if runErr == nil {
		return v, nil
	}
	return Verdict{}, fmt.Errorf("defender: scan %s did not produce a verdict (%v): %s", abs, runErr, tail(text))
}

func parseThreats(text string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range threatName.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return []string{"unnamed-detection"}
	}
	return names
}

func tail(text string) string {
	text = strings.TrimSpace(text)
	const limit = 400
	if len(text) <= limit {
		return text
	}
	return "..." + text[len(text)-limit:]
}

func locateScanner() (string, error) {
	if pinned := strings.TrimSpace(os.Getenv("CSX_DEFENDER_MPCMDRUN")); pinned != "" {
		if fi, err := os.Stat(pinned); err == nil && fi.Mode().IsRegular() {
			return pinned, nil
		}
		return "", fmt.Errorf("%w: CSX_DEFENDER_MPCMDRUN=%s is not a file", ErrUnavailable, pinned)
	}
	if p, ok := newestPlatformScanner(); ok {
		return p, nil
	}
	bases := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432")}
	if os.Getenv("ProgramFiles") == "" && os.Getenv("ProgramW6432") == "" {
		bases = append(bases, `C:\Program Files`)
	}
	for _, base := range bases {
		if base == "" {
			continue
		}
		p := filepath.Join(base, fallbackVendor, scannerName)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", ErrUnavailable
}

func newestPlatformScanner() (string, bool) {
	programData := strings.TrimSpace(os.Getenv("ProgramData"))
	if programData == "" {
		programData = `C:\ProgramData`
	}
	platformRoot := filepath.Join(programData, "Microsoft", "Windows Defender", "Platform")
	entries, err := os.ReadDir(platformRoot)
	if err != nil {
		return "", false
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	for _, v := range versions {
		p := filepath.Join(platformRoot, v, scannerName)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

func definitionVersion() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, definitionsKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("AVSignatureVersion")
	if err != nil {
		return ""
	}
	return v
}

func ensureWindowsEnv(env []string) []string {
	hasDrive := false
	hasData := false
	hasFiles := false
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if strings.EqualFold(k, "SystemDrive") {
			hasDrive = true
		}
		if strings.EqualFold(k, "ProgramData") {
			hasData = true
		}
		if strings.EqualFold(k, "ProgramFiles") {
			hasFiles = true
		}
	}
	out := append([]string(nil), env...)
	if !hasDrive {
		out = append(out, "SystemDrive=C:")
	}
	if !hasData {
		out = append(out, `ProgramData=C:\ProgramData`)
	}
	if !hasFiles {
		out = append(out, `ProgramFiles=C:\Program Files`)
	}
	return out
}
