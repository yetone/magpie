package claudetui

import (
	"os"
	"path/filepath"
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
