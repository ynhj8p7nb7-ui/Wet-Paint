package main

// system.go — finding DCS, finding packs, remembering settings.

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func configDir() string {
	if d := os.Getenv("WETPAINT_CONFIG"); d != "" {
		return d
	}
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "WetPaint")
}

type Settings struct {
	DCSDir    string `json:"dcs_dir"`
	LastPack  string `json:"last_pack"`
	BuildSrc  string `json:"build_src"`
	BuildOut  string `json:"build_out"`
	BuildPrev string `json:"build_prev"`
	BuildURL  string `json:"build_url"`
	FeedURL   string `json:"feed_url"`
	// InstallDir overrides DCS install auto-detection ("" = auto).
	InstallDir string `json:"install_dir"`
	// NoAutoCheck turns off the update check when Wet Paint opens.
	NoAutoCheck bool `json:"no_auto_check"`
}

func loadSettings() Settings {
	var s Settings
	if b, err := os.ReadFile(filepath.Join(configDir(), "settings.json")); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (s Settings) save() { _ = writeJSON(filepath.Join(configDir(), "settings.json"), s) }

// ---------------------------------------------------------------- DCS folders

type DCSDir struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	HasLiveries bool   `json:"has_liveries"`
	Release     string `json:"release"` // Wet Boys release installed, if any
	UsedBy      string `json:"used_by"` // e.g. "Steam" when a detected DCS install uses this folder
}

var dcsNameRe = regexp.MustCompile(`(?i)^DCS([._ -].*)?$`)

// findDCSDirs lists the DCS profile folders under Saved Games
// (DCS, DCS.openbeta, DCS.release_server, …).
func findDCSDirs() []DCSDir {
	var out []DCSDir
	used := map[string]string{}
	for _, in := range findDCSInstalls() {
		if _, ok := used[strings.ToLower(in.SavedGames)]; !ok {
			used[strings.ToLower(in.SavedGames)] = in.Kind
		}
	}
	sg := savedGamesDir()
	ents, _ := os.ReadDir(sg)
	for _, e := range ents {
		if !e.IsDir() || !dcsNameRe.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(sg, e.Name())
		d := describeDCSDir(p)
		d.UsedBy = used[strings.ToLower(e.Name())]
		out = append(out, d)
	}
	sort.SliceStable(out, func(a, b int) bool {
		// the folder a detected DCS install actually uses comes first
		if (out[a].UsedBy != "") != (out[b].UsedBy != "") {
			return out[a].UsedBy != ""
		}
		// plain "DCS" first, then ones that already have liveries
		if (out[a].Name == "DCS") != (out[b].Name == "DCS") {
			return out[a].Name == "DCS"
		}
		return out[a].HasLiveries && !out[b].HasLiveries
	})
	return out
}

func describeDCSDir(p string) DCSDir {
	d := DCSDir{Path: p, Name: filepath.Base(p)}
	if fi, err := os.Stat(filepath.Join(p, "Liveries")); err == nil && fi.IsDir() {
		d.HasLiveries = true
	}
	if m, err := loadInstalledManifest(p); err == nil {
		d.Release = m.Release
	}
	return d
}

// normalizeDCSDir lets people pick Saved Games\DCS, its Liveries folder, or
// Saved Games itself, and figures out what they meant.
func normalizeDCSDir(p string) string {
	p = filepath.Clean(strings.TrimSpace(strings.Trim(p, `"`)))
	if strings.EqualFold(filepath.Base(p), "Liveries") {
		return filepath.Dir(p)
	}
	if fi, err := os.Stat(filepath.Join(p, "DCS")); err == nil && fi.IsDir() {
		return filepath.Join(p, "DCS")
	}
	return p
}

// ---------------------------------------------------------------- finding packs

// PendingPack is a file that looks like a Wet Boys pack but can't be used
// yet: still downloading (Discord, Chrome, Edge, Firefox) or cut short.
type PendingPack struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// packDirs are the folders searched for packs: where browsers and Discord
// save downloads, the Desktop, and the folder WetPaint.exe sits in.
func packDirs(extra ...string) []string {
	if d := os.Getenv("WETPAINT_PACKS_DIR"); d != "" { // tests
		return append([]string{d}, extra...)
	}
	dirs := []string{downloadsDir(), desktopDir()}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	return append(dirs, extra...)
}

// looksLikePack reports whether a file name looks like one of Fridge's packs,
// including browser/Discord renames like "… (1).zip".
func looksLikePack(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "wetboys-liveries") || strings.Contains(l, "wetboys liveries") || strings.Contains(l, "wetpaint")
}

// Extensions browsers use while a download is still running.
var partialExts = map[string]bool{".crdownload": true, ".part": true, ".partial": true, ".download": true, ".opdownload": true, ".tmp": true}

// findPacks lists the Wet Boys packs in the usual download places.
func findPacks(extra ...string) []PackInfo {
	p, _, _ := scanForPacks(extra...)
	return p
}

// scanForPacks looks for zips that contain a wetpaint.json, newest first.
// The same pack saved twice ("pack.zip" and "pack (1).zip") is listed once.
// Ordinary livery zips (no wetpaint.json, see plain.go) are returned as plain.
// Files that look like a pack but can't be opened yet are returned as pending.
func scanForPacks(extra ...string) ([]PackInfo, []string, []PendingPack) {
	seenPath := map[string]bool{}
	seenPack := map[string]bool{}
	var out []PackInfo
	var plain []string
	var pending []PendingPack
	cutoff := time.Now().AddDate(-2, 0, 0)
	for _, d := range packDirs(extra...) {
		if d == "" {
			continue
		}
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		type cand struct {
			path string
			fi   os.FileInfo
		}
		var zips []cand
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := strings.ToLower(filepath.Ext(name))
			p := filepath.Join(d, name)
			if seenPath[strings.ToLower(p)] {
				continue
			}
			seenPath[strings.ToLower(p)] = true
			if partialExts[ext] && (looksLikePack(name) || looksLikeLiveries(name)) {
				pending = append(pending, PendingPack{name, p, "still downloading"})
				continue
			}
			if ext != ".zip" {
				continue
			}
			fi, err := e.Info()
			if err != nil || fi.ModTime().Before(cutoff) {
				continue
			}
			zips = append(zips, cand{p, fi})
		}
		// Newest first, so a busy Downloads folder can't push this month's pack
		// past the limit just because "WetBoys…" sorts late in the alphabet.
		sort.Slice(zips, func(a, b int) bool { return zips[a].fi.ModTime().After(zips[b].fi.ModTime()) })
		if len(zips) > 300 {
			zips = zips[:300]
		}
		for _, z := range zips {
			ok, opened := zipHasManifest(z.path)
			if !ok {
				if opened && isPlainLiveryZip(z.path) {
					plain = append(plain, z.path)
				} else if !opened && (looksLikePack(filepath.Base(z.path)) || looksLikeLiveries(filepath.Base(z.path))) {
					pending = append(pending, PendingPack{filepath.Base(z.path), z.path, "incomplete or damaged"})
				}
				continue
			}
			info, err := inspectPack(z.path)
			if err != nil {
				continue
			}
			key := info.Release + "|" + info.Kind + "|" + info.Created.UTC().Format(time.RFC3339Nano)
			if seenPack[key] {
				continue
			}
			seenPack[key] = true
			out = append(out, *info)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Created.After(out[b].Created) })
	return out, plain, pending
}

func isPlainLiveryZip(p string) bool {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return false
	}
	defer zr.Close()
	_, err = readPlainZip(&zr.Reader)
	return err == nil
}

// looksLikeLiveries guesses from a file name that it's a livery download.
func looksLikeLiveries(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "livery") || strings.Contains(l, "liveries") || strings.Contains(l, "wet")
}

// zipHasManifest reports whether p is a zip containing wetpaint.json, and
// whether it could be opened as a zip at all (a half-downloaded zip can't).
func zipHasManifest(p string) (has, opened bool) {
	zr, err := zip.OpenReader(p) // only reads the central directory — cheap even for 20 GB
	if err != nil {
		return false, false
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == manifestName {
			return true, true
		}
	}
	return false, true
}
