package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkLivery(t *testing.T, root, ac, folder, ref string) {
	t.Helper()
	d := filepath.Join(root, "Liveries", ac, folder)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "description.lua"), []byte(`livery = {{"x", DIFFUSE, "../`+ref+`/tex", false};}`), 0o644)
	repaint(t, d)
	os.WriteFile(filepath.Join(d, "._tex.dds"), []byte("mac junk"), 0o644)
}

func repaint(t *testing.T, d string) {
	b := make([]byte, 50000)
	rand.Read(b)
	os.WriteFile(filepath.Join(d, "tex.dds"), b, 0o644)
}

func TestMonthlyCycle(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	master, out, dcs := filepath.Join(tmp, "master"), filepath.Join(tmp, "out"), filepath.Join(tmp, "DCS")
	cache := loadHashCache()
	noop := func(string, int64, int64, string) {}
	p := func(int64, int64, string) {}

	mkLivery(t, master, "FA-18C_hornet", "VMAQ-101 'Swipers'", "VMAQ-101 'Swipers'")
	mkLivery(t, master, "F-14B", "VF-32 Wet Iron", "VF-32 Wet Iron")
	oct, err := buildPack(BuildOptions{Source: master, Out: out, Release: "2026-10", Full: true}, cache, noop)
	if err != nil || len(oct.New) != 2 {
		t.Fatalf("oct build: %v %+v", err, oct)
	}
	octPack := oct.PackPath

	// Nov: one new jet, one same-size repaint, one broken description.lua
	mkLivery(t, master, "F-16C_50", "18th FS Wet Venom", "18th FS Wet Venom")
	repaint(t, filepath.Join(master, "Liveries", "F-14B", "VF-32 Wet Iron"))
	mkLivery(t, master, "FA-18C_hornet", "VT-404", "VT-404 OLD")
	nov, err := buildPack(BuildOptions{Source: master, Out: out, Release: "2026-11", Prev: oct.LibPath}, cache, noop)
	if err != nil {
		t.Fatal(err)
	}
	if len(nov.New) != 2 || len(nov.Changed) != 1 || nov.Shipped != 3 {
		t.Fatalf("nov diff wrong: %+v", nov)
	}
	if len(nov.Warnings) != 1 || nov.Warnings[0].Level != "error" || !strings.Contains(nov.Warnings[0].Message, "VT-404 OLD") {
		t.Fatalf("broken lua not flagged: %+v", nov.Warnings)
	}
	zr, _ := zip.OpenReader(nov.PackPath)
	for _, f := range zr.File {
		if strings.Contains(f.Name, "._") || strings.Contains(f.Name, "Swipers") {
			t.Errorf("pack should not contain %s", f.Name)
		}
	}
	zr.Close()

	// Pilot who skipped October installs only November.
	if _, _, err := installPack(nov.PackPath, dcs, cache, p); err != nil {
		t.Fatal(err)
	}
	m, _ := loadInstalledManifest(dcs)
	res, _ := scanInstalled(filepath.Join(dcs, "Liveries"), m, cache, p)
	if res.UpToDate || res.OK != 3 || len(res.NeedPacks) != 1 || res.NeedPacks[0] != "2026-10" {
		t.Fatalf("expected Swipers missing from 2026-10: %+v", res)
	}
	// Then installs the October pack: must NOT downgrade Wet Iron.
	ir, _, err := installPack(octPack, dcs, cache, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.Skipped) != 1 || ir.Skipped[0] != "F-14B/VF-32 Wet Iron" {
		t.Fatalf("expected Wet Iron skipped: %+v", ir)
	}
	res, _ = scanInstalled(filepath.Join(dcs, "Liveries"), m, cache, p)
	if !res.UpToDate {
		t.Fatalf("expected green: %+v", res)
	}
	// Same-size corruption is caught.
	repaint(t, filepath.Join(dcs, "Liveries", "F-14B", "VF-32 Wet Iron"))
	res, _ = scanInstalled(filepath.Join(dcs, "Liveries"), m, cache, p)
	if res.UpToDate || res.Liveries[0].State != "outdated" {
		t.Fatalf("expected outdated: %+v", res.Liveries[0])
	}
	// No changes → no pack.
	dec, err := buildPack(BuildOptions{Source: master, Out: out, Release: "2026-12", Prev: nov.LibPath}, cache, noop)
	if err != nil || !dec.NoChanges {
		t.Fatalf("expected no changes: %v %+v", err, dec)
	}
}

func TestZipSlipRejected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	m := Manifest{Format: 1, Release: "x", PackContains: []string{"A/B"},
		Liveries: []Livery{{Aircraft: "A", Folder: "B", Files: []FileEntry{{Path: "../../../evil.txt", Size: 1}}}}}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(manifestName)
	json.NewEncoder(w).Encode(m)
	w, _ = zw.Create("Liveries/A/B/../../../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	pk := filepath.Join(tmp, "evil.zip")
	os.WriteFile(pk, buf.Bytes(), 0o644)
	_, _, err := installPack(pk, filepath.Join(tmp, "DCS"), loadHashCache(), func(int64, int64, string) {})
	if _, serr := os.Stat(filepath.Join(tmp, "evil.txt")); serr == nil {
		t.Fatal("zip-slip wrote outside Liveries")
	}
	if err == nil {
		t.Log("pack ignored (ok)")
	}
}

func TestNotAPack(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.Create("a.txt")
	zw.Close()
	pk := filepath.Join(tmp, "old-style.zip")
	os.WriteFile(pk, buf.Bytes(), 0o644)
	if _, err := inspectPack(pk); err == nil || !strings.Contains(err.Error(), "isn't a Wet Paint pack") {
		t.Fatalf("got %v", err)
	}
}

func TestOnlineUpdates(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	t.Setenv("WETPAINT_DOWNLOADS", filepath.Join(tmp, "dl"))
	master, site := filepath.Join(tmp, "master"), filepath.Join(tmp, "site")
	cache := loadHashCache()
	noop := func(string, int64, int64, string) {}

	// Fridge: Oct baseline (full), Nov + Dec updates, all into the same web folder.
	mkLivery(t, master, "FA-18C_hornet", "Swipers", "Swipers")
	mkLivery(t, master, "F-14B", "Wet Iron", "Wet Iron")
	if _, err := buildPack(BuildOptions{Source: master, Out: site, Release: "2026-10", Full: true}, cache, noop); err != nil {
		t.Fatal(err)
	}
	mkLivery(t, master, "F-16C_50", "Wet Venom", "Wet Venom")
	if _, err := buildPack(BuildOptions{Source: master, Out: site, Release: "2026-11", Prev: filepath.Join(site, "feed.json")}, cache, noop); err != nil {
		t.Fatal(err)
	}
	repaint(t, filepath.Join(master, "Liveries", "F-14B", "Wet Iron"))
	mkLivery(t, master, "C-130J-30", "Revenants", "Revenants")
	dec, err := buildPack(BuildOptions{Source: master, Out: site, Release: "2026-12", Prev: filepath.Join(site, "feed.json")}, cache, noop)
	if err != nil || len(dec.New) != 1 || len(dec.Changed) != 1 {
		t.Fatalf("dec: %v %+v", err, dec)
	}

	srv := httptest.NewServer(http.FileServer(http.Dir(site)))
	defer srv.Close()
	ctx := context.Background()
	f, err := fetchFeed(ctx, srv.URL+"/feed.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Packs) != 3 || f.Latest != "2026-12" || len(f.Library.Liveries) != 4 {
		t.Fatalf("feed wrong: latest=%s packs=%d", f.Latest, len(f.Packs))
	}

	plan := func(dcs string) *UpdatePlan {
		_, ie := loadInstalledManifest(dcs)
		sc, err := scanInstalled(filepath.Join(dcs, "Liveries"), f.Library, cache, nil)
		if err != nil {
			t.Fatal(err)
		}
		return makePlan(f, sc, ie != nil)
	}
	kinds := func(s []FeedPack) (out []string) {
		for _, x := range s {
			out = append(out, x.Release+"/"+x.Kind)
		}
		return
	}

	// Brand-new pilot: full Oct + Nov + Dec updates.
	fresh := filepath.Join(tmp, "fresh", "DCS")
	p := plan(fresh)
	if !p.FirstTime || strings.Join(kinds(p.Steps), ",") != "2026-10/full,2026-11/update,2026-12/update" {
		t.Fatalf("fresh plan: %v first=%v", kinds(p.Steps), p.FirstTime)
	}
	if _, err := runUpdate(ctx, f, p.Steps, fresh, cache, noop); err != nil {
		t.Fatal(err)
	}
	if p = plan(fresh); !p.UpToDate {
		t.Fatalf("fresh pilot not green after update: %+v", p.Scan)
	}
	if left, _ := os.ReadDir(filepath.Join(tmp, "dl")); len(left) != 0 {
		t.Errorf("downloads not cleaned up: %d files left", len(left))
	}

	// Pilot who only has Oct: just the two small updates.
	octOnly := filepath.Join(tmp, "oct", "DCS")
	octPack := filepath.Join(site, "WetBoys-Liveries-2026-10-FULL.zip")
	if _, _, err := installPack(octPack, octOnly, cache, func(int64, int64, string) {}); err != nil {
		t.Fatal(err)
	}
	if p = plan(octOnly); strings.Join(kinds(p.Steps), ",") != "2026-11/update,2026-12/update" || p.FirstTime {
		t.Fatalf("oct-only plan: %v", kinds(p.Steps))
	}

	// Resume: half a download already on disk.
	nov := f.Packs[1]
	full, _ := os.ReadFile(filepath.Join(site, "WetBoys-Liveries-2026-11-update.zip"))
	os.MkdirAll(filepath.Join(tmp, "dl"), 0o755)
	part := filepath.Join(tmp, "dl", "WetBoys-Liveries-2026-11-update.zip.download")
	os.WriteFile(part, full[:len(full)/2], 0o644)
	var first int64 = -1
	got, err := downloadPack(ctx, nov, func(d, tot int64) {
		if first < 0 {
			first = d
		}
	})
	if err != nil || first != int64(len(full)/2) {
		t.Fatalf("resume failed: err=%v first progress=%d want %d", err, first, len(full)/2)
	}
	os.Remove(got)
	// Corrupt partial → detected, not installed.
	bad := append([]byte{}, full[:len(full)/2]...)
	bad[100] ^= 0xff
	os.WriteFile(part, bad, 0o644)
	if _, err := downloadPack(ctx, nov, func(int64, int64) {}); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Fatalf("corrupt download not caught: %v", err)
	}

	// Fridge later publishes a Dec full pack: a new pilot now needs just that one.
	if _, err := buildPack(BuildOptions{Source: master, Out: filepath.Join(tmp, "site2"), Release: "2026-12", Full: true}, cache, noop); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(tmp, "site2", "feed.json"))
	var f2 Feed
	json.Unmarshal(b, &f2)
	f.Packs = append(f.Packs, FeedPack{Release: "2026-12", Kind: "full", URL: "x", Bytes: f2.Packs[0].Bytes, Created: f2.Packs[0].Created})
	if p = plan(filepath.Join(tmp, "nobody", "DCS")); strings.Join(kinds(p.Steps), ",") != "2026-12/full" {
		t.Fatalf("with newer full pack: %v", kinds(p.Steps))
	}

	// No feed published yet.
	if _, err := fetchFeed(ctx, srv.URL+"/nope.json"); err != errNoFeed {
		t.Fatalf("want errNoFeed, got %v", err)
	}
}

func TestDriveLinks(t *testing.T) {
	for _, in := range []string{
		"https://drive.google.com/file/d/1AbCdEfGhIjKlMnOpQrStUvWxYz012345/view?usp=sharing",
		"https://drive.google.com/open?id=1AbCdEfGhIjKlMnOpQrStUvWxYz012345",
	} {
		if got := directLink(in); got != "https://drive.usercontent.google.com/download?id=1AbCdEfGhIjKlMnOpQrStUvWxYz012345&export=download&confirm=t" {
			t.Errorf("%s → %s", in, got)
		}
	}
}

func TestSteamLibraries(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "steamapps"), 0o755)
	os.WriteFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"), []byte(`"libraryfolders"
{
	"0"
	{
		"path"		"C:\\Program Files (x86)\\Steam"
		"label"		""
	}
	"1"
	{
		"path"		"D:\\SteamLibrary"
		"apps" { "223750"		"123" }
	}
}`), 0o644)
	libs := steamLibraries(root)
	if len(libs) != 3 || libs[2] != `D:\SteamLibrary` || libs[1] != `C:\Program Files (x86)\Steam` {
		t.Fatalf("got %q", libs)
	}
}

func fakeInstall(t *testing.T, dir, variant string, aircraft ...string) {
	os.MkdirAll(filepath.Join(dir, "Bazar", "Liveries", "uh-1h"), 0o755)
	for _, a := range aircraft {
		os.MkdirAll(filepath.Join(dir, "CoreMods", "aircraft", "Mod-"+a, "Liveries", a, "default"), 0o755)
	}
	if variant != "" {
		os.WriteFile(filepath.Join(dir, "dcs_variant.txt"), []byte("\ufeff"+variant+"\r\n"), 0o644)
	}
}

func TestInstallAndAircraftNames(t *testing.T) {
	tmp := t.TempDir()
	inst := filepath.Join(tmp, "steamapps", "common", "DCSWorld")
	fakeInstall(t, inst, "", "FA-18C_hornet", "F-16C_50", "F-14B")
	sg := filepath.Join(tmp, "Saved Games")
	dcs := filepath.Join(sg, "DCS")
	// a third-party mod installed in Saved Games
	os.MkdirAll(filepath.Join(dcs, "Mods", "aircraft", "C-130J", "Liveries", "C-130J-30", "x"), 0o755)
	os.MkdirAll(filepath.Join(sg, "DCS.openbeta"), 0o755)
	t.Setenv("WETPAINT_SAVED_GAMES", sg)
	t.Setenv("WETPAINT_DCS_INSTALL", inst)
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))

	ins := findDCSInstalls()
	if len(ins) != 1 || ins[0].SavedGames != "DCS" {
		t.Fatalf("installs: %+v", ins)
	}
	if dirs := findDCSDirs(); dirs[0].Name != "DCS" || dirs[0].UsedBy == "" || dirs[1].UsedBy != "" {
		t.Fatalf("dirs: %+v", dirs)
	}
	names := readAircraftNames(&ins[0], dcs)
	for _, n := range []string{"fa-18c_hornet", "f-16c_50", "f-14b", "uh-1h", "c-130j-30"} {
		if _, ok := names.Names[n]; !ok {
			t.Errorf("missing %s in %v", n, names.Names)
		}
	}
	lib := &Manifest{Liveries: []Livery{
		{Aircraft: "FA-18C_Hornet", Folder: "Swipers"}, // case differs: fine on Windows
		{Aircraft: "F-16C-50", Folder: "Wet Venom"},    // typo
		{Aircraft: "FA18C_hornet", Folder: "VT-404"},   // typo
		{Aircraft: "A-10CII", Folder: "Abyss Hogs"},    // module not installed here
	}}
	iss := checkAircraft(lib, names.Names)
	got := map[string]AircraftIssue{}
	for _, i := range iss {
		got[i.Aircraft] = i
	}
	if len(iss) != 3 || got["F-16C-50"].Suggest != "F-16C_50" || got["FA18C_hornet"].Suggest != "FA-18C_hornet" ||
		got["A-10CII"].Level != "info" || iss[0].Level != "error" {
		t.Fatalf("issues: %+v", iss)
	}

	// dcs_variant.txt → Saved Games\DCS.openbeta is the one in use
	os.WriteFile(filepath.Join(inst, "dcs_variant.txt"), []byte("\ufeffopenbeta\r\n"), 0o644)
	if in := findDCSInstalls(); in[0].SavedGames != "DCS.openbeta" {
		t.Fatalf("variant: %+v", in)
	}
	if dirs := findDCSDirs(); dirs[0].Name != "DCS.openbeta" {
		t.Fatalf("variant dir not first: %+v", dirs)
	}
}

func TestBuilderFlagsAircraftTypos(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	master := filepath.Join(tmp, "master")
	mkLivery(t, master, "F-16C-50", "Wet Venom", "Wet Venom")
	mkLivery(t, master, "FA-18C_hornet", "Swipers", "Swipers")
	mkLivery(t, master, "E-2J Ghost Eye", "Witchglass", "Witchglass") // a mod: unknown name, not a typo
	res, err := buildPack(BuildOptions{Source: master, Out: filepath.Join(tmp, "out"), Release: "2026-11", Full: true}, loadHashCache(), func(string, int64, int64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	var typo, mod bool
	for _, w := range res.Warnings {
		if w.Livery == "F-16C-50" && w.Level == "error" && strings.Contains(w.Message, "F-16C_50") {
			typo = true
		}
		if w.Livery == "E-2J Ghost Eye" && w.Level == "info" {
			mod = true
		}
		if w.Livery == "FA-18C_hornet" {
			t.Errorf("valid name flagged: %+v", w)
		}
	}
	if !typo || !mod {
		t.Fatalf("warnings: %+v", res.Warnings)
	}
}
