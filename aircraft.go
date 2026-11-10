package main

// aircraft.go — making sure the aircraft folder names match what DCS expects.
//
// There is no official list from Eagle Dynamics. The real authority is the
// DCS install itself: every aircraft ships its default liveries in a folder
// whose name is the one Saved Games\DCS\Liveries\<name> must use. So on a
// pilot's PC Wet Paint finds DCS (Steam or standalone) and reads those names.
// Fridge builds on a Mac with no DCS, so the builder falls back to a built-in
// community list (Hoggit wiki) and flags likely typos.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type DCSInstall struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`        // Steam | Standalone | Chosen
	SavedGames string `json:"saved_games"` // folder name under Saved Games: DCS or DCS.<variant>
}

// isDCSInstall checks a folder looks like a DCS World install.
func isDCSInstall(p string) bool {
	for _, sub := range []string{"CoreMods", "Bazar"} {
		if fi, err := os.Stat(filepath.Join(p, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

func describeInstall(p, kind string) DCSInstall {
	in := DCSInstall{Path: p, Kind: kind, SavedGames: "DCS"}
	// dcs_variant.txt (e.g. "openbeta") makes DCS use Saved Games\DCS.<variant>
	if f, err := os.Open(filepath.Join(p, "dcs_variant.txt")); err == nil {
		sc := bufio.NewScanner(f)
		if sc.Scan() {
			if v := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff")); v != "" {
				in.SavedGames = "DCS." + v
			}
		}
		f.Close()
	}
	return in
}

// findDCSInstalls returns every DCS World install we can find (platform code
// supplies the candidates: registry, Steam libraries, default folders).
func findDCSInstalls() []DCSInstall {
	var out []DCSInstall
	seen := map[string]bool{}
	add := func(p, kind string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		k := strings.ToLower(p)
		if seen[k] || !isDCSInstall(p) {
			return
		}
		seen[k] = true
		out = append(out, describeInstall(p, kind))
	}
	if p := os.Getenv("WETPAINT_DCS_INSTALL"); p != "" {
		// Pinned to one install (tests, or someone with an odd setup):
		// don't also pick up whatever DCS is on this PC.
		add(p, "Standalone")
		return out
	}
	for _, c := range installCandidates() {
		add(c.path, c.kind)
	}
	return out
}

type installCandidate struct{ path, kind string }

var vdfPathRe = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// steamLibraries reads Steam's libraryfolders.vdf and returns every library folder.
func steamLibraries(steamRoot string) []string {
	libs := []string{steamRoot}
	b, err := os.ReadFile(filepath.Join(steamRoot, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libs
	}
	for _, m := range vdfPathRe.FindAllStringSubmatch(string(b), -1) {
		libs = append(libs, strings.ReplaceAll(m[1], `\\`, `\`))
	}
	return libs
}

// ---------------------------------------------------------------- names from the install

type AircraftNames struct {
	Install *DCSInstall       `json:"install"`
	Names   map[string]string `json:"-"` // lower(name) → name
	Count   int               `json:"count"`
}

// readAircraftNames collects every livery folder name DCS itself uses.
func readAircraftNames(in *DCSInstall, dcsDir string) *AircraftNames {
	an := &AircraftNames{Install: in, Names: map[string]string{}}
	addDir := func(dir string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			n := e.Name()
			if !e.IsDir() {
				if !strings.EqualFold(filepath.Ext(n), ".zip") {
					continue
				}
				n = strings.TrimSuffix(n, filepath.Ext(n))
			}
			if isJunk(n) || strings.HasPrefix(n, ".") {
				continue
			}
			an.Names[strings.ToLower(n)] = n
		}
	}
	modLiveries := func(root string) {
		mods, _ := os.ReadDir(root)
		for _, m := range mods {
			if m.IsDir() {
				addDir(filepath.Join(root, m.Name(), "Liveries"))
			}
		}
	}
	if in != nil {
		addDir(filepath.Join(in.Path, "Bazar", "Liveries"))
		modLiveries(filepath.Join(in.Path, "CoreMods", "aircraft"))
		modLiveries(filepath.Join(in.Path, "Mods", "aircraft"))
	}
	if dcsDir != "" {
		modLiveries(filepath.Join(dcsDir, "Mods", "aircraft")) // third-party mods live in Saved Games
	}
	an.Count = len(an.Names)
	return an
}

// ---------------------------------------------------------------- checking a library

type AircraftIssue struct {
	Aircraft string   `json:"aircraft"`
	Liveries []string `json:"liveries"`
	Suggest  string   `json:"suggest,omitempty"` // a DCS name it's probably meant to be
	Level    string   `json:"level"`             // error = name looks wrong; info = aircraft not installed here
}

// checkAircraft compares a library's aircraft folders with the names DCS knows.
func checkAircraft(lib *Manifest, known map[string]string) []AircraftIssue {
	if lib == nil || len(known) == 0 {
		return nil
	}
	by := map[string]*AircraftIssue{}
	var order []string
	for _, l := range lib.Liveries {
		if _, ok := known[strings.ToLower(l.Aircraft)]; ok {
			continue
		}
		is := by[l.Aircraft]
		if is == nil {
			is = &AircraftIssue{Aircraft: l.Aircraft, Level: "info"}
			if s := nearestName(l.Aircraft, known); s != "" {
				is.Suggest, is.Level = s, "error"
			}
			by[l.Aircraft] = is
			order = append(order, l.Aircraft)
		}
		is.Liveries = append(is.Liveries, l.Folder)
	}
	out := make([]AircraftIssue, 0, len(order))
	for _, a := range order {
		out = append(out, *by[a])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level == "error" && out[j].Level != "error" })
	return out
}

func normName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == ' ' || r == '.' {
			return -1
		}
		return r
	}, strings.ToLower(s))
}

// nearestName finds a known name that differs only by punctuation or by a
// small typo — "FA18C_hornet", "F-16C-50", "F-14B " → the real DCS name.
func nearestName(name string, known map[string]string) string {
	n := normName(name)
	best, bestD := "", 3
	for _, k := range known {
		kn := normName(k)
		if kn == n {
			return k
		}
		if len(n) < 4 || len(kn) < 4 {
			continue
		}
		if d := editDistance(n, kn); d < bestD {
			best, bestD = k, d
		}
	}
	if bestD <= 1 || (bestD == 2 && len(n) >= 8) {
		return best
	}
	return ""
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// ---------------------------------------------------------------- built-in list (Fridge side)

// Community-maintained list of DCS livery folder names (Hoggit wiki,
// wiki.hoggitworld.com/view/Liveries). Used only where no DCS install is
// available to read — i.e. when Fridge builds on his Mac. Unknown names are
// reported as "info" (fine for mods); near-misses as likely typos.
var builtinAircraft = func() map[string]string {
	names := []string{"A-10A", "A-10C", "A-10CII", "AH-64D_BLK_II", "AJS37", "AV8BNA", "Bf-109K-4", "C-101CC", "C-101EB",
		"C-130J-30", "CH-47F", "Christen Eagle II", "F-14A-135-GR", "f-14b", "F-15C", "F-15ESE", "F-16C_50", "F-4E-45MC", "F-5E-3",
		"f-86f sabre", "FA-18C_hornet", "FW-190A8", "FW-190D9", "I-16", "J-11A", "JF-17", "ka-50", "Ka-50_3", "L-39C", "L-39ZA",
		"M-2000C", "MB-339A", "MB-339APAN", "Mi-24P", "mi-8mt", "MiG-15bis", "MiG-19P", "MiG-21bis", "MiG-29-Fulcrum", "mig-29a",
		"mig-29g", "mig-29s", "Mirage-F1BE", "Mirage-F1CE", "Mirage-F1EE", "MosquitoFBMkVI", "OH-58D", "P-47D-30", "P-51D",
		"SA342L", "SA342M", "SA342Minigun", "SA342Mistral", "SpitfireLFMkIX", "SpitfireLFMkIXCW", "su-25", "su-25T", "su-27",
		"Su-33", "TF-51D", "uh-1h", "Yak-52"}
	m := make(map[string]string, len(names))
	for _, n := range names {
		m[strings.ToLower(n)] = n
	}
	return m
}()

// checkSourceAircraft adds builder warnings for aircraft folder names.
func checkSourceAircraft(lib *Manifest) []Warning {
	var w []Warning
	for _, is := range checkAircraft(lib, builtinAircraft) {
		lv := strings.Join(is.Liveries, ", ")
		if is.Suggest != "" {
			w = append(w, Warning{is.Aircraft, "error", `Aircraft folder "` + is.Aircraft + `" looks like a typo of the DCS name "` + is.Suggest + `" — DCS won't find these liveries: ` + lv})
		} else {
			w = append(w, Warning{is.Aircraft, "info", `"` + is.Aircraft + `" isn't a standard DCS aircraft folder name. Fine if it's a mod's own name; otherwise double-check the spelling. Liveries: ` + lv})
		}
	}
	return w
}
