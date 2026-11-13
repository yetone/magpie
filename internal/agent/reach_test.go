package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// fakeReach answers reachProbe from answers (a base missing is down), and
// starts with no probe made.
func fakeReach(t *testing.T, answers map[string]bool) *sync.Mutex {
	t.Helper()
	var mu sync.Mutex
	was, age := reachProbe, reachAge
	reachProbe = func(base string) bool { mu.Lock(); defer mu.Unlock(); return answers[base] }
	reachAge = time.Hour
	reach.Lock()
	reach.m = nil
	reach.Unlock()
	t.Cleanup(func() {
		reachForget()
		reachProbe, reachAge = was, age
	})
	return &mu
}

// reachForget drops every answer, so the next Drift asks again. A probe
// still out (one the last Drift started, for an address it was just set
// at) is waited for first: its answer would be written into no map.
func reachForget() {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		reach.Lock()
		busy := false
		for _, r := range reach.m {
			busy = busy || r.busy
		}
		if !busy {
			reach.m = nil
			reach.Unlock()
			return
		}
		reach.Unlock()
	}
	panic("a reach probe never answered")
}

// driftSettled is the agent's drift once the probes it starts have
// answered: the first ask only starts them.
func driftSettled(t *testing.T, a *Agent) *Drift {
	t.Helper()
	a.Drift()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		reach.Lock()
		busy := false
		for _, r := range reach.m {
			busy = busy || r.busy
		}
		reach.Unlock()
		if !busy {
			return a.Drift()
		}
	}
	t.Fatal("the probes never answered")
	return nil
}

// codexAt is this machine's Codex connected on fake/m1, its config then
// pointed at magpie by base as the user wrote it (#816).
func codexAt(t *testing.T, base string) (string, func() string) {
	t.Helper()
	home, read := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`, "")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetTOMLTop(cx.Path, edit.KV{Path: "openai_base_url", Value: base + gateway.CodexPath}); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetTOMLKey(cx.Path, "model_providers."+magpieID, "base_url", base+"/v1"); err != nil {
		t.Fatal(err)
	}
	if c := codex(home).Check(); c != "" {
		t.Fatal(c)
	}
	return home, read
}

// #1013 (cjyrainbow): Codex on Windows, its WSL agent reading Windows'
// config, pointed at magpie by Windows' address as WSL sees it and a
// portproxy of the user's to 127.0.0.1. After a reboot the portproxy
// listened no more, and the Agents page still said connected: the config
// was right. Now the address is tried, and one where nothing answers
// while magpie does on loopback is told, with what has to listen there.
func TestKeptAddressNotAnswering(t *testing.T) {
	kept := "http://172.28.96.1:" + gateway.Port()
	answers := map[string]bool{gateway.URL(): true}
	mu := fakeReach(t, answers)
	home, _ := codexAt(t, kept)

	d := driftSettled(t, codex(home))
	if d == nil || d.Kind != "unreachable" || d.Addr != kept || d.Move != "" {
		t.Fatalf("drift %+v", d)
	}
	if !strings.Contains(d.Detail, kept) || !strings.Contains(d.Detail, gateway.URL()) {
		t.Fatalf("detail: %s", d.Detail)
	}

	// the forward is back: connected again
	mu.Lock()
	answers[kept] = true
	mu.Unlock()
	reachForget()
	if d := driftSettled(t, codex(home)); d != nil {
		t.Fatalf("answering, still %+v", d)
	}

	// magpie's gateway itself down: nothing answers anywhere, which is no
	// fault of the address
	mu.Lock()
	answers[kept], answers[gateway.URL()] = false, false
	mu.Unlock()
	reachForget()
	if d := driftSettled(t, codex(home)); d != nil {
		t.Fatalf("gateway down, told %+v", d)
	}
}

// The first ask doesn't wait for the network: Drift answers at once, and
// the probe's answer shows at a later ask.
func TestKeptAddressProbeNeverWaitedFor(t *testing.T) {
	kept := "http://172.28.96.1:" + gateway.Port()
	fakeReach(t, map[string]bool{gateway.URL(): true})
	home, _ := codexAt(t, kept)
	release := make(chan struct{})
	slow := reachProbe
	reachProbe = func(base string) bool { <-release; return slow(base) }
	// the probe can't answer before release is closed, below: a Drift that
	// waited for it would never return, so how long Drift takes on a busy
	// machine (its files) is no measure of it
	done := make(chan *Drift, 1)
	go func() { done <- codex(home).Drift() }()
	select {
	case d := <-done:
		if d != nil {
			t.Fatalf("drift before any answer: %+v", d)
		}
	case <-time.After(30 * time.Second):
		close(release)
		<-done
		t.Fatal("Drift waited for the probe")
	}
	close(release)
	if d := driftSettled(t, codex(home)); d == nil || d.Kind != "unreachable" {
		t.Fatalf("drift %+v", d)
	}
}

// Under WSL's NAT the address WSL reaches Windows at can change; a config
// still on the one a distro reached Windows at before is offered the one
// now, and Reconnect moves both of Codex's URLs there. An address no
// distro was ever seen at is the user's own and is never offered a move.
func TestKeptAddressMovedWithWSL(t *testing.T) {
	old, now := "http://172.28.96.1:"+gateway.Port(), "http://172.29.0.1:"+gateway.Port()
	fakeReach(t, map[string]bool{gateway.URL(): true, now: true})
	wsl.Lock()
	seen := wsl.seen
	wsl.seen = map[string]*distro{"Ubuntu": {Name: "Ubuntu", Gateway: "172.29.0.1", Was: []string{"172.28.96.1"}}}
	wsl.Unlock()
	t.Cleanup(func() { wsl.Lock(); wsl.seen = seen; wsl.Unlock() })

	home, read := codexAt(t, old)
	cx := codex(home)
	d := driftSettled(t, cx)
	if d == nil || d.Kind != "unreachable" || d.Move != now || !strings.Contains(d.Detail, "Use "+now) {
		t.Fatalf("drift %+v", d)
	}
	if err := cx.Reapply(); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, `openai_base_url = "`+now+gateway.CodexPath+`"`) || !strings.Contains(cfg, `base_url = "`+now+`/v1"`) || strings.Contains(cfg, "172.28.96.1") {
		t.Fatalf("not moved:\n%s", cfg)
	}
	if c := codex(home).Check(); c != "" {
		t.Fatal(c)
	}
	if d := driftSettled(t, codex(home)); d != nil {
		t.Fatalf("moved, still %+v", d)
	}

	// the user's own forward on another host: told, never moved
	reachForget()
	home, read = codexAt(t, "http://10.0.0.7:"+gateway.Port())
	cx = codex(home)
	if d := driftSettled(t, cx); d == nil || d.Kind != "unreachable" || d.Move != "" {
		t.Fatalf("drift %+v", d)
	}
	if err := cx.Reapply(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, "10.0.0.7") {
		t.Fatalf("the user's address was changed:\n%s", cfg)
	}
}

// Codex on loopback is never probed: there is nothing apart from the
// gateway to try.
func TestLoopbackNotProbed(t *testing.T) {
	var asked []string
	var mu sync.Mutex
	fakeReach(t, nil)
	reachProbe = func(base string) bool { mu.Lock(); asked = append(asked, base); mu.Unlock(); return false }
	home, _ := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`, "")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if d := driftSettled(t, cx); d != nil {
		t.Fatalf("drift %+v", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 0 {
		t.Fatalf("probed %v", asked)
	}
}

// The probe takes any HTTP answer for one (a gateway shared on the network
// answers 401 without a key) and a refused connection for none.
func TestReachProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("asked %s", r.URL.Path)
		}
		http.Error(w, "no key", http.StatusUnauthorized)
	}))
	if !realReachProbe(srv.URL) {
		t.Fatal("a 401 taken for nothing listening")
	}
	srv.Close()
	if realReachProbe(srv.URL) {
		t.Fatal("a closed port taken for an answer")
	}
}

func TestWSLWas(t *testing.T) {
	for _, c := range []struct {
		before   []string
		was, now string
		want     string
	}{
		{nil, "", "172.20.0.1", ""},
		{nil, "172.20.0.1", "172.20.0.1", ""},
		{nil, "172.20.0.1", "172.21.0.1", "172.20.0.1"},
		{[]string{"172.21.0.1"}, "172.20.0.1", "172.21.0.1", "172.20.0.1"},
		{[]string{"a", "b", "c", "d"}, "e", "f", "b c d e"},
		{[]string{"a"}, "b", "a", "b"},
	} {
		if got := strings.Join(wslWas(c.before, c.was, c.now), " "); got != c.want {
			t.Errorf("wslWas(%v, %q, %q) = %q, want %q", c.before, c.was, c.now, got, c.want)
		}
	}
}

// The same for every agent in a WSL distro under NAT, pointed at Windows as
// WSL sees it: answering only while the gateway listens beyond loopback, it
// is told when nothing does; one in mirrored networking is on 127.0.0.1
// and never probed.
func TestWSLAgentAddressNotAnswering(t *testing.T) {
	win := "http://172.20.0.1:" + gateway.Port()
	fakeReach(t, map[string]bool{gateway.URL(): true})
	for _, mirrored := range []bool{false, true} {
		reachForget()
		home, _ := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`, "model = \"gpt-5.5\"\n")
		a := wslCodex(fakeDistro(home, mirrored))
		if err := a.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		d := driftSettled(t, a)
		if mirrored {
			if d != nil {
				t.Fatalf("mirrored: %+v", d)
			}
			continue
		}
		if d == nil || d.Kind != "unreachable" || d.Addr != win || !strings.Contains(d.Detail, "Share on local network") || !strings.Contains(d.Detail, "portproxy") || !strings.Contains(d.Detail, "firewall") {
			t.Fatalf("drift %+v", d)
		}
	}
}
