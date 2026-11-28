package claudetui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/tuidrive"
)

// A /usage figure redrawn in place reads as redrawn, where the stream had
// both ("10% used" then "2"), and its reset time drawn in two reads whole
// (tuidrive's TestGridDrawsInPlace has the grid's side).
func TestUsageReadOnGridDrawnInPlace(t *testing.T) {
	g := tuidrive.NewGrid(200, 50)
	g.Draw([]byte("\x1b[10;3HSettings  Status\x1b[12;3HCurrent session \x1b[38;2;1;2;3m██\x1b[m 10% used"))
	g.Draw([]byte("\x1b[10;3HSettin\x1b[1Cs\x1b[m  Status"))
	g.Draw([]byte("\x1b[12;23H2"))
	g.Draw([]byte("\x1b[13;3HResets 7pm \x1b[3"))
	g.Draw([]byte("8;2;1;2;3m(Asia/Shanghai) \xe2\x96"))
	g.Draw([]byte("\x88"))
	if got := usageText(g.Text()); got != "Current session: 12% used · resets 7pm (Asia/Shanghai)" {
		t.Fatalf("usageText = %q", got)
	}
}

// What the pseudo console passed on for a real Claude Code 2.1.316's
// /usage panel, loading its figures and failing to, reads as that panel.
func TestGridReadsConPTYPanel(t *testing.T) {
	for file, want := range map[string]string{
		"usage-loading-win-2.1.316.bin": "Loading usage data…",
		"usage-failed-win-2.1.316.bin":  "Error: Failed to load usage data",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		g := tuidrive.NewGrid(200, 50)
		g.Draw(b)
		text := g.Text()
		if !strings.Contains(text, want) || !strings.Contains(text, "❯ /usage") || usageUsed.MatchString(text) {
			t.Fatalf("%s:\n%s", file, text)
		}
	}
}

// screenWithRules is a real Claude Code 2.1.319's screen as it came up, at
// 200 columns (the paste fixture up to its first clear): its banner, then
// its prompt box, ruled the screen's width over and under, and the line
// under it. With err, the screen is cleared after it, as send clears it
// before a paste, and Claude Code draws a frame again with err over the
// box, the box as it drew it.
func screenWithRules(t *testing.T, err string) *shim {
	t.Helper()
	b, rerr := os.ReadFile(filepath.Join("testdata", "paste-images-2.1.319.bin"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	start := b[:1994]
	at := bytes.Index(start, []byte("\r\x1b[1B\x1b[38;2;136;136;136m─"))
	if at < 0 {
		t.Fatal("the fixture has no prompt box")
	}
	if err == "" {
		return drawn(start, 0)
	}
	// err on the row over the effort's, the box drawn from the effort's
	// row on, where it was
	frame := "\x1b[?2026h\x1b[?25l\x1b[45;3H\x1b[38;2;255;107;128m⎿  " + err + "\x1b[39m\x1b[46;1H" + string(start[at:])
	return drawn(append(slices.Clone(start), frame...), len(start))
}

// An error that says what Claude Code's screen showed says its words, not
// the rules of its prompt box: a screen-wide rule is 600 bytes of "─",
// the whole of what the error kept, and FremontDango (Windows 11, a Claude
// subscription) was shown one long line where Claude Code had said why.
func TestLastScreenSaysTheWordsNotTheRules(t *testing.T) {
	const expired = `API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired. Please obtain a new token or refresh your existing token."},"request_id":"req_011CVzD4x7"} · Please run /login`
	for _, c := range []struct {
		name, err string
		want      []string
	}{
		{"the prompt as it came up", "", []string{"Claude Code v2.1.319", `❯ Try "fix lint errors"`, "⏸ manual mode on · ? for shortcuts"}},
		{"an error over the prompt", expired, []string{"⎿ API Error: 401", "OAuth token has expired", "· Please run /login ❯"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := fmt.Errorf("Claude Code did not take the message: %s", screenWithRules(t, c.err).LastScreen()).Error()
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("the error lacks %q:\n%s", w, got)
				}
			}
			if strings.Contains(got, "─") {
				t.Errorf("the error has the prompt's rules:\n%s", got)
			}
		})
	}
}

// Why the helper ends is said on the lines magpie logs, why first, then
// the screen as it stands, row by row, from its top: the error keeps only
// its end. The rows are their words, a rule alone left out.
func TestSayKeepsTheWholeScreen(t *testing.T) {
	s := screenWithRules(t, "API Error: 401 · Please run /login")
	why := "Claude Code did not take the message: " + s.LastScreen()
	var b strings.Builder
	s.say(&b, why)
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if lines[0] != "claude-tui: "+why {
		t.Fatalf("the first line is %q, not why", lines[0])
	}
	for _, want := range []string{"claude-tui: screen| Fable 5.1 · Claude API", "claude-tui: screen| ● high · /effort", "claude-tui: screen| ⎿ API Error: 401 · Please run /login", `claude-tui: screen| ❯ Try "fix lint errors"`} {
		if !slices.Contains(lines, want) {
			t.Errorf("no line %q:\n%s", want, b.String())
		}
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "claude-tui: ") || strings.Contains(l, "─") || strings.TrimSpace(strings.TrimPrefix(l, "claude-tui: screen|")) == "" {
			t.Errorf("line %q", l)
		}
	}
}
