package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// spendingBackend is a ChatGPT backend whose one account spends cost
// points of its five hours on each turn, counted as the turn ends, as
// ChatGPT counts a turn once it is done: the codex.rate_limits event at
// a reply's start, and /wham/usage, say the usage as it is then, without
// the turns still under way.
type spendingBackend struct {
	mu         sync.Mutex
	used, cost float64
	out, most  int
	turns      int
	five, week int64
}

func newSpendingBackend(t *testing.T, used, cost float64, hold time.Duration) *spendingBackend {
	t.Helper()
	now := time.Now()
	b := &spendingBackend{used: used, cost: cost, five: now.Add(2 * time.Hour).Unix(), week: now.Add(4 * 24 * time.Hour).Unix()}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/codex/responses":
			io.ReadAll(r.Body)
			b.mu.Lock()
			used := b.used
			if used >= 100 {
				b.mu.Unlock()
				w.WriteHeader(429)
				io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)
				return
			}
			b.out++
			b.most = max(b.most, b.out)
			b.turns++
			b.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`event: codex.rate_limits`+"\n"+`data: {"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":`+jsonFloat(used)+`,"window_minutes":300,"reset_at":`+jsonInt(b.five)+`},"secondary":{"used_percent":10,"window_minutes":10080,"reset_at":`+jsonInt(b.week)+`}},"credits":null,"metered_limit_name":null,"limit_name":null}`,
				`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`))
			w.(http.Flusher).Flush()
			select {
			case <-time.After(hold):
			case <-r.Context().Done():
			}
			b.mu.Lock()
			b.used += b.cost
			b.out--
			b.mu.Unlock()
			io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"pong"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		case "/backend-api/wham/usage":
			b.mu.Lock()
			used := b.used
			b.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus",
				"rate_limit": map[string]any{"allowed": true,
					"primary_window":   map[string]any{"used_percent": used, "limit_window_seconds": 18000, "reset_at": b.five},
					"secondary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 604800, "reset_at": b.week}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return b
}

// yalifesign on X: a Codex Plus account capped at 90%, four or five
// sessions at once, went to 100% of its five hours. Each session's turns
// come one after another, all of them at once on the one account; each
// turn spends its points as it ends, so a reading taken while others are
// under way doesn't have them yet. The cap holds all the same: what a
// turn spends is learned on the way up, the turns out are counted ahead
// of the reading, and near the cap a turn waits for them and for a
// reading that has them, so the account goes past its cap by one turn at
// most, never on to the vendor's own 100%.
func TestCapHoldsUnderParallelSessions(t *testing.T) {
	codexSignedIn(t)
	const start, cost, capAt = 60.0, 1.5, 90
	b := newSpendingBackend(t, start, cost, 40*time.Millisecond)
	nearCapAccounts(t, capAt, start)
	srv := New()
	const sessions = 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for range sessions {
		wg.Go(func() {
			for range 40 {
				code, _ := resetPost(t, srv)
				mu.Lock()
				codes[code]++
				mu.Unlock()
				if code != 200 {
					return
				}
			}
		})
	}
	wg.Wait()
	b.mu.Lock()
	used, turns := b.used, b.turns
	b.mu.Unlock()
	t.Logf("turns %d, codes %v, used %.1f%%", turns, codes, used)
	// the turn out as the account came to its cap may end past it; none
	// goes out once it is there
	if most := float64(capAt) + cost; used > most {
		t.Fatalf("used %.1f%% of a %d%% cap after %d turns (codes %v); want at most %.1f%%", used, capAt, turns, codes, most)
	}
	if codes[429] == 0 {
		t.Fatalf("codes %v; want the sessions held at the cap", codes)
	}
}

// What a turn spends is learned as the usage moves up over the turns come
// back, and counted ahead of the reading for the turns out and those come
// back since it last moved; a renewed window keeps what a turn spent.
func TestTurnSpendsAhead(t *testing.T) {
	var ts turnSpends
	// nothing learned yet, and no turn read since: a few points a turn
	if got := ts.ahead("a", 60, 0, 0, 3); got != 3*nearCapPoints {
		t.Fatalf("nothing learned yet: ahead %v; want %v", got, 3*nearCapPoints)
	}
	if got := ts.own("a"); got != 0 {
		t.Fatalf("nothing learned yet: own %v; want 0", got)
	}
	// two turns came back, the reading not moved: under a point between
	// them, still nothing learned
	if got := ts.ahead("a", 60, 2, 2, 3); got != 2.5 {
		t.Fatalf("ahead %v; want 5 turns at half a point", got)
	}
	// it moved 3 points over those two: 1.5 a turn, none since
	if got := ts.ahead("a", 63, 2, 2, 4); got != 6 {
		t.Fatalf("ahead %v; want 4 turns out at 1.5", got)
	}
	// one more came back unread, three out
	if got := ts.ahead("a", 63, 3, 2, 3); got != 6 {
		t.Fatalf("ahead %v; want 4 turns at 1.5", got)
	}
	if got := ts.own("a"); got != 1.5 {
		t.Fatalf("own %v; want 1.5", got)
	}
	// renewed: what a turn spends stands, counted from here
	if got := ts.ahead("a", 2, 3, 3, 1); got != 1.5 {
		t.Fatalf("ahead %v; want one turn at 1.5", got)
	}
	// a jump the vendor's own app made too is not taken as one turn's,
	// and holds a turn short of the cap by nearCapPoints at most
	if got := ts.ahead("a", 40, 4, 4, 1); got != maxTurnSpend {
		t.Fatalf("ahead %v; want %v", got, maxTurnSpend)
	}
	if got := ts.own("a"); got != nearCapPoints {
		t.Fatalf("own %v; want %v", got, nearCapPoints)
	}
	if got := ts.ahead("b", 50, 0, 0, 2); got != 2*nearCapPoints {
		t.Fatalf("another account: ahead %v; want %v", got, 2*nearCapPoints)
	}
	// #6958: five turns out as the reading was told came back after it.
	// Its 7.5 points are those five's before them, not ten's, and the
	// five come back since are in no reading yet
	ts.ahead("c", 60, 0, 0, 5)
	if got := ts.ahead("c", 67.5, 10, 5, 0); got != 7.5 {
		t.Fatalf("ahead %v; want the 5 turns no reading has, at 1.5", got)
	}
	// yalifesign on X again: turns don't spend alike, and one is taken
	// to spend the most one spent in the window
	ts.ahead("c", 75, 10, 10, 0)   // those five, at 1.5
	ts.ahead("c", 79, 11, 11, 0)   // one long turn: 4 points
	ts.ahead("c", 79.5, 12, 12, 0) // a short one: half a point
	if got := ts.own("c"); got != 4 {
		t.Fatalf("own %v; want the long turn's 4", got)
	}
	if got := ts.ahead("c", 79.5, 12, 12, 2); got != 8 {
		t.Fatalf("ahead %v; want 2 turns out at 4", got)
	}
	// renewed, the first turn in the new window counts alone
	ts.ahead("c", 1, 12, 12, 0)
	ts.ahead("c", 2, 13, 13, 0)
	if got := ts.own("c"); got != 1 {
		t.Fatalf("own %v; want the new window's 1", got)
	}
	// a reading taken again with five turns back moved 20 points, and
	// another session planned on it before its readThrough: the rise
	// waits for the five, not learned of none and lost
	ts.ahead("d", 60, 0, 0, 5)
	if got := ts.ahead("d", 80, 5, 0, 0); got != 5*nearCapPoints {
		t.Fatalf("ahead %v; want the 5 turns at %v", got, nearCapPoints)
	}
	ts.ahead("d", 80, 5, 5, 0)
	if got := ts.own("d"); got != 4 {
		t.Fatalf("own %v; want the five's 4 a turn", got)
	}
	// gakiclin1 on X: a point learned over one turn, then 29 turns read
	// that left a usage told in whole points where it was. They spent
	// under a point between them, and are in the reading: a point for
	// them, not 29, and a turn's point for each one no reading has
	ts.ahead("e", 40, 0, 0, 0)
	ts.ahead("e", 41, 1, 1, 0)
	for n := 2; n <= 30; n++ {
		ts.ahead("e", 41, n, n, 0)
	}
	if got := ts.ahead("e", 41, 30, 30, 0); got != 1 {
		t.Fatalf("ahead %v; want the point 29 turns read at 41%% spent at most", got)
	}
	if got := ts.ahead("e", 41, 32, 30, 1); got != 4 {
		t.Fatalf("ahead %v; want that point and 3 turns no reading has, at 1", got)
	}
}

// gakiclin1 on X: a Claude Code account capped at 70% was held at 41%,
// "its next turn would take it past its 70% cap: a turn there has spent
// up to 30 points". One turn had taken it from 40% to 41%, so a turn was
// taken to spend a point there, and the 29 after it came back and were
// read at 41%, a usage told in whole points that a big plan's turns move
// one at a time. Each was counted ahead of the reading as though none had
// it, at that point, and the account was held 29 points short of its
// cap. It is held a turn short of it: its point, and the point a reading
// in whole points may be under what was spent.
func TestNearCapHeldByTurnsNotRead(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	used := 40.0
	old := allowances
	t.Cleanup(func() { allowances = old })
	five, week := time.Now().Add(3*time.Hour), time.Now().Add(4*24*time.Hour)
	allowances = func(string) map[string]provider.Allowance {
		mu.Lock()
		defer mu.Unlock()
		return map[string]provider.Allowance{"me@example.com": {
			{Used: used, Resets: five, Span: 5 * time.Hour},
			{Used: 10, Resets: week, Span: 7 * 24 * time.Hour},
		}}
	}
	c := candidate{p: provider.Provider{ID: "claude", AccountCaps: map[string]int{"me@example.com": 70},
		Account: &provider.Account{Agent: "claude", User: "me@example.com"}}, model: "claude-sonnet-5", rest: "claude"}
	srv := New()
	who := c.who()
	// at reading, the turns come back, read of them in the latest reading,
	// checked as the next turn is routed
	at := func(reading float64, back, read int) (ahead float64, near bool) {
		mu.Lock()
		used = reading
		mu.Unlock()
		srv.lanes.mu.Lock()
		if srv.lanes.turns == nil {
			srv.lanes.turns = map[string]int{}
		}
		srv.lanes.turns[who] = back
		srv.lanes.mu.Unlock()
		srv.lanes.readThrough(who, read)
		_, ahead, near = srv.nearCapOf(c, time.Now())
		return ahead, near
	}
	at(40, 0, 0)
	at(41, 1, 1) // one turn, a point
	for n := 2; n <= 30; n++ {
		at(41, n, n)
	}
	if ahead, near := at(41, 30, 30); ahead > 1 || near {
		t.Fatalf("29 turns read at 41%%: %v points ahead, near %v; want 1 at most, not near a 70%% cap", ahead, near)
	}
	if h := srv.nearCapHeld(c, time.Now()); h != nil {
		t.Fatalf("held at %v%% of a %d%% cap by %v points; want let in", h.used, h.cap, h.turn)
	}
	// read at 69%, and a turn back since that no reading has: its point
	// and the next turn's would pass the cap
	at(69, 32, 31)
	h := srv.nearCapHeld(c, time.Now())
	if h == nil || h.turn != 2 || h.ahead != 1 {
		t.Fatalf("at 69%% of a 70%% cap: held %+v; want held by 2 points, 1 of them the turn no reading has", h)
	}
	// the reason tells a turn's own spend apart from the turns no reading
	// has yet, not their sum as one turn's
	why, _ := heldReasons([]Weighed{
		{Name: "Claude", Who: "me@example.com", Used: 69, Capped: 70, CapTurn: 1},
		{Name: "Claude", Who: "you@example.com", Used: 60, Capped: 70, CapTurn: 12, CapAhead: 8},
	}, time.Now())
	for i, want := range []string{
		"its next turn would take it past its 70% cap: a turn there has spent up to 1 point",
		"a turn there has spent up to 4 points, and turns its last reading doesn't have yet are taken to spend 8 points",
	} {
		if i >= len(why) || !strings.Contains(why[i], want) {
			t.Fatalf("reasons %q; want %q", why, want)
		}
	}
}

// A turn near its cap waits for the turns out on its account, not for a
// slot its own request already holds (handed it from another's queue).
func TestLaneIdleLeavesItsOwnSlot(t *testing.T) {
	var l lanes
	mine, ok := l.tryTake("acct", 0)
	if !ok {
		t.Fatal("no slot")
	}
	defer mine()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.idle(ctx, "acct", 1); err != nil {
		t.Fatalf("waited on its own slot: %v", err)
	}
	other, _ := l.tryTake("acct", 0)
	done := make(chan error, 1)
	go func() { done <- l.idle(ctx, "acct", 1) }()
	// it waits for a slot to come back: the channel it waits on is made
	waitFor(t, "idle waiting", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.freed != nil
	})
	select {
	case err := <-done:
		t.Fatalf("went with another turn out: %v", err)
	default:
	}
	other()
	if err := <-done; err != nil {
		t.Fatalf("still waiting once the other came back: %v", err)
	}
	if _, ended, turns := l.out("acct"); ended.IsZero() || turns != 1 {
		t.Fatalf("ended %v, turns %d; want the other's return noted", ended, turns)
	}
}

// #6958: the same five sessions, with each try's pause after its near-cap
// check widened, as a busy machine widens it (ubuntu CI: 93% of a 90%
// cap). A turn found not near is counted out on the account from its
// check, before its try takes its lane, and the reading the account is
// checked by has only the turns come back before the reply that told it
// began, not those of the round under way then.
func TestCapHoldsWithAPauseAfterTheCheck(t *testing.T) {
	nearChecked = func(bool) { time.Sleep(15 * time.Millisecond) }
	t.Cleanup(func() { nearChecked = nil })
	TestCapHoldsUnderParallelSessions(t)
}
