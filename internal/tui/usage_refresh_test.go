package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yetone/magpie/internal/provider"
)

// fakeQuotas stands in for the vendors: each read answers the next of
// answers (the last one again once they run out), and says whether it was
// asked.
type fakeQuotas struct {
	answers []fakeAnswer
	asked   []bool
}

type fakeAnswer struct {
	used    float64
	reading bool
}

func (f *fakeQuotas) install(t *testing.T) {
	t.Helper()
	readQ, gap, again := readQuotas, quotasOpenGap, quotasAgain
	t.Cleanup(func() { readQuotas, quotasOpenGap, quotasAgain = readQ, gap, again })
	quotasAgain = time.Millisecond
	readQuotas = func(asked bool) ([]provider.SubscriptionQuota, bool) {
		f.asked = append(f.asked, asked)
		a := f.answers[min(len(f.asked), len(f.answers))-1]
		return []provider.SubscriptionQuota{{Provider: "claude", User: "a@b.c", Windows: []provider.QuotaWindow{{Name: "5h", Used: a.used}}}}, a.reading
	}
}

// run feeds cmd's messages back into m till nothing more is asked, as the
// program does (a tick waits quotasAgain).
func run(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for i := 0; cmd != nil; i++ {
		if i > 100 {
			t.Fatal("the page never stops asking")
		}
		next, c := m.Update(cmd())
		m, cmd = next.(model), c
	}
	return m
}

func used(m model) float64 {
	if len(m.quotas) == 0 || len(m.quotas[0].Windows) == 0 {
		return -1
	}
	return m.quotas[0].Windows[0].Used
}

// TestUsagePageAsksAgainWhileReading: an answer given while the accounts'
// usage is still being read is the stale copy that read replaces. The page
// asks again until the new one has landed, as the app's does (#959), not
// leave the stale one up till r is pressed (sean_80289).
func TestUsagePageAsksAgainWhileReading(t *testing.T) {
	f := &fakeQuotas{answers: []fakeAnswer{{10, true}, {10, true}, {64, false}}}
	f.install(t)
	m := model{w: 160, h: 60}
	cmd := m.goTo(pageUsage)
	m = run(t, m, cmd)
	if got := used(m); got != 64 {
		t.Fatalf("the page shows %v%% used, want the new reading's 64%%", got)
	}
	if want := []bool{true, false, false}; len(f.asked) != len(want) || f.asked[0] != true || f.asked[1] || f.asked[2] {
		t.Fatalf("reads asked %v, want %v: the page opened asks, its asks again take what is read", f.asked, want)
	}
}

// TestUsagePageAsksAgainAtMostTries: a read that never lands is asked for
// quotasTries times, not forever.
func TestUsagePageAsksAgainAtMostTries(t *testing.T) {
	f := &fakeQuotas{answers: []fakeAnswer{{10, true}}}
	f.install(t)
	m := model{w: 160, h: 60}
	m = run(t, m, m.goTo(pageUsage))
	if len(f.asked) != 1+quotasTries {
		t.Fatalf("%d reads, want %d", len(f.asked), 1+quotasTries)
	}
}

// TestUsagePageStopsAskingOffThePage: the asks again stop once the page is
// left; coming back reads anew.
func TestUsagePageStopsAskingOffThePage(t *testing.T) {
	f := &fakeQuotas{answers: []fakeAnswer{{10, true}}}
	f.install(t)
	m := model{w: 160, h: 60}
	cmd := m.goTo(pageUsage)
	m.page = pageSessions
	m = run(t, m, cmd)
	if len(f.asked) != 1 {
		t.Fatalf("%d reads off the page, want 1", len(f.asked))
	}
}

// TestUsagePageReadsAgainOnReturn: the page opened again reads anew, as
// the app's does (quotasOpened), where the terminal read once a session:
// within quotasOpenGap of the last ask it takes what is kept, after it it
// asks.
func TestUsagePageReadsAgainOnReturn(t *testing.T) {
	f := &fakeQuotas{answers: []fakeAnswer{{10, false}, {30, false}, {64, false}}}
	f.install(t)
	m := model{w: 160, h: 60}
	m = run(t, m, m.goTo(pageUsage))
	m.page = pageSessions
	m = run(t, m, m.goTo(pageUsage))
	if got := used(m); got != 30 {
		t.Fatalf("back on the page within the gap it shows %v%%, want what is kept now, 30%%", got)
	}
	m.page = pageSessions
	m.qaskedAt = time.Now().Add(-quotasOpenGap - time.Second)
	m = run(t, m, m.goTo(pageUsage))
	if got := used(m); got != 64 {
		t.Fatalf("back on the page after the gap it shows %v%%, want 64%%", got)
	}
	if want := []bool{true, false, true}; len(f.asked) != 3 || f.asked[0] != want[0] || f.asked[1] != want[1] || f.asked[2] != want[2] {
		t.Fatalf("reads asked %v, want %v", f.asked, want)
	}
}

// TestUsageOldAnswerStandsAside: an answer to a load since replaced (r
// pressed while an ask again was on its way) is dropped, so it neither
// draws over the new one nor starts asks of its own.
func TestUsageOldAnswerStandsAside(t *testing.T) {
	m := model{w: 160, h: 60, page: pageUsage, qgen: 2}
	next, cmd := m.Update(quotaMsg{qs: []provider.SubscriptionQuota{{Provider: "claude"}}, reading: true, gen: 1})
	m = next.(model)
	if cmd != nil || m.quotas != nil {
		t.Fatalf("an old load's answer was taken: cmd %v, quotas %v", cmd != nil, m.quotas)
	}
}
