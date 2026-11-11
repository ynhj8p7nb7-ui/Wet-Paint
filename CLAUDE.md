# Wet Paint — guide for Claude (and humans) working on this code

Wet Paint installs, updates and verifies the **Wet Boys (CVW-0) DCS World livery library**.
Pilots run `WetPaint.exe` on Windows. Fridge, the livery artist, builds monthly packs on a Mac
with the same program (Build Pack tab). Read `README.md` for the user-facing story.

Testers are sharing this folder so you can **find what's missing or broken and fix it**. Before
changing anything, read the rules below. They exist because earlier versions got flagged as
malware.

## Hard rules (don't break these)

1. **No antivirus red flags.** Versions 1.0–1.2 were blocked by Microsoft Defender, and a tester saw
   AdGuard appear in Edge. Never reintroduce any of these:
   - launching a browser with its own profile (`--user-data-dir`, `--app` windows, Chrome/Edge flags)
   - running PowerShell, `cmd`, `rundll32`, `mshta`, `wscript` or other helper processes, especially hidden ones
   - hidden windows (`HideWindow`, `CREATE_NO_WINDOW`), `-H windowsgui`, stripped builds (`-s -w`)
   - asking for admin rights, writing to Program Files, the registry (read-only lookups are fine), startup items or scheduled tasks
   - downloading or running executables
   Open the UI with `ShellExecute` (default browser). Use direct Win32 calls for dialogs (see `platform_windows.go`).
2. **Never write outside** `Saved Games\DCS*\Liveries\`, `Saved Games\DCS*\WetPaint\`, `%AppData%\WetPaint\`
   and `%LocalAppData%\WetPaint\`. Every zip path goes through `safeJoin`, so keep it that way. Never move or
   delete the pilot's own zips in Downloads.
3. **Keep the pack format backward compatible.** Packs and `feed.json` carry `format: 1`. Pilots
   will have old packs and old versions of the app. If you must change the format, bump
   `manifestFormat`/`feedFormat` and keep reading the old one.
4. **Never overwrite a newer livery with an older one.** `installPack` skips liveries whose hash
   differs from the newest known library. Keep that guard.
5. **Only dependency is `golang.org/x/sys`.** Don't add third-party modules without a very good reason;
   every dependency is something a pilot has to trust.
6. **No personal data.** Don't add analytics, telemetry or crash uploads. The only network
   access is fetching `feed.json` and pack zips from the URLs in the feed.

## How it fits together

| File | What it does |
|---|---|
| `main.go` | Entry point and command line (`build`, `install`, `scan`, `update`, `serve`) |
| `server.go` | Local web UI on `127.0.0.1` (random port plus a per-run token) and the background job runner |
| `ui/index.html` | The whole interface (plain HTML/JS, embedded into the exe) |
| `library.go` | Manifest format, scanning folders, SHA-256 with a cache, `description.lua` checks, verify |
| `pack.go` | Building packs (Fridge) and installing them safely (pilots) |
| `feed.go` | Online updates: `feed.json`, picking the cheapest set of downloads, resumable verified downloads |
| `aircraft.go` | Finding DCS (Steam and standalone, `dcs_variant.txt`) and checking aircraft folder names |
| `system.go` | Saved Games folders, finding packs in Downloads, settings |
| `local.go` | Packs saved by hand (Discord etc.): which ones are needed, installing them, letting Update reuse them |
| `plain.go` | Ordinary livery zips (no `wetpaint.json`): spotting them, comparing with disk by CRC-32, installing without downgrades |
| `platform_windows.go` | Known folders, registry and Steam lookups, native Open and Browse dialogs, opening the browser |
| `platform_other.go` | Mac/Linux versions (Fridge builds on a Mac) |
| `tools/mkicon` | Writes `rsrc_windows_amd64.syso` (icon, version info, manifest) |
| `wetpaint_test.go` | Tests. **Add one for every bug you fix.** |

Key facts:
- DCS loads liveries from `Saved Games\DCS\Liveries\<aircraft folder>\<livery folder>\description.lua`.
  The livery folder name must match the `"../<folder>/"` paths inside `description.lua`. The aircraft folder
  name must match what DCS uses; the real list is the livery folder names in the DCS install itself
  (`Bazar\Liveries`, `CoreMods\aircraft\*\Liveries`, `Mods\aircraft\*\Liveries`).
- Every pack contains `wetpaint.json`, the **whole** library index, so installing one pack is
  enough to verify everything.
- Online updates: `https://flywetboys.com/downloads/wetpaint/feed.json` (override with the
  `WETPAINT_FEED` environment variable).

## Build and test

Windows (needs Go: `winget install GoLang.Go`):
```
build.cmd                 :: vet + tests + dist\WetPaint.exe
go test ./...
```
Mac/Linux: `./build.sh 1.3.1` (Mac also makes `Wet Paint.app`).

Useful environment variables for testing without touching your real DCS:
`WETPAINT_SAVED_GAMES`, `WETPAINT_DCS_INSTALL`, `WETPAINT_CONFIG`, `WETPAINT_DOWNLOADS`,
`WETPAINT_FEED`, `WETPAINT_PACKS_DIR` (look for hand-downloaded packs only here instead of Downloads/Desktop),
`WETPAINT_NO_WINDOW=1` (with `WetPaint.exe serve`, which prints the URL).

Before saying a change works: `go vet ./...` and `go test ./...` must pass, and if you touched the UI,
run `WetPaint.exe serve` and click through it.

## What still needs real-world testing (good places to look)

These were written and unit-tested on a Mac but never run on real Windows. Check them first:
- Finding the DCS install: the Eagle Dynamics registry `Path` key, Steam `libraryfolders.vdf`,
  default folders, and `dcs_variant.txt`
- The native Open-file and Browse-for-folder dialogs (do they appear in front of the browser?)
- Saved Games on a moved or OneDrive-redirected profile
- Installing while DCS is running (files locked): is the error message clear?
- Very long paths, non-English Windows, liveries with unusual characters in their names
- Real Wet Boys mod aircraft folder names (Sea Hog, Ghost Eye, MH-47, C-130J…): do the name checks
  behave sensibly?
- Defender/SmartScreen behaviour of a locally built exe vs a downloaded one

## Sending fixes back

- **On GitHub (public repo):** fork or branch, commit with a clear message, and open a pull request
  describing what you found, how you tested it, and what you couldn't test. GitHub tests every PR
  on Windows and macOS automatically. Nothing reaches pilots until a maintainer reviews and merges it.
  Never post a built exe in a PR or DM; pilots only run builds of `main`.
- **Without GitHub:** describe the problem and paste the diff (`git diff`) or the changed files to
  the squadron's dev channel. Don't edit the shared Dropbox folder directly; copy it to your own PC first.
