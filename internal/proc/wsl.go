package proc

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// ErrWSLOff is what a wsl.exe start ends with, unrun, while Settings'
// Detect agents in WSL is off.
var ErrWSLOff = errors.New("not run: Detect agents in WSL is off in Settings")

// ErrNoWSLDistro is what a wsl.exe start ends with, unrun, while Windows
// has no WSL distro registered for this user: there is nothing it could
// list or run, and on a computer without WSL's package Windows' own
// wsl.exe answers each run with a console window of its own asking to
// install it (TraderX219 on X: one every 20 to 30 seconds).
var ErrNoWSLDistro = errors.New("not run: WSL has no distribution installed")

// ErrWSLNeedsUpdate is what a wsl.exe start ends with, unrun, for a while
// after wsl.exe said WSL must be installed or updated (WSLSaid): each run
// would put the same window up again.
var ErrWSLNeedsUpdate = errors.New("not run: wsl.exe said lately that WSL must be updated (wsl.exe --update)")

// wslOff refuses cmd when it is wsl.exe and Settings' Detect agents in WSL
// is off (#1264). Every command magpie starts is made here, so this is the
// one place that holds for all of them: a request's Claude Code stand-in
// and classifier asked wsl.exe which distros run past the gates in agent
// and wslrun, and on a computer without WSL Windows' own wsl.exe answers
// each ask with a console window offering to install it, one per request
// (#4872).
//
// For the same window it is refused with detection on too: while the
// registry says no distro is registered (wslRegistered), and for
// wslUpdateAge after wsl.exe said WSL must be updated (WSLSaid).
func wslOff(cmd *exec.Cmd, name string) {
	if !isWSL(name) {
		return
	}
	if settings.Shared().NoWSLAgents {
		cmd.Err = ErrWSLOff
		return
	}
	names, ok := wslRegistered()
	if ok && len(names) == 0 {
		cmd.Err = ErrNoWSLDistro
		return
	}
	if wslUpdateLately(names, ok) {
		cmd.Err = ErrWSLNeedsUpdate
	}
}

// isWSL is whether name is wsl.exe, by name or path, in any case.
func isWSL(name string) bool {
	base := name[strings.LastIndexAny(name, `/\`)+1:]
	return strings.EqualFold(base, "wsl.exe") || strings.EqualFold(base, "wsl")
}

// wslRegistered is the WSL distros registered for this user, as the
// registry has them (HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss,
// a key with a DistributionName for each), read without running wsl.exe;
// ok is false when that can't be told (off Windows, or a read failed), and
// wsl.exe is asked as before. It is read at every start, which costs
// little beside wsl.exe's own: a distro installed later is seen at once.
// A var for tests.
var wslRegistered = registeredDistros

// wslPackage is the installed WSL package's version, as its installer
// registered it (HKLM's Lxss\MSI Version), "" for none or unknown. A var
// for tests.
var wslPackage = wslPackageVersion

// wslUpdateAge is how long wsl.exe isn't run again after it said WSL must
// be updated, unless WSL changes meanwhile (wslSig).
const wslUpdateAge = 6 * time.Hour

// wslUpdatePath, while recent, says wsl.exe answered that WSL must be
// installed or updated: every magpie process (the app, a CLI, the Claude
// Code helper) reads it.
func wslUpdatePath() string { return filepath.Join(settings.Dir(), "wsl-update") }

type wslUpdateMark struct {
	At  time.Time `json:"at"`
	Sig string    `json:"sig"`
}

// wslSig is what the update mark holds of WSL, so that a change to it (the
// package updated or installed, a distro added or removed) has wsl.exe
// asked again at once rather than wslUpdateAge on. "" when the distros
// can't be read: such a mark stands its whole age.
func wslSig(names []string, ok bool) string {
	if !ok {
		return ""
	}
	names = slices.Clone(names)
	slices.Sort(names)
	return wslPackage() + "|" + strings.Join(names, "\n")
}

func wslUpdateLately(names []string, ok bool) bool {
	b, err := os.ReadFile(wslUpdatePath())
	if err != nil {
		return false
	}
	var m wslUpdateMark
	// a mark that doesn't parse isn't one: wsl.exe is asked, and says again
	if json.Unmarshal(b, &m) != nil || m.At.IsZero() {
		return false
	}
	if age := time.Since(m.At); age < 0 || age >= wslUpdateAge {
		return false
	}
	return m.Sig == "" || !ok || m.Sig == wslSig(names, ok)
}

// WSLSaid is told what a wsl.exe run printed and how it ended. When that
// says WSL must be installed or updated, wsl.exe isn't run again for
// wslUpdateAge (wslOff), by any magpie process, unless WSL changes. Every
// runner of magpie's that reads wsl.exe's output tells it.
func WSLSaid(out []byte, err error) {
	if !wslWantsUpdate(out, err) {
		return
	}
	names, ok := wslRegistered()
	b, _ := json.Marshal(wslUpdateMark{At: time.Now().UTC(), Sig: wslSig(names, ok)})
	if edit.WriteAtomic(wslUpdatePath(), b) == nil {
		log.Printf("wsl.exe says WSL must be updated or installed (%s): not run again for %s, or until WSL changes", firstLine(wslText(out, err)), wslUpdateAge)
	}
}

// wslUpdateSays are what wsl.exe prints when it can't run until WSL is
// installed or updated, in any language: the commands it names stay in
// English (Locked in WSL's own strings), as do its error codes. The inbox
// wsl.exe's own prompt is in its .mui (English and Chinese below), and the
// last is the reporter's.
var wslUpdateSays = []string{
	"wsl.exe --update", "wsl --update", "--prompt-before-exit",
	"--install --no-distribution", // the optional component is missing
	"WSL_E_WSL_OPTIONAL_COMPONENT_REQUIRED",
	"Press any key to install Windows Subsystem for Linux",
	"按任意键安装适用于 Linux 的 Windows 子系统",
	"必须更新到最新版本",
}

// wslWantsUpdate is whether a wsl.exe run that failed said WSL must be
// installed or updated. One that succeeded is never taken so, whatever it
// printed: a distro's own output may name those commands.
func wslWantsUpdate(out []byte, err error) bool {
	if err == nil || errors.Is(err, ErrWSLOff) || errors.Is(err, ErrNoWSLDistro) || errors.Is(err, ErrWSLNeedsUpdate) {
		return false
	}
	s := strings.ToLower(wslText(out, err))
	for _, w := range wslUpdateSays {
		if strings.Contains(s, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// wslText is what a wsl.exe run wrote, stdout and stderr, UTF-16LE (as
// wsl.exe writes unless WSL_UTF8 is set, which the inbox one may not
// read) or UTF-8.
func wslText(out []byte, err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return decodeWSL(out) + "\n" + decodeWSL(ee.Stderr)
	}
	return decodeWSL(out)
}

func decodeWSL(b []byte) string {
	// UTF-8 text has no NUL: one says UTF-16
	if !bytes.HasPrefix(b, []byte{0xff, 0xfe}) && bytes.IndexByte(b, 0) < 0 {
		return string(b)
	}
	b = bytes.TrimPrefix(b, []byte{0xff, 0xfe})
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
