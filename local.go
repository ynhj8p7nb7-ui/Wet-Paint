package main

// local.go — packs a pilot downloaded by hand (from Discord, a Drive link, a
// squadron mate) and left in Downloads.
//
// Wet Paint works out which of those zips actually fix something, installs
// them oldest first, and lets an online update use a pack that's already in
// Downloads instead of fetching it again. It never moves or deletes the
// pilot's own files.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// LocalPlan says what the packs found in Downloads can do.
type LocalPlan struct {
	Latest   string     `json:"latest"`            // newest release among installed + packs on disk
	Steps    []PackInfo `json:"steps"`             // packs to install, oldest first
	Bytes    int64      `json:"bytes"`             // total size of those zips
	Jets     int        `json:"jets"`              // liveries they install or repair
	Missing  []string   `json:"missing,omitempty"` // needed liveries no pack on disk has
	UpToDate bool       `json:"up_to_date"`        // nothing on disk needs fixing
	Newer    bool       `json:"newer"`             // a pack on disk is a newer release than what's installed
	library  *Manifest
}

// planLocal picks the packs on disk that bring the pilot up to date with the
// newest release any of them (or the installed library) knows about.
// last is the most recent verify result, used to spot broken jets.
func planLocal(packs []PackInfo, installed *Manifest, last *ScanResult) *LocalPlan {
	newest := installed
	for i := range packs {
		if l := packs[i].lib; l != nil && (newest == nil || l.Created.After(newest.Created)) {
			newest = l
		}
	}
	if newest == nil {
		return nil
	}
	lp := &LocalPlan{Latest: newest.Release, library: newest}
	lp.Newer = installed == nil || newest.Created.After(installed.Created)

	var instIdx map[string]*Livery
	if installed != nil {
		instIdx = installed.Index()
	}
	broken := map[string]bool{}
	if last != nil && installed != nil && last.Release == installed.Release {
		for _, l := range last.Liveries {
			if l.State != "ok" {
				broken[strings.ToLower(l.ID)] = true
			}
		}
	}
	need := map[string]string{}
	for i := range newest.Liveries {
		l := &newest.Liveries[i]
		k := strings.ToLower(l.ID())
		if il := instIdx[k]; il == nil || il.Hash != l.Hash || broken[k] {
			need[l.ID()] = l.Updated
		}
	}
	lp.UpToDate = len(need) == 0
	if lp.UpToDate || len(packs) == 0 {
		lp.Missing = sortedKeys(need)
		return lp
	}

	// Reuse the online planner: the packs on disk act as a tiny feed.
	f := &Feed{Library: newest}
	byPath := map[string]PackInfo{}
	for _, p := range packs {
		kind := p.Kind
		if kind == "" {
			kind = "update"
		}
		f.Packs = append(f.Packs, FeedPack{Release: p.Release, Kind: kind, URL: p.Path, Bytes: p.Bytes, Created: p.Created, Liveries: p.Liveries})
		byPath[p.Path] = p
	}
	steps, bytes, missing := planDownloads(f, need, false)
	for _, s := range steps {
		lp.Steps = append(lp.Steps, byPath[s.URL])
	}
	lp.Bytes, lp.Missing = bytes, missing
	if len(lp.Steps) > 0 {
		lp.Jets = len(need) - len(missing)
	}
	return lp
}

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// installLocal installs the plan's packs in order. The newest library is
// recorded first so an older pack can never overwrite a newer jet.
func installLocal(ctx context.Context, lp *LocalPlan, dcs string, cache *HashCache, set func(string, int64, int64, string)) (*UpdateResult, error) {
	if lp == nil || len(lp.Steps) == 0 {
		return nil, errors.New("no pack in your Downloads folder would change anything")
	}
	var extract int64
	idx := lp.library.Index()
	for _, s := range lp.Steps {
		for _, id := range s.Liveries {
			if l := idx[strings.ToLower(id)]; l != nil {
				extract += l.Size()
			}
		}
	}
	if err := checkSpace(dcs, 0, dcs, extract); err != nil {
		return nil, err
	}
	if have, err := loadInstalledManifest(dcs); err != nil || lp.library.Created.After(have.Created) {
		if err := saveInstalledManifest(dcs, lp.library); err != nil {
			return nil, err
		}
	}
	res := &UpdateResult{}
	for i, s := range lp.Steps {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		label := fmt.Sprintf("Installing %s from Downloads (%d of %d)", s.Release, i+1, len(lp.Steps))
		ir, _, err := installPack(s.Path, dcs, cache, func(d, t int64, c string) { set(label, d, t, c) })
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Name, err)
		}
		res.Installed = append(res.Installed, s.Release)
		res.Liveries = append(res.Liveries, ir.Installed...)
		res.Files += ir.Files
		res.Unchanged += ir.Unchanged
		res.Bytes += ir.Bytes
	}
	return res, nil
}

// localCopy returns a pack on disk that is the same pack as the online one,
// so Update can skip the download.
func localCopy(s FeedPack, local []PackInfo) string {
	for _, p := range local {
		if p.Release == s.Release && p.Kind == s.Kind && p.Created.Equal(s.Created) && (s.Bytes == 0 || p.Bytes == s.Bytes) {
			return p.Path
		}
	}
	return ""
}
