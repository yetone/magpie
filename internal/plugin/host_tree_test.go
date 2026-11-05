package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary stands in for a program a plugin starts (a headless
// browser, a daemon) when MAGPIE_HOST_TREE_CHILD names a file: it starts
// one of its own, as a browser starts its GPU and renderer processes, and
// sits; that one writes its pid to the file and sits too.
func init() {
	if f := os.Getenv("MAGPIE_HOST_TREE_CHILD"); f != "" {
		kid := exec.Command(os.Args[0])
		kid.Env = append(os.Environ(), "MAGPIE_HOST_TREE_CHILD=", "MAGPIE_HOST_TREE_GRANDCHILD="+f)
		if kid.Start() != nil {
			os.Exit(1)
		}
		time.Sleep(90 * time.Second)
		os.Exit(0)
	}
	if f := os.Getenv("MAGPIE_HOST_TREE_GRANDCHILD"); f != "" {
		_ = os.WriteFile(f+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600)
		_ = os.Rename(f+".tmp", f)
		time.Sleep(90 * time.Second)
		os.Exit(0)
	}
}

// A plugin host ended ends what its plugin started, at any depth: a host
// killed alone left a plugin's headless Edge's processes running with no
// parent (Bun ends its own children with it on Windows, through a job they
// can start others out of, but not theirs), one more set for
// every host ended (620 msedge.exe and 57GB of temp profiles in an hour on
// one Windows machine, #5402, found and first fixed by baijiu666). Each way
// a host ends is tried: stopped (Restart, as a proxy change or an update
// does) and leaving as its input closes, stopped and killed once stopWait
// has passed (a line too long kills it the same way, host.kill), and the
// host ending by itself.
func TestHostEndedEndsWhatItsPluginStarted(t *testing.T) {
	for _, how := range []string{"stop", "killed", "own end"} {
		t.Run(how, func(t *testing.T) {
			sandbox(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
			spawn := "Bun.spawn({ cmd: [" + q(self) + "], env: { ...process.env, MAGPIE_HOST_TREE_CHILD: " + q(pidFile) + " }, stdout: \"ignore\", stderr: \"ignore\" })\n"
			if how == "killed" {
				// a host that doesn't leave when its input closes, so
				// stop kills it once stopWait has passed
				spawn += "process.exit = () => {}\nsetInterval(() => {}, 1000)\n"
			}
			if how == "own end" {
				spawn += "setTimeout(() => process.exit(3), 500)\n"
			}
			b, err := os.ReadFile("testdata/fake/index.js")
			if err != nil {
				t.Fatal(err)
			}
			js := filepath.Join(t.TempDir(), "index.js")
			if err := os.WriteFile(js, append([]byte(spawn), b...), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Add(ctx, js); err != nil {
				t.Fatal(err)
			}
			_, _ = Providers(ctx) // the host may end under it ("own end")
			kid := readPID(t, pidFile)
			t.Cleanup(func() { endPID(kid) })

			hostMu.Lock()
			h := current
			hostMu.Unlock()
			switch how {
			case "stop", "killed":
				if h == nil {
					t.Fatal("no host")
				}
				Restart()
			}
			if h != nil {
				select {
				case <-h.dead:
				case <-time.After(30 * time.Second):
					t.Fatal("the host isn't ended 30s on")
				}
			}
			until := time.Now().Add(15 * time.Second)
			for living(kid) {
				if time.Now().After(until) {
					t.Fatalf("the process %d the plugin's child started outlived the host (%s)", kid, how)
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

// readPID waits for the plugin's child to write its pid down.
func readPID(t *testing.T, file string) int {
	t.Helper()
	until := time.Now().Add(30 * time.Second)
	for time.Now().Before(until) {
		if b, err := os.ReadFile(file); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the plugin never started its child")
	return 0
}
