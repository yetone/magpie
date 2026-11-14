package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// wilsonxuuuu on X: Codex's requests for a remote magpie's auto group
// (cloud/auto/gpt-6.1-sol, the remote on 0.1.3295) were held 30s "in no
// wait magpie knows of" and stopped, and the report pasted in the tweet
// was cut before its goroutines. A request held in a step that is no
// named wait (here a lock routing needs, held and never let go) has a
// report that says by name where the request itself stood, in its first
// lines, with its own stack ahead of every goroutine's, and no values.
func TestHeldReportSaysWhereItStood(t *testing.T) {
	fresh(t)
	unroutedLimits(t, 20*time.Millisecond, time.Hour)
	unknownLimit(t, 300*time.Millisecond)
	// the remote magpie answers slowly, as an old one far off would
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"object":"list","data":[{"id":"auto/gpt-6.1-sol","object":"model"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "cloud", Name: "Remote magpie", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-x",
		Chat: remote.URL + "/v1", Models: []string{"auto/gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	sticks.Lock()
	var once sync.Once
	unlock := func() { once.Do(sticks.Unlock) }
	t.Cleanup(unlock)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+CodexPath+"/responses", strings.NewReader(
		`{"model":"cloud/auto/gpt-6.1-sol","stream":true,"instructions":"be","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "codex_cli_rs/0.200.0")
	req.Header.Set("session_id", "sess-held")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	unlock()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("held request answered %d %s", resp.StatusCode, b)
	}
	var held string
	for deadline := time.Now().Add(10 * time.Second); held == ""; time.Sleep(10 * time.Millisecond) {
		for _, r := range routesOf(s, "cloud/auto/gpt-6.1-sol") {
			held = cmpOr(held, r.Held)
		}
		if held == "" && time.Now().After(deadline) {
			t.Fatalf("the held request's report was never saved: %+v", routesOf(s, "cloud/auto/gpt-6.1-sol"))
		}
	}
	text, err := HeldReport(held)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(text, "\nwhere every goroutine stood:")
	at := ""
	for _, l := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(l, "stood at: "); ok {
			at = v
		}
	}
	// the lock's taker, in the affinity code, and what it waited in
	if !strings.HasPrefix(at, "gateway.") || !strings.Contains(at, "Mutex") {
		t.Fatalf("the report doesn't name where the request stood: %q\n%s", at, head)
	}
	own, ok := strings.CutPrefix(head[strings.Index(head, "\nwhere this request stood:")+1:], "where this request stood:\n")
	if !ok || !strings.Contains(own, "internal/gateway/affinity.go:") || !strings.Contains(own, "serveCodexBackend") && !strings.Contains(own, "codexBackend") {
		t.Fatalf("the report's own stack doesn't show the request's path:\n%s", head)
	}
	if strings.Contains(own, "0x") {
		t.Fatalf("the request's own stack holds values:\n%s", own)
	}
}

// heldOf finds a goroutine by its id at the dump's start or after a
// blank line, never one whose id only begins the same, and strips what
// values the runtime writes.
func TestHeldOfFindsItsGoroutine(t *testing.T) {
	dump := "goroutine 12 [running]:\nmain.x(0x1)\n\t/a.go:1 +0x2\n\n" +
		"goroutine 1 [sync.Mutex.Lock, 2 minutes]:\nsync.(*Mutex).Lock(...)\n\t/go/sync/mutex.go:90\n" +
		"github.com/yetone/magpie/internal/gateway.(*Server).pick(0xc000123, {0x10, 0x2})\n\t/m/internal/gateway/x.go:7 +0x1c\n" +
		"created by net/http.(*Server).Serve in goroutine 9\n\t/go/net/http/server.go:3 +0x5\n"
	got := heldOf(dump, 1)
	if got.at != "gateway.(*Server).pick (sync.Mutex.Lock, 2 minutes)" {
		t.Fatalf("at %q", got.at)
	}
	if strings.Contains(got.stack, "0x") || !strings.Contains(got.stack, "/m/internal/gateway/x.go:7") || strings.Contains(got.stack, "main.x") {
		t.Fatalf("stack:\n%s", got.stack)
	}
	if heldOf(dump, 2).stack != "" || heldOf(dump, 13).stack != "" {
		t.Fatal("found a goroutine that isn't in the dump")
	}
}
