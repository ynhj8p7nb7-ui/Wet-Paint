// mkicon writes a Windows resource object (.syso) for WetPaint.exe. The Go
// linker picks up any *_windows_amd64.syso in the package folder. It embeds:
//
//   - the app icon (one RT_ICON per PNG + RT_GROUP_ICON #1)
//
//   - version info (publisher, product name, description, version) shown in
//     the file's Properties → Details tab
//
//   - an application manifest: runs as the normal user (never asks for admin),
//     declares Windows 10/11 support, long paths and DPI awareness
//
//     go run ./tools/mkicon -version 1.2.0 -out rsrc_windows_amd64.syso assets/icon-16.png …
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image/png"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	rtManifest  = 24
	langEnUS    = 0x0409
)

func w(b *bytes.Buffer, vals ...any) {
	for _, v := range vals {
		binary.Write(b, binary.LittleEndian, v)
	}
}

type resource struct {
	typ, id int
	data    []byte
}

func main() {
	out := flag.String("out", "rsrc_windows_amd64.syso", "output .syso")
	ver := flag.String("version", "1.0.0", "product version x.y.z")
	flag.Parse()

	var res []resource

	// ---- icons
	var grp bytes.Buffer
	w(&grp, uint16(0), uint16(1), uint16(len(flag.Args())))
	for i, p := range flag.Args() {
		b, err := os.ReadFile(p)
		check(err)
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		check(err)
		if cfg.Width > 256 || cfg.Height > 256 {
			panic(p + ": icons must be 256px or smaller")
		}
		res = append(res, resource{rtIcon, i + 1, b})
		w(&grp, uint8(cfg.Width%256), uint8(cfg.Height%256), uint8(0), uint8(0), uint16(1), uint16(32), uint32(len(b)), uint16(i+1))
	}
	if len(flag.Args()) > 0 {
		res = append(res, resource{rtGroupIcon, 1, grp.Bytes()})
	}

	// ---- version info
	res = append(res, resource{rtVersion, 1, versionInfo(*ver)})

	// ---- manifest
	res = append(res, resource{rtManifest, 1, []byte(manifest(*ver))})

	b := coff(res)
	check(os.WriteFile(*out, b, 0o644))
	fmt.Printf("wrote %s (%d resources, %d bytes)\n", *out, len(res), len(b))
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// ---------------------------------------------------------------- VS_VERSIONINFO

func u16z(s string) []byte {
	var b bytes.Buffer
	for _, c := range utf16.Encode([]rune(s)) {
		w(&b, c)
	}
	w(&b, uint16(0))
	return b.Bytes()
}

func pad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

// node builds one version-info block: header, key, value, children.
// valueLen is in the unit the format wants (bytes for binary, WORDs for text).
func node(key string, value []byte, valueLen int, text bool, children ...[]byte) []byte {
	var b bytes.Buffer
	typ := uint16(0)
	if text {
		typ = 1
	}
	w(&b, uint16(0), uint16(valueLen), typ) // length patched below
	b.Write(u16z(key))
	pad4(&b)
	b.Write(value)
	for _, c := range children {
		pad4(&b)
		b.Write(c)
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out[0:], uint16(len(out)))
	return out
}

func parseVersion(v string) [4]uint16 {
	var n [4]uint16
	for i, p := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 4) {
		x, _ := strconv.Atoi(p)
		n[i] = uint16(x)
	}
	return n
}

func versionInfo(ver string) []byte {
	n := parseVersion(ver)
	ms, ls := uint32(n[0])<<16|uint32(n[1]), uint32(n[2])<<16|uint32(n[3])
	var fixed bytes.Buffer
	w(&fixed, uint32(0xFEEF04BD), uint32(0x00010000), ms, ls, ms, ls,
		uint32(0x3F), uint32(0), uint32(0x00040004), uint32(1), uint32(0), uint32(0), uint32(0))

	full := fmt.Sprintf("%d.%d.%d.%d", n[0], n[1], n[2], n[3])
	strs := [][2]string{
		{"CompanyName", "The Wet Boys (CVW-0)"},
		{"FileDescription", "Wet Paint - Wet Boys DCS livery installer"},
		{"FileVersion", full},
		{"InternalName", "WetPaint"},
		{"LegalCopyright", "The Wet Boys - flywetboys.com"},
		{"OriginalFilename", "WetPaint.exe"},
		{"ProductName", "Wet Paint"},
		{"ProductVersion", ver},
		{"Comments", "Installs and verifies Wet Boys liveries in Saved Games\\DCS\\Liveries. Source and docs: flywetboys.com"},
	}
	var strings_ [][]byte
	for _, kv := range strs {
		v := u16z(kv[1])
		strings_ = append(strings_, node(kv[0], v, len(v)/2, true))
	}
	table := node("040904B0", nil, 0, true, strings_...)
	sfi := node("StringFileInfo", nil, 0, true, table)
	var tr bytes.Buffer
	w(&tr, uint16(langEnUS), uint16(1200))
	vfi := node("VarFileInfo", nil, 0, true, node("Translation", tr.Bytes(), 4, false))

	var root bytes.Buffer
	w(&root, uint16(0), uint16(fixed.Len()), uint16(0))
	root.Write(u16z("VS_VERSION_INFO"))
	pad4(&root)
	root.Write(fixed.Bytes())
	pad4(&root)
	root.Write(sfi)
	pad4(&root)
	root.Write(vfi)
	out := root.Bytes()
	binary.LittleEndian.PutUint16(out[0:], uint16(len(out)))
	return out
}

func manifest(ver string) string {
	n := parseVersion(ver)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="TheWetBoys.WetPaint" version="%d.%d.%d.%d" processorArchitecture="amd64"/>
  <description>Wet Paint - Wet Boys DCS livery installer</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges>
      <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
    </requestedPrivileges></security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <longPathAware xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">true</longPathAware>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
    </windowsSettings>
  </application>
</assembly>
`, n[0], n[1], n[2], n[3])
}

// ---------------------------------------------------------------- COFF .rsrc

func coff(res []resource) []byte {
	sort.Slice(res, func(i, j int) bool {
		if res[i].typ != res[j].typ {
			return res[i].typ < res[j].typ
		}
		return res[i].id < res[j].id
	})
	type group struct {
		typ   int
		items []int // indexes into res
	}
	var groups []group
	for i, r := range res {
		if len(groups) == 0 || groups[len(groups)-1].typ != r.typ {
			groups = append(groups, group{typ: r.typ})
		}
		groups[len(groups)-1].items = append(groups[len(groups)-1].items, i)
	}
	dirSize := func(n int) int { return 16 + 8*n }
	align := func(x int) int { return (x + 7) &^ 7 }

	// layout: root | type dirs | id dirs (1 lang each) | data entries | raw data
	off := dirSize(len(groups))
	typeOff := make([]int, len(groups))
	for i, g := range groups {
		typeOff[i] = off
		off += dirSize(len(g.items))
	}
	idOff := make([]int, len(res))
	for i := range res {
		idOff[i] = off
		off += dirSize(1)
	}
	dataOff := make([]int, len(res))
	for i := range res {
		dataOff[i] = off
		off += 16
	}
	rawOff := make([]int, len(res))
	off = align(off)
	for i, r := range res {
		rawOff[i] = off
		off = align(off + len(r.data))
	}
	secSize := off

	var sec bytes.Buffer
	dirHdr := func(n int) { w(&sec, uint32(0), uint32(0), uint16(0), uint16(0), uint16(0), uint16(n)) }
	const sub = 0x80000000
	dirHdr(len(groups))
	for i, g := range groups {
		w(&sec, uint32(g.typ), uint32(sub|typeOff[i]))
	}
	for _, g := range groups {
		dirHdr(len(g.items))
		for _, ri := range g.items {
			w(&sec, uint32(res[ri].id), uint32(sub|idOff[ri]))
		}
	}
	for i := range res {
		dirHdr(1)
		w(&sec, uint32(langEnUS), uint32(dataOff[i]))
	}
	var relocs []uint32
	for i, r := range res {
		relocs = append(relocs, uint32(sec.Len()))
		w(&sec, uint32(rawOff[i]), uint32(len(r.data)), uint32(0), uint32(0))
	}
	for i, r := range res {
		for sec.Len() < rawOff[i] {
			sec.WriteByte(0)
		}
		sec.Write(r.data)
	}
	for sec.Len() < secSize {
		sec.WriteByte(0)
	}

	const fileHdr, secHdr = 20, 40
	ptrRaw := fileHdr + secHdr
	ptrReloc := ptrRaw + sec.Len()
	ptrSym := ptrReloc + 10*len(relocs)
	var o bytes.Buffer
	w(&o, uint16(0x8664), uint16(1), uint32(0), uint32(ptrSym), uint32(1), uint16(0), uint16(0))
	o.WriteString(".rsrc\x00\x00\x00")
	w(&o, uint32(0), uint32(0), uint32(sec.Len()), uint32(ptrRaw), uint32(ptrReloc), uint32(0), uint16(len(relocs)), uint16(0), uint32(0x40000040))
	o.Write(sec.Bytes())
	for _, r := range relocs {
		w(&o, r, uint32(0), uint16(3)) // IMAGE_REL_AMD64_ADDR32NB → symbol 0 (.rsrc)
	}
	o.WriteString(".rsrc\x00\x00\x00")
	w(&o, uint32(0), int16(1), uint16(0), uint8(3), uint8(0))
	w(&o, uint32(4))
	return o.Bytes()
}
