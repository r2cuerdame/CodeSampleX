// Package winres writes the Windows resource object (.syso) that gives a
// release binary a VERSIONINFO block and an application manifest.
//
// An unsigned Go executable with no version resource, no manifest and a
// stripped symbol table carries almost no identity: Explorer shows a blank
// publisher and product, and a reputation model scoring the file has nothing
// to anchor it to a known application. Giving it one is free and changes no
// behaviour.
//
// This is a few hundred lines rather than a third-party generator on purpose.
// The release cross-compiles on ubuntu, and everything that runs there is
// either in this module or pinned in a reviewed diff; a COFF object holding
// two resources is small enough to own. The Go linker merges any
// rsrc_windows_<arch>.syso in a main package into the PE's .rsrc section, and
// the filename suffix keeps it out of every non-Windows build.
package winres

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Info is the identity stamped into one binary.
type Info struct {
	CompanyName      string
	ProductName      string
	FileDescription  string
	InternalName     string
	OriginalFilename string
	LegalCopyright   string
	// Version is the release version as tagged ("v0.1.105" or "0.1.105").
	// Anything that is not a dotted number ("dev", a git describe suffix)
	// keeps its text in the string table and numbers 0.0.0.0 where it stops.
	Version string
	// ManifestName is the assemblyIdentity name, e.g. "r2cuerdame.CodeSampleX.CLI".
	ManifestName string
}

// Binaries is the identity of each Windows release binary, keyed by its main
// package. OriginalFilename is the name it is installed under, not the
// release asset name: the launcher runs as csx.exe and the client as
// payloads\<version>\csx-payload.exe.
func Binaries(version string) map[string]Info {
	const (
		company   = "r2cuerdame"
		product   = "CodeSampleX"
		copyright = "Copyright (c) 2026 r2cuerdame. Licensed under the Apache License 2.0."
	)
	return map[string]Info{
		"cmd/csx": {
			CompanyName: company, ProductName: product, LegalCopyright: copyright, Version: version,
			FileDescription:  "CodeSampleX CLI",
			InternalName:     "csx",
			OriginalFilename: "csx-payload.exe",
			ManifestName:     "r2cuerdame.CodeSampleX.CLI",
		},
		"cmd/csx-launcher": {
			CompanyName: company, ProductName: product, LegalCopyright: copyright, Version: version,
			FileDescription:  "CodeSampleX Launcher",
			InternalName:     "csx-launcher",
			OriginalFilename: "csx.exe",
			ManifestName:     "r2cuerdame.CodeSampleX.Launcher",
		},
	}
}

// Arches are the Windows architectures the release ships.
var Arches = []string{"amd64", "arm64"}

// SysoName is the file the Go toolchain links only into GOOS=windows
// GOARCH=goarch builds of the package that contains it.
func SysoName(goarch string) string { return "rsrc_windows_" + goarch + ".syso" }

const (
	rtVersion  = 16
	rtManifest = 24
	langEnUS   = 0x0409
	codepage   = 1200 // UTF-16LE, the code page the string table is written in
)

// Arch values accepted by Syso, named as GOARCH.
var machines = map[string]struct {
	machine uint16
	reloc   uint16 // the ADDR32NB relocation: an image-relative 32-bit address
}{
	"386":   {0x014c, 0x0007},
	"amd64": {0x8664, 0x0003},
	"arm64": {0xaa64, 0x0002},
}

// Syso returns the COFF object for goarch carrying info's version resource
// and manifest. Output depends only on its inputs: no timestamp, no paths.
func Syso(goarch string, info Info) ([]byte, error) {
	m, ok := machines[goarch]
	if !ok {
		return nil, fmt.Errorf("winres: unsupported GOARCH %q", goarch)
	}
	if err := info.validate(); err != nil {
		return nil, err
	}
	vi, err := VersionInfo(info)
	if err != nil {
		return nil, err
	}
	section, relocs := resourceSection([]resource{
		{typ: rtVersion, id: 1, data: vi},
		{typ: rtManifest, id: 1, data: []byte(Manifest(info))},
	})
	return coff(m.machine, m.reloc, section, relocs), nil
}

func (info Info) validate() error {
	for name, v := range map[string]string{
		"CompanyName":      info.CompanyName,
		"ProductName":      info.ProductName,
		"FileDescription":  info.FileDescription,
		"OriginalFilename": info.OriginalFilename,
		"Version":          info.Version,
		"ManifestName":     info.ManifestName,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("winres: %s is empty", name)
		}
	}
	return nil
}

// ParseVersion splits a release version into the four 16-bit fields of
// VS_FIXEDFILEINFO and the display text. "v0.1.105" → [0 1 105 0], "0.1.105".
// It stops at the first part that is not a number, so "dev" is 0.0.0.0 and
// "v0.1.105-3-gabc" is 0.1.105.0 with its full text kept.
func ParseVersion(v string) ([4]uint16, string) {
	text := strings.TrimPrefix(strings.TrimSpace(v), "v")
	var out [4]uint16
	head := text
	if i := strings.IndexAny(head, "-+ "); i >= 0 {
		head = head[:i]
	}
	for i, part := range strings.SplitN(head, ".", 4) {
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil {
			break
		}
		out[i] = uint16(n)
	}
	return out, text
}

// Manifest is the application manifest: run as the invoking user, never
// elevate, and declare Windows 10/11 support so the loader does not apply
// compatibility shims or version lies. Nothing else — no DPI, theming or
// long-path settings, which the Go runtime already handles or a console
// program does not use.
func Manifest(info Info) string {
	n, _ := ParseVersion(info.Version)
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="` + html.EscapeString(info.ManifestName) + `" version="` +
		fmt.Sprintf("%d.%d.%d.%d", n[0], n[1], n[2], n[3]) + `"/>
  <description>` + html.EscapeString(info.FileDescription) + `</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <!-- Windows 10 and Windows 11 -->
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
</assembly>
`
}

// VersionInfo encodes the VS_VERSIONINFO resource.
func VersionInfo(info Info) ([]byte, error) {
	n, text := ParseVersion(info.Version)
	ms := func(a, b uint16) uint32 { return uint32(a)<<16 | uint32(b) }

	fixed := new(bytes.Buffer)
	for _, v := range []uint32{
		0xfeef04bd,     // dwSignature
		0x00010000,     // dwStrucVersion
		ms(n[0], n[1]), // dwFileVersionMS
		ms(n[2], n[3]), // dwFileVersionLS
		ms(n[0], n[1]), // dwProductVersionMS
		ms(n[2], n[3]), // dwProductVersionLS
		0x3f,           // dwFileFlagsMask
		0,              // dwFileFlags
		0x00040004,     // dwFileOS: VOS_NT_WINDOWS32
		0x00000001,     // dwFileType: VFT_APP
		0, 0, 0,        // dwFileSubtype, dwFileDateMS, dwFileDateLS
	} {
		binary.Write(fixed, binary.LittleEndian, v)
	}

	strs := []struct{ k, v string }{
		{"CompanyName", info.CompanyName},
		{"FileDescription", info.FileDescription},
		{"FileVersion", text},
		{"InternalName", info.InternalName},
		{"LegalCopyright", info.LegalCopyright},
		{"OriginalFilename", info.OriginalFilename},
		{"ProductName", info.ProductName},
		{"ProductVersion", text},
	}
	var table []node
	for _, s := range strs {
		if s.v != "" {
			table = append(table, node{key: s.k, text: s.v, isText: true})
		}
	}
	translation := make([]byte, 4)
	binary.LittleEndian.PutUint16(translation[0:], langEnUS)
	binary.LittleEndian.PutUint16(translation[2:], codepage)

	root := node{
		key:   "VS_VERSION_INFO",
		value: fixed.Bytes(),
		children: []node{
			{key: "StringFileInfo", isText: true, children: []node{
				{key: fmt.Sprintf("%04x%04x", langEnUS, codepage), isText: true, children: table},
			}},
			{key: "VarFileInfo", isText: true, children: []node{
				{key: "Translation", value: translation},
			}},
		},
	}
	out := root.encode()
	if len(out) > 0xffff {
		return nil, fmt.Errorf("winres: version resource is %d bytes", len(out))
	}
	return out, nil
}

// node is one VS_VERSIONINFO-family block: wLength, wValueLength, wType,
// a NUL-terminated UTF-16 key, then the value and children, each on a
// 32-bit boundary.
type node struct {
	key      string
	isText   bool   // wType 1; value is text, wValueLength counts WCHARs
	text     string // value when isText
	value    []byte // value when binary; wValueLength counts bytes
	children []node
}

func (n node) encode() []byte {
	b := new(bytes.Buffer)
	b.Write(make([]byte, 6)) // header, patched below
	b.Write(utf16z(n.key))
	pad4(b)
	var valueLen int
	switch {
	case n.isText && n.text != "":
		b.Write(utf16z(n.text))
		valueLen = len(utf16.Encode([]rune(n.text))) + 1
	case !n.isText && len(n.value) > 0:
		b.Write(n.value)
		valueLen = len(n.value)
	}
	for _, c := range n.children {
		pad4(b)
		b.Write(c.encode())
	}
	out := b.Bytes()
	typ := uint16(0)
	if n.isText {
		typ = 1
	}
	binary.LittleEndian.PutUint16(out[0:], uint16(len(out)))
	binary.LittleEndian.PutUint16(out[2:], uint16(valueLen))
	binary.LittleEndian.PutUint16(out[4:], typ)
	return out
}

func utf16z(s string) []byte {
	u := append(utf16.Encode([]rune(s)), 0)
	out := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(out[2*i:], c)
	}
	return out
}

func pad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

type resource struct {
	typ, id uint32
	data    []byte
}

// resourceSection lays out the three-level resource directory (type → id →
// language) followed by the data. Every IMAGE_RESOURCE_DATA_ENTRY holds the
// section offset of its data; the returned offsets are where those fields
// sit, for the linker to relocate into image-relative addresses.
func resourceSection(res []resource) ([]byte, []uint32) {
	sort.Slice(res, func(i, j int) bool {
		if res[i].typ != res[j].typ {
			return res[i].typ < res[j].typ
		}
		return res[i].id < res[j].id
	})
	// Group by type; ids within a type are already sorted.
	type group struct {
		typ   uint32
		items []resource
	}
	var groups []group
	for _, r := range res {
		if len(groups) == 0 || groups[len(groups)-1].typ != r.typ {
			groups = append(groups, group{typ: r.typ})
		}
		g := &groups[len(groups)-1]
		g.items = append(g.items, r)
	}

	const dirHeader, dirEntry, dataEntry = 16, 8, 16
	size := dirHeader + dirEntry*len(groups)
	nameDirAt := make([]int, len(groups))
	for i, g := range groups {
		nameDirAt[i] = size
		size += dirHeader + dirEntry*len(g.items)
	}
	langDirAt := make([]int, len(res))
	for i := range res {
		langDirAt[i] = size
		size += dirHeader + dirEntry
	}
	dataEntryAt := make([]int, len(res))
	for i := range res {
		dataEntryAt[i] = size
		size += dataEntry
	}
	dataAt := make([]int, len(res))
	for i, r := range res {
		size = align(size, 8)
		dataAt[i] = size
		size += len(r.data)
	}
	out := make([]byte, align(size, 8))
	le := binary.LittleEndian

	dir := func(at, entries int) {
		// Characteristics, TimeDateStamp, Major/MinorVersion all zero;
		// zero named entries, then the id entries.
		le.PutUint16(out[at+14:], uint16(entries))
	}
	entry := func(at int, id uint32, target int, subdir bool) {
		le.PutUint32(out[at:], id)
		v := uint32(target)
		if subdir {
			v |= 0x80000000
		}
		le.PutUint32(out[at+4:], v)
	}

	dir(0, len(groups))
	k := 0
	for i, g := range groups {
		entry(dirHeader+dirEntry*i, g.typ, nameDirAt[i], true)
		dir(nameDirAt[i], len(g.items))
		for j, r := range g.items {
			entry(nameDirAt[i]+dirHeader+dirEntry*j, r.id, langDirAt[k], true)
			k++
		}
	}
	relocs := make([]uint32, len(res))
	for i, r := range res {
		dir(langDirAt[i], 1)
		entry(langDirAt[i]+dirHeader, langEnUS, dataEntryAt[i], false)
		le.PutUint32(out[dataEntryAt[i]:], uint32(dataAt[i])) // relocated by the linker
		le.PutUint32(out[dataEntryAt[i]+4:], uint32(len(r.data)))
		le.PutUint32(out[dataEntryAt[i]+8:], 0) // CodePage
		copy(out[dataAt[i]:], r.data)
		relocs[i] = uint32(dataEntryAt[i])
	}
	return out, relocs
}

// coff wraps one .rsrc section in an object file: header, section header,
// raw data, relocations, a symbol table holding the section symbol every
// relocation refers to, and an empty string table.
func coff(machine, relocType uint16, section []byte, relocs []uint32) []byte {
	const fileHeader, sectionHeader, relocSize, symbolSize = 20, 40, 10, 18
	dataAt := fileHeader + sectionHeader
	relocAt := dataAt + len(section)
	symAt := relocAt + relocSize*len(relocs)

	b := new(bytes.Buffer)
	w := func(v any) { binary.Write(b, binary.LittleEndian, v) }

	w(machine)
	w(uint16(1))     // NumberOfSections
	w(uint32(0))     // TimeDateStamp: zero keeps the object reproducible
	w(uint32(symAt)) // PointerToSymbolTable
	w(uint32(1))     // NumberOfSymbols
	w(uint16(0))     // SizeOfOptionalHeader
	w(uint16(0))     // Characteristics

	w([8]byte{'.', 'r', 's', 'r', 'c'})
	w(uint32(0))            // VirtualSize
	w(uint32(0))            // VirtualAddress
	w(uint32(len(section))) // SizeOfRawData
	w(uint32(dataAt))       // PointerToRawData
	w(uint32(relocAt))      // PointerToRelocations
	w(uint32(0))            // PointerToLinenumbers
	w(uint16(len(relocs)))  // NumberOfRelocations
	w(uint16(0))            // NumberOfLinenumbers
	w(uint32(0x40000040))   // IMAGE_SCN_CNT_INITIALIZED_DATA | IMAGE_SCN_MEM_READ

	b.Write(section)
	for _, off := range relocs {
		w(off)       // VirtualAddress: the field to fix up
		w(uint32(0)) // SymbolTableIndex: the .rsrc section symbol
		w(relocType)
	}

	w([8]byte{'.', 'r', 's', 'r', 'c'})
	w(uint32(0)) // Value
	w(int16(1))  // SectionNumber
	w(uint16(0)) // Type
	w(uint8(3))  // StorageClass: IMAGE_SYM_CLASS_STATIC
	w(uint8(0))  // NumberOfAuxSymbols

	w(uint32(4)) // string table: just its own size
	return b.Bytes()
}

func align(n, a int) int { return (n + a - 1) / a * a }
