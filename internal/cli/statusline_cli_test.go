package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// What Claude Code hands a statusLine command on stdin (code.claude.com
// docs, "Full JSON schema"), cut to the fields around the one read.
const claudeStatusJSON = `{
  "cwd": "/Users/me/work",
  "session_id": "6f1d2c3a-9b8e-4a7f-8c1d-2e3f4a5b6c7d",
  "session_name": "my-session",
  "transcript_path": "/Users/me/.claude/projects/work/6f1d2c3a.jsonl",
  "model": {"id": "claude-opus-5-5", "display_name": "Opus"},
  "workspace": {"current_dir": "/Users/me/work", "project_dir": "/Users/me/work", "added_dirs": []},
  "version": "2.1.296",
  "output_style": {"name": "default"},
  "cost": {"total_cost_usd": 0.01234, "total_duration_ms": 45000, "total_api_duration_ms": 2300, "total_lines_added": 156, "total_lines_removed": 23},
  "context_window": {"total_input_tokens": 15500, "total_output_tokens": 1200, "context_window_size": 200000, "used_percentage": 8, "remaining_percentage": 92},
  "exceeds_200k_tokens": false
}`

// statuslineGateway is a gateway answering kept quotas only (a vendor read,
// without kept=1, fails the test) and the session's route.
func statuslineGateway(t *testing.T, now time.Time) *httptest.Server {
	t.Helper()
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	quotas := []provider.Quota{
		{Provider: "claude", Name: "Claude Code", Kind: "subscription", User: "a@example.com", LastServedAt: at(-time.Minute), Last: true,
			Windows: []provider.QuotaSpan{{Name: "5 hours", Used: 62, Remaining: 38, ResetsAt: at(2 * time.Hour)}, {Name: "7 days", Used: 41, Remaining: 59, ResetsAt: at(72 * time.Hour)}}},
		{Provider: "claude", Name: "Claude Code", Kind: "subscription", User: "b@example.com", Alias: "B", LastServedAt: at(-time.Hour),
			Windows: []provider.QuotaSpan{{Name: "5 hours", Used: 8, Remaining: 92, ResetsAt: at(-time.Minute)}, {Name: "7 days", Used: 30, Remaining: 70}}},
		{Provider: "codex", Name: "Codex", Kind: "subscription", User: "c@example.com",
			Windows: []provider.QuotaSpan{{Name: "5 hours", Used: 10, Remaining: 90}}},
		{Provider: "deepseek", Name: "DeepSeek", Kind: "balance", Balance: "¥12.00", LastServedAt: at(-time.Minute), Windows: []provider.QuotaSpan{}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/magpie/quotas", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("kept") != "1" {
			t.Errorf("statusline asked %s: that reads the vendors, not what magpie kept", r.URL)
			http.Error(w, "vendor read", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": quotas})
	})
	mux.HandleFunc("GET /v1/magpie/route", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("session") != "6f1d2c3a-9b8e-4a7f-8c1d-2e3f4a5b6c7d" {
			json.NewEncoder(w).Encode(map[string]any{"route": nil})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"session": r.URL.Query().Get("session"), "seq": 3,
			"route": map[string]any{"asked": "smart", "group": "smart", "model": "claude/claude-opus-5-5", "tries": []any{}, "done": true, "status": 200}})
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestStatuslineTellsRouteAndEveryAccount(t *testing.T) {
	now := time.Now()
	s := statuslineGateway(t, now)
	got := statusline(strings.NewReader(claudeStatusJSON), nil, s.URL, now)
	// the session's member, then the two accounts that served lately (the
	// codex one that didn't and the balance left out); B's 5 hours started
	// again since it was read
	want := "→ claude/claude-opus-5-5 | claude a@example.com* 5h 62% 7d 41% | claude B 5h 0% 7d 30%"
	if got != want {
		t.Fatalf("statusline =\n  %q\nwant\n  %q", got, want)
	}
	if got := statusline(strings.NewReader(claudeStatusJSON), []string{"codex"}, s.URL, now); got != "→ claude/claude-opus-5-5 | codex c@example.com 5h 10%" {
		t.Fatalf("statusline codex = %q", got)
	}
	// no session on stdin: the accounts alone
	if got := statusline(strings.NewReader(""), []string{"b@example.com"}, s.URL, now); got != "claude B 5h 0% 7d 30%" {
		t.Fatalf("statusline without a session = %q", got)
	}
}

// A gateway that isn't running is an empty line, at once.
func TestStatuslineGatewayOff(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	base := s.URL
	s.Close()
	start := time.Now()
	if got := statusline(strings.NewReader(claudeStatusJSON), nil, base, time.Now()); got != "" {
		t.Fatalf("statusline with the gateway off = %q, want nothing", got)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("statusline took %s with the gateway off", d)
	}
}

// A Claude Code on another computer than the gateway asks the gateway for
// the line (curl, steven_4422's Docker): the real gateway's answer is the
// line `magpie statusline` prints from the same gateway, every account and
// with one named.
func TestStatuslineEndpointIsTheCLIsLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MAGPIE_ADDR", "")
	// plan cards are the keys of a provider with a plan source (Ollama
	// Cloud's base); nothing here is called
	for _, p := range []provider.Provider{
		{ID: "alpha", Name: "Alpha", Key: "alpha-one", Chat: "http://127.0.0.1:1/v1", Anthropic: "https://ollama.com", Models: []string{"m1"}},
		{ID: "beta", Name: "Beta", Key: "beta-one", Chat: "http://127.0.0.1:1/v1", Anthropic: "https://ollama.com", Models: []string{"m1"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	kept := map[string]any{}
	for i, k := range []struct{ id, key string }{{"alpha", "alpha-one"}, {"beta", "beta-one"}} {
		sum := sha256.Sum256([]byte(k.key))
		reset := time.Now().Add(2 * time.Hour)
		kept[k.id+"/plan "+hex.EncodeToString(sum[:8])] = map[string]any{"at": time.Now().Add(-time.Minute), "quota": provider.SubscriptionQuota{
			Provider: k.id, Windows: []provider.QuotaWindow{{Name: "5 hours", Used: float64(62 - 20*i), ResetsAt: &reset}, {Name: "7 days", Used: float64(41 + i), ResetsAt: &reset}}}}
	}
	b, _ := json.Marshal(kept)
	if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "quotas.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetKeptCardsForTest()
	provider.ForgetBalances()
	provider.ForgetPlanQuotas()
	t.Cleanup(func() {
		provider.ForgetKeptCardsForTest()
		provider.ForgetBalances()
		provider.ForgetPlanQuotas()
	})

	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)
	for _, only := range [][]string{nil, {"beta"}} {
		want := statusline(strings.NewReader(""), only, gw.URL, time.Now())
		if want == "" || (only == nil && !strings.Contains(want, "alpha") && !strings.Contains(want, "beta")) {
			t.Fatalf("magpie statusline %v printed %q: the fixture shows no account", only, want)
		}
		target := gw.URL + gateway.StatuslinePath
		if len(only) > 0 {
			target += "?only=" + strings.Join(only, ",")
		}
		res, err := http.Post(target, "application/json", strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || strings.TrimSuffix(string(got), "\n") != want {
			t.Errorf("%s: %d %q, `magpie statusline %v` prints %q", target, res.StatusCode, got, only, want)
		}
	}
}
