package main

// pack.go — building update packs (Fridge) and installing them (pilots).
//
// A pack is an ordinary .zip:
//
//   wetpaint.json                         full library manifest + what this pack ships
//   README.txt
//   Liveries/<aircraft>/<livery>/...      only the liveries that are new/changed
//
// Because every pack carries the manifest for the WHOLE library, installing
// just this month's pack is enough for Wet Paint to verify everything else
// the pilot already has.

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------- build (Fridge)

type BuildOptions struct {
	Source  string `json:"source"`  // Fridge's master Liveries folder
	Prev    string `json:"prev"`    // last month's library.json ("" = first ever build)
	Release string `json:"release"` // e.g. 2026-11
	Out     string `json:"out"`     // output folder
	Full    bool   `json:"full"`    // ship every livery (new master pack)
	URL     string `json:"url"`     // where the pack will be downloadable; "" = next to feed.json
}

type BuildResult struct {
	Release   string    `json:"release"`
	PackPath  string    `json:"pack_path"`
	LibPath   string    `json:"lib_path"`
	New       []string  `json:"new"`
	Changed   []string  `json:"changed"`
	Removed   []string  `json:"removed"`
	Shipped   int       `json:"shipped"`
	PackBytes int64     `json:"pack_bytes"`
	LibBytes  int64     `json:"lib_bytes"`
	FeedPath  string    `json:"feed_path"`
	Total     int       `json:"total"`
	Warnings  []Warning `json:"warnings"`
	NoChanges bool      `json:"no_changes"`
}

func buildPack(o BuildOptions, cache *HashCache, prog func(phase string, done, total int64, cur string)) (*BuildResult, error) {
	if o.Release == "" {
		o.Release = time.Now().Format("2006-01")
	}
	if strings.ContainsAny(o.Release, `/\:*?"<>|`) {
		return nil, fmt.Errorf("release label %q can't contain / \\ : * ? \" < > |", o.Release)
	}
	if o.Out == "" {
		return nil, errors.New("pick an output folder")
	}
	cur, warns, err := scanSource(o.Source, cache, func(d, t int64, c string) { prog("Reading master library", d, t, c) })
	if err != nil {
		return nil, fmt.Errorf("reading master folder: %w", err)
	}
	if len(cur.Liveries) == 0 {
		return nil, fmt.Errorf("no liveries found in %s — point this at the folder that contains the aircraft folders (FA-18C_hornet, F-14B, …)", o.Source)
	}
	var prev *Manifest
	if o.Prev != "" {
		if prev, err = readManifestFile(o.Prev); err != nil {
			return nil, fmt.Errorf("reading last month's library file: %w", err)
		}
		if prev.Release == o.Release {
			return nil, fmt.Errorf("last month's library file is already release %q — use a new release label", o.Release)
		}
	}
	warns = append(warns, checkSourceAircraft(cur)...)
	res := &BuildResult{Release: o.Release, Warnings: warns, Total: len(cur.Liveries)}
	pidx := map[string]*Livery{}
	if prev != nil {
		pidx = prev.Index()
	}
	cur.Release, cur.Created = o.Release, time.Now().UTC()
	var ship []*Livery
	for i := range cur.Liveries {
		l := &cur.Liveries[i]
		p := pidx[strings.ToLower(l.ID())]
		switch {
		case p == nil:
			l.Added, l.Updated = o.Release, o.Release
			res.New = append(res.New, l.ID())
			ship = append(ship, l)
		case p.Hash != l.Hash:
			l.Added, l.Updated = p.Added, o.Release
			res.Changed = append(res.Changed, l.ID())
			ship = append(ship, l)
		default:
			l.Added, l.Updated = p.Added, p.Updated
			if o.Full {
				ship = append(ship, l)
			}
		}
		delete(pidx, strings.ToLower(l.ID()))
	}
	for _, p := range pidx {
		res.Removed = append(res.Removed, p.ID())
	}
	sort.Strings(res.Removed)
	cur.Removed = res.Removed
	if len(ship) == 0 {
		res.NoChanges = true
		return res, nil
	}

	kind, name := "update", fmt.Sprintf("WetBoys-Liveries-%s-update.zip", o.Release)
	if o.Full {
		kind, name = "full", fmt.Sprintf("WetBoys-Liveries-%s-FULL.zip", o.Release)
	}
	packMan := *cur
	packMan.PackKind = kind
	for _, l := range ship {
		packMan.PackContains = append(packMan.PackContains, l.ID())
	}
	res.Shipped = len(ship)

	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, err
	}
	res.PackPath = filepath.Join(o.Out, name)
	packSHA, err := writePackZip(res.PackPath, liveriesRoot(o.Source), &packMan, ship, func(d, t int64, c string) { prog("Writing pack", d, t, c) })
	if err != nil {
		return nil, err
	}
	res.LibPath = filepath.Join(o.Out, "library.json")
	if err := writeJSON(res.LibPath, cur); err != nil {
		return nil, err
	}
	// dated copy, so next month's build can find it even if library.json gets overwritten
	_ = writeJSON(filepath.Join(o.Out, fmt.Sprintf("library-%s.json", o.Release)), cur)
	if fi, err := os.Stat(res.PackPath); err == nil {
		res.PackBytes = fi.Size()
	}
	if fi, err := os.Stat(res.LibPath); err == nil {
		res.LibBytes = fi.Size()
	}
	packURL := strings.TrimSpace(o.URL)
	if packURL == "" {
		packURL = urlPathEscape(name)
	}
	res.FeedPath, err = updateFeed(o.Out, cur, FeedPack{Release: o.Release, Kind: kind, URL: packURL,
		Bytes: res.PackBytes, SHA256: packSHA, Created: cur.Created, Liveries: packMan.PackContains})
	if err != nil {
		return nil, err
	}
	return res, nil
}

const packReadme = `WET BOYS CVW-0 — LIVERY PACK %s

Don't unzip this by hand.

1. Run WetPaint.exe
2. It finds this pack in your Downloads folder automatically
   (or click "Choose pack…" and pick this zip)
3. Hit INSTALL & VERIFY and wait for the green light.

Wet Paint puts every livery in the exact folder DCS expects:
  Saved Games\DCS\Liveries\<aircraft>\<livery>\
`

func writePackZip(dst, root string, m *Manifest, ship []*Livery, prog Progress) (string, error) {
	tmp := dst + ".wetpaint-part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	fileHash := sha256.New()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	zw := zip.NewWriter(io.MultiWriter(f, fileHash))
	b, err := jsonBytes(m)
	if err != nil {
		return "", err
	}
	w, err := zw.Create(manifestName)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(b); err != nil {
		return "", err
	}
	w, _ = zw.Create("README.txt")
	fmt.Fprintf(w, strings.ReplaceAll(packReadme, "\n", "\r\n"), m.Release)

	var total, done int64
	for _, l := range ship {
		total += l.Size()
	}
	for _, l := range ship {
		for _, fe := range l.Files {
			src := filepath.Join(root, l.Aircraft, l.Folder, filepath.FromSlash(fe.Path))
			hdr := &zip.FileHeader{Name: path.Join("Liveries", l.Aircraft, l.Folder, fe.Path), Method: zip.Deflate, Modified: time.Now()}
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return "", err
			}
			in, err := os.Open(src)
			if err != nil {
				return "", err
			}
			h := sha256.New()
			n, err := io.Copy(io.MultiWriter(w, h), &countingReader{in, func(n int64) {
				done += n
				prog(done, total, fe.Path)
			}})
			in.Close()
			if err != nil {
				return "", err
			}
			if n != fe.Size || hex.EncodeToString(h.Sum(nil)) != fe.SHA256 {
				return "", fmt.Errorf("%s changed while the pack was being built — close any editors and build again", src)
			}
		}
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	os.Remove(dst)
	return hex.EncodeToString(fileHash.Sum(nil)), os.Rename(tmp, dst)
}

// ---------------------------------------------------------------- read (pilot)

type PackInfo struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Release  string    `json:"release"`
	Kind     string    `json:"kind"`
	Created  time.Time `json:"created"`
	Liveries []string  `json:"liveries"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
}

func openPack(p string) (*zip.ReadCloser, *Manifest, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, nil, fmt.Errorf("can't open %s as a zip (is the download finished?): %w", filepath.Base(p), err)
	}
	for _, f := range zr.File {
		if f.Name == manifestName {
			rc, err := f.Open()
			if err != nil {
				zr.Close()
				return nil, nil, err
			}
			b, err := io.ReadAll(io.LimitReader(rc, 64<<20))
			rc.Close()
			if err != nil {
				zr.Close()
				return nil, nil, err
			}
			m, err := parseManifest(b)
			if err != nil {
				zr.Close()
				return nil, nil, err
			}
			return zr, m, nil
		}
	}
	zr.Close()
	return nil, nil, fmt.Errorf("%s isn't a Wet Paint pack (no %s inside) — ask Fridge for the Wet Paint version of the pack", filepath.Base(p), manifestName)
}

func inspectPack(p string) (*PackInfo, error) {
	zr, m, err := openPack(p)
	if err != nil {
		return nil, err
	}
	zr.Close()
	fi, _ := os.Stat(p)
	info := &PackInfo{Path: p, Name: filepath.Base(p), Release: m.Release, Kind: m.PackKind, Created: m.Created, Liveries: m.PackContains}
	if fi != nil {
		info.Bytes, info.Modified = fi.Size(), fi.ModTime()
	}
	return info, nil
}

// ---------------------------------------------------------------- install (pilot)

type InstallResult struct {
	Release   string   `json:"release"`
	Installed []string `json:"installed"`
	Skipped   []string `json:"skipped"` // older versions than what the newest manifest wants
	Files     int      `json:"files"`
	Unchanged int      `json:"unchanged"` // files already correct on disk, left alone
	Bytes     int64    `json:"bytes"`
}

// safeJoin refuses any zip entry that would land outside the Liveries folder.
func safeJoin(base string, parts ...string) (string, error) {
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "\\:\x00") {
			return "", fmt.Errorf("unsafe path %q in pack", strings.Join(parts, "/"))
		}
	}
	full := filepath.Join(append([]string{base}, parts...)...)
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q in pack", strings.Join(parts, "/"))
	}
	return full, nil
}

func installPack(packPath, dcsDir string, cache *HashCache, prog Progress) (*InstallResult, *Manifest, error) {
	zr, m, err := openPack(packPath)
	if err != nil {
		return nil, nil, err
	}
	defer zr.Close()

	liveriesDir := filepath.Join(dcsDir, "Liveries")
	if err := os.MkdirAll(liveriesDir, 0o755); err != nil {
		return nil, nil, err
	}

	// Never let an old pack downgrade a livery that a newer release changed.
	newest := m
	if have, err := loadInstalledManifest(dcsDir); err == nil && have.Created.After(m.Created) {
		newest = have
	}
	wantIdx := newest.Index()
	packIdx := m.Index()

	res := &InstallResult{Release: m.Release}
	allowed := map[string]*Livery{} // lower(id) -> livery entry from pack manifest
	for _, id := range m.PackContains {
		k := strings.ToLower(id)
		pl, want := packIdx[k], wantIdx[k]
		if pl == nil {
			continue
		}
		if want != nil && want.Hash != pl.Hash {
			res.Skipped = append(res.Skipped, id)
			continue
		}
		allowed[k] = pl
	}

	// Index the zip entries we're going to extract and validate them first,
	// so a bad pack fails before anything is written.
	type job struct {
		zf   *zip.File
		dst  string
		want FileEntry
	}
	var jobs []job
	var total int64
	for _, zf := range zr.File {
		name := strings.TrimPrefix(zf.Name, "./")
		if zf.FileInfo().IsDir() || !strings.HasPrefix(name, "Liveries/") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(name, "Liveries/"), "/")
		if len(parts) < 3 || isJunk(parts[len(parts)-1]) || parts[0] == "__MACOSX" {
			continue
		}
		l := allowed[strings.ToLower(parts[0]+"/"+parts[1])]
		if l == nil {
			continue
		}
		rel := strings.Join(parts[2:], "/")
		var want *FileEntry
		for i := range l.Files {
			if l.Files[i].Path == rel {
				want = &l.Files[i]
				break
			}
		}
		if want == nil {
			continue // not in the manifest: don't install stray files
		}
		// use the canonical names from the manifest, not whatever casing the zip has
		dst, err := safeJoin(liveriesDir, append([]string{l.Aircraft, l.Folder}, strings.Split(want.Path, "/")...)...)
		if err != nil {
			return nil, nil, err
		}
		jobs = append(jobs, job{zf, dst, *want})
		total += want.Size
	}

	var done int64
	installed := map[string]bool{}
	for _, j := range jobs {
		if fi, err := os.Stat(j.dst); err == nil && fi.Size() == j.want.Size {
			if h, err := hashFiles(cache, []string{j.dst}, []os.FileInfo{fi}, nil); err == nil && h[0] == j.want.SHA256 {
				res.Unchanged++
				done += j.want.Size
				prog(done, total, j.want.Path)
				continue
			}
		}
		if err := extractOne(j.zf, j.dst, j.want, cache, func(n int64) {
			done += n
			prog(done, total, j.want.Path)
		}); err != nil {
			return nil, nil, err
		}
		res.Files++
		res.Bytes += j.want.Size
		parts := strings.SplitN(strings.TrimPrefix(j.zf.Name, "Liveries/"), "/", 3)
		installed[parts[0]+"/"+parts[1]] = true
	}
	cache.Save()
	for id := range installed {
		res.Installed = append(res.Installed, id)
	}
	sort.Strings(res.Installed)

	if newest == m {
		if err := saveInstalledManifest(dcsDir, m); err != nil {
			return nil, nil, err
		}
	}
	return res, newest, nil
}

func extractOne(zf *zip.File, dst string, want FileEntry, cache *HashCache, tick func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp := dst + ".wetpaint-part"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("can't write %s (is DCS running? close it and try again): %w", dst, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), &countingReader{io.LimitReader(rc, want.Size+1), tick})
	cerr := out.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && (n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256) {
		err = fmt.Errorf("%s is corrupt inside the pack — re-download the pack", want.Path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Remove(dst) // Windows rename won't overwrite
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("can't replace %s (is DCS running? close it and try again): %w", dst, err)
	}
	if fi, err := os.Stat(dst); err == nil {
		cache.put(dst, fi, want.SHA256)
	}
	return nil
}

// The pilot's copy of the newest manifest lives next to Liveries (DCS ignores
// unknown folders in Saved Games\DCS, but would treat a folder inside
// Liveries as an aircraft type).
func installedManifestPath(dcsDir string) string {
	return filepath.Join(dcsDir, "WetPaint", "library.json")
}

func loadInstalledManifest(dcsDir string) (*Manifest, error) {
	m, err := readManifestFile(installedManifestPath(dcsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoLibrary
	}
	return m, err
}

func saveInstalledManifest(dcsDir string, m *Manifest) error {
	c := *m
	c.PackKind, c.PackContains = "", nil
	return writeJSON(installedManifestPath(dcsDir), &c)
}

func urlPathEscape(name string) string { return (&url.URL{Path: name}).EscapedPath() }
