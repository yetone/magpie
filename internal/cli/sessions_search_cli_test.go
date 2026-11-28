package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

const fixClaude = "claude:11111111-2222-3333-4444-555555555555"

func runSearch(t *testing.T, show bool, args ...string) (string, error) {
	t.Helper()
	var b bytes.Buffer
	var err error
	if show {
		err = sessionsShowTo(&b, args)
	} else {
		err = sessionsSearchTo(&b, args)
	}
	return b.String(), err
}

// magpie sessions search (yansheng1003 on X): the Sessions page's search
// over every agent's sessions, from the command line, as the sessions
// MCP server writes it, each hit named so that show reads it.
func TestSessionsSearch(t *testing.T) {
	sessionsHome(t)
	out, err := runSearch(t, false, "login", "bug")
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, `1 session says "login bug":`, "1. "+fixClaude, "/work/app · ", "user: Fix the [login bug] in auth.go", "magpie sessions show <agent:id> --find")
	if strings.Contains(out, "codex:") {
		t.Errorf("a Codex session doesn't say it:\n%s", out)
	}

	if out, err = runSearch(t, false, "readme", "--agent", "Codex"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "1. codex:01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd", "user: Add a [README]")
	if out, _ = runSearch(t, false, "login", "--agent", "codex"); !strings.Contains(out, `No session says "login".`) {
		t.Errorf("--agent codex found Claude Code's:\n%s", out)
	}
	// the fixtures are from September 2026: none was used today
	if out, _ = runSearch(t, false, "login", "--days", "today"); !strings.Contains(out, "No session says") {
		t.Errorf("--days today found an old session:\n%s", out)
	}
	if out, _ = runSearch(t, false, "login", "--days", "all"); !strings.Contains(out, fixClaude) {
		t.Errorf("--days all left it out:\n%s", out)
	}

	if out, err = runSearch(t, false, "--json", "login"); err != nil {
		t.Fatal(err)
	}
	var found sessions.SaidFound
	if err := json.Unmarshal([]byte(out), &found); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(found.Hits) != 1 || found.Hits[0].Session.Agent != "claude" || found.Hits[0].Hit.Match == "" || found.Listing || found.Indexed != found.Total {
		t.Fatalf("json: %+v", found)
	}
	if strings.Contains(found.Hits[0].Session.Path, os.Getenv("HOME")) {
		t.Errorf("the path isn't under ~: %s", found.Hits[0].Session.Path)
	}

	// words that start with a dash, after --
	if _, err := runSearch(t, false, "--", "--login"); err != nil {
		t.Errorf("after -- every argument is words: %v", err)
	}
}

// magpie sessions show reads one session as text, from where the words
// were said, or as the page's transcript in --json; an id alone is
// enough when one agent has it.
func TestSessionsShow(t *testing.T) {
	sessionsHome(t)
	out, err := runSearch(t, true, fixClaude)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, fixClaude, "/work/app · ", "user:\nFix the   login\nbug in auth.go", "called Read")
	if out, err = runSearch(t, true, "11111111-2222-3333-4444-555555555555", "--find", "login"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, fixClaude, "Fix the   login")
	if out, err = runSearch(t, true, fixClaude, "--json"); err != nil {
		t.Fatal(err)
	}
	var j struct {
		Session    sessions.Session    `json:"session"`
		Transcript sessions.Transcript `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(out), &j); err != nil || j.Session.ID == "" || len(j.Transcript.Parts) == 0 {
		t.Fatalf("json %v: %s", err, out)
	}
	if _, err := runSearch(t, true, "claude:nope"); err == nil || !strings.Contains(err.Error(), "no session claude:nope") {
		t.Errorf("a session that isn't there: %v", err)
	}
	// reading on from where a read stopped
	if out, err = runSearch(t, true, fixClaude, "--from", "2"); err != nil || !strings.Contains(out, "(messages before 2 left out)") || strings.Contains(out, "[0]") {
		t.Errorf("--from 2: %v\n%s", err, out)
	}
}

// hdhfhf_hdh219 on X: each `magpie sessions show agent:id --from N` took
// 4-5s over ~3000 sessions, every agent's files listed for the one asked
// for. A Claude Code or Codex session named so is read from its agent's
// files alone (sessions.FindOne); one that isn't there is still looked
// for everywhere.
func TestSessionsShowListsOneAgent(t *testing.T) {
	sessionsHome(t)
	for _, ref := range []string{fixClaude, "codex:01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd"} {
		sessions.Reset()
		before := sessions.FullPasses()
		out, err := runSearch(t, true, ref, "--from", "1")
		if err != nil {
			t.Fatal(err)
		}
		wantAll(t, out, ref)
		if n := sessions.FullPasses() - before; n != 0 {
			t.Errorf("show %s listed every agent's files %d times, want none", ref, n)
		}
	}
	before := sessions.FullPasses()
	if _, err := runSearch(t, true, "claude:nope"); err == nil {
		t.Fatal("claude:nope was shown")
	}
	if sessions.FullPasses() == before {
		t.Error("a session not found under its agent wasn't looked for everywhere")
	}
}

// A flag either command doesn't know, or one missing its value, is
// refused with the usage, never taken for words or a session (LESSONS:
// magpie snow --help wrote "--help" into a profile); --help prints the
// command's own usage.
func TestSessionsSearchFlags(t *testing.T) {
	sessionsHome(t)
	for _, c := range []struct {
		show bool
		args []string
		want string
	}{
		{false, []string{"login", "--bogus"}, `unknown flag "--bogus"`},
		{false, []string{"login", "--find", "x"}, `unknown flag "--find"`},
		{false, []string{"login", "--limit"}, "--limit takes a value"},
		{false, []string{"login", "--limit", "0"}, "--limit takes a number from 1 to 100"},
		{false, []string{"login", "--days", "soon"}, "--days takes a number"},
		{false, []string{"login", "--json=1"}, "--json takes no value"},
		{false, []string{"--agent", "nobody", "login"}, `unknown agent "nobody"`},
		{false, []string{}, sessionsSearchUsage},
		{false, []string{"?!"}, "nothing to search for"},
		{true, []string{fixClaude, "--agent", "claude"}, `unknown flag "--agent"`},
		{true, []string{fixClaude, "--from", "x"}, "--from takes a number"},
		{true, []string{fixClaude, "other"}, "one session at a time"},
		{true, []string{"--help"}, sessionsShowUsage},
		{true, []string{}, sessionsShowUsage},
	} {
		_, err := runSearch(t, c.show, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
	for args, want := range map[string]string{
		"sessions search --help":   sessionsSearchUsage,
		"sessions show -h":         sessionsShowUsage,
		"sessions search login -h": sessionsSearchUsage,
		"sessions --help":          sessionsUsage,
	} {
		a := strings.Fields(args)
		if !helpAsked(a) || helpFor(a) != want {
			t.Errorf("%s: %q", args, helpFor(a))
		}
	}
}

// Until the user answers which agents' sessions magpie may read, search
// and show read nothing, not even to index, and say how to allow it.
func TestSessionsSearchNotAsked(t *testing.T) {
	sessionsHome(t)
	if err := settings.SetSessionReads(func(*settings.SessionReads) *settings.SessionReads {
		return &settings.SessionReads{}
	}); err != nil {
		t.Fatal(err)
	}
	for _, show := range []bool{false, true} {
		arg := "login"
		if show {
			arg = fixClaude
		}
		out, err := runSearch(t, show, arg)
		if !errors.Is(err, errNotAsked) || out != "" {
			t.Errorf("show %v: %v\n%s", show, err, out)
		}
	}
	if _, err := os.Stat(sessions.SaidPath()); !os.IsNotExist(err) {
		t.Errorf("the index was made before reads were allowed: %v", err)
	}
	// allowed for Codex alone: Claude Code's session is neither found nor read
	if err := settings.SetSessionReads(func(*settings.SessionReads) *settings.SessionReads {
		return &settings.SessionReads{Asked: true, Agents: map[string]bool{"codex": true}}
	}); err != nil {
		t.Fatal(err)
	}
	if out, err := runSearch(t, false, "login"); err != nil || strings.Contains(out, fixClaude) {
		t.Errorf("Claude Code isn't read: %v\n%s", err, out)
	}
	if _, err := runSearch(t, true, fixClaude); !errors.Is(err, sessions.ErrNotRead) {
		t.Errorf("show of an agent not read: %v", err)
	}
	if _, err := runSearch(t, false, "login", "--agent", "claude"); !errors.Is(err, sessions.ErrNotRead) {
		t.Errorf("--agent of an agent not read: %v", err)
	}
}
