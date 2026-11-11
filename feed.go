package main

// feed.go — online updates.
//
// Fridge uploads two files to the website each month:
//
//	WetBoys-Liveries-<release>-update.zip   (or -FULL.zip)
//	feed.json                                every published pack + the full library index
//
// Wet Paint downloads feed.json, checks the pilot's Liveries folder against the
// library index, works out the cheapest set of packs that fixes everything
// (e.g. "full pack + the two monthly updates since"), then downloads them
// (resumable, SHA-256 verified), installs them in order and re-verifies.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultFeedURL = "https://flywetboys.com/downloads/wetpaint/feed.json"
const feedFormat = 1

type FeedPack struct {
	Release  string    `json:"release"`
	Kind     string    `json:"kind"` // update | full
	URL      string    `json:"url"`  // relative to feed.json, or absolute
	Bytes    int64     `json:"bytes"`
	SHA256   string    `json:"sha256"`
	Created  time.Time `json:"created"`
	Liveries []string  `json:"liveries"`
}

type FeedApp struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Notes   string `json:"notes,omitempty"`
}

type Feed struct {
	Format  int        `json:"format"`
	Updated time.Time  `json:"updated"`
	Latest  string     `json:"latest"`
	App     *FeedApp   `json:"app,omitempty"`
	Packs   []FeedPack `json:"packs"`
	Library *Manifest  `json:"library"`
}

func feedURL(s Settings) string {
	if u := os.Getenv("WETPAINT_FEED"); u != "" {
		return u
	}
	if s.FeedURL != "" {
		return s.FeedURL
	}
	return defaultFeedURL
}

// ---------------------------------------------------------------- Fridge side

// updateFeed adds this build's pack to feed.json in the output folder (creating
// it if needed) and replaces the library index with the new one.
func updateFeed(out string, lib *Manifest, pack FeedPack) (string, error) {
	p := filepath.Join(out, "feed.json")
	f := &Feed{Format: feedFormat}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, f); err != nil {
			return "", fmt.Errorf("existing feed.json is damaged: %w", err)
		}
	}
	keep := f.Packs[:0]
	for _, x := range f.Packs {
		if !(x.Release == pack.Release && x.Kind == pack.Kind) {
			keep = append(keep, x)
		}
	}
	f.Packs = append(keep, pack)
	sort.SliceStable(f.Packs, func(a, b int) bool { return f.Packs[a].Created.Before(f.Packs[b].Created) })
	f.Format, f.Updated, f.Latest, f.Library = feedFormat, time.Now().UTC(), lib.Release, lib
	return p, writeJSON(p, f)
}

// ---------------------------------------------------------------- fetching

var httpClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 30 * time.Second,
	IdleConnTimeout:       60 * time.Second,
	TLSHandshakeTimeout:   15 * time.Second,
}}

func newRequest(ctx context.Context, u string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err == nil {
		req.Header.Set("User-Agent", "WetPaint/"+version)
	}
	return req, err
}

func fetchFeed(ctx context.Context, u string) (*Feed, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := newRequest(ctx, u)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach %s — check your internet connection", hostOf(u))
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errNoFeed
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered %s", hostOf(u), resp.Status)
	}
	var f Feed
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&f); err != nil {
		return nil, fmt.Errorf("the update feed is damaged: %w", err)
	}
	if f.Format == 0 || f.Format > feedFormat || f.Library == nil {
		return nil, errors.New("the update feed needs a newer Wet Paint — download the latest from flywetboys.com")
	}
	base, _ := url.Parse(u)
	for i := range f.Packs {
		if r, err := url.Parse(f.Packs[i].URL); err == nil {
			f.Packs[i].URL = directLink(base.ResolveReference(r).String())
		}
	}
	if f.App != nil && f.App.URL != "" {
		if r, err := url.Parse(f.App.URL); err == nil {
			f.App.URL = base.ResolveReference(r).String()
		}
	}
	return &f, nil
}

var errNoFeed = errors.New("no online updates have been published yet")

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

var driveFileRe = regexp.MustCompile(`drive\.google\.com/(?:file/d/|open\?id=|uc\?(?:.*&)?id=)([\w-]{20,})`)

// directLink turns a Google Drive share link into a direct download, so Fridge
// can host the big full pack on Drive if he wants to.
func directLink(u string) string {
	if m := driveFileRe.FindStringSubmatch(u); m != nil {
		return "https://drive.usercontent.google.com/download?id=" + m[1] + "&export=download&confirm=t"
	}
	return u
}

// ---------------------------------------------------------------- planning

type UpdatePlan struct {
	Latest     string      `json:"latest"`
	Scan       *ScanResult `json:"scan"`
	Steps      []FeedPack  `json:"steps"`
	Bytes      int64       `json:"bytes"`
	UpToDate   bool        `json:"up_to_date"`
	FirstTime  bool        `json:"first_time"`
	Missing    []string    `json:"missing,omitempty"` // liveries no published pack can provide
	FullOption *FullOption `json:"full_option,omitempty"`
	App        *FeedApp    `json:"app,omitempty"`
}

// FullOption is the "just download everything" alternative, offered to
// first-time pilots or anyone who wants a clean re-download.
type FullOption struct {
	Steps []FeedPack `json:"steps"`
	Bytes int64      `json:"bytes"`
}

// planDownloads picks the cheapest set of packs that brings every livery in
// `need` (livery ID → release it needs) up to date. A full pack of release F
// provides any livery whose needed release is ≤ F; an update pack of release R
// provides exactly the liveries last changed in R.
func planDownloads(f *Feed, need map[string]string, forceFull bool) ([]FeedPack, int64, []string) {
	rank := releaseRanker(f)
	updates := map[string]FeedPack{}
	var fulls []FeedPack
	for _, p := range f.Packs {
		switch p.Kind {
		case "full":
			fulls = append(fulls, p)
		default:
			if old, ok := updates[p.Release]; !ok || p.Created.After(old.Created) {
				updates[p.Release] = p
			}
		}
	}
	releases := map[string]bool{}
	for _, r := range need {
		releases[r] = true
	}
	try := func(full *FeedPack) ([]FeedPack, int64, []string) {
		var steps []FeedPack
		var bytes int64
		var missing []string
		if full != nil {
			steps, bytes = append(steps, *full), full.Bytes
		}
		var rs []string
		for r := range releases {
			if full != nil && rank(r) <= rank(full.Release) {
				continue
			}
			rs = append(rs, r)
		}
		sort.Slice(rs, func(a, b int) bool { return rank(rs[a]) < rank(rs[b]) })
		for _, r := range rs {
			if u, ok := updates[r]; ok {
				steps, bytes = append(steps, u), bytes+u.Bytes
			} else {
				for id, nr := range need {
					if nr == r {
						missing = append(missing, id)
					}
				}
			}
		}
		sort.Strings(missing)
		return steps, bytes, missing
	}
	var best []FeedPack
	var bestBytes int64 = -1
	var bestMissing []string
	consider := func(s []FeedPack, b int64, m []string) {
		better := bestBytes < 0 || len(m) < len(bestMissing) || (len(m) == len(bestMissing) && b < bestBytes)
		if better {
			best, bestBytes, bestMissing = s, b, m
		}
	}
	if !forceFull {
		consider(try(nil))
	}
	for i := range fulls {
		consider(try(&fulls[i]))
	}
	if bestBytes < 0 {
		return nil, 0, nil
	}
	return best, bestBytes, bestMissing
}

// releaseRanker orders release labels by when their packs were published,
// falling back to label order (YYYY-MM sorts naturally).
func releaseRanker(f *Feed) func(string) int {
	type rc struct {
		r string
		t time.Time
	}
	seen := map[string]time.Time{}
	for _, p := range f.Packs {
		if t, ok := seen[p.Release]; !ok || p.Created.Before(t) {
			seen[p.Release] = p.Created
		}
	}
	for _, l := range f.Library.Liveries {
		if _, ok := seen[l.Updated]; !ok {
			seen[l.Updated] = time.Time{}
		}
	}
	var all []rc
	for r, t := range seen {
		all = append(all, rc{r, t})
	}
	sort.Slice(all, func(a, b int) bool {
		if !all[a].t.Equal(all[b].t) && !all[a].t.IsZero() && !all[b].t.IsZero() {
			return all[a].t.Before(all[b].t)
		}
		return all[a].r < all[b].r
	})
	idx := map[string]int{}
	for i, x := range all {
		idx[x.r] = i
	}
	return func(r string) int { return idx[r] }
}

func makePlan(f *Feed, scan *ScanResult, firstTime bool) *UpdatePlan {
	p := &UpdatePlan{Latest: f.Latest, Scan: scan, FirstTime: firstTime, App: f.App}
	need := map[string]string{}
	all := map[string]string{}
	for _, l := range scan.Liveries {
		all[l.ID] = l.Updated
		if l.State != "ok" {
			need[l.ID] = l.Updated
		}
	}
	p.UpToDate = len(need) == 0
	if !p.UpToDate {
		p.Steps, p.Bytes, p.Missing = planDownloads(f, need, false)
	}
	if steps, bytes, missing := planDownloads(f, all, true); len(steps) > 0 && len(missing) == 0 {
		p.FullOption = &FullOption{steps, bytes}
	}
	return p
}

// ---------------------------------------------------------------- downloading

func downloadsCacheDir() string {
	if d := os.Getenv("WETPAINT_DOWNLOADS"); d != "" {
		return d
	}
	d, err := os.UserCacheDir() // %LocalAppData% on Windows — never roams
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "WetPaint", "downloads")
}

func safeFileName(s string) string {
	s = filepath.Base(s)
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
	if s == "" || s == "." || s == ".." {
		s = "pack.zip"
	}
	return s
}

// downloadPack fetches one pack into the cache folder, resuming a partial
// download if one exists, and verifies its SHA-256 before returning the path.
func downloadPack(ctx context.Context, p FeedPack, prog func(done, total int64)) (string, error) {
	dir := downloadsCacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := p.Release + "-" + p.Kind + ".zip"
	if u, err := url.Parse(p.URL); err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".zip") {
		name = safeFileName(u.Path)
	}
	final := filepath.Join(dir, name)
	part := final + ".download"

	// Already downloaded and intact?
	if fi, err := os.Stat(final); err == nil && (p.Bytes == 0 || fi.Size() == p.Bytes) {
		if p.SHA256 == "" || fileSHA(ctx, final, nil) == p.SHA256 {
			prog(fi.Size(), fi.Size())
			return final, nil
		}
		os.Remove(final)
	}

	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		lastErr = downloadOnce(ctx, p, part, prog)
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var perm permanentErr
		if errors.As(lastErr, &perm) {
			return "", lastErr
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("download of %s failed: %w (run Update again to resume)", p.Release, lastErr)
	}
	if p.SHA256 != "" {
		if got := fileSHA(ctx, part, nil); got != p.SHA256 {
			os.Remove(part)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("the %s pack downloaded corrupted — run Update again", p.Release)
		}
	}
	os.Remove(final)
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	return final, nil
}

type permanentErr struct{ error }

func downloadOnce(ctx context.Context, p FeedPack, part string, prog func(done, total int64)) error {
	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}
	if p.Bytes > 0 && have > p.Bytes {
		os.Remove(part)
		have = 0
	}
	if p.Bytes > 0 && have == p.Bytes {
		return nil
	}
	req, err := newRequest(ctx, p.URL)
	if err != nil {
		return permanentErr{err}
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode == http.StatusOK:
		have = 0 // server ignored Range: start over
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return errors.New("restarting download")
	case resp.StatusCode == 404 || resp.StatusCode == 403 || resp.StatusCode == 410:
		return permanentErr{fmt.Errorf("%s isn't on the server (%s) — let Fridge know", p.URL, resp.Status)}
	default:
		return fmt.Errorf("server answered %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return permanentErr{fmt.Errorf("%s is a web page, not a download link — let Fridge know", p.URL)}
	}
	total := p.Bytes
	if total == 0 && resp.ContentLength > 0 {
		total = have + resp.ContentLength
	}
	flag := os.O_CREATE | os.O_WRONLY
	if have > 0 {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flag, 0o644)
	if err != nil {
		return permanentErr{err}
	}
	done := have
	prog(done, total)
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return permanentErr{fmt.Errorf("can't write the download (disk full?): %w", werr)}
			}
			done += int64(n)
			prog(done, total)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if p.Bytes > 0 && done != p.Bytes {
		return fmt.Errorf("download stopped early (%s of %s)", human(done), human(p.Bytes))
	}
	return nil
}

func fileSHA(ctx context.Context, path string, prog func(done, total int64)) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, _ := f.Stat()
	h := sha256.New()
	var done int64
	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			return ""
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			done += int64(n)
			if prog != nil {
				prog(done, fi.Size())
			}
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------- the update job

type UpdateResult struct {
	Installed []string    `json:"installed"` // releases installed
	Liveries  []string    `json:"liveries"`  // liveries written
	Files     int         `json:"files"`
	Unchanged int         `json:"unchanged"`
	Bytes     int64       `json:"bytes"`
	Scan      *ScanResult `json:"scan"`
	// FromDownloads lists releases installed from a pack already on disk.
	FromDownloads []string `json:"from_downloads,omitempty"`
}

// runUpdate downloads and installs the given plan steps, then verifies.
// A step whose pack is already on disk (local, e.g. saved from Discord into
// Downloads) is installed from there instead of being downloaded again; the
// pilot's own copy is never deleted.
func runUpdate(ctx context.Context, f *Feed, steps []FeedPack, dcs string, cache *HashCache, local []PackInfo, set func(string, int64, int64, string)) (*UpdateResult, error) {
	if len(steps) == 0 {
		return nil, errors.New("nothing to download")
	}
	var dl, extract int64
	for _, s := range steps {
		if localCopy(s, local) == "" {
			dl += s.Bytes
		}
	}
	idx := f.Library.Index()
	for _, s := range steps {
		for _, id := range s.Liveries {
			if l := idx[strings.ToLower(id)]; l != nil {
				extract += l.Size()
			}
		}
	}
	if err := checkSpace(downloadsCacheDir(), dl, dcs, extract); err != nil {
		return nil, err
	}

	// The feed's library is the newest truth: record it first so the
	// no-downgrade rule protects newer jets while older packs install.
	if have, err := loadInstalledManifest(dcs); err != nil || f.Library.Created.After(have.Created) {
		if err := saveInstalledManifest(dcs, f.Library); err != nil {
			return nil, err
		}
	}

	res := &UpdateResult{}
	download := func(i int) (string, error) {
		s := steps[i]
		label := fmt.Sprintf("Downloading %s %s (%d of %d)", s.Release, map[string]string{"full": "full pack", "update": "update"}[s.Kind], i+1, len(steps))
		return downloadPack(ctx, s, func(d, t int64) { set(label, d, t, filepath.Base(s.URL)) })
	}
	var paths []string
	fromDisk := make([]bool, len(steps))
	for i, s := range steps {
		if p := localCopy(s, local); p != "" {
			paths, fromDisk[i] = append(paths, p), true
			res.FromDownloads = append(res.FromDownloads, s.Release)
			continue
		}
		p, err := download(i)
		if err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	for i := range paths {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		label := fmt.Sprintf("Installing %s (%d of %d)", steps[i].Release, i+1, len(paths))
		ir, _, err := installPack(paths[i], dcs, cache, func(d, t int64, c string) { set(label, d, t, c) })
		if err != nil && fromDisk[i] && ctx.Err() == nil {
			// The copy in Downloads is damaged: fetch a fresh one and use that.
			p, derr := download(i)
			if derr != nil {
				return nil, fmt.Errorf("%v; downloading a fresh copy also failed: %w", err, derr)
			}
			paths[i], fromDisk[i] = p, false
			res.FromDownloads = res.FromDownloads[:0]
			for j := range steps {
				if fromDisk[j] {
					res.FromDownloads = append(res.FromDownloads, steps[j].Release)
				}
			}
			ir, _, err = installPack(p, dcs, cache, func(d, t int64, c string) { set(label, d, t, c) })
		}
		if err != nil {
			return nil, err
		}
		res.Installed = append(res.Installed, steps[i].Release)
		res.Liveries = append(res.Liveries, ir.Installed...)
		res.Files += ir.Files
		res.Unchanged += ir.Unchanged
		res.Bytes += ir.Bytes
	}
	// Installed fine: the zips we downloaded aren't needed any more (the full
	// pack is huge). Packs the pilot saved themselves stay where they are.
	for i, p := range paths {
		if !fromDisk[i] {
			os.Remove(p)
		}
	}
	return res, nil
}

func checkSpace(dlDir string, dl int64, dcs string, extract int64) error {
	os.MkdirAll(dlDir, 0o755)
	const slack = 512 << 20
	fd, okd := diskFree(dlDir)
	fl, okl := diskFree(dcs)
	if !okd || !okl {
		return nil // can't tell — let it try
	}
	if fd == fl { // almost certainly the same drive
		if fd < dl+extract+slack {
			return fmt.Errorf("not enough disk space: need about %s free, have %s", human(dl+extract+slack), human(fd))
		}
		return nil
	}
	if fd < dl+slack {
		return fmt.Errorf("not enough space for the download in %s: need %s, have %s", dlDir, human(dl+slack), human(fd))
	}
	if fl < extract+slack {
		return fmt.Errorf("not enough space on the DCS drive: need %s, have %s", human(extract+slack), human(fl))
	}
	return nil
}

// newerVersion reports whether feed version a is newer than b (dotted numbers).
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}
