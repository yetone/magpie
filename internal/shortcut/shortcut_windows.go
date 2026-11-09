package shortcut

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/yetone/magpie/internal/appdir"
)

// appPath is where Win+R and `start magpie` look magpie.exe up: the user's
// own, no administrator needed. A var so the test writes a key of its own.
var appPath = `Software\Microsoft\Windows\CurrentVersion\App Paths\magpie.exe`

func registerAppPath(exe string) {
	if k, _, err := registry.CreateKey(registry.CURRENT_USER, appPath, registry.SET_VALUE); err == nil {
		_ = k.SetStringValue("", exe)
		_ = k.SetStringValue("Path", filepath.Dir(exe))
		k.Close()
	}
	registerUninstall(exe)
}

// uninstallKey is magpie's entry in Settings › Apps › Installed apps (and
// Control Panel's Programs and Features), the user's own. With none,
// Windows' own way to remove a program didn't list magpie, so it was
// deleted by hand, which runs no code: Codex kept openai_base_url on a
// gateway that was gone (Jevik_AI on X). Its Uninstall runs magpie's own
// (UninstallCommand), which puts every agent back first. A var so the
// test writes a key of its own.
var uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\magpie`

// registerUninstall writes the entry for exe, at every start, so it
// follows an exe that was moved.
func registerUninstall(exe string) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	for name, v := range map[string]string{
		"DisplayName":     Name,
		"DisplayIcon":     exe + ",0",
		"Publisher":       Name,
		"InstallLocation": filepath.Dir(exe),
		"UninstallString": UninstallCommand(exe),
		"URLInfoAbout":    "https://usemagpie.ai",
	} {
		_ = k.SetStringValue(name, v)
	}
	// Uninstall is the one button: there is nothing to modify or repair
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
}

// unregisterUninstall takes the entry out while it is exe's, or its exe
// is gone; another magpie's stays.
func unregisterUninstall(exe string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return nil // none
	}
	v, _, err := k.GetStringValue("DisplayIcon")
	k.Close()
	v = strings.TrimSuffix(v, ",0")
	if err == nil && v != "" && !strings.EqualFold(filepath.Clean(v), filepath.Clean(exe)) {
		if _, err := os.Stat(v); err == nil {
			return nil // another magpie's
		}
	}
	return registry.DeleteKey(registry.CURRENT_USER, uninstallKey)
}

func registeredAppPath() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, appPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func startMenuLink() (string, error) {
	dir, err := programs()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, Name+".lnk"), nil
}

func writeShortcut(lnk, exe string) (bool, error) { return write(lnk, exe) }

// programs is the user's Start menu Programs folder.
func programs() (string, error) {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Programs, windows.KF_FLAG_CREATE); err == nil && p != "" {
		return p, nil
	}
	a := appdir.Getenv("APPDATA")
	if a == "" {
		return "", errors.New("no Start menu folder")
	}
	p := filepath.Join(a, `Microsoft\Windows\Start Menu\Programs`)
	return p, os.MkdirAll(p, 0o755)
}

// write has the shortcut at lnk open exe, in exe's folder, with its icon,
// and reports whether it had to: one already opening exe is left as it is.
// It is Windows' own shell link, made by the shell's WScript.Shell.
func write(lnk, exe string) (bool, error) {
	// COM is set up per thread; this one keeps the goroutine
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		switch {
		case !errors.As(err, &oe):
			return false, err
		case oe.Code() == 1: // S_FALSE: set up already, still ours to undo
			defer ole.CoUninitialize()
		case oe.Code() != 0x80010106: // RPC_E_CHANGED_MODE: set up otherwise, usable
			return false, err
		}
	} else {
		defer ole.CoUninitialize()
	}
	unk, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return false, err
	}
	defer unk.Release()
	shell, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return false, err
	}
	defer shell.Release()
	// the shortcut at lnk, read in when there is one
	v, err := oleutil.CallMethod(shell, "CreateShortcut", lnk)
	if err != nil {
		return false, err
	}
	sc := v.ToIDispatch()
	defer sc.Release()
	prop := func(name string) string {
		p, err := oleutil.GetProperty(sc, name)
		if err != nil {
			return ""
		}
		defer p.Clear()
		return p.ToString()
	}
	if !Stale(prop("TargetPath"), prop("WorkingDirectory"), exe) {
		return false, nil
	}
	for name, val := range map[string]any{
		"TargetPath":       exe,
		"WorkingDirectory": filepath.Dir(exe),
		"IconLocation":     exe + ",0",
		"Description":      Name,
	} {
		if _, err := oleutil.PutProperty(sc, name, val); err != nil {
			return false, err
		}
	}
	if _, err := oleutil.CallMethod(sc, "Save"); err != nil {
		return false, err
	}
	return true, nil
}

// unregisterAppPath takes magpie's App Paths entry out while it opens exe,
// or an exe that is gone, and its Installed apps entry the same way.
func unregisterAppPath(exe string) error {
	uerr := unregisterUninstall(exe)
	k, err := registry.OpenKey(registry.CURRENT_USER, appPath, registry.QUERY_VALUE)
	if err != nil {
		return uerr // none
	}
	v, _, err := k.GetStringValue("")
	k.Close()
	if err == nil && v != "" && !strings.EqualFold(filepath.Clean(v), filepath.Clean(exe)) {
		if _, err := os.Stat(v); err == nil {
			return uerr // another magpie's
		}
	}
	return errors.Join(uerr, registry.DeleteKey(registry.CURRENT_USER, appPath))
}
