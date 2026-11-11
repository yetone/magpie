package library

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/wslrun"
)

// wslCheck is a Windows machine with WSL whose running distros are up;
// probes are the distros a command was looked for in, and runs the
// wsl.exe command line of each check started there. The check's run is
// this test binary as the fake server, in mode (MAGPIE_FAKE_MCP: "exit"
// for one that fails), or as the server's own variables say for "".
type wslCheck struct {
	mu     sync.Mutex
	probes []string
	runs   [][]string
	mode   string
}

func fakeWSLCheck(t *testing.T, up ...string) *wslCheck {
	t.Helper()
	sandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w := &wslCheck{}
	on, run, cmd := wslrun.On, wslrun.Run, wslCommand
	wslrun.On = true
	wslrun.Forget()
	wslrun.Run = func(_ time.Duration, args ...string) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"-l", "--running", "-q"}):
			return []byte(strings.Join(up, "\n")), nil
		case len(args) > 4 && args[0] == "-d" && args[2] == "-e" && args[4] == "-lc":
			w.mu.Lock()
			w.probes = append(w.probes, args[1])
			w.mu.Unlock()
			return []byte("bin:/home/loong/.nvm/versions/node/v24.9.0/bin/node\npath:/home/loong/.nvm/versions/node/v24.9.0/bin:/usr/bin\nlpath:/usr/bin\nmount:/mnt/c/\n"), nil
		case len(args) > 3 && args[0] == "-d" && args[2] == "--exec":
			return nil, nil // the run in the distro ended
		}
		return nil, errors.New("unexpected wsl.exe " + strings.Join(args, " "))
	}
	wslCommand = func(ctx context.Context, tl wslrun.Tool, args ...string) *exec.Cmd {
		// wsl.exe as magpie starts it, its program swapped for the fake
		c := tl.Command(ctx, args...)
		w.mu.Lock()
		w.runs = append(w.runs, slices.Clone(c.Args))
		mode := w.mode
		w.mu.Unlock()
		c.Err = nil
		c.Path, c.Args = self, []string{self, "-test.run=^TestFakeMCPServer$"}
		if mode != "" {
			c.Path, c.Args = "/usr/bin/env", append([]string{"/usr/bin/env", fakeMode + "=" + mode}, c.Args...)
		}
		return c
	}
	t.Cleanup(func() {
		wslrun.On, wslrun.Run, wslCommand = on, run, cmd
		wslrun.Forget()
	})
	return w
}

// loong on Discord: a server whose command is a program in WSL, given
// only to agents there, was checked on Windows, where it "exited (1)",
// while the agents started it fine. It is checked in their distro, through
// wsl.exe, on the PATH an interactive shell there has (nvm's node).
func TestCheckWSLOnlyServerInItsDistro(t *testing.T) {
	w := fakeWSLCheck(t, "Ubuntu", "Debian")
	s := &Server{Name: "x", Transport: "stdio", Command: "node", Args: []string{"/home/loong/tools/xxx.js"},
		Env: map[string]string{fakeMode: "ok"}, Agents: []string{"claude@wsl:Ubuntu", "codex@wsl:Ubuntu"}}
	h := CheckServer(context.Background(), s)
	if h.State != "ok" || h.Tools != 3 || !slices.Equal(h.Checked, []string{"Ubuntu"}) {
		t.Fatalf("got %+v, want ok with 3 tools, checked in Ubuntu", h)
	}
	if !slices.Equal(w.probes, []string{"Ubuntu"}) || len(w.runs) != 1 {
		t.Fatalf("probes %v, runs %v: want one run, in Ubuntu", w.probes, w.runs)
	}
	r := w.runs[0]
	want := []string{"wsl.exe", "-d", "Ubuntu", "--exec", "/usr/bin/env", "PATH=/home/loong/.nvm/versions/node/v24.9.0/bin:/usr/bin"}
	if len(r) != 9 || !slices.Equal(r[:6], want) || !strings.HasPrefix(r[6], "MAGPIE_WSL_RUN=") ||
		r[7] != "/home/loong/.nvm/versions/node/v24.9.0/bin/node" || r[8] != "/home/loong/tools/xxx.js" {
		t.Fatalf("run %q", r)
	}

	// a distro that isn't running isn't started to check it: the check
	// says it couldn't, not that the server failed
	s.Agents = []string{"claude@wsl:Arch"}
	h = CheckServer(context.Background(), s)
	if h.State != "error" || h.Why != "wsldown" || h.Detail != "Arch" || len(h.Checked) != 0 {
		t.Fatalf("got %+v, want wsldown for Arch", h)
	}
	if slices.Contains(w.probes, "Arch") || len(w.runs) != 1 {
		t.Fatalf("a stopped distro was asked: probes %v, runs %v", w.probes, w.runs)
	}

	// a program the distro hasn't is not found there
	old := wslrun.Run
	wslrun.Run = func(d time.Duration, args ...string) ([]byte, error) {
		if len(args) > 4 && args[4] == "-lc" {
			return []byte("mount:/mnt/c/\n"), nil
		}
		return old(d, args...)
	}
	s.Command, s.Agents = "magpie-no-such-node", []string{"claude@wsl:Debian"}
	if h = CheckServer(context.Background(), s); h.Why != "notfound" || h.Detail != "magpie-no-such-node" || !slices.Equal(h.Checked, []string{"Debian"}) {
		t.Fatalf("got %+v, want notfound in Debian", h)
	}
}

// A server only this machine's agents have is checked here as before,
// WSL never asked; so is a Windows program given to agents in WSL
// (magpie's own image server, which runs on Windows whoever starts it).
func TestCheckWindowsServerHere(t *testing.T) {
	w := fakeWSLCheck(t, "Ubuntu")
	s := fakeStdio(t, "ok", nil)
	s.Agents = []string{"claude", "codex"}
	h := CheckServer(context.Background(), s)
	if h.State != "ok" || h.Tools != 3 || h.Checked != nil {
		t.Fatalf("got %+v, want ok here with nothing named", h)
	}
	for _, c := range []string{`D:\tools\Magpie\magpie-windows-amd64.exe`, `C:/x/npx.cmd`} {
		if got := checkSides(&Server{Transport: "stdio", Command: c, Agents: []string{"claude@wsl:Ubuntu"}}); !slices.Equal(got, []string{""}) {
			t.Errorf("%s: sides %q, want this machine", c, got)
		}
	}
	if len(w.probes) != 0 || len(w.runs) != 0 {
		t.Fatalf("WSL was asked: probes %v, runs %v", w.probes, w.runs)
	}
}

// A server both this machine's agents and a distro's have is checked in
// each place, and a failure says which one failed.
func TestCheckMixedServerEachSide(t *testing.T) {
	if runtime.GOOS == "windows" {
		// the fake server is this test binary, a Windows program here, and
		// one is checked on this machine only (TestCheckWindowsServerHere);
		// the distro's side runs it through /usr/bin/env
		t.Skip("the fake server is a Windows program here")
	}
	w := fakeWSLCheck(t, "Ubuntu")
	s := fakeStdio(t, "ok", nil)
	s.Agents = []string{"claude", "codex@wsl:Ubuntu"}
	h := CheckServer(context.Background(), s)
	if h.State != "ok" || h.Tools != 3 || !slices.Equal(h.Checked, []string{"", "Ubuntu"}) {
		t.Fatalf("got %+v, want ok here and in Ubuntu", h)
	}
	if len(w.runs) != 1 || w.runs[0][2] != "Ubuntu" {
		t.Fatalf("runs %v", w.runs)
	}

	// it fails in WSL only: named as Ubuntu's
	w.mode = "exit"
	h = CheckServer(context.Background(), s)
	if h.State != "error" || h.Why != "exited" || h.Code != 3 || !slices.Equal(h.Checked, []string{"", "Ubuntu"}) {
		t.Fatalf("got %+v, want exited 3 in Ubuntu after ok here", h)
	}

	// it fails here: named as this machine's, before WSL is asked
	w.mode = ""
	runs := len(w.runs)
	bad := *s
	bad.Env = map[string]string{fakeMode: "exit"}
	h = CheckServer(context.Background(), &bad)
	if h.State != "error" || h.Why != "exited" || !slices.Equal(h.Checked, []string{""}) || len(w.runs) != runs {
		t.Fatalf("got %+v (runs %d → %d), want exited here", h, runs, len(w.runs))
	}

	// a distro that isn't running: checked here only, and said so
	s.Agents = []string{"claude", "codex@wsl:Arch"}
	if h = CheckServer(context.Background(), s); h.State != "ok" || !slices.Equal(h.Checked, []string{""}) {
		t.Fatalf("got %+v, want ok here only", h)
	}

	// the agents moving from here into WSL is a check of its own
	a, b := *s, *s
	a.Agents, b.Agents = []string{"claude"}, []string{"claude@wsl:Ubuntu"}
	if healthKey(&a, nil) == healthKey(&b, nil) {
		t.Fatal("a server moved into WSL keeps its check from Windows")
	}
}
