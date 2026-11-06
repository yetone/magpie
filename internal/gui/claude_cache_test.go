package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
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
