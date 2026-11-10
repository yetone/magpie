//go:build darwin || linux

package gui

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// gaubeebangeel on X: an agent magpie serves runs `magpie update` at 4am.
// It put the new version in, and the app ran on as the old one until it
// was quit and reopened, which cut the agent off; to keep it going the
// agent needed a provider of its own. Now the command asks the app
// (gateway.RestartPath) to restart into what it put in, and the app does
// so as its own update would, handing the gateway over: the reply the
// agent has streaming finishes whole, the new version serves the next
// requests, and the old one quits once its stream has ended. On the old
// code the route isn't there (404) and nothing restarts.
func TestUpdateInATerminalRestartsTheAppWithoutCuttingIt(t *testing.T) {
	release := make(chan struct{})
	up := slowUpstream(t, release)
	defer close(release)
	gw := handoverSandbox(t, up.URL)
	addr := gw.Addr()

	// the binary as this magpie started from it, which the terminal's
	// magpie update then replaces
	exe := filepath.Join(t.TempDir(), "magpie")
	if err := os.WriteFile(exe, []byte("v0.1.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := updates
	t.Cleanup(func() { updates = old; gateway.SetRestarter(nil) })
	updates = &updater{state: "latest", exe: exe}
	updates.self, _ = os.Stat(exe)
	quit := make(chan struct{})
	updates.autoRestart = func() bool {
		if restartInto(false, false, "", true, false) {
			leave(func() { close(quit) })
			return true
		}
		return false
	}
	gateway.SetRestarter(updates.restartBeside)

	// the restart starts the child on a goroutine of its own, while this
	// test waits for it to serve
	var child atomic.Pointer[exec.Cmd]
	startBeside = func(exe string, window bool, view string) error {
		c := exec.Command(startExe, "-test.run=^TestHandoverSuccessor$", "-test.v")
		c.Env = append(os.Environ(), successorEnv+"=1")
		c.Stdout, c.Stderr = os.Stderr, os.Stderr
		if err := c.Start(); err != nil {
			return err
		}
		child.Store(c)
		return nil
	}
	t.Cleanup(func() {
		if c := child.Load(); c != nil {
			c.Process.Kill()
			c.Wait()
		}
	})
	relaunchBinary = func(string, string, bool, string) error {
		t.Error("restarted as before, rather than handing over")
		return nil
	}

	resp, err := http.Post("http://"+addr+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	stream := bufio.NewReader(resp.Body)
	if line, err := stream.ReadString('\n'); err != nil || !strings.Contains(line, "first") {
		t.Fatalf("the stream's first chunk: %q %v", line, err)
	}

	// magpie update in a terminal: the binary replaced, then the ask
	if err := os.WriteFile(exe, []byte("v0.1.2 is longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := http.Post("http://"+addr+gateway.RestartPath, "application/json", strings.NewReader(`{"version":"v0.1.2"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reply gateway.RestartReply
	json.NewDecoder(r.Body).Decode(&reply)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || reply.State != "restarting" || !reply.Handover {
		t.Fatalf("magpie update's ask: %d %+v", r.StatusCode, reply)
	}

	eventually2(t, 30*time.Second, "the new magpie never served the gateway", func() bool {
		c := child.Load()
		return c != nil && servedByPid(addr) == c.Process.Pid
	})
	q, err := http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("a request after the restart: %v", err)
	}
	b, _ := io.ReadAll(q.Body)
	q.Body.Close()
	if q.StatusCode != 200 || !strings.Contains(string(b), "quick") {
		t.Fatalf("a request after the restart: %d %s", q.StatusCode, b)
	}
	select {
	case <-quit:
		t.Fatal("quit with a reply streaming")
	case <-time.After(time.Second):
	}
	release <- struct{}{}
	rest, err := io.ReadAll(stream)
	if err != nil || !strings.Contains(string(rest), "last") || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("the stream was cut: %q %v", rest, err)
	}
	select {
	case <-quit:
	case <-time.After(10 * time.Second):
		t.Fatal("the old one didn't quit once its stream ended")
	}
}

// A magpie that isn't the copy magpie update replaced stays as it is, and
// says so.
func TestUpdateOfAnotherCopyLeavesTheAppBe(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "magpie")
	if err := os.WriteFile(exe, []byte("v0.1.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &updater{state: "latest", exe: exe}
	u.self, _ = os.Stat(exe)
	u.autoRestart = func() bool { t.Error("restarted"); return true }
	if r := u.restartBeside("v0.1.2"); r.State != "current" {
		t.Fatalf("answered %+v", r)
	}
	if u.state != "latest" {
		t.Fatalf("state %q", u.state)
	}
}
