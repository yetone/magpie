package gui

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// TestOverviewSummedOncePerStats: the Sessions page asks for the overview
// on every poll, and the same stats under the same filters are summed
// once; new stats, or another filter, are summed again.
func TestOverviewSummedOncePerStats(t *testing.T) {
	h := sandboxHome(t)
	claude := filepath.Join(h, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	proj := filepath.Join(claude, "projects", "-work")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id string, out int) {
		at := time.Now().Add(-time.Hour).Format(time.RFC3339)
		line := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"hello"},"uuid":"u%s","timestamp":%q,"cwd":"/work","sessionId":%q}`+"\n"+
			`{"type":"assistant","message":{"model":"claude-sonnet-5","id":"msg_%s","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":%d}},"uuid":"a%s","timestamp":%q,"cwd":"/work","sessionId":%q}`+"\n", id, at, id, id, out, id, at, id)
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	forget := func() {
		sessions.Reset()
		statsMemo.Lock()
		statsMemo.m = nil
		statsMemo.Unlock()
	}
	write("one", 2)
	forget()
	t.Cleanup(forget)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	ask := func(q string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/overview?"+q, nil))
		if w.Code != 200 {
			t.Fatalf("overview: %d %s", w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	made := overviewsMade.Load()
	first := ask("days=7")
	if again := ask("days=7"); !bytes.Equal(first, again) {
		t.Fatalf("the same stats answered differently:\n%s\n%s", first, again)
	}
	if n := overviewsMade.Load() - made; n != 1 {
		t.Fatalf("two asks of the same stats summed %d times, want 1", n)
	}
	ask("days=7&agent=codex")
	if n := overviewsMade.Load() - made; n != 2 {
		t.Fatalf("another filter summed %d times in all, want 2", n)
	}
	// the stats read again, with a second session: summed again, and
	// the answer says so
	write("two", 5)
	forget()
	if next := ask("days=7"); bytes.Equal(first, next) {
		t.Fatalf("new stats answered with the old overview: %s", next)
	}
	if n := overviewsMade.Load() - made; n != 3 {
		t.Fatalf("new stats summed %d times in all, want 3", n)
	}
}
