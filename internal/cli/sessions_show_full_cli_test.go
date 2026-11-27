package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

const fixLong = "claude:33333333-2222-3333-4444-555555555555"

// longShowSession adds a Claude Code session to sessionsHome's: six
// messages, the fourth (number 3) 20,000 characters long and ending in
// TAIL-WORDS, past where a read cuts a message.
func longShowSession(t *testing.T) string {
	t.Helper()
	long := "LONG-START " + strings.Repeat("y", 20_000-len("LONG-START  TAIL-WORDS")) + " TAIL-WORDS"
	var lines []string
	for i, text := range []string{"message zero", "message one", "message two", long, "message four", "message five"} {
		role := "user"
		content := fmt.Sprintf("%q", text)
		if i%2 == 1 {
			role = "assistant"
			content = fmt.Sprintf(`[{"type":"text","text":%q}]`, text)
		}
		lines = append(lines, fmt.Sprintf(`{"type":%q,"timestamp":"2026-09-20T10:00:%02dZ","message":{"role":%q,"content":%s},"cwd":"/work/app","sessionId":"33333333-2222-3333-4444-555555555555","uuid":"u%d"}`, role, i, role, content, i))
	}
	path := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-work-app", "33333333-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	return long
}

type showJSON struct {
	Session    sessions.Session    `json:"session"`
	Transcript sessions.Transcript `json:"transcript"`
	Next       int                 `json:"next"`
	Notes      []string            `json:"notes"`
}

func runShowJSON(t *testing.T, args ...string) showJSON {
	t.Helper()
	out, err := runSearch(t, true, append(args, "--json")...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	var j showJSON
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		t.Fatalf("%v: %v\n%.300s", args, err, out)
	}
	return j
}

// show --json reads from --from as the text does, and says where the
// next page starts (hdhfhf_hdh219 on X: --json always began at the
// start, whatever --from said).
func TestSessionsShowJSONFrom(t *testing.T) {
	sessionsHome(t)
	longShowSession(t)
	j := runShowJSON(t, fixLong, "--from", "4")
	if j.Transcript.From != 4 || len(j.Transcript.Parts) != 2 || j.Transcript.Parts[0].Text != "message four" || j.Next != 0 {
		t.Fatalf("--from 4: from %d, %d parts, next %d: %+v", j.Transcript.From, len(j.Transcript.Parts), j.Next, j.Transcript.Parts)
	}
	// a page of --max-chars, then the next from where it says
	j = runShowJSON(t, fixLong, "--max-chars", "1000")
	if len(j.Transcript.Parts) != 3 || j.Next != 3 || !j.Transcript.Cut {
		t.Fatalf("--max-chars 1000: %d parts, next %d", len(j.Transcript.Parts), j.Next)
	}
	if !strings.Contains(strings.Join(j.Notes, "\n"), "magpie sessions show "+fixLong+" --from 3 --max-chars 1000 --json") {
		t.Fatalf("no way on in %q", j.Notes)
	}
	j = runShowJSON(t, fixLong, "--max-chars", "1000", "--from", "3")
	if j.Transcript.From != 3 || len(j.Transcript.Parts) != 1 || j.Next != 4 {
		t.Fatalf("--from 3: from %d, %d parts, next %d", j.Transcript.From, len(j.Transcript.Parts), j.Next)
	}
	// with no flags, the whole session as before, nothing more to read
	if j = runShowJSON(t, fixLong); j.Transcript.From != 0 || len(j.Transcript.Parts) != 6 || j.Next != 0 {
		t.Fatalf("no flags: from %d, %d parts, next %d", j.Transcript.From, len(j.Transcript.Parts), j.Next)
	}
}

// show cuts a message at 8000 characters unless --full, says so where it
// did and names --full; --full gives it whole, in text and in --json
// (hdhfhf_hdh219 on X: search found words past 8000 characters that show
// couldn't show).
func TestSessionsShowFull(t *testing.T) {
	sessionsHome(t)
	long := longShowSession(t)

	out, err := runSearch(t, true, fixLong)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "TAIL-WORDS") {
		t.Fatalf("the default read isn't cut any more:\n%.300s", out)
	}
	wantAll(t, out, "[3] assistant:\nLONG-START", "(cut: 12000 more characters of this message; magpie sessions show "+fixLong+" --from 3 --full)", "[5] assistant:\nmessage five")

	// a whole message longer than the rest of the page starts the next
	if out, err = runSearch(t, true, fixLong, "--full"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "[2] user:\nmessage two", "(more: magpie sessions show "+fixLong+" --from 3 --full)")
	if out, err = runSearch(t, true, fixLong, "--full", "--from", "3"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "[3] assistant:\n"+long+"\n")
	if strings.Contains(out, "(cut:") {
		t.Errorf("--full still says a message was cut:\n%.300s", out)
	}
	if out, err = runSearch(t, true, fixLong, "--full", "--max-chars", "100000"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "[3] assistant:\n"+long+"\n", "[5] assistant:\nmessage five")
	// the next page keeps --full
	if out, err = runSearch(t, true, fixLong, "--full", "--max-chars", "1000", "--from", "3"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, long, "(more: magpie sessions show "+fixLong+" --from 4 --max-chars 1000 --full)")

	j := runShowJSON(t, fixLong)
	if p := j.Transcript.Parts[3]; p.Cut != 12000 || strings.Contains(p.Text, "TAIL-WORDS") {
		t.Fatalf("--json by default: cut %d, %d characters", p.Cut, len(p.Text))
	}
	if notes := strings.Join(j.Notes, "\n"); !strings.Contains(notes, "--full") || !strings.Contains(notes, "--from 3 --full --json") {
		t.Fatalf("--json doesn't name --full: %q", j.Notes)
	}
	j = runShowJSON(t, fixLong, "--full")
	if p := j.Transcript.Parts[3]; p.Text != long || p.Cut != 0 || len(j.Notes) != 0 {
		t.Fatalf("--json --full: cut %d, %d characters, notes %q", p.Cut, len(p.Text), j.Notes)
	}
	// --find past the cut, whole
	if j = runShowJSON(t, fixLong, "--full", "--find", "tail-words"); j.Transcript.Parts[3].Text != long {
		t.Fatalf("--find --full: %.40q", j.Transcript.Parts[3].Text)
	}
}

// --full takes no value, and search doesn't take it.
func TestSessionsShowFullFlag(t *testing.T) {
	sessionsHome(t)
	for _, c := range []struct {
		show bool
		args []string
		want string
	}{
		{true, []string{fixClaude, "--full=yes"}, "--full takes no value\n" + sessionsShowUsage},
		{false, []string{"login", "--full"}, `unknown flag "--full"`},
		{true, []string{fixClaude, "--from", "-1"}, "--from takes a value"},
		{true, []string{fixClaude, "--full", "--help"}, sessionsShowUsage},
	} {
		out, err := runSearch(t, c.show, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) || out != "" {
			t.Errorf("%v: %v, wrote %q, want %q", c.args, err, out, c.want)
		}
	}
	if !strings.Contains(sessionsShowUsage, "[--full]") || !strings.Contains(helpFor([]string{"sessions", "show", "--help"}), "--full") {
		t.Error("the usage doesn't name --full")
	}
}
