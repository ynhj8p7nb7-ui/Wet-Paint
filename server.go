package main

// server.go — the local UI. Wet Paint serves its interface on 127.0.0.1 only
// and opens it in a chromeless Edge window, so it looks and behaves like a
// normal desktop program with no runtime to install.

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed ui/index.html
var indexHTML string

//go:embed assets/icon-128.png
var iconPNG []byte

type Job struct {
	ID      int       `json:"id"`
	Kind    string    `json:"kind"` // install | scan | build
	Phase   string    `json:"phase"`
	Running bool      `json:"running"`
	Done    int64     `json:"done"`
	Total   int64     `json:"total"`
	Current string    `json:"current"`
	Result  any       `json:"result,omitempty"`
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
}

type App struct {
	mu       sync.Mutex
	token    string
	settings Settings
	cache    *HashCache
	job      Job
	lastScan *ScanResult
	lastPing time.Time
	byeAt    time.Time
	argPack  string
	cancel   context.CancelFunc
	feed     *Feed
	plan     *UpdatePlan
	feedErr  string
	names    *AircraftNames
	namesKey string
}

// selectedInstall is the DCS install whose aircraft names we check against:
// the one the pilot picked, else the one that uses the chosen Saved Games
// folder (e.g. Steam → Saved Games\DCS), else the first one found.
func (a *App) selectedInstall(dcsDir string, installs []DCSInstall) *DCSInstall {
	a.mu.Lock()
	manual := a.settings.InstallDir
	a.mu.Unlock()
	if manual != "" && isDCSInstall(manual) {
		in := describeInstall(manual, "Chosen")
		return &in
	}
	for i := range installs {
		if strings.EqualFold(installs[i].SavedGames, filepath.Base(dcsDir)) {
			return &installs[i]
		}
	}
	if len(installs) > 0 {
		return &installs[0]
	}
	return nil
}

func (a *App) aircraftNames(in *DCSInstall, dcsDir string) *AircraftNames {
	key := dcsDir
	if in != nil {
		key += "|" + in.Path
	}
	a.mu.Lock()
	if a.names != nil && a.namesKey == key {
		n := a.names
		a.mu.Unlock()
		return n
	}
	a.mu.Unlock()
	n := readAircraftNames(in, dcsDir)
	a.mu.Lock()
	a.names, a.namesKey = n, key
	a.mu.Unlock()
	return n
}

func newApp() *App {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	a := &App{token: hex.EncodeToString(b), settings: loadSettings(), cache: loadHashCache(), lastPing: time.Now()}
	if a.settings.DCSDir == "" {
		if ds := findDCSDirs(); len(ds) > 0 {
			a.settings.DCSDir = ds[0].Path
		}
	}
	return a
}

func (a *App) busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.job.Running
}

// startJob runs fn in the background; the UI polls /api/job.
func (a *App) startJob(kind string, fn func(ctx context.Context, set func(phase string, done, total int64, cur string)) (any, error)) error {
	a.mu.Lock()
	if a.job.Running {
		a.mu.Unlock()
		return errors.New("already working on something — wait for it to finish")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.job = Job{ID: a.job.ID + 1, Kind: kind, Running: true, Started: time.Now(), Phase: "Starting"}
	a.mu.Unlock()
	go func() {
		var last time.Time
		set := func(phase string, done, total int64, cur string) {
			if time.Since(last) < 50*time.Millisecond && done < total {
				return
			}
			last = time.Now()
			a.mu.Lock()
			a.job.Phase, a.job.Done, a.job.Total, a.job.Current = phase, done, total, cur
			a.mu.Unlock()
		}
		res, err := func() (res any, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("internal error: %v", r)
				}
			}()
			return fn(ctx, set)
		}()
		cancel()
		a.mu.Lock()
		a.job.Running = false
		a.job.Result = res
		if errors.Is(err, context.Canceled) {
			a.job.Error = "Stopped. Run it again to pick up where it left off."
		} else if err != nil {
			a.job.Error = err.Error()
		}
		a.mu.Unlock()
	}()
	return nil
}

func (a *App) doScan(set func(string, int64, int64, string)) (*ScanResult, error) {
	a.mu.Lock()
	dcs := a.settings.DCSDir
	a.mu.Unlock()
	m, err := loadInstalledManifest(dcs)
	if err != nil {
		return nil, err
	}
	set("Verifying liveries", 0, 1, "")
	res, err := scanInstalled(filepath.Join(dcs, "Liveries"), m, a.cache, func(d, t int64, c string) { set("Verifying liveries", d, t, c) })
	if err == nil {
		a.mu.Lock()
		a.lastScan = res
		a.mu.Unlock()
		_ = writeJSON(filepath.Join(dcs, "WetPaint", "last-scan.json"), res)
	}
	return res, err
}

// ---------------------------------------------------------------- HTTP

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, strings.Replace(indexHTML, "__TOKEN__", a.token, 1))
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(iconPNG)
	})
	mux.HandleFunc("/api/alive", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "wetpaint") })

	api := func(path string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			// Only our own page (which has the token) may drive the API.
			if r.Header.Get("X-Token") != a.token && r.URL.Query().Get("t") != a.token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if path != "/api/bye" {
				a.mu.Lock()
				a.lastPing = time.Now()
				a.mu.Unlock()
			}
			v, err := fn(r)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(v)
		})
	}
	body := func(r *http.Request, v any) error {
		return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
	}

	api("/api/state", func(r *http.Request) (any, error) {
		a.mu.Lock()
		dcs, argPack, last := a.settings.DCSDir, a.argPack, a.lastScan
		a.mu.Unlock()
		a.mu.Lock()
		plan, feedErr, feed := a.plan, a.feedErr, a.feed
		a.mu.Unlock()
		st := map[string]any{"version": version, "os": runtime.GOOS, "saved_games": savedGamesDir(),
			"dcs_dirs": findDCSDirs(), "dcs_dir": dcs, "arg_pack": argPack, "settings": a.settings,
			"feed_url": feedURL(a.settings), "auto_check": !a.settings.NoAutoCheck, "plan": plan, "feed_error": feedErr}
		installs := findDCSInstalls()
		st["installs"] = installs
		in := a.selectedInstall(dcs, installs)
		st["install"] = in
		if dcs != "" {
			st["dcs"] = describeDCSDir(dcs)
			var lib *Manifest
			if plan != nil && feed != nil {
				lib = feed.Library
			} else if m, err := loadInstalledManifest(dcs); err == nil {
				lib = m
			}
			if in != nil && lib != nil {
				names := a.aircraftNames(in, dcs)
				st["aircraft_known"] = names.Count
				if names.Count > 0 {
					st["aircraft_issues"] = checkAircraft(lib, names.Names)
				}
			}
			if m, err := loadInstalledManifest(dcs); err == nil {
				st["installed"] = map[string]any{"release": m.Release, "created": m.Created, "liveries": len(m.Liveries), "bytes": m.TotalSize()}
				if last == nil {
					var s ScanResult
					if b, err := os.ReadFile(filepath.Join(dcs, "WetPaint", "last-scan.json")); err == nil && json.Unmarshal(b, &s) == nil && s.Release == m.Release {
						last = &s
					}
				}
			} else {
				last = nil
			}
			st["last_scan"] = last
		}
		return st, nil
	})

	api("/api/packs", func(r *http.Request) (any, error) {
		var extra []string
		if p := a.settings.LastPack; p != "" {
			extra = append(extra, filepath.Dir(p))
		}
		packs := findPacks(extra...)
		if packs == nil {
			packs = []PackInfo{}
		}
		return packs, nil
	})

	api("/api/dcs", func(r *http.Request) (any, error) {
		var q struct{ Path string }
		if err := body(r, &q); err != nil {
			return nil, err
		}
		p := normalizeDCSDir(q.Path)
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s doesn't exist", p)
		}
		a.mu.Lock()
		a.settings.DCSDir = p
		a.lastScan, a.plan = nil, nil
		a.settings.save()
		a.mu.Unlock()
		return describeDCSDir(p), nil
	})

	api("/api/pick", func(r *http.Request) (any, error) {
		var q struct{ Kind, Start string }
		if err := body(r, &q); err != nil {
			return nil, err
		}
		var p string
		var err error
		switch q.Kind {
		case "install":
			p, err = pickFolder(`Pick your DCS World install folder (Steam: …\steamapps\common\DCSWorld)`, q.Start)
		case "dcs":
			p, err = pickFolder(`Pick your DCS Saved Games folder (e.g. Saved Games\DCS)`, firstNonEmpty(q.Start, savedGamesDir()))
		case "pack":
			p, err = pickFile("Pick the Wet Boys livery pack (.zip)", firstNonEmpty(q.Start, downloadsDir()), "Livery pack (*.zip)|*.zip")
		case "src":
			p, err = pickFolder("Pick your master Liveries folder", q.Start)
		case "out":
			p, err = pickFolder("Where should the pack be saved?", q.Start)
		case "prev":
			p, err = pickFile("Pick last month's library.json", q.Start, "Library file (*.json)|*.json")
		default:
			return nil, errors.New("unknown picker")
		}
		return map[string]string{"path": p}, err
	})

	api("/api/pack", func(r *http.Request) (any, error) {
		var q struct{ Path string }
		if err := body(r, &q); err != nil {
			return nil, err
		}
		info, err := inspectPack(strings.Trim(strings.TrimSpace(q.Path), `"`))
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.settings.LastPack = info.Path
		a.settings.save()
		dcs := a.settings.DCSDir
		a.mu.Unlock()
		out := map[string]any{"pack": info}
		if have, err := loadInstalledManifest(dcs); err == nil {
			out["installed_release"] = have.Release
			out["older"] = have.Created.After(info.Created)
		}
		return out, nil
	})

	api("/api/install", func(r *http.Request) (any, error) {
		var q struct{ Pack string }
		if err := body(r, &q); err != nil {
			return nil, err
		}
		a.mu.Lock()
		dcs := a.settings.DCSDir
		a.mu.Unlock()
		if dcs == "" {
			return nil, errors.New("pick your DCS Saved Games folder first")
		}
		return nil, a.startJob("install", func(ctx context.Context, set func(string, int64, int64, string)) (any, error) {
			ir, _, err := installPack(q.Pack, dcs, a.cache, func(d, t int64, c string) { set("Installing liveries", d, t, c) })
			if err != nil {
				return nil, err
			}
			sr, err := a.doScan(set)
			return map[string]any{"install": ir, "scan": sr}, err
		})
	})

	api("/api/scan", func(r *http.Request) (any, error) {
		return nil, a.startJob("scan", func(ctx context.Context, set func(string, int64, int64, string)) (any, error) {
			sr, err := a.doScan(set)
			return map[string]any{"scan": sr}, err
		})
	})

	api("/api/build", func(r *http.Request) (any, error) {
		var o BuildOptions
		if err := body(r, &o); err != nil {
			return nil, err
		}
		if fi, err := os.Stat(o.Source); err != nil || !fi.IsDir() {
			return nil, errors.New("pick your master Liveries folder")
		}
		a.mu.Lock()
		a.settings.BuildSrc, a.settings.BuildOut, a.settings.BuildURL = o.Source, o.Out, o.URL
		a.settings.save()
		a.mu.Unlock()
		return nil, a.startJob("build", func(ctx context.Context, set func(string, int64, int64, string)) (any, error) {
			res, err := buildPack(o, a.cache, set)
			if err == nil && res.FeedPath != "" {
				a.mu.Lock()
				a.settings.BuildPrev = res.FeedPath
				a.settings.save()
				a.mu.Unlock()
			}
			return res, err
		})
	})

	// ---- online updates
	api("/api/check", func(r *http.Request) (any, error) {
		a.mu.Lock()
		dcs, fu := a.settings.DCSDir, feedURL(a.settings)
		a.mu.Unlock()
		if dcs == "" {
			return nil, errors.New("pick your DCS Saved Games folder first")
		}
		return nil, a.startJob("check", func(ctx context.Context, set func(string, int64, int64, string)) (any, error) {
			a.mu.Lock()
			a.names = nil
			a.mu.Unlock()
			set("Checking flywetboys.com for updates", 0, 0, "")
			f, err := fetchFeed(ctx, fu)
			a.mu.Lock()
			a.feed, a.plan, a.feedErr = f, nil, ""
			if err != nil {
				a.feedErr = err.Error()
			}
			a.mu.Unlock()
			if err != nil {
				return map[string]any{"offline": true, "message": err.Error()}, nil
			}
			_, instErr := loadInstalledManifest(dcs)
			scan, err := scanInstalled(filepath.Join(dcs, "Liveries"), f.Library, a.cache, func(d, t int64, c string) { set("Checking your liveries", d, t, c) })
			if err != nil {
				return nil, err
			}
			plan := makePlan(f, scan, instErr != nil)
			if plan.App != nil && !newerVersion(plan.App.Version, version) {
				plan.App = nil
			}
			a.mu.Lock()
			a.plan = plan
			if instErr == nil {
				a.lastScan = scan
			}
			a.mu.Unlock()
			return map[string]any{"plan": plan}, nil
		})
	})

	api("/api/update", func(r *http.Request) (any, error) {
		var q struct{ Mode string }
		if err := body(r, &q); err != nil {
			return nil, err
		}
		a.mu.Lock()
		dcs, f, plan := a.settings.DCSDir, a.feed, a.plan
		a.mu.Unlock()
		if f == nil || plan == nil {
			return nil, errors.New("check for updates first")
		}
		steps := plan.Steps
		if q.Mode == "full" {
			if plan.FullOption == nil {
				return nil, errors.New("no full pack has been published online yet")
			}
			steps = plan.FullOption.Steps
		}
		return nil, a.startJob("update", func(ctx context.Context, set func(string, int64, int64, string)) (any, error) {
			ur, err := runUpdate(ctx, f, steps, dcs, a.cache, set)
			if err != nil {
				return nil, err
			}
			sr, err := a.doScan(set)
			ur.Scan = sr
			a.mu.Lock()
			if a.plan != nil && sr != nil {
				a.plan = makePlan(f, sr, false)
				a.plan.App = plan.App
			}
			a.mu.Unlock()
			return map[string]any{"update": ur, "scan": sr}, err
		})
	})

	api("/api/install-dir", func(r *http.Request) (any, error) {
		var q struct{ Path string }
		if err := body(r, &q); err != nil {
			return nil, err
		}
		p := strings.Trim(strings.TrimSpace(q.Path), `"`)
		if p != "" {
			p = filepath.Clean(p)
			if strings.EqualFold(filepath.Base(p), "bin") || strings.EqualFold(filepath.Base(p), "bin-mt") {
				p = filepath.Dir(p)
			}
			if !isDCSInstall(p) {
				return nil, fmt.Errorf("%s doesn't look like a DCS World install (no CoreMods/Bazar folders) — pick the folder that contains DCS's bin folder", p)
			}
		}
		a.mu.Lock()
		a.settings.InstallDir = p
		a.names = nil
		a.settings.save()
		a.mu.Unlock()
		return true, nil
	})

	api("/api/cancel", func(r *http.Request) (any, error) {
		a.mu.Lock()
		if a.cancel != nil && a.job.Running {
			a.cancel()
		}
		a.mu.Unlock()
		return true, nil
	})

	api("/api/settings", func(r *http.Request) (any, error) {
		var q struct {
			AutoCheck *bool `json:"auto_check"`
		}
		if err := body(r, &q); err != nil {
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if q.AutoCheck != nil {
			a.settings.NoAutoCheck = !*q.AutoCheck
		}
		a.settings.save()
		return a.settings, nil
	})

	api("/api/job", func(r *http.Request) (any, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.job, nil
	})

	api("/api/reveal", func(r *http.Request) (any, error) {
		var q struct{ Path string }
		if err := body(r, &q); err != nil {
			return nil, err
		}
		revealPath(q.Path)
		return true, nil
	})

	api("/api/ping", func(r *http.Request) (any, error) { return true, nil })
	api("/api/bye", func(r *http.Request) (any, error) {
		a.mu.Lock()
		a.byeAt = time.Now()
		a.mu.Unlock()
		return true, nil
	})
	return mux
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------- run

type instanceFile struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func runGUI(pack string, openUI bool) error {
	instPath := filepath.Join(configDir(), "instance.json")

	// Already running? Just bring up another window on it.
	if b, err := os.ReadFile(instPath); err == nil && openUI {
		var inst instanceFile
		if json.Unmarshal(b, &inst) == nil && inst.Port > 0 {
			c := http.Client{Timeout: time.Second}
			if resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/alive", inst.Port)); err == nil {
				resp.Body.Close()
				u := fmt.Sprintf("http://127.0.0.1:%d/", inst.Port)
				if pack != "" {
					u += "?pack=" + urlEscape(pack)
				}
				fmt.Println("Wet Paint is already running — opening it in your browser.")
				openWindow(u)
				return nil
			}
		}
	}

	a := newApp()
	a.argPack = pack
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = writeJSON(instPath, instanceFile{port, a.token})
	defer os.Remove(instPath)

	srv := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	fmt.Printf("\n  WET PAINT %s  //  Wet Boys CVW-0 livery manager\n\n", version)
	fmt.Println("  Wet Paint is open in your web browser at:")
	fmt.Println("    " + url)
	fmt.Println("\n  It only talks to this PC and to flywetboys.com.")
	fmt.Println("  Leave this window open while it works. Close it to quit.")

	var closed <-chan struct{}
	if openUI {
		closed = openWindow(url)
	}
	for {
		time.Sleep(time.Second)
		a.mu.Lock()
		idle := !a.job.Running
		sinceBye := !a.byeAt.IsZero() && a.lastPing.Before(a.byeAt) && time.Since(a.byeAt) > 4*time.Second
		stale := time.Since(a.lastPing) > 10*time.Minute
		a.mu.Unlock()
		if closed != nil {
			select {
			case <-closed:
				// The window closed. If a job is running, keep going until it finishes
				// so we never leave a half-written livery behind.
				for a.busy() {
					time.Sleep(500 * time.Millisecond)
				}
				return nil
			default:
			}
		}
		if idle && openUI && (sinceBye || stale) {
			return nil
		}
	}
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
