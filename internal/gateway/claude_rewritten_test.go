package gateway

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// almaReply is a reply as Alma is given it: its own notification block at
// the end, which it takes out before the reply goes into its history.
const almaReply = "ok <alma_notification>saved</alma_notification>"

// send asks a turn of the conversation msgs (messages' JSON), with the
// tools named, of the harness's Claude Code.
func (h *resumeHarness) send(system string, tools []string, msgs ...string) {
	h.t.Helper()
	var ts []string
	for _, name := range tools {
		ts = append(ts, fmt.Sprintf(`{"name":%q,"input_schema":{"type":"object"}}`, name))
	}
	body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"` + system + `","tools":[` + strings.Join(ts, ",") + `],"messages":[` + strings.Join(msgs, ",") + `]}`
	var u Usage
	if code, msg := h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		h.t.Fatalf("%d %s", code, msg)
	}
}

func user(s string) string      { return fmt.Sprintf(`{"role":"user","content":%q}`, s) }
func assistant(s string) string { return fmt.Sprintf(`{"role":"assistant","content":%q}`, s) }

// toldLast is what the newest of the runs was told last.
func (h *resumeHarness) toldLast() string {
	h.t.Helper()
	var last string
	for _, log := range h.runs() {
		if i := strings.LastIndex(log, "\ntold "); i >= 0 && strings.Contains(log[i:], "more") {
			last = log[i:]
		}
	}
	return last
}

// Alma sends each turn's conversation back rewritten (#912): the
// [Context: …] it gave the last user message taken out of it, the
// notification block at the end of the reply taken out of that, and the
// tools picked for the turn. Its next turn goes on in the run that had the
// last one all the same, told the new turn alone.
func TestClaudeRewrittenTurnGoesOnInItsRun(t *testing.T) {
	read, both := []string{"read"}, []string{"read", "write"}
	for _, c := range []struct {
		name         string
		tools        []string
		said, asked  string // the turn's user message and reply as sent back
		toolsThen    []string
		system, then string
	}{
		{name: "context taken out", tools: read, toolsThen: read, said: "hi", asked: almaReply},
		{name: "context taken out of the middle", tools: read, toolsThen: read, said: "look at this", asked: almaReply},
		{name: "notification taken out", tools: read, toolsThen: read, said: "[Context: 10:00] hi", asked: "ok"},
		{name: "both", tools: read, toolsThen: read, said: "hi", asked: "ok"},
		{name: "fewer tools", tools: both, toolsThen: read, said: "hi", asked: "ok"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newReplyHarness(t, almaReply)
			first := "[Context: 10:00] hi"
			if c.name == "context taken out of the middle" {
				first = "look [Context: 10:00] at this"
			}
			h.send("rules", c.tools, user(first))
			h.send("rules", c.toolsThen, user(c.said), assistant(c.asked), user("[Context: 10:05] more"))
			if n := len(h.runs()); n != 1 {
				t.Fatalf("runs: %d, want the one that had the last turn", n)
			}
			told := h.toldLast()
			if !strings.Contains(told, "more") || strings.Contains(told, "hi") || strings.Contains(told, "this") {
				t.Fatalf("the run was told more than the turn:\n%s", told)
			}
		})
	}
}

// A rewritten conversation goes on in a run only when it can be no other's:
// one whose earlier turns, settings, user's words or reply aren't those the
// run had, or that offers a tool the run's agent was never told of, is told
// to a new run, as is one two runs could have had.
func TestClaudeRewrittenTurnOfAnotherConversationStartsAnew(t *testing.T) {
	read := []string{"read"}
	for _, c := range []struct {
		name string
		ask  func(h *resumeHarness)
		runs int
	}{
		{"other words", func(h *resumeHarness) {
			h.send("rules", read, user("[Context: 10:00] hi"))
			h.send("rules", read, user("hello"), assistant("ok"), user("more"))
		}, 2},
		{"another reply", func(h *resumeHarness) {
			h.send("rules", read, user("[Context: 10:00] hi"))
			h.send("rules", read, user("hi"), assistant("no"), user("more"))
		}, 2},
		{"a reply longer than the run's", func(h *resumeHarness) {
			h.send("rules", read, user("[Context: 10:00] hi"))
			h.send("rules", read, user("hi"), assistant(almaReply+" and more"), user("more"))
		}, 2},
		{"a tool the run doesn't have", func(h *resumeHarness) {
			h.send("rules", read, user("[Context: 10:00] hi"))
			h.send("rules", []string{"read", "write"}, user("hi"), assistant("ok"), user("more"))
		}, 2},
		{"other rules", func(h *resumeHarness) {
			h.send("rules", read, user("[Context: 10:00] hi"))
			h.send("other rules", read, user("hi"), assistant("ok"), user("more"))
		}, 2},
		{"earlier turns changed", func(h *resumeHarness) {
			h.send("rules", read, user("a"))
			h.send("rules", read, user("a"), assistant(almaReply), user("[Context: 10:00] hi"))
			h.send("rules", read, user("b"), assistant(almaReply), user("hi"), assistant("ok"), user("more"))
		}, 2},
		{"two runs could have it", func(h *resumeHarness) {
			h.send("rules", read, user("[Context: 10:00] hi"))
			h.send("rules", read, user("[Context: 11:00] hi"))
			h.send("rules", read, user("hi"), assistant("ok"), user("more"))
		}, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newReplyHarness(t, almaReply)
			c.ask(h)
			if n := len(h.runs()); n != c.runs {
				t.Fatalf("runs: %d, want %d", n, c.runs)
			}
		})
	}
}

// A rewritten conversation whose run was let go past idleMost goes on from
// its saved session, which is resumed with the tools offered now; not when
// one its session called may be gone from them.
func TestClaudeRewrittenTurnResumesItsSession(t *testing.T) {
	for _, c := range []struct {
		name    string
		tools   []string
		resumed bool
	}{
		{"same tools", []string{"read"}, true},
		{"more tools", []string{"read", "write"}, true},
		{"a tool gone", []string{"write"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newReplyHarness(t, almaReply)
			h.send("rules A", []string{"read"}, user("[Context: 10:00] hi"))
			var sidA string
			for pid := range h.runs() {
				sidA = "s" + pid
			}
			for i := range idleMost {
				h.send(fmt.Sprintf("rules %d", i), []string{"read"}, user("x"))
			}
			eventually(t, "A's run still going", func() bool {
				h.s.subscription.mu.Lock()
				defer h.s.subscription.mu.Unlock()
				return len(h.s.subscription.idle) == idleMost && len(h.s.subscription.shelf) == 1
			})
			h.send("rules A", c.tools, user("hi"), assistant("ok"), user("[Context: 10:05] more"))
			var resumed bool
			for _, log := range h.runs() {
				resumed = resumed || strings.Contains(log, "--resume "+sidA)
			}
			if resumed != c.resumed {
				t.Fatalf("resumed A's session: %t, want %t", resumed, c.resumed)
			}
		})
	}
}

// Claude Code keeps the images a session was given in a folder of its
// own under /tmp (#912), which goes with the session: when its run ends or
// its saved session is discarded, and, left by a gateway that stopped,
// once nothing in it changed for tempLongest and its session file is gone.
func TestClaudeSessionTempFilesAreRemoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Claude Code names its temp folder by the uid")
	}
	t.Setenv("CLAUDE_CODE_TMPDIR", t.TempDir())
	dir := filepath.Join(t.TempDir(), "-tmp-magpie-claude-work-1")
	tmp := claudeTempDir(dir)
	if want := filepath.Join(os.Getenv("CLAUDE_CODE_TMPDIR"), fmt.Sprintf("claude-%d", os.Getuid()), filepath.Base(dir)); tmp != want {
		t.Fatalf("temp folder %s, want %s", tmp, want)
	}
	image := func(sid string, age time.Duration) string {
		p := filepath.Join(tmp, sid, "images", "1.png")
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("png"), 0o600)
		at := time.Now().Add(-age)
		for _, q := range []string{p, filepath.Dir(p), filepath.Join(tmp, sid)} {
			os.Chtimes(q, at, at)
		}
		return filepath.Join(tmp, sid)
	}
	gone := func(p string) bool { _, err := os.Stat(p); return os.IsNotExist(err) }

	ended, other := image("ended", 0), image("other", 0)
	removeSession([]string{dir}, "ended")
	if !gone(ended) || gone(other) {
		t.Fatalf("ended's temp files gone: %t, another's: %t", gone(ended), gone(other))
	}

	os.MkdirAll(dir, 0o700)
	left, kept, recent, touched := image("left", 2*tempLongest), image("kept", 2*tempLongest), image("recent", time.Hour), image("touched", 2*tempLongest)
	os.WriteFile(filepath.Join(dir, "kept.jsonl"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(touched, "images", "2.png"), []byte("png"), 0o600)
	newSubscriptionBridge().sweepSessions([]string{dir}, false)
	eventually(t, "a stopped gateway's session temp files stayed", func() bool { return gone(left) })
	for _, p := range []string{kept, recent, touched, other} {
		if gone(p) {
			t.Fatalf("%s was swept", p)
		}
	}
}

// An Alma conversation whose tools grew between turns (#2375: 17 tools,
// then 32 with its browser tools) is told to a new Claude Code, whose
// agent is told every tool from its start, and the log says that is why:
// the conversation came back rewritten, so none of its keys was the
// run's, and the line said no run had ever had it.
func TestClaudeRewrittenTurnWithMoreToolsSaysWhy(t *testing.T) {
	h := newReplyHarness(t, almaReply)
	logs := keepLog(t)
	h.send("rules", []string{"read"}, user("[Context: 10:00] hi"))
	more := []string{"read", "browser_open", "browser_click", "browser_type", "browser_read", "browser_back", "browser_close"}
	h.send("rules", more, user("hi"), assistant("ok"), user("[Context: 10:05] more"))
	if n := len(h.runs()); n != 2 {
		t.Fatalf("runs: %d, want a new one told every tool", n)
	}
	want := "no run waits for this turn: its run waits under another key: the client offers 6 tool(s) its Claude Code wasn't told of (browser_open, browser_click, browser_type, browser_read, browser_back, 1 more)"
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("the log says:\n%s", logs)
	}
}
