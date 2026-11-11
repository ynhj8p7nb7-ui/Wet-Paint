//go:build !windows

package main

// macOS/Linux support exists so Fridge can build packs on his Mac (and so the
// installer can be tested off-Windows). DCS itself only runs on Windows.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func savedGamesDir() string {
	if d := os.Getenv("WETPAINT_SAVED_GAMES"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Saved Games")
}

func desktopDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Desktop")
}

func downloadsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

func osa(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "-128") {
			return "", nil // user cancelled
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func asQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func pickFolder(title, start string) (string, error) {
	s := `POSIX path of (choose folder with prompt ` + asQuote(title)
	if fi, err := os.Stat(start); err == nil && fi.IsDir() {
		s += ` default location (POSIX file ` + asQuote(start) + `)`
	}
	return osa(s + `)`)
}

func pickFile(title, start, filter string) (string, error) {
	s := `POSIX path of (choose file with prompt ` + asQuote(title)
	if strings.Contains(filter, "*.zip") {
		s += ` of type {"zip", "public.zip-archive"}`
	} else if strings.Contains(filter, "*.json") {
		s += ` of type {"json", "public.json"}`
	}
	if fi, err := os.Stat(start); err == nil && fi.IsDir() {
		s += ` default location (POSIX file ` + asQuote(start) + `)`
	}
	return osa(s + `)`)
}

func diskFree(p string) (int64, bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(p, &st) != nil {
		return 0, false
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize)), true
}

func revealPath(p string) { _ = exec.Command("open", "-R", p).Start() }

// openWindow opens Wet Paint in the default browser, like clicking a link.
func openWindow(url string) <-chan struct{} {
	if os.Getenv("WETPAINT_NO_WINDOW") != "" {
		return nil
	}
	_ = exec.Command("open", url).Start()
	return nil
}

func attachConsole() {}

// DCS only runs on Windows; WETPAINT_DCS_INSTALL lets tests point at a fake install.
func installCandidates() []installCandidate { return nil }

func showError(msg string) { fmt.Fprintln(os.Stderr, msg) }
