//go:build darwin || linux

package gui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/update"
)

// successorEnv has this test binary, started again, play the new magpie
// (TestHandoverSuccessor).
const successorEnv = "MAGPIE_TEST_SUCCESSOR"

// TestHandoverSuccessor is the new version a restart starts, in a process
// of its own: it takes the gateway over from the magpie that started it
// and serves it until it is ended.
func TestHandoverSuccessor(t *testing.T) {
	if os.Getenv(successorEnv) == "" {
		t.Skip("the new magpie of TestRestartHandsTheGatewayOver")
	}
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if takeOver(exe) == 0 {
		t.Fatal("no gateway was taken over")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := gateway.New().ListenAndServe(ctx); err != nil {
		t.Fatal(err)
	}
}

// handoverSandbox is a magpie serving the gateway on a port of its own,
// as the app does, to restart; what it set is put back after.
func handoverSandbox(t *testing.T, up string) *gateway.Server {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream", Chat: up + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	t.Setenv("MAGPIE_NO_HANDOVER", "")
	oldExe, oldHello, oldAck := startExe, helloWait, ackWait
	oldStart, oldRelaunch := startBeside, relaunchBinary
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	startExe = exe
	handoverHost.Store(true)
	oldExit := exitNow
	exitNow = func(int) { t.Error("the magpie that handed over was made to exit before its deadline") }
	t.Cleanup(func() {
		if d := quitDeadline.Swap(nil); d != nil {
			d.Stop()
		}
		exitNow = oldExit
		endBackend()
		serving.Wait()
		backendCtx, endBackend = context.WithCancel(context.Background())
		served.Store(nil)
		pending.Store(nil)
		handedOver.Store(false)
		handoverHost.Store(false)
		startExe, helloWait, ackWait = oldExe, oldHello, oldAck
		startBeside, relaunchBinary = oldStart, oldRelaunch
		gateway.ForwardCallbacks("")
	})
	gw := serveGateway()
	if gw == nil {
		t.Fatal("the gateway wasn't served")
	}
	eventually(t, "the gateway never listened", func() bool { return gw.Addr() != "" })
	t.Setenv("MAGPIE_ADDR", gw.Addr()) // as the new one finds it in settings
	return gw
}

// slowUpstream answers a streamed request with a first chunk, then the
// rest once release is closed; any other at once.
func slowUpstream(t *testing.T, release <-chan struct{}) *httptest.Server {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"quick"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"first \"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"last\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	return up
}

// servedByPid is the pid of the magpie answering at addr, 0 when none.
func servedByPid(addr string) int {
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	r, err := c.Get("http://" + addr + "/")
	if err != nil {
		return 0
	}
	defer r.Body.Close()
	var info struct {
		Pid int `json:"pid"`
	}
	json.NewDecoder(r.Body).Decode(&info)
	return info.Pid
}

// xuanwo on X: a restart to update on a Mac or Linux hands the gateway to
// the new version, a process of its own, rather than waiting for it to be
// idle. A reply streaming through the old one when it restarts finishes
// whole; requests sent meanwhile are answered, by the new one once it
// serves; and the old one quits only once its stream has ended.
func TestRestartHandsTheGatewayOver(t *testing.T) {
	release := make(chan struct{})
	up := slowUpstream(t, release)
	defer close(release)
	gw := handoverSandbox(t, up.URL)
	addr := gw.Addr()

	var child *exec.Cmd
	startBeside = func(exe string, window bool, view string) error {
		child = exec.Command(exe, "-test.run=^TestHandoverSuccessor$", "-test.v")
		child.Env = append(os.Environ(), successorEnv+"=1")
		child.Stdout, child.Stderr = os.Stderr, os.Stderr
		return child.Start()
	}
	t.Cleanup(func() {
		if child != nil && child.Process != nil {
			child.Process.Kill()
			child.Wait()
		}
	})
	relaunchBinary = func(string, string, bool, string) error {
		t.Error("restarted as before, rather than handing over")
		return nil
	}

	// a reply streaming through the gateway
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
	// it doesn't hold the restart back
	if b := restartBusy(); b.Any() {
		t.Fatalf("a restart that hands over waits for %+v", b)
	}

	// requests all along the restart are answered
	var asked, answered atomic.Int32
	stopAsking := make(chan struct{})
	asking := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopAsking) }); <-asking }
	t.Cleanup(stop)
	go func() {
		defer close(asking)
		for {
			select {
			case <-stopAsking:
				return
			default:
			}
			asked.Add(1)
			r, err := http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`))
			if err != nil {
				t.Errorf("a request during the restart: %v", err)
				return
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != 200 || !strings.Contains(string(b), "quick") {
				t.Errorf("a request during the restart: %d %s", r.StatusCode, b)
				return
			}
			answered.Add(1)
			time.Sleep(20 * time.Millisecond)
		}
	}()

	if !beginHandover("", startExe, false, "", false) {
		t.Fatal("the restart didn't hand over")
	}
	quit := make(chan struct{})
	leave(func() { close(quit) })

	eventually2(t, 30*time.Second, "the new magpie never served the gateway", func() bool {
		return child != nil && child.Process != nil && servedByPid(addr) == child.Process.Pid
	})
	if !handedOver.Load() {
		t.Fatal("the new one serves, and this one doesn't know it handed over")
	}
	// and is gone by the end of its drain, whatever holds its quit up (#4938)
	if quitDeadline.Load() == nil {
		t.Fatal("the magpie that handed over has no deadline to quit by")
	}
	// the old one keeps its stream past the 2s an ordinary end gives
	// requests (gateway.shortDrain), and so doesn't quit yet
	select {
	case <-quit:
		t.Fatal("quit with a reply streaming")
	case <-time.After(3 * time.Second):
	}
	for range 5 {
		if pid := servedByPid(addr); pid != child.Process.Pid {
			t.Fatalf("a request after the handover reached pid %d", pid)
		}
	}
	stop()
	if answered.Load() == 0 || answered.Load() != asked.Load() {
		t.Fatalf("%d of %d requests during the restart answered", answered.Load(), asked.Load())
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

// A handover that can't be made leaves the gateway with this one, which
// then restarts as before (the new one opened once it has exited), but
// only once the requests it has in flight have finished: the restart was
// let go without waiting for them, since a handover carries them (the
// sandbox run's bind failed and its stream was cut).
func TestHandoverNotMadeRestartsAsBeforeOnceIdle(t *testing.T) {
	for _, c := range []struct {
		name  string
		start func(string, bool, string) error
	}{
		{"never says hello", func(string, bool, string) error { return nil }},
		{"doesn't start", func(string, bool, string) error { return errors.New("no such file") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			release := make(chan struct{})
			up := slowUpstream(t, release)
			defer close(release)
			gw := handoverSandbox(t, up.URL)
			helloWait = 200 * time.Millisecond
			startBeside = c.start
			var relaunched atomic.Bool
			relaunchBinary = func(exe, aside string, window bool, view string) error {
				relaunched.Store(true)
				return nil
			}
			resp, err := http.Post("http://"+gw.Addr()+"/v1/chat/completions", "application/json",
				strings.NewReader(`{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			stream := bufio.NewReader(resp.Body)
			if line, err := stream.ReadString('\n'); err != nil || !strings.Contains(line, "first") {
				t.Fatalf("the stream's first chunk: %q %v", line, err)
			}
			if !beginHandover("", startExe, false, "", false) {
				t.Fatal("the restart didn't try to hand over")
			}
			quit := make(chan struct{})
			leave(func() { close(quit) })
			select {
			case <-quit:
				t.Fatal("quit with a reply streaming")
			case <-time.After(time.Second):
			}
			if relaunched.Load() {
				t.Fatal("restarted with a reply streaming")
			}
			if servedByPid(gw.Addr()) != os.Getpid() {
				t.Fatal("the gateway isn't this one's while the restart waits")
			}
			release <- struct{}{}
			rest, err := io.ReadAll(stream)
			if err != nil || !strings.Contains(string(rest), "[DONE]") {
				t.Fatalf("the stream was cut: %q %v", rest, err)
			}
			select {
			case <-quit:
			case <-time.After(10 * time.Second):
				t.Fatal("never quit")
			}
			if !relaunched.Load() {
				t.Fatal("not restarted as before")
			}
			if handedOver.Load() {
				t.Fatal("says it handed over")
			}
		})
	}
}

func eventually2(t *testing.T, most time.Duration, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(most); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal(what)
}

// #4938: the copy an update left, hidden, lived on for three hours
// beside the new version, its loops writing and fetching, because its
// quit waited on something that never came. Past its drain and a little
// more it exits, whatever holds the quit up.
func TestHandedOverQuitsByItsDeadline(t *testing.T) {
	oldExit := exitNow
	exited := make(chan int, 1)
	exitNow = func(code int) { exited <- code }
	t.Cleanup(func() {
		if d := quitDeadline.Swap(nil); d != nil {
			d.Stop()
		}
		exitNow = oldExit
	})
	quitBy(20 * time.Millisecond)
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exited %d, want 0", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the magpie that handed over never exited")
	}
}
