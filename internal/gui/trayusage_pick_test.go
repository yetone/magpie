package gui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A subscription's card shows the windows Settings picked for it
// (settings.TrayWindows, StrikingRaw on X: Claude's 5 hours alone, not two
// small numbers squeezed together), named by how long they run, so a
// vendor's order doesn't matter; the tooltip still spells out every one,
// one picked that is gone shows every window rather than none, and a card
// of another provider is left as it was.
func TestTrayWindowsPicked(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	claude := provider.SubscriptionQuota{Provider: "claude", Name: "Claude", User: "a@b.c", Windows: []provider.QuotaWindow{
		{Name: "7 days", Used: 70, Span: 7 * 24 * time.Hour}, // the vendor's order, longest first
		{Name: "5 hours", Used: 42, Span: 5 * time.Hour},
		{Name: "Opus", Used: 90, Span: 7 * 24 * time.Hour, Model: "opus"},
	}}
	codex := provider.SubscriptionQuota{Provider: "codex", Name: "Codex", User: "x@y.z", Windows: []provider.QuotaWindow{
		{Name: "5 hours", Used: 10, Span: 5 * time.Hour},
		{Name: "Weekly", Used: 20, Span: 7 * 24 * time.Hour},
	}}
	cards := []provider.SubscriptionQuota{claude, codex}
	rows := func(pick map[string][]string) ([][]string, string) {
		cells, _, tip := trayCardsView("en", cards, now, false, false, pick)
		var out [][]string
		for _, c := range cells {
			out = append(out, c.Rows)
		}
		return out, tip
	}
	same := func(got, want [][]string) bool { return slices.EqualFunc(got, want, slices.Equal) }

	all, allTip := rows(nil)
	if !same(all, [][]string{{"42%", "70%"}, {"10%", "20%"}}) {
		t.Fatalf("none picked: %q", all)
	}
	got, tip := rows(map[string][]string{"claude": {"5h"}})
	if !same(got, [][]string{{"42%"}, {"10%", "20%"}}) {
		t.Fatalf("5h alone: %q", got)
	}
	if tip != allTip {
		t.Errorf("the tooltip lost a window:\n%s\nwant\n%s", tip, allTip)
	}
	if got, _ = rows(map[string][]string{"claude": {"7d"}, "codex": {"7d"}}); !same(got, [][]string{{"70%"}, {"20%"}}) {
		t.Fatalf("7d alone: %q", got)
	}
	// a picked window gone (the vendor dropped it): every window, not an
	// empty card
	if got, _ = rows(map[string][]string{"claude": {"month"}}); !same(got, all) {
		t.Fatalf("gone: %q", got)
	}
	// the Linux tray's text follows it too
	if _, label, _ := trayCardsView("en", cards, now, true, false, map[string][]string{"claude": {"5h"}}); label != "58% | 90% · 80%" {
		t.Fatalf("text: %q", label)
	}
}

// Windows are named by how long they run; one whose span isn't known by
// the vendor's name for it.
func TestTrayWindowKind(t *testing.T) {
	for _, c := range []struct {
		w    provider.QuotaWindow
		want string
	}{
		{provider.QuotaWindow{Span: 5 * time.Hour}, "5h"},
		{provider.QuotaWindow{Span: 5*time.Hour + 3*time.Second}, "5h"},
		{provider.QuotaWindow{Span: 24 * time.Hour}, "1d"},
		{provider.QuotaWindow{Span: 7 * 24 * time.Hour}, "7d"},
		{provider.QuotaWindow{Span: 30 * 24 * time.Hour}, "month"},
		{provider.QuotaWindow{Span: 31 * 24 * time.Hour}, "month"},
		{provider.QuotaWindow{Span: 15 * time.Minute}, "15m"},
		{provider.QuotaWindow{Name: " Credits "}, "name:credits"},
	} {
		if got := trayWindowKind(c.w); got != c.want {
			t.Errorf("%+v: %q, want %q", c.w, got, c.want)
		}
	}
}

// Settings is told every window the menu bar can say as a percent by a
// kind of its own: those it shows unpicked by their span, and one it shows
// only when picked (a single model's, money aside) by its model or name
// too, marked extra; a row of text gets none. The cards the cache keeps
// are left as they were.
func TestWithTrayKinds(t *testing.T) {
	qs := []provider.SubscriptionQuota{{Provider: "claude", Windows: []provider.QuotaWindow{
		{Name: "5 hours", Span: 5 * time.Hour},
		{Name: "Opus", Span: 7 * 24 * time.Hour, Model: "opus"},
		{Name: "Extra", Span: 30 * 24 * time.Hour, Aside: true, Used: 3},
		{Name: "Note", Text: true, Aside: true},
	}}, {Provider: "deepseek", Balance: "¥5"}}
	out := withTrayKinds(qs)
	ws := out[0].Windows
	for i, want := range []struct {
		kind  string
		extra bool
	}{{"5h", false}, {"7d:opus", true}, {"month:extra", true}, {"", false}} {
		if ws[i].TrayKind != want.kind || ws[i].TrayExtra != want.extra {
			t.Fatalf("%s: %q extra %v, want %q %v", ws[i].Name, ws[i].TrayKind, ws[i].TrayExtra, want.kind, want.extra)
		}
	}
	if qs[0].Windows[0].TrayKind != "" {
		t.Fatal("the cache's window was written")
	}
	if out[1].Balance != "¥5" {
		t.Fatalf("balance card: %+v", out[1])
	}
}

// Every window a card reports can be picked alone (CrazyJal on X: Cursor's
// Grok Bot wasn't offered). Cursor's plugin reports Grok Bot aside, so it
// isn't stopping Cursor's models, with the same 7 days as a weekly plan's
// Included window: each gets a kind of its own, Grok Bot alone shows it
// alone, and unpicked the card is as it was, Grok Bot left out. Two
// windows shown unpicked with one span are told apart by name.
func TestTrayWindowsPickAside(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	cursor := provider.SubscriptionQuota{Provider: "cursor", Name: "Cursor", User: "a@b.c", Windows: []provider.QuotaWindow{
		{Name: "Weekly", Used: 30, Span: week},
		{Name: "Grok Bot", Used: 64, Span: week, Aside: true},
		{Name: "Free usage", Display: "+$5 on us", Aside: true},
		{Name: "Total", Used: 30, Aside: true},
	}}
	out := withTrayKinds([]provider.SubscriptionQuota{cursor})[0].Windows
	if out[0].TrayKind != "7d" || out[1].TrayKind != "7d:grok bot" || !out[1].TrayExtra || out[2].TrayKind != "" || out[3].TrayKind != "name:total" {
		t.Fatalf("kinds: %+v", out)
	}
	rows := func(pick []string) []string {
		cells, _, _ := trayCardsView("en", []provider.SubscriptionQuota{cursor}, now, false, false, map[string][]string{"cursor": pick})
		return cells[0].Rows
	}
	if got := rows(nil); !slices.Equal(got, []string{"30%"}) {
		t.Fatalf("unpicked: %q", got)
	}
	if got := rows([]string{"7d:grok bot"}); !slices.Equal(got, []string{"64%"}) {
		t.Fatalf("Grok Bot alone: %q", got)
	}
	if got := rows([]string{"7d"}); !slices.Equal(got, []string{"30%"}) {
		t.Fatalf("Weekly alone: %q", got)
	}
	if got := rows([]string{"7d", "7d:grok bot"}); !slices.Equal(got, []string{"30%", "64%"}) {
		t.Fatalf("both: %q", got)
	}
	_, _, tip := trayCardsView("en", []provider.SubscriptionQuota{cursor}, now, false, false, map[string][]string{"cursor": {"7d:grok bot"}})
	if !strings.Contains(tip, "Grok Bot 64%") {
		t.Fatalf("tooltip: %q", tip)
	}

	// two windows shown unpicked, one span: each picked alone
	two := provider.SubscriptionQuota{Provider: "x", Name: "X", Windows: []provider.QuotaWindow{
		{Name: "Requests", Used: 10, Span: week},
		{Name: "Tokens", Used: 20, Span: week},
	}}
	if ws := withTrayKinds([]provider.SubscriptionQuota{two})[0].Windows; ws[0].TrayKind != "7d:requests" || ws[1].TrayKind != "7d:tokens" {
		t.Fatalf("one span twice: %+v", ws)
	}
	cells, _, _ := trayCardsView("en", []provider.SubscriptionQuota{two}, now, false, false, map[string][]string{"x": {"7d:tokens"}})
	if !slices.Equal(cells[0].Rows, []string{"20%"}) {
		t.Fatalf("Tokens alone: %q", cells[0].Rows)
	}
}
