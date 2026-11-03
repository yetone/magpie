package gateway

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// tuiScriptHarness is a script harness whose runs are the interactive
// mode's, through a stand-in helper that runs the Claude Code it is handed.
func tuiScriptHarness(t *testing.T, script string) *resumeHarness {
	t.Helper()
	h := newScriptHarness(t, "ok", script)
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
shift; claude=$1; shift
exec "$claude" "$@"
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	return h
}

// The interactive mode's Claude Code reads CLAUDE_CODE_EXTRA_BODY only as
// it starts (a real 2.1.316: an apply_flag_settings or a --settings file
// rewritten later changed nothing it sent), so a run whose turn carries
// safeguards is given them in its environment, and a turn whose
// safeguards differ starts a new run given the whole conversation; one
// with the same goes on in the run. They were refused with the
// interactive mode on (e5f66f8a6).
func TestClaudeTUISafeguardsInEnv(t *testing.T) {
	h := tuiScriptHarness(t, `echo "extra ${CLAUDE_CODE_EXTRA_BODY-unset} betas ${ANTHROPIC_BETAS-unset}" >> $log`)

	h.extra = safeguards
	h.ask("rules", "a1")
	h.ask("rules", "a1", "a2")
	runs := h.runs()
	if len(runs) != 1 {
		t.Fatalf("the same safeguards started %d runs, want 1", len(runs))
	}
	for _, log := range runs {
		if !strings.Contains(log, `extra {"safeguards":[{"type":"dangerous_tool_use","classifier_context":"The user allows file edits in the project."}]} betas dangerous-tool-use-`) {
			t.Fatalf("the run's environment: %s", log)
		}
		if strings.Contains(log, "apply_flag_settings") {
			t.Fatalf("an interactive run was told settings it doesn't read: %s", log)
		}
	}
	h.extra = strings.Replace(safeguards, "file edits", "nothing", 1)
	h.ask("rules", "a1", "a2", "a3")
	runs = h.runs()
	if len(runs) != 2 {
		t.Fatalf("changed safeguards went on in %d runs, want a second", len(runs))
	}
	seen := false
	for _, log := range runs {
		if strings.Contains(log, `"classifier_context":"The user allows nothing in the project."`) {
			seen = true
			// the turns before in a transcript it resumes (replaySession)
			if !strings.Contains(log, "--resume ") || !strings.Contains(log, "a3") {
				t.Fatalf("the new run wasn't given the whole conversation: %s", log)
			}
		}
	}
	if !seen {
		t.Fatalf("no run has the changed safeguards: %v", runs)
	}
}

// Claude Code's auto-mode context changes on nearly every request in parts
// of its own: the tool ids of the turns before, git's status, the
// system-reminder ids. A turn whose safeguards differ only there goes on in
// the interactive run; restarting for them began a new run for every turn
// and tool round, each writing the whole conversation to the cache again,
// and one message spent a Pro account's 5-hour window (xybio). A change to
// what the user allows (the rules here) still starts a new run.
func TestClaudeTUISafeguardsVaryingPartsKeepTheRun(t *testing.T) {
	h := tuiScriptHarness(t, `echo "extra ${CLAUDE_CODE_EXTRA_BODY-unset}" >> $log`)
	varied := func(n int, deny string) string {
		g := strings.Replace(reviewAsked, `"toolu_01Capture001"`, fmt.Sprintf(`"toolu_01Capture%03d"`, n), 1)
		g = strings.Replace(g, `"error":"not_a_repo"}`, fmt.Sprintf(`"error":null,"status":{"clean":false,"counts":{"modified":%d},"porcelain":" M f%d.go","truncated":false},"visibility":null,"truncated":false}`, n, n), 1)
		g = strings.Replace(g, `"prior_turn_context":[`, fmt.Sprintf(`"system_reminders":[{"id":"r%d"}],"shed":[],"prior_turn_context":[`, n), 1)
		g = strings.Replace(g, `"deny":[]`, `"deny":[`+deny+`]`, 1)
		return `"safeguards":` + g + `,`
	}
	said := []string{"a1"}
	for n := 1; n <= 4; n++ {
		h.extra = varied(n, "")
		h.ask("rules", said...)
		said = append(said, fmt.Sprintf("a%d", n+1))
	}
	if runs := h.runs(); len(runs) != 1 {
		t.Fatalf("4 turns whose safeguards differ only in Claude Code's varying parts started %d runs, want 1", len(runs))
	}
	h.extra = varied(5, `"Bash(rm:*)"`)
	h.ask("rules", said...)
	runs := h.runs()
	if len(runs) != 2 {
		t.Fatalf("a changed rule went on in %d runs, want a second", len(runs))
	}
	seen := false
	for _, log := range runs {
		if strings.Contains(log, `Bash(rm:*)`) {
			seen = true
			if !strings.Contains(log, "--resume ") {
				t.Fatalf("the new run wasn't given the whole conversation: %s", log)
			}
		}
	}
	if !seen {
		t.Fatalf("no run has the changed rule: %v", runs)
	}
}

// A client's own cache marks are taken with the interactive mode on: its
// words are pasted without them (claudetui's userPieces), so Claude Code
// keeps its own prompt caching, not turned off for marks it never sends,
// and the reply says the client's marks were dropped. With caching off and
// the marks lost, every turn was written to the cache again at full price.
func TestClaudeTUIClientCacheKeepsItsOwn(t *testing.T) {
	h := tuiScriptHarness(t, `echo "cache-disabled ${DISABLE_PROMPT_CACHING-unset} extra ${CLAUDE_CODE_EXTRA_BODY-unset}" >> $log`)
	h.from = "127.0.0.1:51000"
	system := `"system":[{"type":"text","text":"shared","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"more"}],`
	tool := `"tools":[{"name":"read","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}}],`
	tail := `"cache_control":{"type":"ephemeral","ttl":"1h"},`
	first := `{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"there"}]}`
	w := cacheAsk(t, h, system+tool+tail, `[`+first+`]`, "client")
	if got := w.Header().Get("X-Magpie-Cache-Marks-Dropped"); got != "tools:1,system:1,messages:2" {
		t.Fatalf("dropped %q", got)
	}
	for _, log := range h.runs() {
		if !strings.Contains(log, "cache-disabled unset extra unset") {
			t.Fatalf("an interactive run's caching: %s", log)
		}
	}
	// the next turn goes on in the run
	w = cacheAsk(t, h, system+tool+tail, `[`+first+`,{"role":"assistant","content":"ok"},{"role":"user","content":"again"}]`, "client")
	if got := w.Header().Get("X-Magpie-Cache-Marks-Dropped"); got != "tools:1,system:1,messages:2" {
		t.Fatalf("next turn dropped %q", got)
	}
	if n := len(h.runs()); n != 1 {
		t.Fatalf("the next turn started %d runs", n)
	}
}

// An image known by its URL alone is downloaded for the interactive mode,
// which is pasted files: Claude Code is handed its bytes, and the turns
// after hand the same URL over without downloading it again. One that
// isn't an image, or can't be had, fails the request saying why, and no
// Claude Code runs it as -p instead.
func TestClaudeTUIImageByURL(t *testing.T) {
	h := tuiScriptHarness(t, "")
	srv, hits := secret(t, string(pngBytes))
	allowPrivate(t, "127.0.0.1")
	ask := func(url string, said ...string) (int, string) {
		t.Helper()
		img := `{"role":"user","content":[{"type":"image","source":{"type":"url","url":"` + url + `"}},{"type":"text","text":"what is it"}]}`
		msgs := []string{img}
		for _, m := range said {
			msgs = append(msgs, `{"role":"assistant","content":"ok"}`, fmt.Sprintf(`{"role":"user","content":%q}`, m))
		}
		body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"rules","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + strings.Join(msgs, ",") + `]}`
		rec := httptest.NewRecorder()
		return h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &Usage{})
	}
	if code, msg := ask(srv.URL + "/a.png"); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	if code, msg := ask(srv.URL+"/a.png", "and now"); code != 200 {
		t.Fatalf("next turn: %d %s", code, msg)
	}
	runs := h.runs()
	if len(runs) != 1 || hits.Load() != 1 {
		t.Fatalf("%d runs, the image downloaded %d times", len(runs), hits.Load())
	}
	data := base64.StdEncoding.EncodeToString(pngBytes)
	for _, log := range runs {
		if !strings.Contains(log, `"data":"`+data+`"`) || !strings.Contains(log, `"media_type":"image/png"`) {
			t.Fatalf("Claude Code wasn't handed the image's bytes: %s", log)
		}
	}
	notImage, _ := secret(t, `{"admin_token":"s3cret"}`)
	if code, msg := ask(notImage.URL + "/status"); code != 400 || !strings.Contains(msg, "isn't an image") || strings.Contains(msg, "s3cret") {
		t.Fatalf("not an image: %d %s", code, msg)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	if code, msg := ask(gone.URL + "/b.png"); code != 400 || !strings.Contains(msg, "404") {
		t.Fatalf("a missing image: %d %s", code, msg)
	}
	if n := len(h.runs()); n != 1 {
		t.Fatalf("a request whose image couldn't be had ran: %d runs", n)
	}
}
