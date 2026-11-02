//go:build windows

package winres

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The object has to survive the Go linker and come back out of the Win32
// version API, and the manifest has to be one the loader accepts: a malformed
// manifest fails process creation with a side-by-side error before main runs,
// which would break every install. So link the real object into a throwaway
// program, read it the way Explorer does, and run it.
func TestLinkedResourceReadsBackAndTheProgramStarts(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	for _, pkg := range []string{"cmd/csx", "cmd/csx-launcher"} {
		info := Binaries("v0.1.105")[pkg]
		t.Run(info.FileDescription, func(t *testing.T) {
			dir := t.TempDir()
			obj, err := Syso(runtime.GOARCH, info)
			if err != nil {
				t.Fatal(err)
			}
			write := func(name, body string) {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("go.mod", "module probe\n\ngo 1.21\n")
			write("main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Print(\"started\") }\n")
			write(SysoName(runtime.GOARCH), string(obj))

			exe := filepath.Join(dir, "probe.exe")
			build := exec.Command(goBin, "build", "-trimpath", "-o", exe, ".")
			build.Dir = dir
			build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=", "GOOS=windows", "GOARCH="+runtime.GOARCH)
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("link with resource object: %v\n%s", err, out)
			}

			out, err := exec.Command(exe).CombinedOutput()
			if err != nil || string(out) != "started" {
				t.Fatalf("program with the manifest did not start: %v %q", err, out)
			}

			got := readVersionStrings(t, exe)
			for k, want := range map[string]string{
				"CompanyName":      "r2cuerdame",
				"ProductName":      "CodeSampleX",
				"FileDescription":  info.FileDescription,
				"OriginalFilename": info.OriginalFilename,
				"InternalName":     info.InternalName,
				"FileVersion":      "0.1.105",
				"ProductVersion":   "0.1.105",
				"LegalCopyright":   info.LegalCopyright,
			} {
				if got[k] != want {
					t.Errorf("%s = %q, want %q", k, got[k], want)
				}
			}
			if got["fixed"] != "0.1.105.0" {
				t.Errorf("fixed file version = %q, want 0.1.105.0", got["fixed"])
			}
		})
	}
}

func readVersionStrings(t *testing.T, path string) map[string]string {
	t.Helper()
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		t.Fatalf("GetFileVersionInfoSize: %v (no version resource linked)", err)
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		t.Fatalf("GetFileVersionInfo: %v", err)
	}
	out := map[string]string{}
	var fixed *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &n); err != nil {
		t.Fatalf("VerQueryValue root: %v", err)
	}
	if fixed.Signature != 0xfeef04bd {
		t.Fatalf("fixed info signature = %#x", fixed.Signature)
	}
	out["fixed"] = strings.Join([]string{
		itoa(fixed.FileVersionMS >> 16), itoa(fixed.FileVersionMS & 0xffff),
		itoa(fixed.FileVersionLS >> 16), itoa(fixed.FileVersionLS & 0xffff),
	}, ".")
	for _, k := range []string{"CompanyName", "ProductName", "FileDescription", "OriginalFilename", "InternalName", "FileVersion", "ProductVersion", "LegalCopyright"} {
		var p *uint16
		if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\StringFileInfo\040904b0\`+k, unsafe.Pointer(&p), &n); err != nil {
			continue
		}
		out[k] = windows.UTF16PtrToString(p)
	}
	return out
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
