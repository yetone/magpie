package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// vsoullai on X, #6167: a WSL that hangs (a distro stopping, WSL's file
// share wedged) held wsl's lock while the GUI listed agents, so a
// request's stand-in and Copilot's sign-in (provider's accounts, on every
// request's path) waited on it, the gateway's unrouted watch sent 504s
// after 30s, and the Copilot sign-in's own lock queued every request
// behind the first. Whatever hangs, wsl.exe listing which run, a distro's
// probe or the open of its /, what a request asks of WSL answers within
// wslRunningWait, as running none when wsl.exe hasn't said.
func TestWSLHangHoldsNoRequest(t *testing.T) {
	for _, hang := range []string{"-l --running -q", "-d Ubuntu", "stat"} {
		t.Run(hang, func(t *testing.T) {
			root, home := codexDistroHome(t, "model_provider = \"magpie\"\nmodel = \"relay/main\"\n")
			os.MkdirAll(filepath.Join(home, ".copilot"), 0o755)
			fakeWSL(t, "Ubuntu\r\n", "Ubuntu\r\n", map[string]string{
				"Ubuntu": "home:/home/me\ndir:.codex\ndir:.copilot\nroute:default via 172.20.0.1 dev eth0\n",
			}, map[string]string{"Ubuntu": root})
			// a first look while WSL answered: Ubuntu is known, with Codex
			if ds := wslDistros(); len(ds) != 1 || !ds[0].Running {
				t.Fatalf("%+v", ds)
			}
			wait := wslRunningWait
			wslRunningWait = 200 * time.Millisecond
			t.Cleanup(func() { wslRunningWait = wait })

			release := make(chan struct{})
			var once sync.Once
			free := func() { once.Do(func() { close(release) }) }
			hung := make(chan struct{}, 8)
			run, root0 := wslRun, wslRoot
			wslRun = func(d time.Duration, args ...string) ([]byte, error) {
				if strings.HasPrefix(strings.Join(args, " "), hang) {
					hung <- struct{}{}
					<-release
				}
				return run(d, args...)
			}
			if hang == "stat" {
				// Windows' open of the distro's / over WSL's share hangs
				wslRoot = func(name string) string {
					hung <- struct{}{}
					<-release
					return root0(name)
				}
			}
			gui := make(chan struct{})
			t.Cleanup(func() {
				free()
				<-gui
				wslSettle()
				wslRun, wslRoot = run, root0
			})

			// the GUI looks again (a rescan): it waits on the hung wsl.exe
			wsl.Lock()
			wsl.runAt = time.Time{}
			wsl.probed, wsl.failed = map[string]bool{}, map[string]time.Time{}
			wsl.Unlock()
			go func() { defer close(gui); wslDistros() }()
			select {
			case <-hung:
			case <-time.After(10 * time.Second):
				t.Fatal("the hang was never reached")
			}

			bar := wslRunningWait + 2*time.Second
			within := func(name string, f func()) {
				t.Helper()
				done := make(chan struct{})
				start := time.Now()
				go func() { defer close(done); f() }()
				select {
				case <-done:
				case <-time.After(bar):
					t.Errorf("%s waited on a hung WSL past %v", name, bar)
					free()
					<-done
				}
				if d := time.Since(start); d > bar {
					t.Errorf("%s took %v", name, d)
				}
			}
			within("a Codex stand-in", func() { wslCodexStandIn() })
			within("a Claude classifier stand-in", func() { wslClaudeClassifier("claude-haiku-4-5") })
			within("whether Ubuntu runs", func() { wslUp("Ubuntu") })
			within("Copilot's folders", func() { copilotWSLDirs() })
			within("a path's distro", func() { distroAt(filepath.Join(root, "home", "me")) })
			within("a host a distro reached Windows at", func() { wslHost("http://172.20.0.1:3425") })
			within("what WSLKeep kept", func() { WSLKept("Ubuntu", "x") })
			select {
			case <-gui:
				t.Fatal("the GUI's look didn't wait on the hung WSL")
			default:
			}
			// many requests at once don't queue behind each other
			var wg sync.WaitGroup
			start := time.Now()
			for range 13 {
				wg.Add(1)
				go func() { defer wg.Done(); copilotWSLDirs(); wslCodexStandIn() }()
			}
			wg.Wait()
			if d := time.Since(start); d > bar {
				t.Errorf("13 requests at once took %v", d)
			}
		})
	}
}

// wslSettle waits for a look and an ask of wsl.exe started in the
// background to end.
func wslSettle() {
	for _, m := range []func() chan struct{}{
		func() chan struct{} { wslBg.Lock(); defer wslBg.Unlock(); return wslBg.done },
		func() chan struct{} { wslRunAsk.Lock(); defer wslRunAsk.Unlock(); return wslRunAsk.done },
	} {
		if ch := m(); ch != nil {
			<-ch
		}
	}
}

// A distro's / that Windows can't open in wslRootWait (a stat of WSL's
// share can't be cancelled) fails its probe, tried again in wslRetryAge,
// instead of holding the look for ever.
func TestWSLRootHangFailsProbe(t *testing.T) {
	stat, w := wslStat, wslRootWait
	release := make(chan struct{})
	t.Cleanup(func() { close(release); wslStat, wslRootWait = stat, w })
	wslRootWait = 100 * time.Millisecond
	wslStat = func(string) (os.FileInfo, error) { <-release; return nil, os.ErrNotExist }
	start := time.Now()
	if r := wslRoot("Ubuntu"); r != "" {
		t.Fatalf("root %q", r)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %v", d)
	}
	wslStat = func(string) (os.FileInfo, error) { return nil, nil }
	if r := wslRoot("Ubuntu"); r != `\\wsl.localhost\Ubuntu` {
		t.Fatalf("root %q", r)
	}
}
