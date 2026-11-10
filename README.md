# Wet Paint: CVW-0 Livery Manager

Wet Paint installs the Wet Boys livery library into DCS, keeps it current with small monthly update packs instead of the full 20 GB download, and shows a **green light** once every jet is verified.

- **Pilots** run `WetPaint.exe` on Windows. There's nothing to install.
- **Fridge** builds the monthly pack with the same program on his Mac (`Wet Paint.app`).

---

## For pilots

1. Run **WetPaint.exe**.
2. It checks flywetboys.com for updates and checks your `Saved Games\DCS\Liveries` folder (the same for Steam and standalone DCS).
3. Hit the big red button:
   - **First time:** *Download full library*. Wet Paint grabs the full pack plus any monthly updates since.
   - **Every month after:** *Update now*. It downloads only the new jets, usually a few hundred MB instead of 20 GB.
4. Wait for the green light.

| Light | Meaning |
|---|---|
| 🟢 **Up to date** | Every Wet Boys jet is installed and byte-for-byte correct. Go fly. |
| 🟠 **Update needed** | Something is missing or out of date. Hit **Update now**. |
| ⚪ **Not verified** | Click **Verify now**. |

- **Already have the old master pack unzipped?** Wet Paint scans what you have first and only downloads what's missing or different.
- **Slow connection?** Hit **Stop** any time. Next time it resumes where it left off. Downloads are checked before anything is installed, and deleted afterwards to free the space.
- **Steam or standalone?** Both work. Liveries always go in `Saved Games\DCS\Liveries`. Wet Paint also finds your DCS install (from the Eagle Dynamics registry entry, every Steam library, or common folders) so it can check the aircraft folder names. If it picks the wrong one, hit **Change…** under *DCS install*.
- **Red "Folder names DCS won't recognise" box?** A jet's aircraft folder doesn't match what your DCS calls that aircraft, so it won't show in game. Tell Fridge; the box shows both spellings.
- **"Aircraft isn't installed in your DCS"?** Normal for modules or mods you don't own. Those liveries appear once you install the aircraft.
- **Offline or behind a firewall?** Open *Install a pack file by hand*, pick a downloaded pack zip and hit **Install & Verify**.
- **A small black window opens with it.** That's Wet Paint itself. It shows the address of the page in your browser. Leave it open while it works; close it to quit.
- **Antivirus or "Windows protected your PC" warning?** See [Antivirus & trust](#antivirus--trust) below. Only run a `WetPaint.exe` downloaded from flywetboys.com whose SHA-256 matches the one published there.

> Close DCS before updating. Windows locks texture files while DCS is running.

---

## For Fridge: monthly routine

1. Drop the new jets into your master `Liveries` folder as usual (`Liveries/<aircraft>/<livery>/`).
2. Open **Wet Paint**, go to **Build Pack**, and check the fields. They're remembered from last time; *Last release* points at your `feed.json`.
3. Hit **BUILD PACK**. **Fix anything in red** under *Problems found* before publishing:
   - a `description.lua` pointing at an old folder name;
   - an aircraft folder that looks like a typo of a real DCS name (e.g. `F-16C-50` instead of `F-16C_50`).

   Grey **check** notes list aircraft folders that aren't standard DCS names. That's expected for mods, which use their own names; just make sure the spelling matches the mod.
4. Upload **two files** to `flywetboys.com/downloads/wetpaint/`, **in this order**:
   1. `WetBoys-Liveries-2026-12-update.zip`
   2. `feed.json`. As soon as this file changes, every pilot's Wet Paint sees the update.
5. **Keep older pack zips online** so pilots who missed a month can catch up. Once you publish a new *Full master pack*, you can delete update zips older than it.

### One-time setup (do this first)

1. Create the folder `downloads/wetpaint/` on the website.
2. In Build Pack, leave **Last release** empty, tick **Full master pack**, release `2026-11` (or whatever month it is), and build into an empty folder.
3. Upload the FULL zip, then `feed.json`.
   - The full pack is about 20 GB. If the web host won't take a file that size, put it on Google Drive with a public share link, paste that link into **Pack download link** *before* building, and upload just `feed.json` to the site. Drive can throttle popular big files, so the website is the better home if it fits.
4. From then on, monthly builds are update-only and small. Re-publish a **Full master pack** every few months so new pilots get a short download.

### Command line (optional)

```
wetpaint build -src ~/Liveries -out ~/Desktop/wetpaint -prev ~/Desktop/wetpaint/feed.json -release 2026-12
wetpaint build -src ~/Liveries -out ~/Desktop/wetpaint -release 2026-11 -full     # first baseline
WetPaint.exe update            # check + download + install + verify
WetPaint.exe update -check     # just show what would be downloaded
WetPaint.exe install -pack WetBoys-Liveries-2026-12-update.zip
WetPaint.exe scan              # exit code 0 = green, 2 = update needed
```

On the Mac the binary is `Wet Paint.app/Contents/MacOS/WetPaint`.

---

## How it works

**Update feed:** `https://flywetboys.com/downloads/wetpaint/feed.json` lists every published pack (release, full or update, size, SHA-256, which jets it contains) and carries the full library index. Wet Paint checks your Liveries folder against that index, then picks the **cheapest set of packs** that fixes everything. For a new pilot that's usually "latest full pack + the updates since"; for a regular, it's just this month's update. Pack links in the feed can be relative (next to `feed.json`) or absolute, and Google Drive share links are converted to direct downloads automatically.

**Aircraft folder names:** Eagle Dynamics doesn't publish an official list. The names DCS expects are the folders it ships its own liveries in: `Bazar\Liveries\*`, `CoreMods\aircraft\*\Liveries\*` and `Mods\aircraft\*\Liveries\*` in the install, plus `Saved Games\DCS\Mods\aircraft\*\Liveries\*` for third-party mods. On a pilot's PC, Wet Paint reads those directly from their DCS install. Fridge builds on a Mac with no DCS, so the builder uses a community list from the [Hoggit wiki](https://wiki.hoggitworld.com/view/Liveries) and only flags near-misses as errors. Names are compared case-insensitively, as Windows does.

**DCS install detection (Windows):** `HKCU\Software\Eagle Dynamics\DCS World` (and the old OpenBeta/OpenAlpha keys) → `Path`; Steam's `libraryfolders.vdf` → `<library>\steamapps\common\DCSWorld`; common folders on each fixed drive. A `dcs_variant.txt` in the install (e.g. `openbeta`) means DCS uses `Saved Games\DCS.<variant>`, so Wet Paint selects that folder.

**Downloads** go to `%LocalAppData%\WetPaint\downloads`. They resume after a stop or dropped connection, are checked against the SHA-256 in the feed, and are deleted once installed. Wet Paint checks free disk space before starting.

**Pack format:** a normal `.zip`:

```
wetpaint.json                          index of the WHOLE library (every jet, every file, SHA-256)
README.txt
Liveries/<aircraft>/<livery>/...       only the jets this pack ships
```

**Install:** each file is extracted to a temp file, its hash is checked against the index, then it's moved into place. A corrupt download never leaves a half-written texture. Files already correct on disk are skipped, so re-running a pack is fast. Entries that would land outside `Liveries\` are refused. Mac clutter (`__MACOSX`, `._*`, `.DS_Store`) is stripped at build time and ignored at install.

**No downgrades:** if the pilot already has a newer index than the pack they're installing, any jet the newer release changed is skipped.

**Verify:** every file is checked for presence, size and SHA-256. Hashes are cached by size and modification time, so the first scan of 20 GB takes a minute or two and later scans are near-instant.

**Where things live (pilot PC)**
- Liveries: `Saved Games\DCS\Liveries\…`, found via the Windows *Saved Games* known folder, so moved profiles work.
- Installed index: `Saved Games\DCS\WetPaint\library.json`. It sits beside `Liveries`, not inside it, so DCS never mistakes it for an aircraft.
- Hash cache and settings: `%AppData%\WetPaint\`

**Window:** Wet Paint serves its UI on `127.0.0.1` only (with a per-run token) and opens it as a normal tab in your default browser. It runs as a plain console program: the small window shows what it's doing, and closing it quits. It quits by itself shortly after you close the browser tab, but always finishes an install first. It never starts a browser with its own profile, never runs PowerShell or other helper programs, and never asks for administrator rights.

---

## Antivirus & trust

Versions **1.0–1.2** (before 9 Nov 2026) were flagged by Microsoft Defender, and one tester saw AdGuard appear in Edge. The cause was how those builds opened their window, not anything in the livery code:

- They started Microsoft Edge with its **own new browser profile** (`%AppData%\WetPaint\window`). Edge can sign a new profile into your Microsoft account automatically and **sync your account's extensions into it**. That's the most likely way AdGuard appeared, since its "thanks for installing" page opens on install. Wet Paint contains no code that installs or touches extensions.
- They ran hidden PowerShell windows for the file pickers, were built as a windowless app with symbols stripped, and weren't code-signed. Together that matches adware and "browser hijacker" patterns, so Defender blocked them.

**1.3.0 removes all of that:** it opens a normal tab in your default browser, uses the standard Windows Open and Browse dialogs directly, runs as a visible console program, isn't stripped, and carries publisher and version details plus a manifest that runs it as a normal user (no admin).

**What will still trigger warnings until it's signed:** any brand-new unsigned exe can still get a SmartScreen "Windows protected your PC" prompt, or a Defender machine-learning flag, until it builds reputation. The real fixes:

1. **Code-sign it.** [Azure Trusted Signing](https://learn.microsoft.com/azure/trusted-signing/) is the cheapest route (about $10/month, identity verification required); a standard code-signing certificate from a CA is the alternative. Signed files from a verified publisher are trusted far more readily.
2. **Report the false positive to Microsoft** at the [Defender file submission portal](https://www.microsoft.com/wdsi/filesubmission) ("Incorrect detection", as a software developer). They usually clear a file within a few days. Re-submit each new version until it's signed.
3. **Only ever run a `WetPaint.exe` that came from flywetboys.com, a build of the `main` branch on GitHub, or one you built yourself.** The code is public, so anyone can open a pull request. Pull requests get tested but never produce a downloadable exe. Never run an exe someone sends you directly.
4. **Distribute only `WetPaint.exe`** from flywetboys.com and publish its SHA-256 from `dist/WetPaint.exe.sha256.txt` next to it, so pilots can check it with `certutil -hashfile WetPaint.exe SHA256`. Don't share the whole project folder.

**Cleaning up after 1.0–1.2** (testers only):
1. Close Wet Paint and Edge, then delete the folder `%AppData%\WetPaint\window`. That's the separate Edge profile the old version made, including anything Edge synced into it.
2. If AdGuard is also in your **normal** Edge (`edge://extensions`), it came from your Microsoft account's extension sync, not from Wet Paint. Remove it there, and check Edge → Settings → Profiles → Sync → Extensions if you don't want it to come back.

## Building from source

Needs Go 1.23+ (`winget install GoLang.Go` on Windows, or https://go.dev/dl/).

- **Windows:** double-click `build.cmd` (vet, tests, then `dist\WetPaint.exe`), or `Run Wet Paint (build from source).cmd` to build and start it.
- **Mac:** `./build.sh 1.3.0` makes `dist/WetPaint.exe`, `dist/Wet Paint.app` and `dist/WetPaint-mac.zip`.
- **GitHub:** every push runs `.github/workflows/build.yml`, which tests on real Windows and macOS and attaches a freshly built `WetPaint.exe` to the run.
- `go test ./...` runs the tests anywhere. Working on the code with Claude? It reads `CLAUDE.md` first.

| File | What's in it |
|---|---|
| `library.go` | Manifest format, scanning, hashing and cache, `description.lua` checks, verify |
| `pack.go` | Building packs (Fridge) and installing them safely (pilots) |
| `feed.go` | Update feed, download planning, resumable verified downloads |
| `aircraft.go` | DCS install detection (Steam and standalone), aircraft folder-name checks |
| `system.go` | Finding DCS folders and packs in Downloads, settings |
| `server.go` | Local UI server and job runner |
| `platform_windows.go` | Saved Games lookup, DCS install detection, opening the browser, native Windows file and folder dialogs |
| `platform_other.go` | Mac equivalents (for Fridge) |
| `ui/index.html` | The interface |
| `tools/mkicon` | Embeds `Wet Paint Icon.png` into the exe (`rsrc_windows_amd64.syso`); `build.sh` runs it |
