package artifact

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
)

func commentCode(err error) string {
	var ce *CommentError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// A thread starts, takes replies, resolves, opens again when a person
// writes in it (not when the agent answers), loses a comment, and goes
// with its last; every change says so to Watch, and moves the stamp.
func TestCommentThreads(t *testing.T) {
	sandbox(t)
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><title>Board</title><p>x")})
	if err != nil {
		t.Fatal(err)
	}
	changes, stop := Watch(a.ID)
	defer stop()
	stamp := CommentsStamp(a.ID)
	th, err := StartThread(a.ID, NewThread{Anchor: &Anchor{Path: "#top", X: 1.5, Y: -1}, Label: "Plan\x00\n", Version: 1, Author: "me", Text: "Bigger"})
	if err != nil || th.Anchor.X != 1 || th.Anchor.Y != 0 || th.Label != "Plan" {
		t.Fatalf("start: %+v %v", th, err)
	}
	if c := <-changes; !c.Comments {
		t.Fatalf("a change: %+v", c)
	}
	if st := CommentsStamp(a.ID); st == stamp {
		t.Fatal("the stamp didn't move")
	}
	if err := Resolve(a.ID, th.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Reply(a.ID, th.ID, "agent:claude", "claude", "Done.", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetThread(a.ID, th.ID); !got.Resolved {
		t.Fatal("the agent's answer opened it again")
	}
	c, err := Reply(a.ID, th.ID, "me", "", "Not quite", true)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := GetThread(a.ID, th.ID)
	if got.Resolved || len(got.Comments) != 3 || !got.Comments[2].Sent {
		t.Fatalf("after a person's reply: %+v", got)
	}
	if err := SetRun(a.ID, th.ID, &Run{Agent: "claude", State: "working"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetThread(a.ID, th.ID); got.Run == nil || got.Run.State != "working" {
		t.Fatalf("run: %+v", got.Run)
	}
	if err := DeleteComment(a.ID, th.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := DeleteComment(a.ID, th.ID, "c_nope"); commentCode(err) != "not_found" {
		t.Fatalf("no such comment: %v", err)
	}
	for _, cm := range []string{got.Comments[0].ID, got.Comments[1].ID} {
		if err := DeleteComment(a.ID, th.ID, cm); err != nil {
			t.Fatal(err)
		}
	}
	if ts, _ := Threads(a.ID); len(ts) != 0 {
		t.Fatalf("the thread outlived its comments: %+v", ts)
	}
	if err := Resolve(a.ID, th.ID, false); commentCode(err) != "not_found" {
		t.Fatalf("a gone thread: %v", err)
	}
}

// Text and anchors are checked as the contract has them.
func TestCommentChecks(t *testing.T) {
	sandbox(t)
	a, _ := PublishPage(Publish{Page: []byte("<!doctype html><title>B</title><p>x")})
	for _, c := range []struct {
		why    string
		anchor *Anchor
		text   string
	}{
		{"empty", nil, " \n "},
		{"too long", nil, strings.Repeat("é", MaxCommentText/2+1)},
		{"a control character", nil, "a\x07b"},
		{"not UTF-8", nil, "a\xffb"},
		{"no path", &Anchor{X: .5}, "hi"},
		{"a zero-width path", &Anchor{Path: "#a​"}, "hi"},
		{"NaN", &Anchor{Path: "#a", X: math.NaN()}, "hi"},
	} {
		if _, err := StartThread(a.ID, NewThread{Anchor: c.anchor, Text: c.text}); commentCode(err) != "invalid" {
			t.Errorf("%s: %v", c.why, err)
		}
	}
	if _, err := StartThread(a.ID, NewThread{Text: "line\n\ttabbed"}); err != nil {
		t.Fatalf("newlines and tabs: %v", err)
	}
	if _, err := StartThread("nope", NewThread{Text: "hi"}); err == nil {
		t.Fatal("a thread on no artifact")
	}
}

// The shell's token is its own: not the page's, not another artifact's.
func TestShellToken(t *testing.T) {
	sandbox(t)
	st, err := ShellToken("a1")
	pt, _ := PageToken("a1")
	other, _ := ShellToken("a2")
	if err != nil || st == "" || st == pt || st == other || !ShellTokenOK("a1", st) || ShellTokenOK("a1", pt) || ShellTokenOK("a2", st) || PageTokenOK("a1", st) || ShellTokenOK("a1", "") {
		t.Fatalf("tokens: %q %q %q %v", st, pt, other, err)
	}
}

// The shell's page: its script and style carry the nonce its CSP allows,
// a title can't end its script or break its markup, and the frame's
// address is set by the script (with the fragment), not the markup.
func TestShellPage(t *testing.T) {
	page, nonce := ShellPage(ShellConfig{Title: `</script><img src=x onerror=alert(1)>"`, Src: "/a/x/", Origin: "http://127.0.0.1:3425", Lang: "zh"})
	s := string(page)
	_, n2 := ShellPage(ShellConfig{})
	if nonce == "" || nonce == n2 || strings.Count(s, `nonce="`+nonce+`"`) != 2 || !strings.Contains(ShellCSP(nonce), "'nonce-"+nonce+"'") {
		t.Fatalf("nonce %q: %.300s", nonce, s)
	}
	if strings.Count(s, "</script>") != 1 || strings.Contains(s, "<img") || strings.Contains(s, ` src="`) {
		t.Fatalf("escaping: %s", regexp.MustCompile(`(?s)<style.*</style>`).ReplaceAllString(s, ""))
	}
	i := strings.LastIndex(s, ");</script>")
	j := strings.LastIndex(s[:i], "})(")
	var cfg ShellConfig
	if j < 0 || json.Unmarshal([]byte(s[j+3:i]), &cfg) != nil || cfg.Src != "/a/x/" || cfg.Lang != "zh" {
		t.Fatalf("config: %v", s[max(0, j):])
	}
	if !strings.Contains(string(shellJS), "frame.src = C.src + location.hash") {
		t.Fatal("the script doesn't set the frame's address")
	}
}

// comments is served; composer_only reads as its own; a declaration of
// the wrong shape is refused.
func TestCommentsCapability(t *testing.T) {
	c, err := ParseCapabilities(json.RawMessage(`{"comments":{"composer_only":true}}`))
	if err != nil || !CommentsComposerOnly(c) {
		t.Fatalf("composer_only: %v %v", c, err)
	}
	if c, err := ParseCapabilities(json.RawMessage(`{"comments":{}}`)); err != nil || CommentsComposerOnly(c) {
		t.Fatalf("full: %v %v", c, err)
	}
	if _, err := ParseCapabilities(json.RawMessage(`{"comments":{"composer_only":"yes"}}`)); err == nil {
		t.Fatal("composer_only: \"yes\"")
	}
}
