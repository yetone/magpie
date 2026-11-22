package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// agyCard is an agy-cli reading as the plugin gives it: a model family's
// window at its pool's bucket, and the pool's own 7-day window.
func agyCard(provider string, left float64, reset time.Time) SubscriptionQuota {
	week := 7 * 24 * time.Hour
	r := reset
	return SubscriptionQuota{Provider: provider, Name: "agy CLI", User: "a@x.com", Windows: []QuotaWindow{
		{Name: "Gemini 3.8 Flash", Model: "gemini-3.8-flash", Family: "Gemini", Pool: "Gemini", Aside: true, Used: 100 - left, Span: week, ResetsAt: &r},
		{Name: "7 days", Pool: "Gemini", Aside: true, Used: 100 - left, Span: week, ResetsAt: &r},
	}}
}

// TestQuotaStaleReplicaAfterReset: Google's quota, after a reset, answers
// from replicas that still tell the window the reset ended. The owner's
// agy readings of 11-17 (Gemini 7 days, to the second): 7.32% left of the
// window to 11-21 00:00:17, then the reset at 23:28:31, then the old
// window again at 23:30:55 and 23:37:11 between readings of the new one.
// The old window's readings aren't kept, and the card shows the new
// window: its share, its reset, and so its start and pace.
func TestQuotaStaleReplicaAfterReset(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldReset := mustTime(t, "2026-11-21T00:00:17Z")
	readings := []struct {
		at    string
		left  float64
		reset string
	}{
		{"2026-11-17T09:44:06Z", 7.32, "2026-11-21T00:00:17Z"},
		{"2026-11-17T23:28:31Z", 100, "2026-11-24T23:28:31Z"},
		{"2026-11-17T23:29:41Z", 100, "2026-11-24T23:29:40Z"},
		{"2026-11-17T23:30:55Z", 5.05, "2026-11-21T00:00:17Z"},
		{"2026-11-17T23:34:31Z", 100, "2026-11-24T23:34:31Z"},
		{"2026-11-17T23:37:11Z", 5.05, "2026-11-21T00:00:17Z"},
	}
	var last []SubscriptionQuota
	for _, r := range readings {
		at := mustTime(t, r.at)
		last = []SubscriptionQuota{agyCard("agy-cli", r.left, mustTime(t, r.reset))}
		last[0].ReadAt = &at
		noteQuotaHistory(last, at)
	}

	pts := lineOf(t, QuotaHistories(mustTime(t, "2026-11-17T00:00:00Z"), "", ""), "a@x.com", "Gemini · 7 days")
	for _, p := range pts[1:] {
		if p.ResetsAt.Equal(oldReset) {
			t.Errorf("a stale replica's reading of the ended window was kept: %+v", pts)
		}
	}
	if got := pts[len(pts)-1]; got.Left != 100 || got.Start.Before(mustTime(t, "2026-11-17T23:28:31Z")) {
		t.Errorf("last point = %+v, want the new window's", got)
	}

	// the card read last (a stale replica's) shows the new window
	readAt := mustTime(t, "2026-11-17T23:37:11Z")
	for _, w := range last[0].Windows {
		if w.Used != 0 || w.ResetsAt == nil || w.ResetsAt.Equal(oldReset) {
			t.Errorf("%s: used %v, reset %v; want the new window's 0 and its reset", w.Name, w.Used, w.ResetsAt)
			continue
		}
		start, _, ok := w.Bounds(readAt)
		if !ok || start.Before(mustTime(t, "2026-11-17T23:28:31Z")) {
			t.Errorf("%s starts %v, want at or after the reset", w.Name, start)
		}
	}
}

// TestQuotaStaleKeptBefore: flips recorded before stale replicas were
// known (the owner's of 11-21, Gemini 7 days) are left out of what the
// Usage page reads, so the cycle starts where the reset began it.
func TestQuotaStaleKeptBefore(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	type pt struct{ at, start, reset string }
	old := pt{"", "2026-11-20T15:24:43Z", "2026-11-27T15:24:43Z"}
	cur := pt{"", "2026-11-21T08:48:20Z", "2026-11-28T08:48:20Z"}
	var pts []QuotaPoint
	for _, r := range []struct {
		at   string
		left float64
		w    pt
	}{
		{"2026-11-21T08:44:36Z", 99.67, old},
		{"2026-11-21T08:48:31Z", 98.41, old},
		{"2026-11-21T08:49:51Z", 99.91, cur},
		{"2026-11-21T08:53:37Z", 98.85, cur},
		{"2026-11-21T08:56:07Z", 98.33, old},
		{"2026-11-21T08:57:22Z", 98.79, cur},
		{"2026-11-21T09:00:36Z", 98.31, old},
		{"2026-11-21T09:02:50Z", 98.76, cur},
	} {
		s, e := mustTime(t, r.w.start), mustTime(t, r.w.reset)
		pts = append(pts, QuotaPoint{At: mustTime(t, r.at), Left: r.left, Start: &s, ResetsAt: &e})
	}
	h := quotaHist{"agy-cli|a@x.com": {"Gemini · 7 days": pts}, "codex|a@x.com": {"Weekly": pts}}
	b, _ := json.Marshal(h)
	if err := os.MkdirAll(filepath.Dir(quotaHistPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quotaHistPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	hs := QuotaHistories(mustTime(t, "2026-11-21T00:00:00Z"), "", "")
	var got []QuotaPoint
	for _, a := range hs {
		if a.Provider == "agy-cli" {
			got = a.Lines[0].Points
		}
		if a.Provider == "codex" && len(a.Lines[0].Points) != len(pts) {
			t.Errorf("another vendor's window moving back was changed: %d points of %d", len(a.Lines[0].Points), len(pts))
		}
	}
	if len(got) != 6 {
		t.Fatalf("got %d points, want the old window's two before the reset and the new window's four: %+v", len(got), got)
	}
	for _, p := range got[2:] {
		if !p.Start.Equal(mustTime(t, cur.start)) {
			t.Errorf("after the reset, a reading of the old window: %+v", p)
		}
	}
}

// TestQuotaStaleOnlyGoogle: another vendor's reading of an earlier window
// is taken as it says, and agy's first use after an unused stretch (its
// reset rolling with each reading, then fixed) is not a stale one.
func TestQuotaStaleOnlyGoogle(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t0 := mustTime(t, "2026-11-17T23:00:00Z")
	note := func(provider string, m int, left float64, reset time.Time) SubscriptionQuota {
		at := t0.Add(time.Duration(m) * time.Minute)
		qs := []SubscriptionQuota{agyCard(provider, left, reset)}
		qs[0].ReadAt = &at
		noteQuotaHistory(qs, at)
		return qs[0]
	}
	note("codex", 0, 100, t0.Add(7*24*time.Hour))
	if q := note("codex", 10, 5, t0.Add(3*24*time.Hour)); q.Windows[1].Used != 95 {
		t.Errorf("codex's reading was changed: %+v", q.Windows[1])
	}
	// unused: the reset rolls; first used at 30, read at 40
	note("agy-cli", 0, 100, t0.Add(7*24*time.Hour))
	note("agy-cli", 20, 100, t0.Add(20*time.Minute+7*24*time.Hour))
	if q := note("agy-cli", 40, 98, t0.Add(30*time.Minute+7*24*time.Hour)); q.Windows[1].Used != 2 {
		t.Errorf("first use after a rolling reset taken for stale: %+v", q.Windows[1])
	}
}
