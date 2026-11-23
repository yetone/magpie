package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
)

// Linka on Discord: Claude Code's prompt cache is set in its settings,
// beside its compaction. Reading the settings shows the user's own value
// and writes nothing; a pick goes through POST /api/settings/claude-cache,
// and Claude Code's default ("") takes the key out.
func TestClaudeCacheRoute(t *testing.T) {
	h := sandboxHome(t)
	path := filepath.Join(h, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	own := []byte(`{"theme": "dark", "promptCacheTtl": "1h"}`)
	if err := os.WriteFile(path, own, 0o644); err != nil {
		t.Fatal(err)
	}
	got := claudeCacheState()
	want := []claudeCacheJSON{{Knob: agent.TuneCache, Value: "1h"}, {Knob: agent.TuneSubCache}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("read %+v; want %+v", got, want)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, own) {
		t.Fatalf("reading wrote the user's file:\n%s", b)
	}
	mux := http.NewServeMux()
	tuneRoutes(mux)
	post := func(knob, value string) int {
		body, _ := json.Marshal(map[string]string{"knob": knob, "value": value})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings/claude-cache", bytes.NewReader(body)))
		return rec.Code
	}
	if c := post(agent.TuneSubCache, "5m"); c != 200 {
		t.Fatalf("5m for subagents: %d", c)
	}
	if c := post(agent.TuneCache, ""); c != 200 {
		t.Fatalf("default: %d", c)
	}
	got = claudeCacheState()
	if got[0].Value != "" || got[1].Value != "5m" {
		t.Fatalf("after the picks %+v; want default and 5m", got)
	}
	if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(`"promptCacheTtl"`)) || !bytes.Contains(b, []byte(`"theme"`)) {
		t.Fatalf("default left the key, or lost the user's theme:\n%s", b)
	}
	if c := post(agent.TuneCache, "2h"); c == 200 {
		t.Fatal("took 2h")
	}
}

func TestClaudeSettingsWSL(t *testing.T) {
	host := sandboxHome(t)
	root, other := t.TempDir(), t.TempDir()
	const probe = "home:/home/me\ndir:.claude\nbin:claude\n"
	t.Cleanup(agent.FakeWSL(map[string]string{"Ubuntu": probe, "Debian": probe}, map[string]string{"Ubuntu": root, "Debian": other}))
	path := filepath.Join(root, "home", "me", ".claude", "settings.json")
	otherPath := filepath.Join(other, "home", "me", ".claude", "settings.json")
	for path, own := range map[string]string{
		path:      `{"theme":"dark","promptCacheTtl":"5m","attribution":{"pr":"keep this"}}`,
		otherPath: `{"promptCacheTtl":"1h","subagentPromptCacheTtl":"5m","attribution":{"commit":"My trailer"}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(own), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	tuneRoutes(mux)
	request := func(method, route, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/settings/"+route, strings.NewReader(body)))
		if rec.Code != want {
			t.Fatalf("%s %s returned %d: %.250s", method, route, rec.Code, rec.Body)
		}
		return rec
	}
	check := func(rec *httptest.ResponseRecorder, main, sub, attribution string) {
		t.Helper()
		var got struct {
			ClaudeCache       []claudeCacheJSON
			ClaudeAttribution string
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := []claudeCacheJSON{{Knob: agent.TuneCache, Value: main}, {Knob: agent.TuneSubCache, Value: sub}}
		if len(got.ClaudeCache) != 2 || got.ClaudeCache[0] != want[0] || got.ClaudeCache[1] != want[1] || got.ClaudeAttribution != attribution {
			t.Fatalf("settings = %+v, want %+v and attribution %q", got, want, attribution)
		}
	}
	t.Setenv("FORCE_PROMPT_CACHING_5M", "1")
	post := request("POST", "claude-cache", `{"agent":"claude@wsl:Ubuntu","knob":"subagent_cache_ttl","value":"1h"}`, 200)
	check(post, "5m", "1h", "on")
	check(request("GET", "claude-options/claude@wsl:Debian", "", 200), "1h", "5m", "custom")
	check(request("POST", "claude-attribution", `{"agent":"claude@wsl:Ubuntu","on":false}`, 200), "5m", "1h", "off")
	check(request("POST", "claude-cache", `{"agent":"claude@wsl:Ubuntu","knob":"cache_ttl","value":""}`, 200), "", "1h", "off")
	if err := edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_PROMPT_CACHE_TTL", Value: "5m"}); err != nil {
		t.Fatal(err)
	}
	settings := request("GET", "claude-options/claude@wsl:Ubuntu", "", 200)
	if !bytes.Contains(settings.Body.Bytes(), []byte(`"locked":"CLAUDE_CODE_PROMPT_CACHE_TTL"`)) {
		t.Fatalf("WSL cache override missing from %s", settings.Body)
	}
	request("POST", "claude-cache", `{"agent":"claude@wsl:Ubuntu","knob":"cache_ttl","value":"1h"}`, 400)
	request("POST", "claude-attribution", `{"agent":"claude@wsl:Debian","on":false}`, 400)
	request("POST", "claude-cache", `{"agent":"codex","knob":"cache_ttl","value":"1h"}`, 400)
	request("POST", "claude-cache", `{"agent":"claude@wsl:Missing","knob":"cache_ttl","value":"1h"}`, 400)
	file, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(file, []byte(`"keep this"`)) || !bytes.Contains(file, []byte(`"theme"`)) || bytes.Contains(file, []byte(`"promptCacheTtl"`)) {
		t.Fatalf("Ubuntu settings = %s (%v)", file, err)
	}
	if _, err := os.Stat(filepath.Join(host, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("host settings unexpectedly touched: %v", err)
	}
	agent.StopFakeWSL("Ubuntu")
	runs := agent.FakeWSLRuns("Ubuntu")
	request("GET", "claude-options/claude@wsl:Ubuntu", "", 400)
	request("POST", "claude-cache", `{"agent":"claude@wsl:Ubuntu","knob":"cache_ttl","value":"1h"}`, 400)
	if got := agent.FakeWSLRuns("Ubuntu"); got != runs {
		t.Fatalf("settings started a stopped distro: %d to %d runs", runs, got)
	}
}
