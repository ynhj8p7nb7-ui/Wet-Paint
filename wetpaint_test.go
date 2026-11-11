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
	"time"
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
	if _, err := runUpdate(ctx, f, p.Steps, fresh, cache, nil, noop); err != nil {
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
	t.Setenv("WETPAINT_DCS_INSTALL", filepath.Join(tmp, "no-dcs-here")) // don't read a real DCS on the test PC
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

// "Check folder only" must read and report but never write a pack, library or feed.
func TestCheckOnlyWritesNothing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	t.Setenv("WETPAINT_DCS_INSTALL", filepath.Join(tmp, "no-dcs-here"))
	master := filepath.Join(tmp, "master")
	mkLivery(t, master, "FA-18C_hornet", "15th ADS 'Wet Boys' Gold #6", "15th ADS 'Wet Boys' Gold #6")
	mkLivery(t, master, "FA-18C_hornet", "Renamed", "Old Name") // broken description.lua
	out := filepath.Join(tmp, "out")
	before, _ := os.ReadDir(master)
	res, err := buildPack(BuildOptions{Source: master, Out: out, Release: "2026-11", Full: true, CheckOnly: true}, loadHashCache(), func(string, int64, int64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CheckOnly || res.Total != 2 || res.Shipped != 2 || res.SrcBytes == 0 || res.PackPath != "" || res.FeedPath != "" {
		t.Fatalf("result: %+v", res)
	}
	broken := false
	for _, w := range res.Warnings {
		if w.Level == "error" && strings.Contains(w.Message, "Old Name") {
			broken = true
		}
	}
	if !broken {
		t.Fatalf("broken description.lua not reported: %+v", res.Warnings)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("check-only created the output folder")
	}
	if after, _ := os.ReadDir(master); len(after) != len(before) {
		t.Fatalf("check-only changed the master folder")
	}
	// and it works with no output folder at all
	if _, err := buildPack(BuildOptions{Source: master, CheckOnly: true}, loadHashCache(), func(string, int64, int64, string) {}); err != nil {
		t.Fatal(err)
	}
}

// Ship and ground-unit liveries (CVN_75, …) live under CoreMods\tech and
// Mods\tech, not under "aircraft". Found on a real Wet Boys PC.
func TestTechLiveryNames(t *testing.T) {
	tmp := t.TempDir()
	inst := filepath.Join(tmp, "DCSWorld")
	fakeInstall(t, inst, "", "FA-18C_hornet")
	os.MkdirAll(filepath.Join(inst, "CoreMods", "tech", "USS_Nimitz", "Liveries", "CVN_75", "default"), 0o755)
	dcs := filepath.Join(tmp, "Saved Games", "DCS")
	os.MkdirAll(filepath.Join(dcs, "Mods", "tech", "SomeAssetPack", "Liveries", "billboard_motorized", "x"), 0o755)
	in := describeInstall(inst, "Steam")
	names := readAircraftNames(&in, dcs)
	for _, n := range []string{"cvn_75", "billboard_motorized", "fa-18c_hornet"} {
		if _, ok := names.Names[n]; !ok {
			t.Errorf("missing %s in %v", n, names.Names)
		}
	}
}

// Requests that reach the local server under some other host name (a web page
// that pointed its own domain at 127.0.0.1) are refused.
func TestLocalOnly(t *testing.T) {
	t.Setenv("WETPAINT_CONFIG", filepath.Join(t.TempDir(), "cfg"))
	h := newApp().routes()
	for host, want := range map[string]int{"127.0.0.1:5123": 200, "localhost:5123": 200, "evil.example:5123": 403, "evil.example": 403} {
		req := httptest.NewRequest("GET", "/api/alive", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: got %d, want %d", host, rec.Code, want)
		}
	}
	req := httptest.NewRequest("GET", "/fridge.webp", nil)
	req.Host = "127.0.0.1:5123"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.Len() < 1000 || rec.Header().Get("Content-Type") != "image/webp" {
		t.Errorf("fridge image: %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

// Real case from the Wet Boys library: the C-130J livery points at "../common/",
// a shared texture folder that lives in the DCS install, not in Saved Games.
// And "../Thunderbird Blank/" style references to a sibling livery are fine.
func TestDescriptionRefsToStockAndSiblings(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	inst := filepath.Join(tmp, "DCSWorld")
	fakeInstall(t, inst, "", "F-16C_50")
	os.MkdirAll(filepath.Join(inst, "CoreMods", "aircraft", "C130J", "Liveries", "C-130J-30", "Shared Skins"), 0o755)
	t.Setenv("WETPAINT_DCS_INSTALL", inst)
	t.Setenv("WETPAINT_SAVED_GAMES", filepath.Join(tmp, "Saved Games"))
	master := filepath.Join(tmp, "master")
	mkLivery(t, master, "C-130J-30", "Heather", "common")         // well-known shared folder
	mkLivery(t, master, "C-130J-30", "Heather 2", "shared skins") // exists in the install (case differs)
	mkLivery(t, master, "F-16C_50", "Thunderbird Blank", "Thunderbird Blank")
	mkLivery(t, master, "F-16C_50", "Thunderbird 1", "thunderbird blank") // sibling, case differs
	mkLivery(t, master, "F-16C_50", "Wet Venom", "Wet Venon")             // really broken
	res, err := buildPack(BuildOptions{Source: master, CheckOnly: true}, loadHashCache(), func(string, int64, int64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	var errs []string
	for _, w := range res.Warnings {
		if w.Level == "error" {
			errs = append(errs, w.Livery)
		}
	}
	if len(errs) != 1 || errs[0] != "F-16C_50/Wet Venom" {
		t.Fatalf("want only F-16C_50/Wet Venom flagged, got %v\n%+v", errs, res.Warnings)
	}
}

// Real aircraft whose names are one character apart must not be reported as
// typos of each other (KC130 was flagged as a misspelt KC-135 on a real PC).
func TestSimilarAircraftAreNotTypos(t *testing.T) {
	known := map[string]string{"kc-135": "KC-135", "f-15c": "F-15C", "mig-21bis": "MiG-21Bis", "fa-18c_hornet": "FA-18C_hornet", "f-16c_50": "F-16C_50", "su-25": "su-25"}
	for _, n := range []string{"KC130", "KC135MPRS", "F-15E", "F-15D", "su-25T", "FA-18E", "Hercules", "CVN_75"} {
		if s := nearestName(n, known); s != "" {
			t.Errorf("%s wrongly taken for a typo of %s", n, s)
		}
	}
	for n, want := range map[string]string{"MiG-21 Bis": "MiG-21Bis", "FA18C_hornet": "FA-18C_hornet", "F-16C-50": "F-16C_50", "FA-18C_hornt": "FA-18C_hornet", "kc 135": "KC-135"} {
		if s := nearestName(n, known); s != want {
			t.Errorf("%s: got %q, want %q", n, s, want)
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Packs handed around on Discord and saved into Downloads: found once each,
// half-finished downloads reported, and installed oldest first.
func TestPacksFromDownloads(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	t.Setenv("WETPAINT_DOWNLOADS", filepath.Join(tmp, "cache"))
	dl := filepath.Join(tmp, "Downloads")
	os.MkdirAll(dl, 0o755)
	t.Setenv("WETPAINT_PACKS_DIR", dl)
	master, site := filepath.Join(tmp, "master"), filepath.Join(tmp, "site")
	cache := loadHashCache()
	noop := func(string, int64, int64, string) {}
	ctx := context.Background()

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
	if _, err := buildPack(BuildOptions{Source: master, Out: site, Release: "2026-12", Prev: filepath.Join(site, "feed.json")}, cache, noop); err != nil {
		t.Fatal(err)
	}
	octZip := filepath.Join(site, "WetBoys-Liveries-2026-10-FULL.zip")
	novZip := filepath.Join(site, "WetBoys-Liveries-2026-11-update.zip")
	decZip := filepath.Join(site, "WetBoys-Liveries-2026-12-update.zip")

	// Downloads: Oct full and Dec update, Dec saved twice, a pack cut short,
	// one still downloading, and an unrelated zip.
	copyFile(t, octZip, filepath.Join(dl, "WetBoys-Liveries-2026-10-FULL.zip"))
	copyFile(t, decZip, filepath.Join(dl, "WetBoys-Liveries-2026-12-update.zip"))
	copyFile(t, decZip, filepath.Join(dl, "WetBoys-Liveries-2026-12-update (1).zip"))
	b, _ := os.ReadFile(novZip)
	os.WriteFile(filepath.Join(dl, "WetBoys-Liveries-2027-01-update.zip"), b[:len(b)/2], 0o644)
	os.WriteFile(filepath.Join(dl, "WetBoys-Liveries-2027-02-update.zip.crdownload"), b[:100], 0o644)
	os.WriteFile(filepath.Join(dl, "holiday photos.zip"), []byte("PK not really"), 0o644)

	packs, _, pending := scanForPacks()
	if len(packs) != 2 || packs[0].Release != "2026-12" || packs[1].Release != "2026-10" {
		t.Fatalf("packs: %+v", packs)
	}
	if len(pending) != 2 {
		t.Fatalf("pending: %+v", pending)
	}
	kinds := func(s []PackInfo) string {
		var out []string
		for _, x := range s {
			out = append(out, x.Release+"/"+x.Kind)
		}
		return strings.Join(out, ",")
	}

	// New pilot: Oct full then Dec update. Wet Venom (Nov) isn't in Downloads.
	dcs := filepath.Join(tmp, "Saved Games", "DCS")
	lp := planLocal(packs, nil, nil)
	if kinds(lp.Steps) != "2026-10/full,2026-12/update" || lp.Latest != "2026-12" || !lp.Newer ||
		len(lp.Missing) != 1 || lp.Missing[0] != "F-16C_50/Wet Venom" || lp.Jets != 3 {
		t.Fatalf("fresh local plan: %s %+v", kinds(lp.Steps), lp)
	}
	if _, err := installLocal(ctx, lp, dcs, cache, noop); err != nil {
		t.Fatal(err)
	}
	m, _ := loadInstalledManifest(dcs)
	sr, _ := scanInstalled(filepath.Join(dcs, "Liveries"), m, cache, nil)
	if m.Release != "2026-12" || sr.OK != 3 || sr.Total != 4 {
		t.Fatalf("after local install: release %s, %d of %d ok", m.Release, sr.OK, sr.Total)
	}
	for _, f := range []string{"WetBoys-Liveries-2026-10-FULL.zip", "WetBoys-Liveries-2026-12-update.zip"} {
		if _, err := os.Stat(filepath.Join(dl, f)); err != nil {
			t.Errorf("pilot's own pack was removed: %s", f)
		}
	}

	// The Nov update turns up on Discord later: only that one is needed.
	copyFile(t, novZip, filepath.Join(dl, "WetBoys-Liveries-2026-11-update.zip"))
	packs, _, _ = scanForPacks()
	if lp = planLocal(packs, m, sr); kinds(lp.Steps) != "2026-11/update" || lp.Newer || lp.Jets != 1 {
		t.Fatalf("catch-up plan: %s %+v", kinds(lp.Steps), lp)
	}
	installLocal(ctx, lp, dcs, cache, noop)
	sr, _ = scanInstalled(filepath.Join(dcs, "Liveries"), m, cache, nil)
	if !sr.UpToDate {
		t.Fatalf("not green: %+v", sr)
	}
	if lp = planLocal(packs, m, sr); !lp.UpToDate || len(lp.Steps) != 0 {
		t.Fatalf("green pilot offered packs: %+v", lp)
	}

	// A broken jet is repaired from the pack that has it.
	repaint(t, filepath.Join(dcs, "Liveries", "F-16C_50", "Wet Venom"))
	sr, _ = scanInstalled(filepath.Join(dcs, "Liveries"), m, cache, nil)
	if lp = planLocal(packs, m, sr); kinds(lp.Steps) != "2026-11/update" {
		t.Fatalf("repair plan: %s", kinds(lp.Steps))
	}
}

// Online Update uses a pack already in Downloads instead of fetching it, and
// falls back to downloading if that copy turns out to be damaged.
func TestUpdateUsesPackInDownloads(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	t.Setenv("WETPAINT_DOWNLOADS", filepath.Join(tmp, "cache"))
	dl := filepath.Join(tmp, "Downloads")
	os.MkdirAll(dl, 0o755)
	t.Setenv("WETPAINT_PACKS_DIR", dl)
	master, site := filepath.Join(tmp, "master"), filepath.Join(tmp, "site")
	cache := loadHashCache()
	noop := func(string, int64, int64, string) {}
	ctx := context.Background()
	mkLivery(t, master, "FA-18C_hornet", "Swipers", "Swipers")
	mkLivery(t, master, "F-14B", "Wet Iron", "Wet Iron")
	if _, err := buildPack(BuildOptions{Source: master, Out: site, Release: "2026-10", Full: true}, cache, noop); err != nil {
		t.Fatal(err)
	}
	zipName := "WetBoys-Liveries-2026-10-FULL.zip"
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			hits++
		}
		http.FileServer(http.Dir(site)).ServeHTTP(w, r)
	}))
	defer srv.Close()
	f, err := fetchFeed(ctx, srv.URL+"/feed.json")
	if err != nil {
		t.Fatal(err)
	}

	// Saved from Discord with a different name: no download happens.
	copyFile(t, filepath.Join(site, zipName), filepath.Join(dl, "pack from discord.zip"))
	local, _, _ := scanForPacks()
	dcs := filepath.Join(tmp, "a", "DCS")
	ur, err := runUpdate(ctx, f, f.Packs, dcs, cache, local, noop)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 || len(ur.FromDownloads) != 1 {
		t.Fatalf("downloaded anyway: hits=%d from=%v", hits, ur.FromDownloads)
	}
	if _, err := os.Stat(filepath.Join(dl, "pack from discord.zip")); err != nil {
		t.Fatal("pilot's copy was deleted")
	}

	// Same size but damaged inside: Wet Paint downloads a fresh copy.
	b, _ := os.ReadFile(filepath.Join(site, zipName))
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	var off int64
	for _, zf := range zr.File {
		if strings.HasSuffix(zf.Name, "tex.dds") {
			off, _ = zf.DataOffset()
			break
		}
	}
	b[off+10] ^= 0xff
	os.WriteFile(filepath.Join(dl, "pack from discord.zip"), b, 0o644)
	local, _, _ = scanForPacks()
	dcs2 := filepath.Join(tmp, "b", "DCS")
	ur, err = runUpdate(ctx, f, f.Packs, dcs2, cache, local, noop)
	if err != nil {
		t.Fatalf("fallback download failed: %v", err)
	}
	if hits != 1 || len(ur.FromDownloads) != 0 {
		t.Fatalf("hits=%d from=%v", hits, ur.FromDownloads)
	}
	m, _ := loadInstalledManifest(dcs2)
	if sr, _ := scanInstalled(filepath.Join(dcs2, "Liveries"), m, cache, nil); !sr.UpToDate {
		t.Fatalf("not green after fallback: %+v", sr)
	}
}

type zentry struct {
	name string
	data string
	mod  time.Time
}

func mkZip(t *testing.T, p string, entries ...zentry) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: e.mod})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e.data))
	}
	zw.Close()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Ordinary livery zips, the way releases travel on Discord ("Livery Release
// 11426.zip" holding aircraft/livery folders, no wetpaint.json).
func TestPlainLiveryZips(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("WETPAINT_CONFIG", filepath.Join(tmp, "cfg"))
	dl := filepath.Join(tmp, "Downloads")
	t.Setenv("WETPAINT_PACKS_DIR", dl)
	dcs := filepath.Join(tmp, "Saved Games", "DCS")
	liv := filepath.Join(dcs, "Liveries")
	cache := loadHashCache()
	nop := func(int64, int64, string) {}
	nov := time.Date(2026, 11, 4, 21, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	lua := func(base string) string { return `livery = {{"x", DIFFUSE, "../` + base + `/tex", false};}` }

	rel := "Livery Release 11426"
	mkZip(t, filepath.Join(dl, rel+".zip"),
		zentry{rel + "/", "", nov},
		zentry{rel + "/F-14b/VF-32 Wet Iron - BROOM/description.lua", lua("VF-32 Wet Iron"), nov},
		zentry{rel + "/F-14b/VF-32 Wet Iron - BROOM/pilot.dds", "broom texture", nov},
		zentry{rel + "/CH-47F/Wet Bandits RAM/description.lua", lua("Wet Bandits"), nov},
		zentry{rel + "/CH-47F/Wet Bandits RAM/tex.dds", "ram texture", nov},
		zentry{rel + "/readme.txt", "hi", nov})
	// The same zip saved twice, a mod (has files outside livery folders),
	// a livery zip cut short, and one still downloading.
	b, _ := os.ReadFile(filepath.Join(dl, rel+".zip"))
	os.WriteFile(filepath.Join(dl, rel+" (1).zip"), b, 0o644)
	mkZip(t, filepath.Join(dl, "F-22A mod.zip"),
		zentry{"f-22a/entry.lua", "mod", nov},
		zentry{"f-22a/Liveries/F-22A/default/description.lua", lua("default"), nov})
	os.WriteFile(filepath.Join(dl, "Wet liveries Dec.zip"), b[:len(b)/2], 0o644)
	os.WriteFile(filepath.Join(dl, "Wet liveries Jan.zip.crdownload"), b[:50], 0o644)

	packs, plains, pending := scanForPacks()
	if len(packs) != 0 || len(plains) != 2 || len(pending) != 2 {
		t.Fatalf("packs=%d plains=%v pending=%+v", len(packs), plains, pending)
	}
	// CH-47F's base folder is on disk already; F-14b's isn't.
	os.MkdirAll(filepath.Join(liv, "CH-47F", "Wet Bandits"), 0o755)
	z, err := inspectPlain(plains[0], dcs, nil, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	if z.New != 2 || z.Work() != 2 || len(z.Liveries) != 2 {
		t.Fatalf("inspect: %+v", z)
	}
	byID := func(z *PlainZip) map[string]PlainLivery {
		m := map[string]PlainLivery{}
		for _, l := range z.Liveries {
			m[l.ID] = l
		}
		return m
	}
	if l := byID(z)["F-14b/VF-32 Wet Iron - BROOM"]; len(l.Needs) != 1 || l.Needs[0] != "VF-32 Wet Iron" {
		t.Fatalf("missing base folder not flagged: %+v", l)
	}
	if l := byID(z)["CH-47F/Wet Bandits RAM"]; len(l.Needs) != 0 {
		t.Fatalf("base folder on disk still flagged: %+v", l)
	}

	// Install: both liveries land in the right folders; the readme doesn't.
	r, err := installPlain(plains[0], dcs, nil, cache, nil, nop)
	if err != nil || len(r.Installed) != 2 || r.Files != 4 {
		t.Fatalf("install: %v %+v", err, r)
	}
	if got, _ := os.ReadFile(filepath.Join(liv, "F-14b", "VF-32 Wet Iron - BROOM", "pilot.dds")); string(got) != "broom texture" {
		t.Fatalf("file not installed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(liv, "readme.txt")); err == nil {
		t.Fatal("readme copied into Liveries")
	}
	if z, _ = inspectPlain(plains[0], dcs, nil, cache, nil); z.Current != 2 || z.Work() != 0 {
		t.Fatalf("not recognised as installed: %+v", z)
	}

	// An older zip with a different BROOM must not replace the newer one.
	mkZip(t, filepath.Join(dl, "old.zip"),
		zentry{"F-14b/VF-32 Wet Iron - BROOM/description.lua", lua("VF-32 Wet Iron"), oct},
		zentry{"F-14b/VF-32 Wet Iron - BROOM/pilot.dds", "OLD broom", oct})
	r, err = installPlain(filepath.Join(dl, "old.zip"), dcs, nil, cache, nil, nop)
	if err != nil || len(r.Installed) != 0 || len(r.Skipped) != 1 {
		t.Fatalf("older zip: %v %+v", err, r)
	}
	if got, _ := os.ReadFile(filepath.Join(liv, "F-14b", "VF-32 Wet Iron - BROOM", "pilot.dds")); string(got) != "broom texture" {
		t.Fatalf("older zip downgraded the livery: %q", got)
	}
	// A newer one does update it, and only the changed file is written.
	dec := nov.AddDate(0, 1, 0)
	mkZip(t, filepath.Join(dl, "dec.zip"),
		zentry{"F-14b/VF-32 Wet Iron - BROOM/description.lua", lua("VF-32 Wet Iron"), nov},
		zentry{"F-14b/VF-32 Wet Iron - BROOM/pilot.dds", "NEW broom", dec})
	if z, _ = inspectPlain(filepath.Join(dl, "dec.zip"), dcs, nil, cache, nil); z.Updates != 1 {
		t.Fatalf("update not seen: %+v", z)
	}
	if r, err = installPlain(filepath.Join(dl, "dec.zip"), dcs, nil, cache, nil, nop); err != nil || r.Files != 1 || r.Unchanged != 1 {
		t.Fatalf("update: %v %+v", err, r)
	}

	// A livery that belongs to the verified library is left to the verified packs.
	managed := &Manifest{Liveries: []Livery{{Aircraft: "CH-47F", Folder: "Wet Bandits RAM"}}}
	if z, _ = inspectPlain(plains[0], dcs, managed, cache, nil); byID(z)["CH-47F/Wet Bandits RAM"].State != "skipped" {
		t.Fatalf("managed livery not skipped: %+v", z)
	}

	// Hostile paths never leave the Liveries folder.
	mkZip(t, filepath.Join(dl, "evil.zip"),
		zentry{"x/../description.lua", "x", nov},
		zentry{"x/../../escape/description.lua", "x", nov})
	if r, err = installPlain(filepath.Join(dl, "evil.zip"), dcs, nil, cache, nil, nop); err == nil && len(r.Installed) != 0 {
		t.Fatalf("unsafe zip installed: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(tmp, "Saved Games", "escape")); err == nil {
		t.Fatal("wrote outside Liveries")
	}
}
