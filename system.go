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

// findPacks looks in the usual places a pilot's browser drops a download, plus
// next to WetPaint.exe, for zips that contain a wetpaint.json.
func findPacks(extra ...string) []PackInfo {
	home, _ := os.UserHomeDir()
	dirs := []string{downloadsDir(), filepath.Join(home, "Desktop")}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	dirs = append(dirs, extra...)
	seen := map[string]bool{}
	var out []PackInfo
	cutoff := time.Now().AddDate(-2, 0, 0)
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		checked := 0
		for _, e := range ents {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".zip") {
				continue
			}
			p := filepath.Join(d, e.Name())
			key := strings.ToLower(p)
			if seen[key] {
				continue
			}
			seen[key] = true
			fi, err := e.Info()
			if err != nil || fi.ModTime().Before(cutoff) {
				continue
			}
			if checked++; checked > 300 {
				break
			}
			if !zipHasManifest(p) {
				continue
			}
			if info, err := inspectPack(p); err == nil {
				out = append(out, *info)
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created.After(out[b].Created) })
	return out
}

func zipHasManifest(p string) bool {
	zr, err := zip.OpenReader(p) // only reads the central directory — cheap even for 20 GB
	if err != nil {
		return false
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == manifestName {
			return true
		}
	}
	return false
}
