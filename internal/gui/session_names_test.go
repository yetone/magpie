package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// The Routing page names a Claude Code session by the title in its own
// file, as the Sessions page does, not by its UUID (#1293). A session with
// no file here, or another agent's id, gets no name.
func TestSessionTitlesNameOtherAgents(t *testing.T) {
	sandboxHome(t)
	claude := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Cleanup(sessions.Reset)
	sessions.Reset()
	const id = "578bd3d1-834b-43fb-b40c-ed9d17ad7731"
	dir := filepath.Join(claude, "projects", "-work-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"把路由页的会话名显示出来"},"uuid":"u1","timestamp":"2026-10-08T10:00:01.000Z","cwd":"/work/app","sessionId":"` + id + `"}`,
		`{"parentUuid":"u1","isSidechain":false,"message":{"model":"claude-opus-5-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"好"}],"usage":{"input_tokens":10,"output_tokens":5}},"requestId":"req_1","type":"assistant","uuid":"a1","timestamp":"2026-10-08T10:00:05.000Z","cwd":"/work/app","sessionId":"` + id + `"}`,
		`{"type":"ai-title","aiTitle":"路由会话名","sessionId":"` + id + `"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	traceRoutes(mux)
	body := `{"ids":[],"routeIds":[],"sessions":["claude:` + id + `","claude:00000000-0000-0000-0000-000000000000","pi:` + id + `"]}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/gateway/session-titles", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var out struct {
		Titles map[string]string `json:"titles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"claude:" + id: "路由会话名"}
	if len(out.Titles) != len(want) || out.Titles["claude:"+id] != want["claude:"+id] {
		t.Fatalf("titles = %v, want %v (body %s)", out.Titles, want, w.Body)
	}
}
