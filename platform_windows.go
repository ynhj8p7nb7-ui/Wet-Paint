//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func regString(root registry.Key, path, name string) string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return v
}

// installCandidates lists places DCS World might be installed: the Eagle
// Dynamics updater's registry keys, every Steam library, and default folders
// on each fixed drive.
func installCandidates() []installCandidate {
	var c []installCandidate
	for _, n := range []string{"DCS World", "DCS World OpenBeta", "DCS World 2 OpenAlpha"} {
		if p := regString(registry.CURRENT_USER, `Software\Eagle Dynamics\`+n, "Path"); p != "" {
			c = append(c, installCandidate{p, "Standalone"})
		}
	}
	steamRoots := []string{
		regString(registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath"),
		regString(registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath"),
		regString(registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath"),
	}
	seen := map[string]bool{}
	for _, r := range steamRoots {
		if r == "" || seen[strings.ToLower(filepath.Clean(r))] {
			continue
		}
		seen[strings.ToLower(filepath.Clean(r))] = true
		for _, lib := range steamLibraries(filepath.Clean(r)) {
			c = append(c, installCandidate{filepath.Join(lib, "steamapps", "common", "DCSWorld"), "Steam"})
		}
	}
	drives, _ := windows.GetLogicalDrives()
	for i := 0; i < 26; i++ {
		if drives&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(rp) != windows.DRIVE_FIXED {
			continue
		}
		for _, sub := range []string{`Program Files\Eagle Dynamics\DCS World`, `Program Files\Eagle Dynamics\DCS World OpenBeta`,
			`Eagle Dynamics\DCS World`, `DCS World`, `DCS World OpenBeta`, `Games\DCS World`, `SteamLibrary\steamapps\common\DCSWorld`} {
			kind := "Standalone"
			if strings.Contains(sub, "steamapps") {
				kind = "Steam"
			}
			c = append(c, installCandidate{root + sub, kind})
		}
	}
	return c
}

func savedGamesDir() string {
	if d := os.Getenv("WETPAINT_SAVED_GAMES"); d != "" {
		return d
	}
	// Respects people who moved Saved Games to another drive.
	if p, err := windows.KnownFolderPath(windows.FOLDERID_SavedGames, 0); err == nil && p != "" {
		return p
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "Saved Games")
}

func downloadsDir() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil && p != "" {
		return p
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "Downloads")
}

func diskFree(p string) (int64, bool) {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, false
	}
	var free, total, totalFree uint64
	if windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree) != nil {
		return 0, false
	}
	return int64(free), true
}

func revealPath(p string) { _ = exec.Command("explorer.exe", "/select,", p).Start() }

// openWindow opens Wet Paint in the pilot's normal default browser, exactly
// like clicking a link. Earlier versions started Edge with a private profile
// to look like an app window; that made Edge sign the new profile in and sync
// the user's extensions into it, and looks like adware to antivirus, so it's gone.
func openWindow(url string) <-chan struct{} {
	verb, _ := windows.UTF16PtrFromString("open")
	u, _ := windows.UTF16PtrFromString(url)
	_ = windows.ShellExecute(0, verb, u, nil, nil, windows.SW_SHOWNORMAL)
	return nil
}

// Wet Paint is a normal console program on Windows: double-clicking it shows a
// small window that says what it's doing, and closing that window quits it.
func attachConsole() {}

func showError(msg string) {
	t, _ := windows.UTF16PtrFromString("Wet Paint")
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

// ---------------------------------------------------------------- native pickers
//
// Standard Windows Open / Browse-for-folder dialogs, called directly — no
// PowerShell, no helper processes.

var (
	comdlg32                = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileNameW    = comdlg32.NewProc("GetOpenFileNameW")
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	procSHBrowseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	procCoTaskMemFree       = windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree")
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procSendMessageW        = user32.NewProc("SendMessageW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

type openFileName struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

type browseInfo struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

// onUIThread runs fn on one locked OS thread with COM initialised, as the
// shell dialogs require.
func onUIThread(fn func() (string, error)) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil {
			defer windows.CoUninitialize()
		}
		s, err := fn()
		ch <- res{s, err}
	}()
	r := <-ch
	return r.s, r.err
}

// topOwner makes a tiny always-on-top window in the middle of the screen to own
// the dialog, so it opens in front of the browser instead of hiding behind it.
func topOwner() uintptr {
	cls, _ := windows.UTF16PtrFromString("STATIC")
	cx, _, _ := procGetSystemMetrics.Call(0)
	cy, _, _ := procGetSystemMetrics.Call(1)
	const wsExTopmost, wsExToolwindow, wsPopup = 0x8, 0x80, 0x80000000
	h, _, _ := procCreateWindowExW.Call(wsExTopmost|wsExToolwindow, uintptr(unsafe.Pointer(cls)), 0, wsPopup,
		cx/2, cy/3, 1, 1, 0, 0, 0, 0)
	if h != 0 {
		procSetForegroundWindow.Call(h)
	}
	return h
}

func utf16z(s string) []uint16 { return append(utf16.Encode([]rune(s)), 0) }

func pickFile(title, start, filter string) (string, error) {
	return onUIThread(func() (string, error) {
		owner := topOwner()
		defer procDestroyWindow.Call(owner)
		buf := make([]uint16, 32768)
		flt := utf16z(strings.ReplaceAll(filter, "|", "\x00") + "\x00")
		ofn := openFileName{
			hwndOwner:    owner,
			lpstrFilter:  &flt[0],
			nFilterIndex: 1,
			lpstrFile:    &buf[0],
			nMaxFile:     uint32(len(buf)),
			flags:        0x00080000 | 0x00001000 | 0x00000800 | 0x00000008 | 0x00000004, // EXPLORER|FILEMUSTEXIST|PATHMUSTEXIST|NOCHANGEDIR|HIDEREADONLY
		}
		ofn.lStructSize = uint32(unsafe.Sizeof(ofn))
		t := utf16z(title)
		ofn.lpstrTitle = &t[0]
		if fi, err := os.Stat(start); err == nil && fi.IsDir() {
			d := utf16z(start)
			ofn.lpstrInitialDir = &d[0]
		}
		if r, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
			return "", nil // cancelled
		}
		return windows.UTF16ToString(buf), nil
	})
}

var browseStart []uint16

// browseCallback selects the starting folder once the dialog is up.
var browseCallback = syscall.NewCallback(func(hwnd, msg, lp, data uintptr) uintptr {
	const bffmInitialized, bffmSetSelectionW = 1, 0x467
	if msg == bffmInitialized && data != 0 {
		procSendMessageW.Call(hwnd, bffmSetSelectionW, 1, data)
	}
	return 0
})

func pickFolder(title, start string) (string, error) {
	return onUIThread(func() (string, error) {
		owner := topOwner()
		defer procDestroyWindow.Call(owner)
		name := make([]uint16, 260)
		t := utf16z(title)
		bi := browseInfo{
			hwndOwner:      owner,
			pszDisplayName: &name[0],
			lpszTitle:      &t[0],
			ulFlags:        0x0001 | 0x0010 | 0x0040, // RETURNONLYFSDIRS | EDITBOX | NEWDIALOGSTYLE
			lpfn:           browseCallback,
		}
		if fi, err := os.Stat(start); err == nil && fi.IsDir() {
			browseStart = utf16z(start)
			bi.lParam = uintptr(unsafe.Pointer(&browseStart[0]))
		}
		pidl, _, _ := procSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
		if pidl == 0 {
			return "", nil // cancelled
		}
		defer procCoTaskMemFree.Call(pidl)
		path := make([]uint16, 32768)
		if r, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&path[0]))); r == 0 {
			return "", errors.New("that isn't a normal folder — pick a folder on a drive")
		}
		return windows.UTF16ToString(path), nil
	})
}
