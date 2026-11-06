//go:build windows && !nogui

package gui

import (
	"encoding/binary"
	"errors"
	"log"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// askInstallFolder has update.Settle ask where to install, in the
// reader's language.
func askInstallFolder() {
	update.AskFolder = func(suggest string, check func(string) error) string {
		w := installWordsFor(trayLang(settings.Load().Lang, systemLang))
		var out string
		done := make(chan struct{})
		// COM and the dialogs on a thread of their own, which ends with
		// the goroutine: Wails sets up the main thread later
		go func() {
			runtime.LockOSThread()
			defer close(done)
			out = installDialog(w, suggest, check)
		}()
		<-done
		return out
	}
}

const (
	idInstall = 100
	idChange  = 101
)

// installDialog asks until the user installs in a folder check accepts,
// or cancels (""). With no dialog to be had, dir is taken, as before
// there was a choice.
func installDialog(w installWords, dir string, check func(string) error) string {
	note := ""
	for {
		btn, err := taskDialog(w, dir, note)
		if err != nil {
			log.Println("install: the folder dialog:", err)
			return dir
		}
		switch btn {
		case idInstall:
			if err := check(dir); err != nil {
				note = w.refusal(dir, err)
				continue
			}
			return dir
		case idChange:
			picked, err := pickInstallFolder(w.pick, dir)
			if err != nil {
				log.Println("install: the folder picker:", err)
			}
			if picked == "" {
				continue
			}
			dir, note = update.InstallDir(picked), ""
			if err := check(dir); err != nil {
				note = w.refusal(dir, err)
			}
		default:
			return ""
		}
	}
}

var (
	comctl32           = windows.NewLazySystemDLL("comctl32.dll")
	procTaskDialogIndi = comctl32.NewProc("TaskDialogIndirect")
	procSHCreateItem   = windows.NewLazySystemDLL("shell32.dll").NewProc("SHCreateItemFromParsingName")
)

// taskDialog shows Windows' task dialog (comctl32 6, which the exe's
// manifest asks for): the folder under the question, Install, Change…
// and Cancel, and note, a refusal, with a warning icon above. It answers
// the button pressed.
func taskDialog(w installWords, dir, note string) (int, error) {
	if err := procTaskDialogIndi.Find(); err != nil {
		return 0, err
	}
	keep := [][]uint16{}
	str := func(s string) uintptr {
		u, _ := windows.UTF16FromString(s)
		keep = append(keep, u)
		return uintptr(unsafe.Pointer(&u[0]))
	}
	// TASKDIALOG_BUTTON and TASKDIALOGCONFIG are packed to 1 byte
	// (commctrl.h's pshpack1), so they are laid out by hand
	buttons := make([]byte, 2*12)
	for i, b := range []struct {
		id    int32
		label string
	}{{idInstall, w.install}, {idChange, w.change}} {
		binary.LittleEndian.PutUint32(buttons[i*12:], uint32(b.id))
		binary.LittleEndian.PutUint64(buttons[i*12+4:], uint64(str(b.label)))
	}
	// the folder, or what is wrong with it, which names it
	content := dir
	if note != "" {
		content = note
	}
	const (
		tdfAllowCancel = 0x0008
		tdfSizeToFit   = 0x01000000 // TDF_SIZE_TO_CONTENT: a long folder isn't cut short
		tdcbfCancel    = 0x0008
		tdWarningIcon  = 0xFFFF // MAKEINTRESOURCEW(-1)
		appIcon        = 3      // the exe's icon group (build/windows/winres.json)
		cfgSize        = 160
	)
	var mod windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &mod)
	show := func(icon uintptr) (int, uintptr) {
		cfg := make([]byte, cfgSize)
		le := binary.LittleEndian
		le.PutUint32(cfg[0:], cfgSize)
		le.PutUint64(cfg[12:], uint64(mod))
		le.PutUint32(cfg[20:], tdfAllowCancel|tdfSizeToFit)
		le.PutUint32(cfg[24:], tdcbfCancel)
		le.PutUint64(cfg[28:], uint64(str(w.title)))
		le.PutUint64(cfg[36:], uint64(icon))
		le.PutUint64(cfg[44:], uint64(str(w.ask)))
		le.PutUint64(cfg[52:], uint64(str(content)))
		le.PutUint32(cfg[60:], 2)
		le.PutUint64(cfg[64:], uint64(uintptr(unsafe.Pointer(&buttons[0]))))
		le.PutUint32(cfg[72:], idInstall)
		var pressed int32
		hr, _, _ := procTaskDialogIndi.Call(uintptr(unsafe.Pointer(&cfg[0])), uintptr(unsafe.Pointer(&pressed)), 0, 0)
		runtime.KeepAlive(cfg)
		return int(pressed), hr
	}
	icon := uintptr(appIcon)
	if note != "" {
		icon = tdWarningIcon
	}
	pressed, hr := show(icon)
	if hr != 0 && icon == appIcon {
		// a build without the icon in its resources
		pressed, hr = show(0)
	}
	runtime.KeepAlive(keep)
	runtime.KeepAlive(buttons)
	if hr != 0 {
		return 0, syscall.Errno(hr)
	}
	return pressed, nil
}

// pickInstallFolder is Windows' folder picker (IFileOpenDialog with
// FOS_PICKFOLDERS), opened where dir is, or the nearest folder above it
// there is: the folder picked, "" when none was.
func pickInstallFolder(title, dir string) (string, error) {
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED|0x4 /* COINIT_DISABLE_OLE1DDE */); err != nil {
		var oe *ole.OleError
		// S_FALSE: COM was set up on this thread already
		if !errors.As(err, &oe) || oe.Code() != 1 {
			return "", err
		}
	}
	defer ole.CoUninitialize()
	d, err := ole.CreateInstance(ole.NewGUID("{DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7}"), ole.NewGUID("{d57c7288-d4ad-4768-be02-9d969532d960}"))
	if err != nil {
		return "", err
	}
	defer d.Release()
	// IFileOpenDialog's vtable: IUnknown (0-2), Show (3), then IFileDialog
	const (
		show        = 3
		setOptions  = 9
		getOptions  = 10
		setFolder   = 12
		setTitle    = 17
		getResult   = 20
		pickFolders = 0x20
		forceFS     = 0x40
		pathMust    = 0x800
		cancelled   = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)
	)
	call := func(obj unsafe.Pointer, i int, args ...uintptr) uintptr {
		vt := *(*unsafe.Pointer)(obj)
		fn := *(*uintptr)(unsafe.Add(vt, i*int(unsafe.Sizeof(uintptr(0)))))
		hr, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
		return hr
	}
	obj := unsafe.Pointer(d)
	var opts uint32
	if hr := call(obj, getOptions, uintptr(unsafe.Pointer(&opts))); hr != 0 {
		return "", syscall.Errno(hr)
	}
	if hr := call(obj, setOptions, uintptr(opts|pickFolders|forceFS|pathMust)); hr != 0 {
		return "", syscall.Errno(hr)
	}
	if t, err := windows.UTF16PtrFromString(title); err == nil {
		call(obj, setTitle, uintptr(unsafe.Pointer(t)))
	}
	start := filepath.Dir(filepath.Clean(dir))
	for !isDir(start) && filepath.Dir(start) != start {
		start = filepath.Dir(start)
	}
	if p, err := windows.UTF16PtrFromString(start); err == nil && isDir(start) {
		iidShellItem := ole.NewGUID("{43826d1e-e718-42ee-bc55-a1e261c37bfe}")
		var item unsafe.Pointer
		if hr, _, _ := procSHCreateItem.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(iidShellItem)), uintptr(unsafe.Pointer(&item))); hr == 0 && item != nil {
			call(obj, setFolder, uintptr(item))
			call(item, 2) // Release
		}
	}
	switch hr := call(obj, show, 0); hr {
	case 0:
	case cancelled:
		return "", nil
	default:
		return "", syscall.Errno(hr)
	}
	var res unsafe.Pointer
	if hr := call(obj, getResult, uintptr(unsafe.Pointer(&res))); hr != 0 || res == nil {
		return "", syscall.Errno(hr)
	}
	defer call(res, 2) // Release
	const getDisplayName, sigdnFileSysPath = 5, 0x80058000
	var name *uint16
	if hr := call(res, getDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); hr != 0 || name == nil {
		return "", syscall.Errno(hr)
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(name))
	return windows.UTF16PtrToString(name), nil
}
