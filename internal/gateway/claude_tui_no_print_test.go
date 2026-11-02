package gateway

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// printMode sets the user's ClaudePrintMode (the interactive mode turned
// off) as print says, put back as it was after the test.
func printMode(t *testing.T, print bool) {
	t.Helper()
	// settings are the package's, not the test's (cleanups run last first,
	// so the first call's is the last)
	was := settings.Load()
	t.Cleanup(func() { _ = settings.Save(was) })
	st := settings.Load()
	st.ClaudePrintMode = print
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
}

// With the interactive mode on, a request it can't take is refused,
// saying why, and no Claude Code starts: it never runs as -p (the owner,
// 11-01). Turned off, the same request runs as -p.
func TestClaudeTUIOnRefusesPrint(t *testing.T) {
	h, started := tuiHelperHarness(t, "")
	// an image known by its URL the interactive mode can't download (this
	// computer's, which magpie never fetches): -p's Claude Code sends the
	// URL as it is
	body := `{"model":"claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"http://127.0.0.1:1/a.png"}},{"type":"text","text":"what is it?"}]}]}`
	ask := func() (int, string) {
		var u Usage
		return h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	}
	code, msg := ask()
	if code != 400 || !strings.Contains(msg, "couldn't fetch the image") {
		t.Fatalf("%d %.300s", code, msg)
	}
	if runs := h.runs(); len(runs) != 0 {
		t.Fatalf("Claude Code ran: %v", runs)
	}
	if n := helperStarts(started); n != 0 {
		t.Fatalf("the helper started %d times for a refused request", n)
	}
	printMode(t, true)
	if _, msg := ask(); strings.Contains(msg, "couldn't fetch the image") || len(h.runs()) == 0 {
		t.Fatalf("turned off, it didn't run as -p: %.300s", msg)
	}
	if n := helperStarts(started); n != 0 {
		t.Fatalf("turned off, the helper started %d times", n)
	}
}

// A first message of any length runs in the interactive mode: a
// conversation of a week, told whole to a new Claude Code, was refused
// over 400KB with a 502 (@crosscn on X, 11-01), while a paste carries it
// (a real 500KB one was answered whole, a 3MB one taken in).
func TestClaudeTUIBigFirstMessage(t *testing.T) {
	h, started := tuiHelperHarness(t, "")
	big := strings.Repeat("the codebase as read so far. ", 40000) // ~1.1MB
	if code, msg := h.askCode("rules", big); code != 200 {
		t.Fatalf("%d %.300s", code, msg)
	}
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the helper started %d times, want once", n)
	}
}

// With the interactive mode on, a test's or a warm-up's "hi" is said
// through the interactive mode's helper, in stream-json, and its run ends
// at the result; turned off, it runs as -p, with no helper.
func TestAskClaudeTUIOn(t *testing.T) {
	h := newReplyHarness(t, "ok")
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	env := filepath.Join(t.TempDir(), "env")
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
echo "trust=$`+claudetui.TrustEnv+`" >> '`+env+`'
shift; claude=$1; shift
exec "$claude" "$@"
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := askClaude(ctx, "", "haiku"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(env)
	if strings.TrimSpace(string(b)) != "trust=1" {
		t.Fatalf("the helper was told %q", b)
	}
	var args, told string
	for _, l := range h.runs() {
		args, _, _ = strings.Cut(l, "\n")
		told = l
	}
	if want := "-p --output-format stream-json --input-format stream-json --verbose --model haiku --tools  --strict-mcp-config --setting-sources "; !strings.Contains(args, want) {
		t.Fatalf("Claude Code ran with %q", args)
	}
	if want := `--settings {"disableDeepLinkRegistration":"disable"}`; !strings.Contains(args, want) {
		t.Fatalf("Claude Code registers no URL handler, ran with %q", args)
	}
	if !strings.Contains(told, `told {"message":{"content":[{"text":"hi","type":"text"}],"role":"user"},"type":"user"}`) {
		t.Fatalf("Claude Code was told:\n%s", told)
	}
	printMode(t, true)
	if err := askClaude(ctx, "", "haiku"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(env); strings.Count(string(b), "trust=") != 1 {
		t.Fatalf("turned off, the helper ran again: %q", b)
	}
}
