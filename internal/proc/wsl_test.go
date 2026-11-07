package proc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/settings"
)

// #4872: with Settings' Detect agents in WSL off, nothing magpie makes
// starts wsl.exe, by any name or path: a request's stand-in asked it past
// agent's gates, and on a computer without WSL each ask put up a window
// offering to install it.
func TestWSLOffStartsNoWSL(t *testing.T) {
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	// a distro registered: only the setting refuses it
	defer FakeWSLRegistry([]string{"Ubuntu"}, true, "")()
	names := []string{"wsl.exe", "WSL.EXE", "wsl", `C:\Windows\System32\wsl.exe`, "/mnt/c/Windows/System32/wsl.exe"}
	// each is only started while it is refused: on, wsl.exe isn't run
	makers := map[string]func(name string) *exec.Cmd{
		"Command": func(n string) *exec.Cmd { return Command(n, "-l", "--running", "-q") },
		"CommandContext": func(n string) *exec.Cmd {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			return CommandContext(ctx, n, "-l", "-q")
		},
		"ProbeContext": func(n string) *exec.Cmd {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			return ProbeContext(ctx, n, "-l", "-q")
		},
	}

	if err := settings.Save(settings.Settings{NoWSLAgents: true}); err != nil {
		t.Fatal(err)
	}
	for how, mk := range makers {
		for _, n := range names {
			c := mk(n)
			if !errors.Is(c.Err, ErrWSLOff) {
				t.Errorf("detection off, %s(%q) wasn't refused: %v", how, n, c.Err)
				continue
			}
			if _, err := c.Output(); !errors.Is(err, ErrWSLOff) || c.Process != nil {
				t.Errorf("detection off, %s(%q) started: %v", how, n, err)
			}
		}
		// only wsl.exe itself
		for _, n := range []string{"wslconfig.exe", "notwsl.exe", `C:\wsl\git.exe`} {
			if c := mk(n); errors.Is(c.Err, ErrWSLOff) {
				t.Errorf("%s(%q) refused as wsl.exe", how, n)
			}
		}
	}

	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	for how, mk := range makers {
		if c := mk("wsl.exe"); errors.Is(c.Err, ErrWSLOff) {
			t.Errorf("detection on, %s(wsl.exe) refused", how)
		}
	}
}

// TraderX219 on X: with no WSL distro registered, nothing magpie makes
// starts wsl.exe, which on a computer without WSL's package put up a
// console window asking to install it every 20 to 30 seconds. The registry
// that can't be read, or that has a distro, starts it as before, and a
// distro registered later is run at once.
func TestNoWSLDistroStartsNoWSL(t *testing.T) {
	defer FakeWSLRegistry(nil, true, "")()
	for _, n := range []string{"wsl.exe", "WSL.EXE", `C:\Windows\System32\wsl.exe`} {
		c := Command(n, "-l", "--running", "-q")
		if !errors.Is(c.Err, ErrNoWSLDistro) {
			t.Fatalf("no distro, %q wasn't refused: %v", n, c.Err)
		}
		if _, err := c.Output(); !errors.Is(err, ErrNoWSLDistro) || c.Process != nil {
			t.Fatalf("no distro, %q started: %v", n, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if c := ProbeContext(ctx, n, "-l", "-q"); !errors.Is(c.Err, ErrNoWSLDistro) {
			t.Fatalf("no distro, probe of %q wasn't refused: %v", n, c.Err)
		}
		cancel()
	}
	if c := Command("git", "version"); c.Err != nil {
		t.Fatalf("git refused: %v", c.Err)
	}

	defer FakeWSLRegistry(nil, false, "")()
	if c := Command("wsl.exe", "-l", "-q"); c.Err != nil {
		t.Fatalf("registry unread, wsl.exe refused: %v", c.Err)
	}
	defer FakeWSLRegistry([]string{"Ubuntu"}, true, "")()
	if c := Command("wsl.exe", "-l", "-q"); c.Err != nil {
		t.Fatalf("a distro registered, wsl.exe refused: %v", c.Err)
	}
}

func utf16le(s string) []byte {
	var b []byte
	for _, r := range utf16.Encode([]rune(s)) {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

// A wsl.exe that says WSL must be updated isn't run again for hours, by
// any route, unless WSL's package or distros change; a failure that says
// something else, or a run that succeeded, holds nothing back.
func TestWSLUpdateBacksOff(t *testing.T) {
	t.Cleanup(func() { os.Remove(wslUpdatePath()) })
	defer FakeWSLRegistry([]string{"Ubuntu"}, true, "2.6.1.0")()
	refused := func() bool {
		return errors.Is(Command("wsl.exe", "-l", "--running", "-q").Err, ErrWSLNeedsUpdate)
	}
	fail := errors.New("exit status 1")

	// what doesn't ask for an update
	WSLSaid([]byte("run wsl.exe --update yourself\n"), nil)
	WSLSaid(utf16le("There is no distribution with the supplied name.\r\nError code: Wsl/Service/WSL_E_DISTRO_NOT_FOUND\r\n"), fail)
	WSLSaid(nil, ErrNoWSLDistro)
	if refused() {
		t.Fatal("refused after runs that didn't ask for an update")
	}

	says := map[string][]byte{
		"utf-16": utf16le("Windows Subsystem for Linux must be updated to the latest version to proceed. You can update by running 'wsl.exe --update'.\r\n"),
		// the reporter's words, as the screenshot has them
		"chinese": utf16le("\ufeffWSL 必须更新到最新版本……按任意键安装\r\n"),
		"prompt":  []byte("\r\n按任意键安装适用于 Linux 的 Windows 子系统。\r\n按 CTRL-C 或关闭此窗口以取消。\r\n"),
		"stderr":  nil,
	}
	for name, out := range says {
		os.Remove(wslUpdatePath())
		err := fail
		if out == nil {
			err = &exec.ExitError{Stderr: []byte("This application requires the Windows Subsystem for Linux Optional Component.\nInstall it by running: wsl.exe --install --no-distribution\n")}
		}
		WSLSaid(out, err)
		if !refused() {
			t.Fatalf("%s: wsl.exe run again after it asked for an update", name)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if c := CommandContext(ctx, "wsl.exe", "-d", "Ubuntu", "-e", "true"); !errors.Is(c.Err, ErrWSLNeedsUpdate) {
			t.Fatalf("%s: a run in a distro wasn't refused: %v", name, c.Err)
		}
		cancel()
	}

	// the package updated, or a distro added: asked again at once
	restore := FakeWSLRegistry([]string{"Ubuntu"}, true, "2.6.2.0")
	if refused() {
		t.Fatal("refused after WSL's package changed")
	}
	restore()
	restore = FakeWSLRegistry([]string{"Ubuntu", "Debian"}, true, "2.6.1.0")
	if refused() {
		t.Fatal("refused after a distro was added")
	}
	restore()
	if !refused() {
		t.Fatal("not refused for WSL as it was")
	}
	// the registry unread: the mark stands
	restore = FakeWSLRegistry(nil, false, "")
	if !refused() {
		t.Fatal("registry unread, the mark didn't stand")
	}
	restore()

	// its age runs out
	b, _ := json.Marshal(wslUpdateMark{At: time.Now().Add(-wslUpdateAge - time.Minute), Sig: wslSig([]string{"Ubuntu"}, true)})
	if err := os.WriteFile(wslUpdatePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if refused() {
		t.Fatal("refused past wslUpdateAge")
	}
	// a mark that doesn't parse isn't one
	if err := os.WriteFile(wslUpdatePath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if refused() {
		t.Fatal("refused for a mark that doesn't parse")
	}
}
