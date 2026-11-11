// Wet Paint — livery installer & verifier for the Wet Boys (CVW-0) DCS squadron.
//
//	WetPaint.exe                    open the app
//	WetPaint.exe pack.zip           open the app with that pack selected (drag a zip onto the exe)
//	WetPaint.exe install -pack P [-dcs D]
//	WetPaint.exe scan [-dcs D]
//	WetPaint.exe update [-dcs D] [-full] [-check]     download + install whatever is missing
//	WetPaint.exe build -src DIR -out DIR [-prev library.json] [-release 2026-11] [-full]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var version = "1.0.0"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && !strings.EqualFold(filepath.Ext(args[0]), ".zip") {
		attachConsole()
		if err := runCLI(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	pack := ""
	if len(args) > 0 {
		pack, _ = filepath.Abs(args[0])
	}
	if err := runGUI(pack, os.Getenv("WETPAINT_NO_WINDOW") == ""); err != nil {
		showError("Wet Paint couldn't start:\n\n" + err.Error())
		os.Exit(1)
	}
}

func cliProgress(label string) func(done, total int64, cur string) {
	var last time.Time
	return func(done, total int64, cur string) {
		if time.Since(last) < 250*time.Millisecond && done < total {
			return
		}
		last = time.Now()
		pct := 100.0
		if total > 0 {
			pct = float64(done) * 100 / float64(total)
		}
		if len(cur) > 40 {
			cur = "…" + cur[len(cur)-39:]
		}
		fmt.Fprintf(os.Stderr, "\r%-24s %5.1f%%  %-40s", label, pct, cur)
		if done >= total {
			fmt.Fprintln(os.Stderr)
		}
	}
}

func defaultDCS(flagVal string) (string, error) {
	if flagVal != "" {
		return normalizeDCSDir(flagVal), nil
	}
	if s := loadSettings(); s.DCSDir != "" {
		return s.DCSDir, nil
	}
	if ds := findDCSDirs(); len(ds) > 0 {
		return ds[0].Path, nil
	}
	return "", fmt.Errorf("couldn't find Saved Games\\DCS under %s — pass -dcs", savedGamesDir())
}

func runCLI(args []string) error {
	cache := loadHashCache()
	switch args[0] {
	case "build":
		fs := flag.NewFlagSet("build", flag.ExitOnError)
		var o BuildOptions
		fs.StringVar(&o.Source, "src", "", "master Liveries folder")
		fs.StringVar(&o.Out, "out", "", "output folder")
		fs.StringVar(&o.Prev, "prev", "", "last release's library.json (omit for the very first build)")
		fs.StringVar(&o.Release, "release", time.Now().Format("2006-01"), "release label")
		fs.BoolVar(&o.Full, "full", false, "ship every livery (a full master pack)")
		fs.StringVar(&o.URL, "url", "", "public download link for this pack, if not next to feed.json")
		fs.BoolVar(&o.CheckOnly, "check", false, "only read and check the folder; don't write a pack")
		fs.Parse(args[1:])
		progs := map[string]func(int64, int64, string){}
		res, err := buildPack(o, cache, func(phase string, d, t int64, c string) {
			if progs[phase] == nil {
				progs[phase] = cliProgress(phase)
			}
			progs[phase](d, t, c)
		})
		if err != nil {
			return err
		}
		for _, w := range res.Warnings {
			fmt.Printf("[%s] %s: %s\n", strings.ToUpper(w.Level), w.Livery, w.Message)
		}
		if res.CheckOnly {
			fmt.Printf("Checked %d liveries (%s). A build would ship %d: %d new, %d changed; %d removed. Nothing was written.\n",
				res.Total, human(res.SrcBytes), res.Shipped, len(res.New), len(res.Changed), len(res.Removed))
			return nil
		}
		if res.NoChanges {
			fmt.Println("Nothing new or changed since", o.Prev, "— no pack built.")
			return nil
		}
		fmt.Printf("Release %s: %d new, %d changed, %d removed, %d liveries total\n", res.Release, len(res.New), len(res.Changed), len(res.Removed), res.Total)
		for _, id := range res.New {
			fmt.Println("  + ", id)
		}
		for _, id := range res.Changed {
			fmt.Println("  ~ ", id)
		}
		for _, id := range res.Removed {
			fmt.Println("  - ", id)
		}
		fmt.Printf("Pack:    %s (%s)\nFeed:    %s\nUpload both to the website (zip first, then feed.json).\n", res.PackPath, human(res.PackBytes), res.FeedPath)
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		pack := fs.String("pack", "", "pack zip")
		dcs := fs.String("dcs", "", `Saved Games\DCS folder`)
		fs.Parse(args[1:])
		d, err := defaultDCS(*dcs)
		if err != nil {
			return err
		}
		res, m, err := installPack(*pack, d, cache, cliProgress("Installing"))
		if err != nil {
			return err
		}
		fmt.Printf("Installed %d liveries (%d files, %s) into %s — %d files were already current\n", len(res.Installed), res.Files, human(res.Bytes), filepath.Join(d, "Liveries"), res.Unchanged)
		for _, s := range res.Skipped {
			fmt.Println("  skipped (a newer version is already expected):", s)
		}
		return printScan(d, m, cache)
	case "scan":
		fs := flag.NewFlagSet("scan", flag.ExitOnError)
		dcs := fs.String("dcs", "", `Saved Games\DCS folder`)
		fs.Parse(args[1:])
		d, err := defaultDCS(*dcs)
		if err != nil {
			return err
		}
		m, err := loadInstalledManifest(d)
		if err != nil {
			return err
		}
		return printScan(d, m, cache)
	case "update":
		fs := flag.NewFlagSet("update", flag.ExitOnError)
		dcs := fs.String("dcs", "", `Saved Games\DCS folder`)
		full := fs.Bool("full", false, "download the full library even if less would do")
		feed := fs.String("feed", feedURL(loadSettings()), "update feed URL")
		dry := fs.Bool("check", false, "only report what would be downloaded")
		fs.Parse(args[1:])
		d, err := defaultDCS(*dcs)
		if err != nil {
			return err
		}
		ctx := context.Background()
		f, err := fetchFeed(ctx, *feed)
		if err != nil {
			return err
		}
		_, instErr := loadInstalledManifest(d)
		scan, err := scanInstalled(filepath.Join(d, "Liveries"), f.Library, cache, cliProgress("Checking"))
		if err != nil {
			return err
		}
		plan := makePlan(f, scan, instErr != nil)
		steps, bytes := plan.Steps, plan.Bytes
		if *full && plan.FullOption != nil {
			steps, bytes = plan.FullOption.Steps, plan.FullOption.Bytes
		}
		if plan.UpToDate && !*full {
			fmt.Printf("GREEN — up to date with release %s, nothing to download\n", plan.Latest)
			return nil
		}
		fmt.Printf("Release %s: %d of %d liveries OK. Plan (%s):\n", plan.Latest, scan.OK, scan.Total, human(bytes))
		for _, s := range steps {
			fmt.Printf("  %-8s %-6s %8s  %s\n", s.Release, s.Kind, human(s.Bytes), s.URL)
		}
		for _, m := range plan.Missing {
			fmt.Println("  not available online:", m)
		}
		if *dry {
			return nil
		}
		progs := map[string]func(int64, int64, string){}
		_, err = runUpdate(ctx, f, steps, d, cache, findPacks(), func(phase string, dd, t int64, c string) {
			if progs[phase] == nil {
				progs[phase] = cliProgress(phase)
			}
			progs[phase](dd, t, c)
		})
		if err != nil {
			return err
		}
		m, _ := loadInstalledManifest(d)
		return printScan(d, m, cache)
	case "serve":
		return runGUI("", false)
	case "version", "-v", "--version":
		fmt.Println("Wet Paint", version)
	default:
		return fmt.Errorf("unknown command %q (build, install, scan)", args[0])
	}
	return nil
}

func printScan(dcs string, m *Manifest, cache *HashCache) error {
	res, err := scanInstalled(filepath.Join(dcs, "Liveries"), m, cache, cliProgress("Verifying"))
	if err != nil {
		return err
	}
	for _, l := range res.Liveries {
		if l.State != "ok" {
			fmt.Printf("  %-10s %s (in the %s pack)\n", strings.ToUpper(l.State), l.ID, l.Updated)
		}
	}
	if res.UpToDate {
		fmt.Printf("GREEN — all %d liveries up to date (release %s)\n", res.Total, res.Release)
		return nil
	}
	fmt.Printf("NOT UP TO DATE — %d of %d liveries OK. Packs needed: %s\n", res.OK, res.Total, strings.Join(res.NeedPacks, ", "))
	os.Exit(2)
	return nil
}

func human(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB"} {
		if f < 1024 || u == "GB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}
