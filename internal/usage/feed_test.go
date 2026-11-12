package usage

import (
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// rdsaltbo on X (#6060): a plugin keeps its own ledger of magpie's requests
// and reads them again and again. The feed has to hand it exactly what is
// new, mended or gone since the cursor it kept.

func feedTest(t *testing.T) time.Time {
	t.Helper()
	resetUsageTest(t)
	now := holdClock(t, time.Now().Truncate(time.Second))
	ResetFeedForTest()
	t.Cleanup(ResetFeedForTest)
	old := LogCalls
	t.Cleanup(func() { LogCalls = old })
	LogCalls = func(time.Time) []sessions.Call { return nil }
	return now
}

// read forgets the memo of the last read, as the next poll minutes later does
func feedRead(t *testing.T, cursor string, limit int) FeedPage {
	t.Helper()
	feed.Lock()
	feed.memo = nil
	feed.Unlock()
	p, err := FeedRead(FeedRequest{Cursor: cursor, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func idsOf(p FeedPage) (live, gone []string) {
	for _, it := range p.Items {
		if it.Gone {
			gone = append(gone, it.ID)
		} else {
			live = append(live, it.ID)
		}
	}
	return
}

func TestFeedResumesWithExactlyTheNewRows(t *testing.T) {
	now := feedTest(t)
	for i := range 3 {
		Append(Record{Time: now.Add(-time.Duration(10-i) * time.Minute), Agent: "codex", Provider: "p", Model: "m", Input: 10 + i, Status: 200, Session: "s"})
	}
	first := feedRead(t, FeedStart, 2)
	if len(first.Items) != 2 || !first.More {
		t.Fatalf("first page: %d items, more %v", len(first.Items), first.More)
	}
	second := feedRead(t, first.Next, 2)
	if len(second.Items) != 1 || second.More {
		t.Fatalf("second page: %d items, more %v", len(second.Items), second.More)
	}
	seen := map[string]bool{}
	for _, p := range []FeedPage{first, second} {
		live, _ := idsOf(p)
		for _, id := range live {
			seen[id] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("three ids expected, got %v", seen)
	}
	// replaying the second page's cursor again gives the same nothing
	if again := feedRead(t, first.Next, 2); len(again.Items) != 1 || again.Items[0].ID != second.Items[0].ID {
		t.Fatalf("a replay of a page that was not committed: %+v", again.Items)
	}

	// rows arrive: two from the gateway, and a late one read from an
	// agent's file with a timestamp older than every row above
	Append(Record{Time: now.Add(-time.Minute), Agent: "codex", Provider: "p", Model: "m", Input: 5, Status: 200, Session: "s"})
	Append(Record{Time: now, Agent: "codex", Provider: "p", Model: "m", Input: 6, Status: 200, Session: "s"})
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{{Time: now.Add(-24 * time.Hour), Agent: "claude", Session: "old", Model: "m", RequestID: "req_late", Msg: "msg_late", Tokens: sessions.Tokens{Input: 1, Output: 1}}}
	}
	third := feedRead(t, second.Next, 100)
	live, gone := idsOf(third)
	if len(live) != 3 || len(gone) != 0 || third.More {
		t.Fatalf("exactly the 3 new rows expected: live %d gone %d more %v", len(live), len(gone), third.More)
	}
	for _, id := range live {
		if seen[id] {
			t.Fatalf("%s was delivered before", id)
		}
	}
	if none := feedRead(t, third.Next, 100); len(none.Items) != 0 || none.More {
		t.Fatalf("nothing new, got %+v", none.Items)
	}
}

func TestFeedIdsAndCursorsSurviveARestart(t *testing.T) {
	now := feedTest(t)
	Append(Record{Time: now.Add(-time.Hour), Agent: "codex", Provider: "p", Model: "m", Input: 10, Status: 200, Session: "s", RequestID: "r1"})
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{{Time: now.Add(-2 * time.Hour), Agent: "claude", Session: "c", Model: "m", RequestID: "req_1", Msg: "msg_1", Tokens: sessions.Tokens{Input: 3}}}
	}
	p := feedRead(t, FeedStart, 100)
	before, _ := idsOf(p)
	if len(before) != 2 {
		t.Fatalf("two rows expected, got %d", len(before))
	}
	ResetFeedForTest() // a restart: the journal is read again
	after, _ := idsOf(feedRead(t, FeedStart, 100))
	slices.Sort(before)
	slices.Sort(after)
	if !slices.Equal(before, after) {
		t.Fatalf("ids changed over a restart: %v then %v", before, after)
	}
	// the cursor from before the restart still resumes, and has nothing new
	if q, err := FeedRead(FeedRequest{Cursor: p.Next, Limit: 100}); err != nil || len(q.Items) != 0 {
		t.Fatalf("resume after restart: %v %+v", err, q.Items)
	}
	// and a row that arrives after the restart is the only one told
	Append(Record{Time: now, Agent: "codex", Provider: "p", Model: "m", Input: 1, Status: 200, Session: "s", RequestID: "r2"})
	q := feedRead(t, p.Next, 100)
	if live, _ := idsOf(q); len(live) != 1 || slices.Contains(before, live[0]) {
		t.Fatalf("one new row expected: %+v", q.Items)
	}
}

func TestFeedIdSurvivesACorrectionAndTellsIt(t *testing.T) {
	now := feedTest(t)
	call := sessions.Call{Time: now.Add(-time.Hour), Agent: "claude", Session: "c", Model: "m", RequestID: "req_1", Msg: "msg_1", Tokens: sessions.Tokens{Input: 3, Output: 1}}
	LogCalls = func(time.Time) []sessions.Call { return []sessions.Call{call} }
	p := feedRead(t, FeedStart, 100)
	if len(p.Items) != 1 {
		t.Fatalf("%+v", p.Items)
	}
	id := p.Items[0].ID
	if p.Items[0].Row.MessageID != "msg_1" || p.Items[0].Row.RequestID != "req_1" {
		t.Fatalf("the ids the file gave: %+v", p.Items[0].Row.Record)
	}
	// the agent's file mends the call's tokens: the same id, again
	call.Output = 40
	q := feedRead(t, p.Next, 100)
	if len(q.Items) != 1 || q.Items[0].ID != id || q.Items[0].Gone || q.Items[0].Row.Output != 40 || q.Items[0].Seq <= p.Items[0].Seq {
		t.Fatalf("a mended row is the same id, newer: %+v", q.Items)
	}
	if r := feedRead(t, q.Next, 100); len(r.Items) != 0 {
		t.Fatalf("unchanged: %+v", r.Items)
	}
}

func TestFeedGatewayTakingOverACallRetiresTheNativeRow(t *testing.T) {
	now := feedTest(t)
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{{Time: now.Add(-10 * time.Minute), Agent: "claude", Session: "s1", Model: "m", RequestID: "req_gw", Msg: "msg_1", Tokens: sessions.Tokens{Input: 5, Output: 5}}}
	}
	p := feedRead(t, FeedStart, 100)
	if len(p.Items) != 1 || p.Items[0].Row.Source != "log" {
		t.Fatalf("the native row alone: %+v", p.Items)
	}
	native := p.Items[0].ID
	// the gateway's own record of the same request is read later
	Append(Record{Time: now.Add(-10*time.Minute + 2*time.Second), Agent: "claude", Provider: "p", Model: "m", Input: 5, Output: 5, Status: 200, Session: "s1", RequestID: "req_gw", Millis: 900})
	q := feedRead(t, p.Next, 100)
	live, gone := idsOf(q)
	if len(live) != 1 || live[0] == native || !slices.Equal(gone, []string{native}) {
		t.Fatalf("one row replaces the native one: live %v gone %v", live, gone)
	}
	// from the start, the retired id is not listed at all
	all, _ := idsOf(feedRead(t, FeedStart, 100))
	if len(all) != 1 || all[0] != live[0] {
		t.Fatalf("the ledger has the one row: %v", all)
	}
}

func TestFeedRefusesACursorItCannotResume(t *testing.T) {
	now := feedTest(t)
	Append(Record{Time: now.Add(-time.Hour), Agent: "codex", Provider: "p", Model: "m", Input: 1, Status: 200, Session: "s"})
	p := feedRead(t, FeedStart, 100)
	code := func(req FeedRequest) string {
		_, err := FeedRead(req)
		var fe *FeedError
		if errors.As(err, &fe) {
			return fe.Code
		}
		if err != nil {
			t.Fatal(err)
		}
		return ""
	}
	feed.Lock()
	feed.memo = nil
	feed.Unlock()
	if c := code(FeedRequest{Cursor: "garbage"}); c != "cursor_invalid" {
		t.Errorf("garbage: %q", c)
	}
	if c := code(FeedRequest{Cursor: p.Next, Filter: Filter{Agent: "claude"}}); c != "cursor_filter_changed" {
		t.Errorf("other filters: %q", c)
	}
	if c := code(FeedRequest{Cursor: p.Next, Key: "k1"}); c != "cursor_filter_changed" {
		t.Errorf("other key: %q", c)
	}
	// the journal is lost: another feed
	ResetFeedForTest()
	if err := os.Remove(FeedPath()); err != nil {
		t.Fatal(err)
	}
	if c := code(FeedRequest{Cursor: p.Next}); c != "cursor_reset" {
		t.Errorf("a lost journal: %q", c)
	}
}

func TestFeedExpiresACursorOlderThanItsTombstones(t *testing.T) {
	now := feedTest(t)
	old := FeedKeep
	FeedKeep = time.Hour
	t.Cleanup(func() { FeedKeep = old })
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{{Time: now.Add(-10 * time.Minute), Agent: "claude", Session: "s1", Model: "m", RequestID: "req_gw", Tokens: sessions.Tokens{Input: 5}}}
	}
	p := feedRead(t, FeedStart, 100) // a client reads the one row, then goes away
	LogCalls = func(time.Time) []sessions.Call { return nil }
	feedRead(t, FeedNow, 1) // the row goes
	holdClock(t, now.Add(3*time.Hour))
	feed.Lock()
	feed.memo = nil
	feed.Unlock()
	_, err := FeedRead(FeedRequest{Cursor: p.Next})
	var fe *FeedError
	if !errors.As(err, &fe) || fe.Code != "cursor_expired" {
		t.Fatalf("a cursor from before the pruned tombstone must be refused, not skipped over: %v", err)
	}
	// from the start works, and so does a fresh "now"
	if _, err := FeedRead(FeedRequest{Cursor: FeedStart}); err != nil {
		t.Fatal(err)
	}
}

func TestFeedNowReadsOnlyWhatComesAfter(t *testing.T) {
	now := feedTest(t)
	Append(Record{Time: now.Add(-time.Hour), Agent: "codex", Provider: "p", Model: "m", Input: 1, Status: 200, Session: "s"})
	p := feedRead(t, FeedNow, 100)
	if len(p.Items) != 0 || p.Next == "" {
		t.Fatalf("now has no rows: %+v", p)
	}
	Append(Record{Time: now, Agent: "codex", Provider: "p", Model: "m", Input: 2, Status: 200, Session: "s"})
	if q := feedRead(t, p.Next, 100); len(q.Items) != 1 || q.Items[0].Row.Input != 2 {
		t.Fatalf("%+v", q.Items)
	}
}

func TestFeedSameRecordTwiceKeepsTwoIds(t *testing.T) {
	now := feedTest(t)
	r := Record{Time: now.Add(-time.Hour), Agent: "codex", Provider: "p", Model: "m", Input: 1, Status: 200, Session: "s", Millis: 5}
	Append(r)
	Append(r)
	live, _ := idsOf(feedRead(t, FeedStart, 100))
	if len(live) != 2 || live[0] == live[1] {
		t.Fatalf("two identical lines are two calls: %v", live)
	}
}
