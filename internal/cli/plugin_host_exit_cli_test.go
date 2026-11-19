package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
	"github.com/yetone/magpie/internal/update"
)

// A command that does its work and ends starts no plugin host: magpie
// --version and magpie group <id>, run while magpie serve was up, each
// started a second host to list the plugins' providers in the background,
// beside the gateway's, and a browser-backed plugin's models opened the
// Chrome profiles the gateway's host held (#7146). What was listed last
// is on disk, and is what they read.
func TestOneShotCommandsStartNoPluginHost(t *testing.T) {
	bun := testenv.Bun(t)
	cliHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"version"}, {"--version"}, {"-v"}, {"licenses"},
		{"groups"}, {"group", "fakeco"}, {"models"}, {"providers"}, {"accounts"}, {"agents"},
	} {
		// the host stopped and the plugins not asked since, as in a
		// magpie just started
		plugin.Settle()
		_, _ = printed(t, func() error { return run(args) })
		plugin.Refreshed()
		if plugin.Running() {
			t.Errorf("magpie %s started a plugin host", strings.Join(args, " "))
		}
	}
	// what serves (as a test does, run having put it back) keeps the
	// plugins' list current in the background
	plugin.Settle()
	plugin.Cached()
	plugin.Refreshed()
	if !plugin.Running() {
		t.Error("a serving magpie's Cached asked the plugins nothing")
	}
}

// hostExitEnv has the test binary start a plugin host on a call that never
// ends, then end as magpie does: its value says how.
const hostExitEnv = "MAGPIE_CLI_TEST_HOST_EXIT"

// hostThenExit, in the test binary started by TestExitEndsPluginHosts,
// asks the stuck plugin for its usage, which never answers and holds the
// host up as a browser would, and ends
// magpie once the hook has it: by a command that succeeds, one that
// fails, or SIGTERM.
func hostThenExit(how string) {
	version = "0.1.9999"
	if how == "signal" {
		// before any host starts, as in Main
		endProbesOnSignal()
	}
	go plugin.AccountUsage(context.Background(), "stuckco", "")
	for deadline := time.Now().Add(time.Minute); !strings.Contains(readString(os.Getenv("STUCK_TRACE")), `"signal"`); {
		if time.Now().After(deadline) {
			os.Stderr.WriteString("the stuck plugin's usage hook was never called\n")
			os.Exit(9)
		}
		time.Sleep(20 * time.Millisecond)
	}
	switch how {
	case "version":
		os.Args = []string{"magpie", "version"}
	case "error":
		os.Args = []string{"magpie", "no-such-agent-7146"}
	case "signal":
		select {}
	}
	Main(version)
	os.Exit(8) // Main ends the process
}

func readString(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// Whatever plugin host a command started ends before the command does,
// however it ends: a host whose stdin closes leaves only once its calls
// are answered, so one still waiting on a plugin outlived magpie, its
// parent init, and held what the plugin had open (#7146).
func TestExitEndsPluginHosts(t *testing.T) {
	bun := testenv.Bun(t)
	cliHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/stuck/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "stuckco", 0, nil, "k-stuckco", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	plugin.Settle()
	for _, how := range []string{"version", "error", "signal"} {
		t.Run(how, func(t *testing.T) {
			if how == "signal" && runtime.GOOS == "windows" {
				t.Skip("no SIGTERM to send on Windows")
			}
			dir := t.TempDir()
			trace, pidFile := filepath.Join(dir, "trace.jsonl"), filepath.Join(dir, "host.pid")
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), hostExitEnv+"="+how, "STUCK_USAGE=1", "STUCK_USAGE_HOLD=1", "STUCK_TRACE="+trace, "STUCK_PID="+pidFile)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			defer func() {
				_ = cmd.Process.Kill()
				<-exited
			}()
			if how == "signal" {
				for !strings.Contains(readString(trace), `"signal"`) {
					select {
					case err := <-exited:
						exited <- err
						t.Fatalf("the magpie ended before its host's call began: %v\n%s", err, out.String())
					case <-time.After(20 * time.Millisecond):
					}
				}
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-exited:
				exited <- err
			case <-time.After(2 * time.Minute):
				t.Fatalf("the magpie never ended\n%s", out.String())
			}
			pid, err := strconv.Atoi(strings.TrimSpace(readString(pidFile)))
			if err != nil {
				t.Fatalf("the host never said its pid: %v\n%s", err, out.String())
			}
			// ended and waited for before magpie exits; the room is for a
			// slow process table, not for the host
			deadline := time.Now().Add(10 * time.Second)
			for update.Alive(pid) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if update.Alive(pid) {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
				t.Fatalf("magpie ended (%s) and left its plugin host %d running\n%s", how, pid, out.String())
			}
		})
	}
}
