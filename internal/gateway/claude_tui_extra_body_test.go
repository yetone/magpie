package gateway

import (
	"bytes"
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

// Claude Code's auto mode classifier is asked afresh each call with the
// transcript so far, its marks moving on with it. Pasted into the
// interactive mode, which places no mark of the client's, each call read
// only Claude Code's own prompt and wrote the whole transcript again
// (#5585, #5655). With a transcript longer than marksPrintAt it runs as
// claude -p, which keeps the marks: Claude Code's own caching is off and
// the run is told the client's cache_control, Desktop's header-less
// classifier and an opted-in client alike. A short one stays interactive.
func TestClaudeTUIClassifierKeepsLongMarksAsPrint(t *testing.T) {
	long := strings.Repeat(`earlier transcript entry\n`, 20000)
	ask := func(h *resumeHarness, grown bool, short bool, header string) {
		t.Helper()
		body := classifierBody(t, grown)
		if short {
			body = bytes.Replace(body, []byte(long), []byte(strings.Repeat(`earlier transcript entry\n`, 100)), 1)
		}
		r := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
		if header != "" {
			r.Header.Set("X-Magpie-Cache-Marks", header)
		}
		w := httptest.NewRecorder()
		if code, msg := h.s.serveClaudeSubscription(w, r, provider.Anthropic, h.p, "claude-sonnet-5", body, &Usage{}); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
	}
	for _, header := range []string{"", "client"} {
		t.Run("header="+header, func(t *testing.T) {
			h := tuiScriptHarness(t, `echo "cache-disabled ${DISABLE_PROMPT_CACHING-unset}" >> $log`)
			if !bytes.Contains(classifierBody(t, false), []byte(long)) {
				t.Fatal("fixture: the transcript isn't where the test shortens it")
			}
			ask(h, false, false, header)
			ask(h, true, false, header)
			runs := h.runs()
			if len(runs) != 2 {
				t.Fatalf("%d runs, want one a call", len(runs))
			}
			for _, log := range runs {
				if !strings.Contains(log, "cache-disabled 1") || !strings.Contains(log, `"cache_control"`) {
					t.Fatalf("a long classifier's marks weren't kept by a -p run: %.400s", log)
				}
			}
			if last := ClaudeModeNow().Last; last == nil || last.TUI || last.Why != cacheMarksCode {
				t.Fatalf("the last run: %+v", last)
			}
			ask(h, false, true, header)
			runs = h.runs()
			if len(runs) != 3 {
				t.Fatalf("%d runs after a short classifier", len(runs))
			}
			short := 0
			for _, log := range runs {
				if strings.Contains(log, "cache-disabled unset") {
					short++
				}
			}
			if short != 1 {
				t.Fatalf("a short classifier left the interactive mode: %d interactive runs", short)
			}
			if last := ClaudeModeNow().Last; last == nil || !last.TUI {
				t.Fatalf("the short classifier's run: %+v", last)
			}
		})
	}
}
