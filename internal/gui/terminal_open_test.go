package gui

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
)

// installed stubs look: the programs named are found at /bin/<name>.
func installed(t *testing.T, names ...string) {
	t.Helper()
	was := look
	look = func(name string) (string, error) {
		for _, n := range names {
			if n == name {
				return "/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { look = was })
}

func psDecode(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%2 != 0 {
		t.Fatalf("not UTF-16LE base64: %q", s)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// lileo0668 on Discord (#4474): a session or an agent opens in PowerShell,
// pwsh or Windows Terminal. A shell gets its console from cmd's start (the
// empty title first), and the line goes encoded, whole, whatever it spells.
func TestWinTerminalArgv(t *testing.T) {
	line := `Set-Location -LiteralPath 'C:\it''s; a $(x) 中文'; & 'C:\a b\claude.cmd' --resume 'x;y'`
	installed(t, "wt.exe", "pwsh.exe", "powershell.exe")
	for _, tc := range []struct {
		choice string
		head   []string
	}{
		{"", []string{"cmd.exe", "/c", "start", "", "/bin/powershell.exe"}},
		{"system", []string{"cmd.exe", "/c", "start", "", "/bin/powershell.exe"}},
		{"powershell", []string{"cmd.exe", "/c", "start", "", "/bin/powershell.exe"}},
		{"pwsh", []string{"cmd.exe", "/c", "start", "", "/bin/pwsh.exe"}},
		{"wt", []string{"/bin/wt.exe", "new-tab", "--", "/bin/pwsh.exe"}},
	} {
		argv, err := winTerminalArgv(line, tc.choice)
		if err != nil {
			t.Fatalf("%q: %v", tc.choice, err)
		}
		n := len(tc.head)
		if len(argv) != n+4 || !reflect.DeepEqual(argv[:n], tc.head) || !reflect.DeepEqual(argv[n:n+3], []string{"-NoExit", "-NoLogo", "-EncodedCommand"}) {
			t.Fatalf("%q: %q", tc.choice, argv)
		}
		if got := psDecode(t, argv[n+3]); got != line {
			t.Fatalf("%q: the line came back as %q", tc.choice, got)
		}
	}
	// Windows Terminal without PowerShell 7 runs Windows PowerShell
	installed(t, "wt.exe", "powershell.exe")
	if argv, err := winTerminalArgv(line, "wt"); err != nil || argv[3] != "/bin/powershell.exe" {
		t.Fatalf("wt, no pwsh: %q %v", argv, err)
	}
	for _, choice := range []string{"pwsh", "com.apple.Terminal", "xterm"} {
		if _, err := winTerminalArgv(line, choice); err == nil || !strings.Contains(err.Error(), "no longer available") {
			t.Fatalf("%q: %v", choice, err)
		}
	}
}

func TestLinuxTerminalArgv(t *testing.T) {
	t.Setenv("TERMINAL", "")
	installed(t, "xfce4-terminal", "kitty", "wezterm", "x-terminal-emulator")
	for choice, want := range map[string][]string{
		"xfce4-terminal": {"/bin/xfce4-terminal", "-x", "sh", "/s"},
		"kitty":          {"/bin/kitty", "sh", "/s"},
		"wezterm":        {"/bin/wezterm", "start", "--", "sh", "/s"},
		// the system's: Debian's alternative, with its -e
		"":       {"/bin/x-terminal-emulator", "-e", "sh", "/s"},
		"system": {"/bin/x-terminal-emulator", "-e", "sh", "/s"},
	} {
		if got, err := linuxTerminalArgv(choice, "/s"); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q %v; want %q", choice, got, err, want)
		}
	}
	// $TERMINAL first, with its own words
	t.Setenv("TERMINAL", "kitty --single-instance")
	if got, _ := linuxTerminalArgv("", "/s"); !reflect.DeepEqual(got, []string{"/bin/kitty", "sh", "/s"}) {
		t.Errorf("$TERMINAL: %q", got)
	}
	if _, err := linuxTerminalArgv("gnome-terminal", "/s"); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Errorf("a terminal gone: %v", err)
	}
	installed(t)
	if _, err := linuxTerminalArgv("", "/s"); err == nil {
		t.Error("no terminal at all opened something")
	}
}

func TestDiscoverOwnTerminals(t *testing.T) {
	t.Setenv("TERMINAL", "")
	installed(t, "wt.exe", "powershell.exe")
	got, err := discoverOwnTerminals("windows")
	if err != nil || got.Default != "powershell" || len(got.Apps) != 2 || got.Apps[0].ID != "wt" || got.Apps[1].ID != "powershell" {
		t.Fatalf("windows: %+v %v", got, err)
	}
	installed(t, "konsole", "xdg-terminal-exec")
	got, err = discoverOwnTerminals("linux")
	if err != nil || got.Default != "xdg-terminal-exec" || len(got.Apps) != 2 {
		t.Fatalf("linux: %+v %v", got, err)
	}
	installed(t)
	if _, err := discoverOwnTerminals("linux"); err == nil {
		t.Fatal("linux with no terminal")
	}
	if _, err := discoverOwnTerminals("freebsd"); err == nil {
		t.Fatal("freebsd")
	}
}

// The Windows line for an agent's own program: PowerShell's call operator,
// the path quoted as PowerShell quotes. On Windows it is run by PowerShell
// for real, from a folder that spells commands.
func TestAgentLineWindows(t *testing.T) {
	sandboxHome(t)
	// no ; in it: Windows reads PATH as split there
	dir := filepath.Join(t.TempDir(), `it's a $(New-Item made1) & New-Item made2 #`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, body := "claude", "#!/bin/sh\n"
	if runtime.GOOS == "windows" {
		name, body = "claude.cmd", "@echo ran> ran\r\n"
	}
	prog := filepath.Join(dir, name)
	if err := os.WriteFile(prog, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	proc.Forget()
	t.Cleanup(proc.Forget)
	a := &agent.Agent{ID: "claude", Bin: "claude"}
	got := agentLine(a, "", "windows")
	if want := "& '" + strings.ReplaceAll(a.BinPath(), "'", "''") + "'"; got != want || !strings.Contains(got, "it''s") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if runtime.GOOS != "windows" {
		return
	}
	work := t.TempDir()
	// by its own path: the sandbox's PATH may leave System32 out
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := proc.Command(ps, "-NoLogo", "-NonInteractive", "-NoProfile", "-EncodedCommand", psEncode(got))
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if b, _ := os.ReadFile(filepath.Join(work, "ran")); err != nil || strings.TrimSpace(string(b)) != "ran" {
		t.Fatalf("%q didn't run the program: %q (%v %s)", got, b, err, out)
	}
	for _, d := range []string{work, dir, filepath.Dir(dir)} {
		if made, _ := filepath.Glob(filepath.Join(d, "made*")); len(made) != 0 {
			t.Fatalf("ran more than the program: %q", made)
		}
	}
}

func TestAgentLineWSL(t *testing.T) {
	a := &agent.Agent{ID: "claude@wsl:Ubuntu 24", Bin: "claude", WSL: "Ubuntu 24"}
	want := `wsl.exe -d 'Ubuntu 24' -e sh -lc 'exec "${SHELL:-sh}" -lic ''claude'''`
	if got := agentLine(a, "", "windows"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// The script a Linux terminal runs, run for real by a stand-in terminal:
// the line runs from a path that spells commands, then the user's shell.
func TestOpenLinuxTerminalRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a POSIX shell's script")
	}
	bin := t.TempDir()
	// xterm: -e, then the program and its words
	if err := os.WriteFile(filepath.Join(bin, "xterm"), []byte("#!/bin/sh\nshift\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), `it's $(touch made1); touch made2 #`)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	// the user's shell, left open after the line: here one that quits
	if err := os.WriteFile(filepath.Join(bin, "shell"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", filepath.Join(bin, "shell"))
	proc.Forget()
	t.Cleanup(proc.Forget)
	line := "cd " + shellQuote(work) + " && echo ran > ran"
	if err := openLinuxTerminal(line, "xterm"); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(work, "ran")
	for deadline := time.Now().Add(30 * time.Second); ; {
		if b, _ := os.ReadFile(ran); string(b) == "ran\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the terminal never ran the line")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if made, _ := filepath.Glob(filepath.Join(filepath.Dir(work), "made*")); len(made) != 0 {
		t.Fatalf("ran more than the line: %q", made)
	}
	if made, _ := filepath.Glob(filepath.Join(work, "made*")); len(made) != 0 {
		t.Fatalf("ran more than the line: %q", made)
	}
	// a terminal that fails at once says so
	if err := os.WriteFile(filepath.Join(bin, "xterm"), []byte("#!/bin/sh\necho 'cannot open display' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := openLinuxTerminal("true", "xterm"); err == nil || !strings.Contains(err.Error(), "cannot open display") {
		t.Fatalf("a failing terminal: %v", err)
	}
}
