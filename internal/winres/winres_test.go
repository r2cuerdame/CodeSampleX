package winres

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestParseVersion(t *testing.T) {
	for _, c := range []struct {
		in   string
		num  [4]uint16
		text string
	}{
		{"v0.1.105", [4]uint16{0, 1, 105, 0}, "0.1.105"},
		{"0.1.105", [4]uint16{0, 1, 105, 0}, "0.1.105"},
		{"v1.2.3.4", [4]uint16{1, 2, 3, 4}, "1.2.3.4"},
		{"v0.1.105-3-gabc1234", [4]uint16{0, 1, 105, 0}, "0.1.105-3-gabc1234"},
		{"dev", [4]uint16{}, "dev"},
		{"abc1234", [4]uint16{}, "abc1234"},
		{"v1.70000.2", [4]uint16{1, 0, 0, 0}, "1.70000.2"},
	} {
		num, text := ParseVersion(c.in)
		if num != c.num || text != c.text {
			t.Errorf("ParseVersion(%q) = %v %q, want %v %q", c.in, num, text, c.num, c.text)
		}
	}
}

// The installed names are what a user, Explorer and a scanner see. A
// mismatch between OriginalFilename and the file on disk is itself a signal.
func TestBinariesNameTheFileTheyAreInstalledAs(t *testing.T) {
	bins := Binaries("v0.1.0")
	for pkg, want := range map[string]string{"cmd/csx": "csx-payload.exe", "cmd/csx-launcher": "csx.exe"} {
		info, ok := bins[pkg]
		if !ok {
			t.Fatalf("no identity for %s", pkg)
		}
		if info.OriginalFilename != want {
			t.Errorf("%s OriginalFilename = %q, want %q", pkg, info.OriginalFilename, want)
		}
		if info.CompanyName != "r2cuerdame" || info.ProductName != "CodeSampleX" {
			t.Errorf("%s company/product = %q/%q", pkg, info.CompanyName, info.ProductName)
		}
		if err := info.validate(); err != nil {
			t.Error(err)
		}
	}
}

func TestManifestIsWellFormedAndAsksForNothing(t *testing.T) {
	for pkg, info := range Binaries("v0.1.105") {
		m := Manifest(info)
		dec := xml.NewDecoder(strings.NewReader(m))
		for {
			_, err := dec.Token()
			if err != nil {
				if err != io.EOF {
					t.Fatalf("%s manifest is not well-formed XML: %v", pkg, err)
				}
				break
			}
		}
		for _, want := range []string{`level="asInvoker"`, `uiAccess="false"`, `version="0.1.105.0"`, `{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}`} {
			if !strings.Contains(m, want) {
				t.Errorf("%s manifest lacks %s", pkg, want)
			}
		}
		for _, forbidden := range []string{"requireAdministrator", "highestAvailable", "autoElevate"} {
			if strings.Contains(m, forbidden) {
				t.Errorf("%s manifest asks for elevation: %s", pkg, forbidden)
			}
		}
	}
}

// Only the launcher declines the console Windows would otherwise allocate.
// The launcher is what an MCP host spawns; the payload is what runs builds,
// and a payload with no console would give every console program it starts
// a brand-new console of its own -- a window per `go build`.
func TestOnlyTheLauncherManifestDetachesFromAConsolelessParent(t *testing.T) {
	for pkg, info := range Binaries("v0.1.105") {
		got := consoleAllocationPolicy(t, Manifest(info))
		want := ""
		if pkg == "cmd/csx-launcher" {
			want = "detached"
		}
		if got != want {
			t.Errorf("%s consoleAllocationPolicy = %q, want %q", pkg, got, want)
		}
	}
}

// consoleAllocationPolicy reads the element where the loader looks for it:
// assembly > application > windowsSettings, in the asm.v3 namespace, with the
// element itself in the 2024 WindowsSettings namespace. Anywhere else, or in
// any other namespace, Windows ignores it and the window comes back.
func consoleAllocationPolicy(t *testing.T, manifest string) string {
	t.Helper()
	const (
		asmV3    = "urn:schemas-microsoft-com:asm.v3"
		settings = "http://schemas.microsoft.com/SMI/2024/WindowsSettings"
	)
	dec := xml.NewDecoder(strings.NewReader(manifest))
	var path []xml.Name
	found := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return found
		}
		if err != nil {
			t.Fatalf("manifest is not well-formed XML: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			path = append(path, tok.Name)
			if tok.Name.Local != "consoleAllocationPolicy" {
				continue
			}
			if len(path) != 4 || path[1] != (xml.Name{Space: asmV3, Local: "application"}) ||
				path[2] != (xml.Name{Space: asmV3, Local: "windowsSettings"}) || tok.Name.Space != settings {
				t.Fatalf("consoleAllocationPolicy at %v, where the loader never reads it", path)
			}
			var v string
			if err := dec.DecodeElement(&v, &tok); err != nil {
				t.Fatal(err)
			}
			found = strings.TrimSpace(v)
			path = path[:len(path)-1]
		case xml.EndElement:
			path = path[:len(path)-1]
		}
	}
}

func TestSysoIsADeterministicCOFFObjectPerArch(t *testing.T) {
	info := Binaries("v0.1.105")["cmd/csx-launcher"]
	for arch, machine := range map[string]uint16{"amd64": 0x8664, "arm64": 0xaa64} {
		a, err := Syso(arch, info)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := Syso(arch, info)
		if !bytes.Equal(a, b) {
			t.Fatalf("%s: two runs produced different objects", arch)
		}
		if got := binary.LittleEndian.Uint16(a); got != machine {
			t.Errorf("%s: machine = %#x, want %#x", arch, got, machine)
		}
		if string(a[20:25]) != ".rsrc" {
			t.Errorf("%s: first section is %q, want .rsrc", arch, a[20:28])
		}
		if binary.LittleEndian.Uint16(a[20+32:]) != 2 {
			t.Errorf("%s: want two relocations, one per resource", arch)
		}
		for _, s := range []string{"CodeSampleX Launcher", "csx.exe", "0.1.105", "r2cuerdame"} {
			if !bytes.Contains(a, utf16z(s)[:2*len(utf16.Encode([]rune(s)))]) {
				t.Errorf("%s: object does not carry %q", arch, s)
			}
		}
	}
	if _, err := Syso("mips", info); err == nil {
		t.Error("an unsupported arch produced an object")
	}
	if _, err := Syso("amd64", Info{}); err == nil {
		t.Error("an empty identity produced an object")
	}
}

// VS_VERSIONINFO is a tree whose every wLength must land on the next sibling.
// Walk it the way VerQueryValue does and require the strings to come back.
func TestVersionInfoWalksLikeVerQueryValue(t *testing.T) {
	vi, err := VersionInfo(Binaries("v0.1.105")["cmd/csx"])
	if err != nil {
		t.Fatal(err)
	}
	if int(binary.LittleEndian.Uint16(vi)) != len(vi) {
		t.Fatalf("root wLength = %d, buffer is %d", binary.LittleEndian.Uint16(vi), len(vi))
	}
	got := map[string]string{}
	var walk func(b []byte, path string)
	walk = func(b []byte, path string) {
		for len(b) >= 6 {
			n := int(binary.LittleEndian.Uint16(b))
			if n < 6 || n > len(b) {
				t.Fatalf("%s: bad wLength %d of %d", path, n, len(b))
			}
			vlen := int(binary.LittleEndian.Uint16(b[2:]))
			typ := binary.LittleEndian.Uint16(b[4:])
			key, off := readUTF16(b, 6)
			off = align(off, 4)
			if typ == 1 && vlen > 0 {
				val, end := readUTF16(b, off)
				got[key] = val
				off = end
			} else {
				off += vlen
			}
			walk(b[min(align(off, 4), n):n], path+"/"+key)
			b = b[min(align(n, 4), len(b)):]
		}
	}
	walk(vi, "")
	for k, v := range map[string]string{
		"CompanyName": "r2cuerdame", "ProductName": "CodeSampleX", "FileDescription": "CodeSampleX CLI",
		"OriginalFilename": "csx-payload.exe", "FileVersion": "0.1.105", "ProductVersion": "0.1.105",
	} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func readUTF16(b []byte, off int) (string, int) {
	var u []uint16
	for off+1 < len(b) {
		c := binary.LittleEndian.Uint16(b[off:])
		off += 2
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u)), off
}
