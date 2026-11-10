package gui

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// trayCards are three cards as the Usage page has them: Claude and Codex
// with a 5-hour and a weekly window, and a plan with a balance.
func trayCards() []provider.SubscriptionQuota {
	week, five := 7*24*time.Hour, 5*time.Hour
	return []provider.SubscriptionQuota{
		{Provider: "claude", Name: "Claude Code", Icon: "claude-color", User: "a@b.c", Windows: []provider.QuotaWindow{
			{Name: "Weekly", Used: 17.6, Span: week}, {Name: "5-hour", Used: 42.2, Span: five}}},
		{Provider: "codex", Name: "Codex", Icon: "codex-color", User: "x@y.z", Windows: []provider.QuotaWindow{
			{Name: "5-hour", Used: 8, Span: five}, {Name: "Weekly", Used: 100, Span: week}}},
		{Provider: "copilot", Name: "Copilot", Icon: "githubcopilot", Windows: []provider.QuotaWindow{
			{Name: "Premium", Used: 63}}},
		{Provider: "deepseek", Name: "DeepSeek", Icon: "deepseek-color", Balance: "¥12.30"},
	}
}

func TestTrayUsageCells(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	cards := trayCards()

	// the ticked ones in the order ticked; one no longer there is left out
	picked := trayPick(cards, []string{"codex|x@y.z", "gone|who", "claude|a@b.c"}, time.Now())
	if len(picked) != 2 || picked[0].Provider != "codex" || picked[1].Provider != "claude" {
		t.Fatalf("picked %+v", picked)
	}
	if got := trayPick(cards, nil, time.Now()); got != nil {
		t.Fatalf("none ticked: %+v", got)
	}
	if got := trayPick(cards, []string{"claude"}, time.Now()); got != nil {
		t.Fatalf("the vendor without the account: %+v", got)
	}

	for _, tc := range []struct {
		ids   []string
		rows  [][]string
		label string
	}{
		{nil, nil, ""},
		{[]string{"claude|a@b.c"}, [][]string{{"42%", "18%"}}, "42% · 18%"},
		{[]string{"claude|a@b.c", "codex|x@y.z"}, [][]string{{"42%", "18%"}, {"8%", "100%"}}, "42% · 18% | 8% · 100%"},
		{[]string{"claude|a@b.c", "codex|x@y.z", "copilot", "deepseek"},
			[][]string{{"42%", "18%"}, {"8%", "100%"}, {"63%"}, {"¥12.30"}}, "42% · 18% | 8% · 100% | 63% | ¥12.30"},
	} {
		cells, label, tip := trayUsageView(trayPick(cards, tc.ids, now), now, false, false)
		if label != tc.label {
			t.Errorf("%v: label %q, want %q", tc.ids, label, tc.label)
		}
		if len(cells) != len(tc.rows) {
			t.Fatalf("%v: %d cells, want %d", tc.ids, len(cells), len(tc.rows))
		}
		for i, c := range cells {
			if !slices.Equal(c.Rows, tc.rows[i]) {
				t.Errorf("%v: cell %d rows %q, want %q", tc.ids, i, c.Rows, tc.rows[i])
			}
		}
		if n := strings.Count(tip, "\n\n"); len(tc.ids) > 0 && n != len(tc.ids)-1 {
			t.Errorf("%v: tip has %d cards apart:\n%s", tc.ids, n+1, tip)
		}
	}

	// each cell with its logo as the page draws it: its own colours, or a
	// black glyph to tint; the name's letter always there to fall back on
	cells, _, _ := trayUsageView(cards, now, false, false)
	for i, want := range []struct {
		mono   bool
		letter string
		svg    bool
	}{{false, "C", true}, {false, "C", true}, {true, "C", true}, {false, "D", true}} {
		c := cells[i]
		if len(c.Icon) == 0 || c.Mono != want.mono || c.Letter != want.letter || strings.Contains(string(c.Icon[:min(len(c.Icon), 200)]), "<svg") != want.svg {
			t.Errorf("cell %d: icon %d bytes mono %v letter %q", i, len(c.Icon), c.Mono, c.Letter)
		}
	}
	// what is left, as Settings says
	if cells, label, _ := trayUsageView(cards[:2], now, true, false); label != "58% · 82% | 92% · 0%" || !slices.Equal(cells[1].Rows, []string{"92%", "0%"}) {
		t.Errorf("left: %q %+v", label, cells)
	}

	// a card that can't be read says so in the tooltip and draws nothing;
	// one with nothing to show is left out of both; one with a logo that
	// isn't among the page's (a picture the user gave) keeps its letter
	odd := []provider.SubscriptionQuota{
		{Provider: "codex", Name: "Codex", Icon: "codex-color", Error: "signed out"},
		{Provider: "zed", Name: "Zed", Icon: "zed"},
		{Provider: "mine", Name: "my plan", Icon: "file:mine.png", Balance: "$3"},
		{Provider: "zcode", Name: "ZCode", Icon: "zcode", Windows: []provider.QuotaWindow{{Name: "5 小时", Used: 1}}},
		{Provider: "x", Name: "", Icon: "../api", Balance: "1"},
	}
	cells, label, tip := trayUsageView(odd, now, false, false)
	if label != "$3 | 1% | 1" || tip != "Codex: signed out\n\nmy plan · $3\n\nZCode\n5 小时 1% used\n\n · 1" {
		t.Errorf("odd: %q %q", label, tip)
	}
	if len(cells) != 3 || cells[0].Icon != nil || cells[0].Letter != "M" || cells[1].Icon == nil || cells[1].Mono || cells[2].Icon != nil || cells[2].Letter != "" {
		t.Errorf("odd cells: %+v", cells)
	}
	// a balance too long for the bar is cut there, whole in the tooltip
	long := provider.SubscriptionQuota{Provider: "p", Name: "Plan", Balance: "12,345.67 credits left"}
	if cells, label, tip := trayUsageView([]provider.SubscriptionQuota{long}, now, false, false); len(cells) != 1 ||
		!slices.Equal(cells[0].Rows, []string{"12,345.67…"}) || label != long.Balance || !strings.Contains(tip, long.Balance) {
		t.Errorf("long: %+v %q %q", cells, label, tip)
	}
	if cells, label, tip := trayUsageView(odd[:2], now, false, false); cells != nil || label != "" || tip != "Codex: signed out" {
		t.Errorf("nothing to draw: %+v %q %q", cells, label, tip)
	}
}

// Every rendered cell carries its own identity, even with logos disabled
// or unreadable cards skipped. JavaScript arguments preserve unusual account IDs.
func TestTrayCellsCards(t *testing.T) {
	cards := trayCards()
	cards[0].User = "a\"b\\c@例子.test"
	cards[1].Error = "signed out"
	cells, _, _ := trayUsageView(cards, time.Now(), false, false)
	plain := trayPlain(cells)
	if len(cells) != len(cards)-1 {
		t.Fatalf("cells: %d", len(cells))
	}
	for i, cell := range cells {
		at := i
		if i > 0 {
			at++
		}
		if cell.ID != trayCardID(cards[at]) || plain[i].ID != cell.ID {
			t.Fatalf("cell %d: %q, plain %q", i, cell.ID, plain[i].ID)
		}
		if got := panelQuotaJS(cell.ID); got != "panelQuotaFocus("+strconv.Quote(cell.ID)+")" {
			t.Fatalf("JS: %s", got)
		}
	}
}

// An arc's flags run into the number after them in many of the logos
// (Codex's among them); the Mac's SVG drawing needs them apart.
func TestSVGArcFlags(t *testing.T) {
	for in, want := range map[string]string{
		`<path d="M19.503 0H4.496A4.496 4.496 0 000 4.496v1a.5.5 0 01-1-2.5e-1z" fill="#fff"/>`: `<path d="M 19.503 0 H 4.496 A 4.496 4.496 0 0 0 0 4.496 v 1 a .5 .5 0 0 1 -1 -2.5e-1 z" fill="#fff"/>`,
		`<path d="M1 1a2 2 0 1 1 2 2" />`: `<path d="M 1 1 a 2 2 0 1 1 2 2" />`,
		// no arc: as it was
		`<path d="M0 0L1 1" />`: `<path d="M0 0L1 1" />`,
		// what isn't a path's data is left alone
		`<svg id="a" width="1em"><path d="M0 0a1 1 0 2 1 1 1"/></svg>`: `<svg id="a" width="1em"><path d="M0 0a1 1 0 2 1 1 1"/></svg>`,
	} {
		if got := string(svgArcFlags([]byte(in))); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
	// as Codex's logo is handed to the menu bar
	b, _ := trayIconFile("codex-color")
	if !strings.Contains(string(b), "A 4.496 4.496 0 0 0 0 4.496") {
		t.Errorf("codex's logo not spaced out: %.300s", b)
	}
}

// A card's logo can be a picture the plugin gave (a stored "file:…" one),
// which the menu bar reads as the page's icons are read, in its own
// colours; anything else under "file:" is refused and the name's letter
// drawn in its place.
func TestTrayIconFileStored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	kept, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	b, mono := trayIconFile(kept)
	if mono || string(b) != string(png) {
		t.Errorf("trayIconFile(%q) = %d bytes, mono %v", kept, len(b), mono)
	}
	// each file a refused name could reach is there, so a refusal is the
	// name's check and not a missing file
	dir := provider.IconDir()
	for _, f := range []string{filepath.Join(dir, "mine.png"), filepath.Join(dir, "..", "providers.json"), filepath.Join(dir, "0123456789abcdef.exe")} {
		if err := os.WriteFile(f, png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{"file:mine.png", "file:../providers.json", "file:", "http://x/y.png", "file:0123456789abcdef.exe"} {
		if b, mono = trayIconFile(bad); b != nil || mono {
			t.Errorf("trayIconFile(%q) = %d bytes, mono %v", bad, len(b), mono)
		}
	}
}
