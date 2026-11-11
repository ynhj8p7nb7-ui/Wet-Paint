package main

// library.go — the manifest format and everything about reading a
// Liveries folder off disk: walking it, hashing files (with a cache so
// re-scans of a 20 GB library are instant), and comparing against a manifest.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const manifestFormat = 1
const manifestName = "wetpaint.json"

type FileEntry struct {
	Path   string `json:"path"` // forward slashes, relative to the livery folder
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Livery struct {
	Aircraft string      `json:"aircraft"` // DCS aircraft folder, e.g. FA-18C_hornet
	Folder   string      `json:"folder"`   // livery folder, must match description.lua references
	Hash     string      `json:"hash"`     // hash of all file hashes: changes whenever any file changes
	Added    string      `json:"added"`    // release it first appeared in
	Updated  string      `json:"updated"`  // release it last changed in (= which pack ships it)
	Files    []FileEntry `json:"files"`
}

func (l *Livery) ID() string { return l.Aircraft + "/" + l.Folder }

func (l *Livery) Size() (n int64) {
	for _, f := range l.Files {
		n += f.Size
	}
	return
}

func (l *Livery) computeHash() {
	h := sha256.New()
	for _, f := range l.Files {
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", f.Path, f.Size, f.SHA256)
	}
	l.Hash = hex.EncodeToString(h.Sum(nil))[:16]
}

type Manifest struct {
	Format       int       `json:"format"`
	Library      string    `json:"library"`
	Release      string    `json:"release"`
	Created      time.Time `json:"created"`
	PackKind     string    `json:"pack_kind,omitempty"`     // "update" or "full" (only inside a pack)
	PackContains []string  `json:"pack_contains,omitempty"` // livery IDs shipped in this pack
	Removed      []string  `json:"removed,omitempty"`       // liveries retired from the library
	Liveries     []Livery  `json:"liveries"`
}

func (m *Manifest) Index() map[string]*Livery {
	idx := make(map[string]*Livery, len(m.Liveries))
	for i := range m.Liveries {
		idx[strings.ToLower(m.Liveries[i].ID())] = &m.Liveries[i]
	}
	return idx
}

func (m *Manifest) TotalSize() (n int64) {
	for i := range m.Liveries {
		n += m.Liveries[i].Size()
	}
	return
}

func readManifestFile(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseManifest(b)
}

func parseManifest(b []byte) (*Manifest, error) {
	// Also accept a feed.json (it embeds the library), so Fridge can point
	// "last release" at either file.
	var probe struct {
		Library json.RawMessage `json:"library"`
		Packs   json.RawMessage `json:"packs"`
	}
	if json.Unmarshal(b, &probe) == nil && len(probe.Packs) > 0 && len(probe.Library) > 0 && probe.Library[0] == '{' {
		b = probe.Library
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("not a valid Wet Paint manifest: %w", err)
	}
	if m.Format == 0 || m.Format > manifestFormat {
		return nil, fmt.Errorf("manifest format %d is not supported by this version of Wet Paint — grab the latest Wet Paint", m.Format)
	}
	return &m, nil
}

func jsonBytes(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

func writeJSON(path string, v any) error {
	b, err := jsonBytes(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------------------------------------------------------- junk files

// isJunk filters out OS clutter that must never be shipped or counted:
// macOS resource forks and Finder files (Fridge builds on a Mac), Windows thumbnails.
func isJunk(name string) bool {
	l := strings.ToLower(name)
	return strings.HasPrefix(name, "._") || l == ".ds_store" || l == "thumbs.db" ||
		l == "desktop.ini" || l == "__macosx" || l == ".spotlight-v100" || l == ".fseventsd" ||
		strings.HasSuffix(l, ".tmp") || strings.HasSuffix(l, ".wetpaint-part")
}

// ---------------------------------------------------------------- hash cache

type cacheEntry struct {
	Size  int64   `json:"s"`
	MTime int64   `json:"m"`
	Hash  string  `json:"h,omitempty"`
	CRC   *uint32 `json:"c,omitempty"` // CRC-32, to compare with plain livery zips cheaply
}

// HashCache remembers file hashes keyed by absolute path + size + mtime,
// so only new or touched files are ever re-read.
type HashCache struct {
	mu    sync.Mutex
	path  string
	dirty bool
	M     map[string]cacheEntry
}

func loadHashCache() *HashCache {
	c := &HashCache{path: filepath.Join(configDir(), "hashcache.json"), M: map[string]cacheEntry{}}
	if b, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(b, &c.M)
	}
	return c
}

func (c *HashCache) Save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return
	}
	if b, err := json.Marshal(c.M); err == nil {
		_ = os.MkdirAll(filepath.Dir(c.path), 0o755)
		_ = os.WriteFile(c.path, b, 0o644)
		c.dirty = false
	}
}

func (c *HashCache) get(abs string, fi os.FileInfo) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.M[abs]
	if ok && e.Hash != "" && e.Size == fi.Size() && e.MTime == fi.ModTime().UnixNano() {
		return e.Hash, true
	}
	return "", false
}

func (c *HashCache) put(abs string, fi os.FileInfo, h string) {
	c.mu.Lock()
	e := c.entry(abs, fi)
	e.Hash = h
	c.M[abs] = e
	c.dirty = true
	c.mu.Unlock()
}

// entry returns the cached entry for this exact file version, or a fresh one.
func (c *HashCache) entry(abs string, fi os.FileInfo) cacheEntry {
	e, ok := c.M[abs]
	if !ok || e.Size != fi.Size() || e.MTime != fi.ModTime().UnixNano() {
		e = cacheEntry{Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
	}
	return e
}

func (c *HashCache) getCRC(abs string, fi os.FileInfo) (uint32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.M[abs]
	if ok && e.CRC != nil && e.Size == fi.Size() && e.MTime == fi.ModTime().UnixNano() {
		return *e.CRC, true
	}
	return 0, false
}

func (c *HashCache) putCRC(abs string, fi os.FileInfo, crc uint32) {
	c.mu.Lock()
	e := c.entry(abs, fi)
	e.CRC = &crc
	c.M[abs] = e
	c.dirty = true
	c.mu.Unlock()
}

// Progress is reported by long jobs (scan, install, build).
type Progress func(done, total int64, current string)

type countingReader struct {
	r    io.Reader
	tick func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.tick(int64(n))
	}
	return n, err
}

// hashFiles hashes many files in parallel (4 workers — enough to saturate an
// NVMe drive without thrashing a spinning disk too badly), using the cache.
func hashFiles(cache *HashCache, abs []string, infos []os.FileInfo, prog Progress) ([]string, error) {
	out := make([]string, len(abs))
	var total int64
	for _, fi := range infos {
		total += fi.Size()
	}
	var mu sync.Mutex
	var done int64
	tick := func(n int64, cur string) {
		mu.Lock()
		done += n
		if prog != nil {
			prog(done, total, cur)
		}
		mu.Unlock()
	}
	jobs := make(chan int)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if h, ok := cache.get(abs[i], infos[i]); ok {
					out[i] = h
					tick(infos[i].Size(), filepath.Base(abs[i]))
					continue
				}
				f, err := os.Open(abs[i])
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					continue
				}
				h := sha256.New()
				name := filepath.Base(abs[i])
				_, err = io.Copy(h, &countingReader{f, func(n int64) { tick(n, name) }})
				f.Close()
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					continue
				}
				out[i] = hex.EncodeToString(h.Sum(nil))
				cache.put(abs[i], infos[i], out[i])
			}
		}()
	}
	for i := range abs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errs:
		return nil, err
	default:
	}
	return out, nil
}

// ---------------------------------------------------------------- source scan (Fridge side)

type Warning struct {
	Livery  string `json:"livery"`
	Level   string `json:"level"` // "error" (will break in DCS) or "warn"
	Message string `json:"message"`
}

// liveriesRoot accepts either a folder that *is* Liveries (contains aircraft
// folders) or one that contains a Liveries folder.
func liveriesRoot(dir string) string {
	for _, n := range []string{"Liveries", "liveries", "LIVERIES"} {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return dir
}

var luaRefRe = regexp.MustCompile(`"\.\./([^/"]+)/`)

// scanSource reads Fridge's master Liveries folder into a manifest.
func scanSource(dir string, cache *HashCache, prog Progress) (*Manifest, []Warning, error) {
	root := liveriesRoot(dir)
	aircraftDirs, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	var warns []Warning
	stock := stockLiveryDirs()
	type fileRef struct {
		liv  int
		rel  string
		abs  string
		info os.FileInfo
	}
	var refs []fileRef
	m := &Manifest{Format: manifestFormat, Library: "Wet Boys CVW-0"}

	for _, ad := range aircraftDirs {
		if isJunk(ad.Name()) || strings.HasPrefix(ad.Name(), ".") {
			continue
		}
		if !ad.IsDir() {
			warns = append(warns, Warning{ad.Name(), "warn", "Loose file in the Liveries root is ignored — files must be inside <aircraft>/<livery>/"})
			continue
		}
		livDirs, err := os.ReadDir(filepath.Join(root, ad.Name()))
		if err != nil {
			return nil, nil, err
		}
		for _, ld := range livDirs {
			if isJunk(ld.Name()) || strings.HasPrefix(ld.Name(), ".") {
				continue
			}
			id := ad.Name() + "/" + ld.Name()
			if !ld.IsDir() {
				warns = append(warns, Warning{id, "warn", "Loose file directly inside an aircraft folder is ignored — is it missing its livery folder?"})
				continue
			}
			li := len(m.Liveries)
			m.Liveries = append(m.Liveries, Livery{Aircraft: ad.Name(), Folder: ld.Name()})
			ldir := filepath.Join(root, ad.Name(), ld.Name())
			hasDesc := false
			err := filepath.Walk(ldir, func(p string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if isJunk(fi.Name()) {
					if fi.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if fi.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(ldir, p)
				rel = filepath.ToSlash(rel)
				if strings.EqualFold(rel, "description.lua") {
					hasDesc = true
					checkDescription(p, ld.Name(), filepath.Join(root, ad.Name()), id, stock[strings.ToLower(ad.Name())], &warns)
				}
				refs = append(refs, fileRef{li, rel, p, fi})
				return nil
			})
			if err != nil {
				return nil, nil, err
			}
			if !hasDesc {
				warns = append(warns, Warning{id, "error", "No description.lua — DCS will not show this livery"})
			}
		}
	}

	abs := make([]string, len(refs))
	infos := make([]os.FileInfo, len(refs))
	for i, r := range refs {
		abs[i], infos[i] = r.abs, r.info
	}
	hashes, err := hashFiles(cache, abs, infos, prog)
	cache.Save()
	if err != nil {
		return nil, nil, err
	}
	for i, r := range refs {
		l := &m.Liveries[r.liv]
		l.Files = append(l.Files, FileEntry{r.rel, r.info.Size(), hashes[i]})
	}
	for i := range m.Liveries {
		sort.Slice(m.Liveries[i].Files, func(a, b int) bool { return m.Liveries[i].Files[a].Path < m.Liveries[i].Files[b].Path })
		m.Liveries[i].computeHash()
		if len(m.Liveries[i].Files) == 0 {
			warns = append(warns, Warning{m.Liveries[i].ID(), "warn", "Empty livery folder"})
		}
	}
	sort.Slice(m.Liveries, func(a, b int) bool { return strings.ToLower(m.Liveries[a].ID()) < strings.ToLower(m.Liveries[b].ID()) })
	return m, warns, nil
}

// checkDescription catches the #1 cause of broken liveries: description.lua
// pointing at "../Some Other Folder/" because the folder got renamed.
//
// A reference is fine when it names the livery's own folder, another livery in
// the master library, or a folder DCS itself ships for that aircraft (stock:
// that aircraft's livery folders in the DCS install, when there is one).
func checkDescription(path, folder, aircraftDir, id string, stock []string, warns *[]Warning) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, mm := range luaRefRe.FindAllStringSubmatch(string(b), -1) {
		ref := mm[1]
		if seen[ref] || strings.EqualFold(ref, folder) {
			continue
		}
		seen[ref] = true
		if hasFolderFold(aircraftDir, ref) || sharedLiveryFolders[strings.ToLower(ref)] {
			continue
		}
		inStock := false
		for _, d := range stock {
			if hasFolderFold(d, ref) {
				inStock = true
				break
			}
		}
		if !inStock {
			*warns = append(*warns, Warning{id, "error", fmt.Sprintf(
				"description.lua points at \"../%s/\" but the folder is named \"%s\" — textures will be missing in DCS", ref, folder)})
		}
	}
}

// ---------------------------------------------------------------- installed scan (pilot side)

type LiveryStatus struct {
	ID       string   `json:"id"`
	Aircraft string   `json:"aircraft"`
	Folder   string   `json:"folder"`
	Updated  string   `json:"updated"`
	State    string   `json:"state"` // ok | missing | outdated | incomplete
	Problems []string `json:"problems,omitempty"`
	Size     int64    `json:"size"`
}

type ScanResult struct {
	Release   string         `json:"release"`
	Total     int            `json:"total"`
	OK        int            `json:"ok"`
	Liveries  []LiveryStatus `json:"liveries"`
	NeedPacks []string       `json:"need_packs"` // releases whose packs would fix the problems
	UpToDate  bool           `json:"up_to_date"`
	Scanned   time.Time      `json:"scanned"`
}

func scanInstalled(liveriesDir string, m *Manifest, cache *HashCache, prog Progress) (*ScanResult, error) {
	res := &ScanResult{Release: m.Release, Total: len(m.Liveries), Scanned: time.Now()}
	type ref struct {
		st   int
		file FileEntry
	}
	var refs []ref
	var abs []string
	var infos []os.FileInfo
	for i := range m.Liveries {
		l := &m.Liveries[i]
		st := LiveryStatus{ID: l.ID(), Aircraft: l.Aircraft, Folder: l.Folder, Updated: l.Updated, State: "ok", Size: l.Size()}
		ldir := filepath.Join(liveriesDir, l.Aircraft, l.Folder)
		if fi, err := os.Stat(ldir); err != nil || !fi.IsDir() {
			st.State = "missing"
		} else {
			for _, f := range l.Files {
				p := filepath.Join(ldir, filepath.FromSlash(f.Path))
				fi, err := os.Stat(p)
				switch {
				case err != nil:
					st.State = "incomplete"
					st.Problems = append(st.Problems, "missing "+f.Path)
				case fi.Size() != f.Size:
					st.State = "outdated"
					st.Problems = append(st.Problems, "changed "+f.Path)
				default:
					refs = append(refs, ref{len(res.Liveries), f})
					abs = append(abs, p)
					infos = append(infos, fi)
				}
			}
		}
		res.Liveries = append(res.Liveries, st)
	}
	hashes, err := hashFiles(cache, abs, infos, prog)
	cache.Save()
	if err != nil {
		return nil, err
	}
	for i, r := range refs {
		if hashes[i] != r.file.SHA256 {
			st := &res.Liveries[r.st]
			if st.State == "ok" {
				st.State = "outdated"
			}
			st.Problems = append(st.Problems, "changed "+r.file.Path)
		}
	}
	need := map[string]bool{}
	for _, st := range res.Liveries {
		if st.State == "ok" {
			res.OK++
		} else {
			need[st.Updated] = true
		}
	}
	for r := range need {
		res.NeedPacks = append(res.NeedPacks, r)
	}
	sort.Strings(res.NeedPacks)
	// problems first, then alphabetical
	sort.SliceStable(res.Liveries, func(a, b int) bool {
		oa, ob := res.Liveries[a].State == "ok", res.Liveries[b].State == "ok"
		if oa != ob {
			return !oa
		}
		return strings.ToLower(res.Liveries[a].ID) < strings.ToLower(res.Liveries[b].ID)
	})
	res.UpToDate = res.OK == res.Total && res.Total > 0
	return res, nil
}

var errNoLibrary = errors.New("no library manifest yet — install a Wet Boys pack first")
