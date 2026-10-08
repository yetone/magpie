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

// A session is exported as Markdown (#1276): in the app to Downloads, never
// over an earlier export, and to a browser as a download; the session is
// found as listed (never a path from the page), and one that isn't there or
// whose agent can't be read says so.
func TestSessionExportMarkdown(t *testing.T) {
	h := sandboxHome(t)
	claude := filepath.Join(h, ".claude")
	proj := filepath.Join(claude, "projects", "-work")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "0a1b2c3d-1111-2222-3333-444444444444"
	body := `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"Rename foo to bar"},"uuid":"u","timestamp":"2026-09-20T10:00:00.000Z","cwd":"/work","sessionId":"` + id + `"}` + "\n" +
		`{"parentUuid":"u","isSidechain":false,"message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Renamed it in a.go."}],"usage":{"input_tokens":10,"output_tokens":2}},"requestId":"req_1","type":"assistant","uuid":"a","timestamp":"2026-09-20T10:00:05.000Z","cwd":"/work","sessionId":"` + id + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	q := "?agent=claude&id=" + id
	check := func(md string) {
		t.Helper()
		for _, want := range []string{"# Rename foo to bar\n", "- **Agent:** Claude Code\n", "- **Folder:** /work\n", "## User\n\nRename foo to bar\n", "## Assistant\n\nRenamed it in a.go.\n"} {
			if !strings.Contains(md, want) {
				t.Fatalf("no %q in\n%s", want, md)
			}
		}
	}
	for _, want := range []string{"magpie-session-claude-0a1b2c3d-111.md", "magpie-session-claude-0a1b2c3d-111-2.md"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/sessions/export"+q, strings.NewReader("{}")))
		var out struct{ Path string }
		json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 || filepath.Base(out.Path) != want {
			t.Fatalf("export: %d %s, want %s", w.Code, w.Body, want)
		}
		b, err := os.ReadFile(filepath.Join(h, "Downloads", want))
		if err != nil {
			t.Fatal(err)
		}
		check(string(b))
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/markdown"+q, nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), `filename="magpie-session-claude-0a1b2c3d-111.md"`) {
		t.Fatalf("download: %d %v", w.Code, w.Header())
	}
	check(w.Body.String())

	for _, bad := range []string{"?agent=claude&id=nope", "?agent=claude&id=../../etc/passwd"} {
		for _, r := range []*http.Request{httptest.NewRequest("GET", "/api/sessions/markdown"+bad, nil), httptest.NewRequest("POST", "/api/sessions/export"+bad, strings.NewReader("{}"))} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code == 200 {
				t.Fatalf("%s %s: %d %s", r.Method, bad, w.Code, w.Body)
			}
		}
	}
}
