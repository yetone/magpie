package gateway

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// sessionsCall is one tools/call of the sessions server from this
// computer, and the tool's text and whether it is an error.
func sessionsCall(t *testing.T, h http.Handler, tool, args string) (string, bool) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	m := readMCP(t, mcpPostTo(h, SessionsMCPPath, "127.0.0.1:50000", body))
	if m.Error != nil || len(m.Result.Content) != 1 {
		t.Fatalf("%s: %+v", tool, m)
	}
	return m.Result.Content[0].Text, m.Result.IsError
}

// What was said in this computer's sessions is for its own agents alone:
// another computer, even with a key, and a web page are turned away.
func TestSessionsMCPOnlyForAgentsHere(t *testing.T) {
	fresh(t)
	h := lanGuard(New().Handler())
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	m := readMCP(t, mcpPostTo(h, SessionsMCPPath, "127.0.0.1:50000", list))
	if len(m.Result.Tools) != 2 || m.Result.Tools[0].Name != sessionsSearchTool || m.Result.Tools[1].Name != sessionsReadTool {
		t.Fatalf("tools: %+v", m.Result.Tools)
	}
	for _, c := range []struct {
		name, addr string
		hdr        []string
	}{
		{"a page", "127.0.0.1:50000", []string{"Origin", "http://127.0.0.1:3425"}},
		{"a browser", "127.0.0.1:50000", []string{"User-Agent", "Mozilla/5.0", "Sec-Fetch-Mode", "cors"}},
		{"another computer", "192.0.2.7:4000", nil},
	} {
		w := mcpPostTo(h, SessionsMCPPath, c.addr, list, c.hdr...)
		if w.Code == 200 {
			t.Errorf("%s was answered: %s", c.name, w.Body)
		}
	}
}

// search_sessions gives each session found as read_session takes it, the
// words round the match, and passes the agent and limit on.
func TestSessionsMCPSearch(t *testing.T) {
	fresh(t)
	h := lanGuard(New().Handler())
	var asked [3]any
	was := searchSaid
	searchSaid = func(_ context.Context, needle, agent string, limit int) (sessions.SaidFound, error) {
		asked = [3]any{needle, agent, limit}
		return sessions.SaidFound{
			Hits: []sessions.SaidHit{{
				Session: sessions.Session{Agent: "codex", ID: "0190aaaa", Title: "Auth fix", Cwd: "/work/app", Last: time.Now()},
				Hit:     sessions.Hit{Role: "user", Before: "the ", Match: "Rainbow table", After: " skips the salt"},
			}},
			Indexed: 3, Total: 5, Cut: true,
		}, nil
	}
	t.Cleanup(func() { searchSaid = was })
	text, isErr := sessionsCall(t, h, sessionsSearchTool, `{"query":"  Rainbow   TABLE ","agent":"codex","limit":"4"}`)
	if isErr {
		t.Fatal(text)
	}
	if asked != [3]any{"rainbow table", "codex", 4} {
		t.Fatalf("searched %v", asked)
	}
	for _, want := range []string{"1 session says \"rainbow table\":", "1. codex:0190aaaa · Auth fix", "/work/app · ", "user: the [Rainbow table] skips the salt", "higher limit", "3 of 5 sessions", sessionsReadTool} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	// the first listing of a long history still running: nothing is
	// said to be missing yet
	searchSaid = func(context.Context, string, string, int) (sessions.SaidFound, error) {
		return sessions.SaidFound{Hits: []sessions.SaidHit{}, Listing: true}, nil
	}
	if text, _ := sessionsCall(t, h, sessionsSearchTool, `{"query":"rainbow"}`); !strings.Contains(text, "No session searched so far says") || !strings.Contains(text, "still being found") {
		t.Fatalf("while listing:\n%s", text)
	}
	if text, isErr := sessionsCall(t, h, sessionsSearchTool, `{"query":"  "}`); !isErr {
		t.Fatalf("an empty query: %s", text)
	}
}

// read_session with find starts a few messages before the words, says
// what it left out, and goes on with from; an agent Settings doesn't let
// magpie read is not read.
func TestSessionsMCPReadAtTheWords(t *testing.T) {
	fresh(t)
	home := os.Getenv("HOME")
	claude := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	at := time.Now().Add(-time.Hour).UTC()
	stamp := func(i int) string { return at.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano) }
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"question %d"},"cwd":"/work/app","sessionId":"s1","uuid":"u%d"}`, stamp(2*i), i, i),
			fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"answer %d %s"}]},"cwd":"/work/app","sessionId":"s1","uuid":"a%d"}`, stamp(2*i+1), i, strings.Repeat("words ", 10), i))
	}
	lines[50] = fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"where is the Zanzibar key"},"cwd":"/work/app","sessionId":"s1","uuid":"u25"}`, stamp(50))
	path := filepath.Join(claude, "projects", "-work-app", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())

	text, isErr := sessionsCall(t, h, sessionsReadTool, `{"session":"claude:s1","find":"zanzibar KEY","max_chars":1000}`)
	if isErr {
		t.Fatal(text)
	}
	if !strings.Contains(text, "(messages before 47 left out)") || !strings.Contains(text, "[47] assistant:\nanswer 23") || !strings.Contains(text, "[50] user:\nwhere is the Zanzibar key") || strings.Contains(text, "answer 22 ") {
		t.Fatalf("read at the words:\n%s", text)
	}
	if !strings.Contains(text, "(more: "+sessionsReadTool+" with from=") {
		t.Fatalf("no way on in\n%s", text)
	}
	whole, _ := sessionsCall(t, h, sessionsReadTool, `{"session":"claude:s1","from":"78"}`)
	if !strings.Contains(whole, "[79] assistant:\nanswer 39") || strings.Contains(whole, "[77]") {
		t.Fatalf("from 78:\n%s", whole)
	}
	if text, isErr := sessionsCall(t, h, sessionsReadTool, `{"session":"s1"}`); !isErr {
		t.Fatalf("no agent: %s", text)
	}

	off := func() bool { return true }
	sessions.OwnOff.Store(&off)
	t.Cleanup(func() { sessions.OwnOff.Store(nil) })
	if text, isErr := sessionsCall(t, h, sessionsReadTool, `{"session":"claude:s1"}`); !isErr || strings.Contains(text, "answer") {
		t.Fatalf("read with no agent's files read: %s", text)
	}
}
