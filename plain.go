package main

// plain.go — ordinary livery zips, the way releases usually get shared on
// Discord: a zip of <aircraft>/<livery>/ folders (often inside one top folder
// such as "Livery Release 11426/"), with no wetpaint.json.
//
// There's no fingerprint list to verify against, so Wet Paint compares each
// file with what's on disk (size + CRC-32, which every zip stores), installs
// only what's new or different, and remembers which zip each livery came from
// so an older zip can never replace a newer livery. Liveries that belong to
// the verified Wet Paint library are left to the verified packs.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type PlainLivery struct {
	ID       string    `json:"id"`
	Aircraft string    `json:"aircraft"`
	Folder   string    `json:"folder"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	Date     time.Time `json:"date"`            // newest file date inside the zip
	State    string    `json:"state"`           // new | update | installed | skipped
	Note     string    `json:"note,omitempty"`  // why it's skipped
	Needs    []string  `json:"needs,omitempty"` // folders description.lua uses that aren't installed
	files    []plainFile
	lua      []string // description.lua contents
}

type plainFile struct {
	zf  *zip.File
	rel string // path inside the livery folder, forward slashes
}

// PlainZip is a livery zip and what installing it would do.
type PlainZip struct {
	Path     string        `json:"path"`
	Name     string        `json:"name"`
	Bytes    int64         `json:"bytes"`
	Modified time.Time     `json:"modified"`
	Date     time.Time     `json:"date"`
	Liveries []PlainLivery `json:"liveries"`
	New      int           `json:"new"`
	Updates  int           `json:"updates"`
	Current  int           `json:"current"`
	Skipped  int           `json:"skipped"`
	// Aircraft folders whose names look like typos of what DCS uses here.
	Aircraft []AircraftIssue `json:"aircraft,omitempty"`
}

func (z *PlainZip) Work() int { return z.New + z.Updates }

// Files that may sit in a livery zip outside the livery folders.
var plainExtras = map[string]bool{".txt": true, ".md": true, ".pdf": true, ".png": true, ".jpg": true, ".jpeg": true,
	".url": true, ".nfo": true, ".html": true, ".htm": true, ".rtf": true, ".docx": true, ".webp": true}

var errNotLiveryZip = errors.New("not a livery zip")

// readPlainZip works out whether zr is a plain livery zip and lists its
// liveries. Mod zips (a whole aircraft mod that also has liveries) are
// rejected because they contain files outside any livery folder.
func readPlainZip(zr *zip.Reader) ([]PlainLivery, error) {
	type root struct{ prefix, aircraft, folder string }
	var roots []root
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		parts := strings.Split(name, "/")
		if f.FileInfo().IsDir() || !strings.EqualFold(parts[len(parts)-1], "description.lua") || hasJunk(parts) {
			continue
		}
		if len(parts) < 3 {
			return nil, fmt.Errorf("%w: a livery in it has no aircraft folder around it", errNotLiveryZip)
		}
		n := len(parts)
		roots = append(roots, root{strings.Join(parts[:n-3], "/"), parts[n-3], parts[n-2]})
	}
	if len(roots) == 0 {
		return nil, errNotLiveryZip
	}
	// The outermost description.lua wins if one livery folder holds another.
	sort.Slice(roots, func(a, b int) bool { return len(roots[a].prefix) < len(roots[b].prefix) })
	byDir := map[string]*PlainLivery{}
	var order []string
	for _, r := range roots {
		dir := path.Join(r.prefix, r.aircraft, r.folder) + "/"
		inner := false
		for d := range byDir {
			if strings.HasPrefix(dir, d) {
				inner = true
			}
		}
		if inner {
			continue
		}
		if _, ok := byDir[dir]; !ok {
			byDir[dir] = &PlainLivery{ID: r.aircraft + "/" + r.folder, Aircraft: r.aircraft, Folder: r.folder}
			order = append(order, dir)
		}
	}
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		parts := strings.Split(name, "/")
		if f.FileInfo().IsDir() || hasJunk(parts) {
			continue
		}
		var lv *PlainLivery
		var rel string
		for d, l := range byDir {
			if strings.HasPrefix(strings.ToLower(name), strings.ToLower(d)) {
				lv, rel = l, name[len(d):]
				break
			}
		}
		if lv == nil {
			if plainExtras[strings.ToLower(path.Ext(name))] {
				continue // a readme or preview image
			}
			return nil, fmt.Errorf("%w: %s isn't part of a livery (looks like a mod)", errNotLiveryZip, name)
		}
		lv.files = append(lv.files, plainFile{f, rel})
		lv.Files++
		lv.Bytes += int64(f.UncompressedSize64)
		if m := f.Modified; m.After(lv.Date) {
			lv.Date = m
		}
		if strings.EqualFold(rel, "description.lua") {
			if rc, err := f.Open(); err == nil {
				b, _ := io.ReadAll(io.LimitReader(rc, 4<<20))
				rc.Close()
				lv.lua = append(lv.lua, string(b))
			}
		}
	}
	out := make([]PlainLivery, 0, len(order))
	for _, d := range order {
		out = append(out, *byDir[d])
	}
	sort.SliceStable(out, func(a, b int) bool { return strings.ToLower(out[a].ID) < strings.ToLower(out[b].ID) })
	return out, nil
}

func hasJunk(parts []string) bool {
	for _, p := range parts {
		if p != "" && isJunk(p) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- what's installed from plain zips

// plainRecord remembers, per livery, which zip it was last installed from and
// how new that zip's files were. Kept in Saved Games\DCS\WetPaint.
type plainRecord struct {
	Zip       string    `json:"zip"`
	Date      time.Time `json:"date"`
	Installed time.Time `json:"installed"`
}

func plainRecordPath(dcs string) string { return filepath.Join(dcs, "WetPaint", "plain-installs.json") }

func loadPlainRecords(dcs string) map[string]plainRecord {
	m := map[string]plainRecord{}
	if b, err := os.ReadFile(plainRecordPath(dcs)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// ---------------------------------------------------------------- comparing with disk

func fileCRC(cache *HashCache, p string, fi os.FileInfo) (uint32, error) {
	if c, ok := cache.getCRC(p, fi); ok {
		return c, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return 0, err
	}
	cache.putCRC(p, fi, h.Sum32())
	return h.Sum32(), nil
}

// sameOnDisk reports whether dst already holds exactly the zip entry's bytes.
func sameOnDisk(cache *HashCache, dst string, zf *zip.File) bool {
	fi, err := os.Stat(dst)
	if err != nil || fi.IsDir() || fi.Size() != int64(zf.UncompressedSize64) {
		return false
	}
	c, err := fileCRC(cache, dst, fi)
	return err == nil && c == zf.CRC32
}

// inspectPlain opens a livery zip and works out, for each livery, whether
// installing it would add, update or skip it.
func inspectPlain(p, dcs string, managed *Manifest, cache *HashCache, stock map[string][]string) (*PlainZip, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return inspectPlainReader(&zr.Reader, p, dcs, managed, cache, stock)
}

func inspectPlainReader(zr *zip.Reader, p, dcs string, managed *Manifest, cache *HashCache, stock map[string][]string) (*PlainZip, error) {
	livs, err := readPlainZip(zr)
	if err != nil {
		return nil, err
	}
	z := &PlainZip{Path: p, Name: filepath.Base(p)}
	if fi, err := os.Stat(p); err == nil {
		z.Bytes, z.Modified = fi.Size(), fi.ModTime()
	}
	var mIdx map[string]*Livery
	if managed != nil {
		mIdx = managed.Index()
	}
	recs := map[string]plainRecord{}
	liveries := filepath.Join(dcs, "Liveries")
	if dcs != "" {
		recs = loadPlainRecords(dcs)
	}
	for i := range livs {
		l := &livs[i]
		if l.Date.After(z.Date) {
			z.Date = l.Date
		}
		l.Needs = plainNeeds(l, livs, liveries, stock)
		k := strings.ToLower(l.ID)
		switch {
		case dcs == "":
			l.State = "new"
		case mIdx[k] != nil:
			l.State, l.Note = "skipped", "part of the verified Wet Boys library; it comes from the official packs"
		default:
			dir, err := safeJoin(liveries, l.Aircraft, l.Folder)
			if err != nil {
				l.State, l.Note = "skipped", "unsafe folder name"
				break
			}
			same, exists := true, false
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				exists = true
			}
			for _, f := range l.files {
				dst, err := safeJoin(dir, strings.Split(f.rel, "/")...)
				if err != nil || !exists || !sameOnDisk(cache, dst, f.zf) {
					same = false
					if !exists || err != nil {
						break
					}
				}
			}
			r, had := recs[k]
			switch {
			case same:
				l.State = "installed"
			case had && r.Date.After(l.Date):
				l.State, l.Note = "skipped", fmt.Sprintf("a newer version (from %s) is already installed", r.Zip)
			case exists:
				l.State = "update"
			default:
				l.State = "new"
			}
		}
		switch l.State {
		case "new":
			z.New++
		case "update":
			z.Updates++
		case "installed":
			z.Current++
		default:
			z.Skipped++
		}
	}
	cache.Save()
	z.Liveries = livs
	return z, nil
}

// plainNeeds lists the folders a livery's description.lua points at that
// aren't in the zip, on disk, or shipped with DCS — textures DCS won't find.
func plainNeeds(l *PlainLivery, all []PlainLivery, liveries string, stock map[string][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, src := range l.lua {
		for _, m := range luaRefRe.FindAllStringSubmatch(src, -1) {
			ref := m[1]
			if seen[strings.ToLower(ref)] || strings.EqualFold(ref, l.Folder) || sharedLiveryFolders[strings.ToLower(ref)] {
				continue
			}
			seen[strings.ToLower(ref)] = true
			found := false
			for _, o := range all {
				if strings.EqualFold(o.Aircraft, l.Aircraft) && strings.EqualFold(o.Folder, ref) {
					found = true
				}
			}
			if !found && liveries != "" {
				found = hasFolderFold(filepath.Join(liveries, l.Aircraft), ref)
			}
			for _, d := range stock[strings.ToLower(l.Aircraft)] {
				if !found && hasFolderFold(d, ref) {
					found = true
				}
			}
			if !found {
				out = append(out, ref)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- installing

type PlainResult struct {
	Zip       string   `json:"zip"`
	Installed []string `json:"installed"`
	Skipped   []string `json:"skipped"`
	Files     int      `json:"files"`
	Unchanged int      `json:"unchanged"`
	Bytes     int64    `json:"bytes"`
}

// installPlain copies the new and changed liveries from a livery zip into
// Saved Games\DCS\Liveries. Every path goes through safeJoin, every file is
// checked against the zip's CRC-32, and files already on disk unchanged are
// left alone. Nothing outside the livery folders is touched.
func installPlain(p, dcs string, managed *Manifest, cache *HashCache, stock map[string][]string, prog Progress) (*PlainResult, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("can't open %s (is the download finished?): %w", filepath.Base(p), err)
	}
	defer zr.Close()
	z, err := inspectPlainReader(&zr.Reader, p, dcs, managed, cache, stock)
	if err != nil {
		return nil, err
	}
	liveries := filepath.Join(dcs, "Liveries")
	if err := os.MkdirAll(liveries, 0o755); err != nil {
		return nil, err
	}
	res := &PlainResult{Zip: z.Name}
	var total, done int64
	for _, l := range z.Liveries {
		if l.State == "new" || l.State == "update" {
			total += l.Bytes
		}
	}
	if err := checkSpace(dcs, 0, dcs, total); err != nil {
		return nil, err
	}
	recs := loadPlainRecords(dcs)
	for _, l := range z.Liveries {
		switch l.State {
		case "skipped":
			res.Skipped = append(res.Skipped, l.ID+": "+l.Note)
			continue
		case "installed":
			if _, ok := recs[strings.ToLower(l.ID)]; !ok {
				recs[strings.ToLower(l.ID)] = plainRecord{z.Name, l.Date, time.Now().UTC()}
			}
			continue
		}
		dir, err := safeJoin(liveries, l.Aircraft, l.Folder)
		if err != nil {
			return nil, err
		}
		for _, f := range l.files {
			dst, err := safeJoin(dir, strings.Split(f.rel, "/")...)
			if err != nil {
				return nil, err
			}
			if sameOnDisk(cache, dst, f.zf) {
				res.Unchanged++
				done += int64(f.zf.UncompressedSize64)
				prog(done, total, f.rel)
				continue
			}
			if err := extractPlainFile(f.zf, dst, cache, func(n int64) { done += n; prog(done, total, f.rel) }); err != nil {
				return nil, err
			}
			res.Files++
			res.Bytes += int64(f.zf.UncompressedSize64)
		}
		res.Installed = append(res.Installed, l.ID)
		recs[strings.ToLower(l.ID)] = plainRecord{z.Name, l.Date, time.Now().UTC()}
	}
	cache.Save()
	if err := writeJSON(plainRecordPath(dcs), recs); err != nil {
		return nil, err
	}
	return res, nil
}

func extractPlainFile(zf *zip.File, dst string, cache *HashCache, tick func(int64)) error {
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
	h := crc32.NewIEEE()
	n, err := io.Copy(io.MultiWriter(out, h), &countingReader{rc, tick}) // zip checks the CRC at the end
	cerr := out.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && (n != int64(zf.UncompressedSize64) || h.Sum32() != zf.CRC32) {
		err = errors.New("damaged")
	}
	if err != nil {
		os.Remove(tmp)
		if errors.Is(err, zip.ErrChecksum) || err.Error() == "damaged" {
			return fmt.Errorf("%s is damaged inside the zip — download the zip again", path.Base(zf.Name))
		}
		return err
	}
	_ = os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("can't replace %s (is DCS running? close it and try again): %w", dst, err)
	}
	if fi, err := os.Stat(dst); err == nil {
		cache.putCRC(dst, fi, zf.CRC32)
	}
	return nil
}
