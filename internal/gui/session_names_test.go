package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// flandern1211_58540 on Discord: Routing headed a session by its folder
// and id ("路由这里的会话名只能看到目录名和会话ID吗"). A Codex thread Codex
// never named is headed by its first prompt, as the Sessions page heads
// it, and a session whose files aren't on this computer by the first words
// the gateway recorded of it. A named thread keeps its name, and a session
// with neither keeps no title.
func TestSessionTitlesFallBackToFirstPrompt(t *testing.T) {
	sandboxHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	codex := t.TempDir()
	t.Setenv("CODEX_HOME", codex)
	t.Cleanup(sessions.Reset)
	sessions.Reset()
	const unnamed, named = "019b0001-aaaa-7bbb-8ccc-000000000001", "019b0001-aaaa-7bbb-8ccc-000000000002"
	day := filepath.Join(codex, "sessions", "2026", "11", "04")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{unnamed, named} {
		lines := []string{
			`{"timestamp":"2026-11-04T10:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-11-04T10:00:00.000Z","cwd":"/work/app","originator":"codex_cli_rs","cli_version":"0.99.0","source":"cli","model_provider":"openai"}}`,
			`{"timestamp":"2026-11-04T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"把路由页的会话名\n显示出来"}]}}`,
			`{"timestamp":"2026-11-04T10:00:01.100Z","type":"event_msg","payload":{"type":"user_message","message":"把路由页的会话名\n显示出来","images":[]}}`,
		}
		if err := os.WriteFile(filepath.Join(day, "rollout-2026-11-04T10-00-00-"+id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(codex, "session_index.jsonl"), []byte(`{"id":"`+named+`","thread_name":"路由会话名","updated_at":"2026-11-04T10:01:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessions.SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	if err := sessions.SaveGatewayTurn(sessions.GatewayTurn{Agent: "pi", Session: "ses_remote", Time: time.Now().UTC(),
		Input: []sessions.Part{{Role: "user", Kind: "text", Text: "Deploy the staging server"}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	traceRoutes(mux)
	ask := func(body string, out any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/gateway/session-titles", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var groups struct {
		Names map[string]string `json:"names"`
	}
	ask(`{"ids":["`+unnamed+`","`+named+`","019b0001-aaaa-7bbb-8ccc-00000000dead"],"groups":true,"routeIds":[]}`, &groups)
	if groups.Names[unnamed] == "" || !strings.HasPrefix(groups.Names[unnamed], "把路由页的会话名") || groups.Names[named] != "路由会话名" || len(groups.Names) != 2 {
		t.Fatalf("Codex names = %q", groups.Names)
	}
	var others struct {
		Titles map[string]string `json:"titles"`
	}
	ask(`{"ids":[],"routeIds":[],"sessions":["pi:ses_remote","pi:ses_none","claude:ses_remote"]}`, &others)
	if others.Titles["pi:ses_remote"] != "Deploy the staging server" || len(others.Titles) != 1 {
		t.Fatalf("titles = %q", others.Titles)
	}
}
