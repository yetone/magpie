package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// The Sessions page draws a session count taken from every session beside its
// list, so the list has to be as long as that count. The endpoint used to fall
// back to sessions.Limit (200) when the page asked for no number — and the
// page never asks for one — so the page said "N sessions" over a list that
// stopped at 200 and the agents whose sessions were oldest fell off it whole.
// A caller that asks for a number of its own still gets it.
func TestSessionsListIsAsLongAsTheCount(t *testing.T) {
	h := sandboxHome(t)
	claude := filepath.Join(h, ".claude")
	// five past sessions.Limit, which is also the window traceRecentFiles
	// sizes gateway attribution over
	const n = sessions.Limit + 5
	line := `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"hi %d"},"uuid":"u","timestamp":"2026-09-20T10:00:0%d.000Z","cwd":"/work","sessionId":"%s"}` + "\n" +
		`{"parentUuid":"u","isSidechain":false,"message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":2}},"requestId":"req_1","type":"assistant","uuid":"a","timestamp":"2026-09-20T10:00:0%d.000Z","cwd":"/work","sessionId":"%s"}` + "\n"
	// as Claude Code keeps them: a session's own <id>.jsonl in its project's
	// folder, one folder per project
	proj := filepath.Join(claude, "projects", "-work")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%08d-1111-2222-3333-444444444444", i)
		b := []byte(fmt.Sprintf(line, i, i%10, id, (i%10)+1, id))
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	get := func(q string) []struct {
		ID   string `json:"id"`
		When int64  `json:"t"`
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions"+q, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
		var out struct {
			Sessions []struct {
				ID   string `json:"id"`
				When int64  `json:"t"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Sessions
	}
	// no number asked for: the whole set, as the count beside the list is
	all := get("")
	if len(all) != n {
		t.Fatalf("the list holds %d sessions, want %d (every one of them)", len(all), n)
	}
	// newest first by last activity: the last one written is the first listed
	for i := 1; i < len(all); i++ {
		if all[i-1].When < all[i].When {
			t.Fatalf("not newest first at %d: %d before %d", i, all[i-1].When, all[i].When)
		}
	}
	// a number of the caller's own is still honoured
	if few := get("?limit=5"); len(few) != 5 {
		t.Fatalf("?limit=5 gave %d sessions, want 5", len(few))
	}
	// and one past sessions.Limit is not quietly clipped to it
	if over := get("?limit=" + fmt.Sprint(sessions.Limit+1)); len(over) != sessions.Limit+1 {
		t.Fatalf("?limit=%d gave %d, want %d", sessions.Limit+1, len(over), sessions.Limit+1)
	}
}
