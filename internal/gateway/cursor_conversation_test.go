package gateway

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A conversation's Runs go to Cursor with one conversation_id, which keeps
// its prompt cached where it was (#498): the client's prompt_cache_key or
// its own session names it; another key, or another conversation in one
// session, is another id; with nothing naming it each Run has a new one.
func TestCursorConversationID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := readConnectFrame(bufio.NewReader(r.Body))
		if err != nil {
			http.Error(w, `{"code":"invalid_argument","message":"no run"}`, 400)
			return
		}
		conv := ""
		for _, rf := range pbFields(f.data) {
			if rf.num == 1 {
				for _, x := range pbFields(rf.data) {
					if x.num == 5 {
						conv = string(x.data)
					}
				}
			}
		}
		got = append(got, conv)
		cursorDuplex(w)
		w.Header().Set("Content-Type", "application/connect+proto")
		w.Write(cursorUpdate(1, pb{}.str(1, "ok")))
		w.Write(cursorUpdate(14, pb{}.varint(1, 3).varint(2, 1)))
		w.(http.Flusher).Flush()
	}))
	defer up.Close()
	tok, ver, agent, api := cursorToken, cursorVersion, cursorAgent, cursorAPI
	cursorToken = func() (string, error) { return "tok", nil }
	cursorVersion = func() string { return "cli-test" }
	cursorAgent, cursorAPI = up.URL, up.URL
	defer func() { cursorToken, cursorVersion, cursorAgent, cursorAPI = tok, ver, agent, api }()
	forgetCursorEndpoint(t)

	conv := func(from provider.Protocol, header, body string) string {
		t.Helper()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if header != "" {
			k, v, _ := strings.Cut(header, ": ")
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		var u Usage
		New().serveCursor(w, r, from, "auto", []byte(body), &u)
		if w.Code != 200 || len(got) == 0 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		id := got[len(got)-1]
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
			t.Fatalf("conversation_id %q is no UUID", id)
		}
		return id
	}
	codex := func(key, first, last string) string {
		return `{"model":"x","stream":false,"prompt_cache_key":"` + key + `","input":[` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + first + `"}]},` +
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"sure"}]},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + last + `"}]}]}`
	}

	// Codex: its thread's prompt_cache_key, turn after turn
	a1 := conv(provider.Responses, "", codex("thread-a", "fix the bug", "go on"))
	a2 := conv(provider.Responses, "", codex("thread-a", "fix the bug", "and the tests"))
	if a1 != a2 {
		t.Fatalf("one thread, two ids: %s %s", a1, a2)
	}
	if b := conv(provider.Responses, "", codex("thread-b", "fix the bug", "go on")); b == a1 {
		t.Fatal("two threads, one id")
	}

	// Claude Code: its session header; a Task agent in that session, with a
	// first message of its own, is a conversation of its own
	cc := func(first, last string) string {
		return `{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"` + first + `"},` +
			`{"role":"assistant","content":"sure"},{"role":"user","content":"` + last + `"}]}`
	}
	const session = "x-claude-code-session-id: 3f0c"
	m1 := conv(provider.Anthropic, session, cc("plan it", "next"))
	m2 := conv(provider.Anthropic, session, cc("plan it", "and then"))
	if m1 != m2 {
		t.Fatalf("one session, two ids: %s %s", m1, m2)
	}
	if sub := conv(provider.Anthropic, session, cc("search the repo", "next")); sub == m1 {
		t.Fatal("a subagent shares the main conversation's id")
	}

	// nothing names the conversation: a new id each Run
	chat := `{"model":"x","messages":[{"role":"user","content":"hi"}]}`
	if conv(provider.Chat, "", chat) == conv(provider.Chat, "", chat) {
		t.Fatal("unnamed conversations share an id")
	}
}

// TurnEndedUpdate's input_tokens counts the cache read and written; Usage's
// Input is the uncached rest, and reasoning_tokens is kept (#498).
func TestCursorUsageTakesTheCacheOut(t *testing.T) {
	for _, c := range []struct {
		end  pb
		want Usage
	}{
		{pb{}.varint(1, 137300).varint(2, 204).varint(3, 132352).varint(5, 90),
			Usage{Input: 4948, Output: 204, CacheRead: 132352, Reasoning: 90}},
		{pb{}.varint(1, 5000).varint(2, 10).varint(3, 1024).varint(4, 2048),
			Usage{Input: 1928, Output: 10, CacheRead: 1024, CacheWrite: 2048}},
		{pb{}.varint(1, 100).varint(2, 1),
			Usage{Input: 100, Output: 1}},
		{pb{}.varint(1, 10).varint(2, 1).varint(3, 64), // never below none
			Usage{Input: 0, Output: 1, CacheRead: 64}},
	} {
		evs, _, _ := cursorDecoded(t, nil, cursorUpdate(1, pb{}.str(1, "Hi")), cursorUpdate(14, c.end))
		var u *Usage
		for _, ev := range evs {
			if ev.Kind == KUsage {
				u = &ev.Usage
			}
		}
		if u == nil || *u != c.want {
			t.Fatalf("usage %+v, want %+v", u, c.want)
		}
		if u.prompt() != int(pbNum(pbFields(c.end), 1)) && u.Input > 0 {
			t.Fatalf("prompt %d", u.prompt())
		}
	}
}
