package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// A long session's sheet reads it a part at a time, and its first read
// says how many turns the whole file has, and how many come before a read
// that starts past its start, so "turn i / N" counts the session, not
// what was read (usrname1833 on Discord). A subagent's sheet the same.
func TestSessionTranscriptCountsEveryTurn(t *testing.T) {
	h := sandboxHome(t)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	const id, turns = "11111111-2222-3333-4444-666666666666", 120
	proj := filepath.Join(h, ".claude", "projects", "-work-app")
	long := func(side, agent string) string {
		var b strings.Builder
		for i := range turns {
			ask := fmt.Sprintf("ask %d", i)
			if i == turns-1 {
				ask = "the zanzibar question"
			}
			fmt.Fprintf(&b, `{"parentUuid":null,"isSidechain":%s,%s"type":"user","message":{"role":"user","content":%q},"uuid":"u%d","timestamp":"2026-09-20T10:%02d:00.000Z","cwd":"/work/app","sessionId":"%s"}`+"\n",
				side, agent, ask, i, i%60, id)
			fmt.Fprintf(&b, `{"parentUuid":"u%d","isSidechain":%s,%s"message":{"model":"claude-opus-5-5","id":"msg_%d","type":"message","role":"assistant","content":[{"type":"text","text":%q}],"usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}},"type":"assistant","uuid":"a%d","timestamp":"2026-09-20T10:%02d:30.000Z","cwd":"/work/app","sessionId":"%s"}`+"\n",
				i, side, agent, i, strings.Repeat("words ", 1500), i, i%60, id)
		}
		return b.String()
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(proj, id+".jsonl"), long("false", ""))
	write(filepath.Join(proj, id, "subagents", "agent-a1.jsonl"), long("true", `"agentId":"a1",`))
	write(filepath.Join(proj, id, "subagents", "agent-a1.meta.json"), `{"agentType":"Explore","description":"Search the repo","spawnDepth":1}`)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	read := func(q string) sessions.Transcript {
		t.Helper()
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/sessions/manage?agent=claude", nil))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/transcript?agent=claude&id="+id+q, nil))
		var tr sessions.Transcript
		if err := json.Unmarshal(w.Body.Bytes(), &tr); w.Code != 200 || err != nil {
			t.Fatalf("%s: %d %.300s", q, w.Code, w.Body)
		}
		return tr
	}
	asks := func(tr sessions.Transcript) (n int) {
		for _, p := range tr.Parts {
			if p.Role == "user" {
				n++
			}
		}
		return n
	}
	for _, sub := range []string{"", "&sub=a1"} {
		first := read(sub)
		if !first.Cut || asks(first) >= turns || first.Turns != turns || first.TurnsBefore != 0 {
			t.Fatalf("first read%s: cut %v, %d asks read, turns %d, before %d; the file has %d", sub, first.Cut, asks(first), first.Turns, first.TurnsBefore, turns)
		}
		// the rest, read when asked, isn't counted again: the sheet has N
		if more := read(sub + fmt.Sprintf("&after=%d", len(first.Parts))); more.Turns != 0 || len(more.Parts) == 0 {
			t.Fatalf("rest%s: turns %d, %d parts", sub, more.Turns, len(more.Parts))
		}
	}
	// a search's read starts near its words, and knows the turns before
	found := read("&find=zanzibar")
	if found.From == 0 || found.Turns != turns || found.TurnsBefore == 0 || found.TurnsBefore+asks(found) != turns {
		t.Fatalf("found read: from %d, turns %d, before %d, %d asks read", found.From, found.Turns, found.TurnsBefore, asks(found))
	}
}
