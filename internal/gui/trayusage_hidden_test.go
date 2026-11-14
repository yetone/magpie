package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// onkashiko on X: a card hidden from the Usage page's Overview showed on in
// the menu bar. Its cell, the tooltip and Windows' tray figure leave it out
// as the Overview and the tray panel do, and the rows hidden there too;
// the cells ticked are kept for when it is shown again.
func TestTrayLeavesOutWhatTheOverviewHides(t *testing.T) {
	was := trayInUseAccount
	trayInUseAccount = func(string) string { return "b@x.y" }
	t.Cleanup(func() { trayInUseAccount = was })
	cards := []provider.SubscriptionQuota{
		{Provider: "claude", Name: "Claude Code", User: "a@x.y", Windows: []provider.QuotaWindow{{Name: "5-hour", Used: 10}, {Name: "Weekly", Used: 20}}},
		{Provider: "claude", Name: "Claude Code", User: "b@x.y", Windows: []provider.QuotaWindow{{Name: "5-hour", Used: 70}, {Name: "Weekly", Used: 30}}},
		{Provider: "stepfun", Name: "StepFun", User: "s", Plan: "Plus", Windows: []provider.QuotaWindow{{Name: "5-hour", Used: 5}}},
		{Provider: "stepfun", Name: "StepFun", User: "s", Balance: "¥9.00"},
		{Provider: "deepseek", Name: "DeepSeek", Balance: "¥1.00"},
		{Provider: "kimi", Name: "Kimi", Windows: []provider.QuotaWindow{{Name: "Weekly", Used: 1}}},
	}
	ids := []string{"claude|*", "claude|a@x.y", "stepfun|s", "deepseek", "kimi"}
	now := time.Now()
	if got := trayShown(cards, ids, now, settings.Settings{}); len(got) != 5 {
		t.Fatalf("nothing hidden: %d cards, want 5", len(got))
	}
	s := settings.Settings{
		UsageCardsHidden:   []string{"claude|b@x.y|plan", "deepseek|balance", "stepfun|s|plan"},
		UsageWindowsHidden: []string{"claude|Weekly"},
	}
	got := trayShown(cards, ids, now, s)
	var names []string
	for _, q := range got {
		names = append(names, trayCardID(q)+":"+q.Balance+":"+strconv.Itoa(len(q.Windows)))
	}
	// the account in use (b) is hidden: its cell is empty, not moved to a;
	// a's card loses its weekly row; StepFun's plan hidden, its balance
	// shows; DeepSeek's balance hidden; Kimi's one row stays, a card never
	// left with none
	want := "claude|a@x.y::1 stepfun|s:¥9.00:0 kimi::1"
	if strings.Join(names, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(names, " "), want)
	}
	if cards[0].Windows[1].Name != "Weekly" || len(cards[0].Windows) != 2 {
		t.Fatal("the cached cards were changed")
	}
}

// A card hidden or shown again from the Usage page reaches the menu bar at
// once, not at its next tick.
func TestUsageArrangeWakesTheMenuBar(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	woke := 0
	was := onTrayUsage
	onTrayUsage = func() { woke++ }
	t.Cleanup(func() { onTrayUsage = was })
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"order":["kimi"]}`, 0},
		{`{"cardsHidden":["deepseek|balance"]}`, 1},
		{`{"windowsHidden":["claude|Weekly"]}`, 2},
		{`{"cardsHidden":[]}`, 3},
	} {
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/usage/arrange", strings.NewReader(c.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.body, rec.Code, rec.Body)
		}
		if woke != c.want {
			t.Fatalf("after %s the menu bar was woken %d times, want %d", c.body, woke, c.want)
		}
	}
}
