package provider

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// #4532: a saved Claude account not needed for weeks (switch-only, or
// switched off as a reserve) had nothing run on it, and its refresh token
// sat idle until it was found signed out. The keep-alive has Claude Code
// itself run on each, in its own config directory, and keeps what Claude
// Code made of the sign-in; magpie asks Anthropic nothing (fakeClaudeUsage
// fails a test that does).
func TestRenewLoginsKeepsSavedClaudeSignedIn(t *testing.T) {
	claudeThree(t) // team@ and max@ saved, Claude Code on pro@
	keepAliveClean(t)
	var text atomic.Value
	text.Store(resetsAhead("Current week (all models): 1% used · resets Nov 6 at 11am (Asia/Dubai)"))
	fakeClaudeUsage(t, &text, nil)
	var mu sync.Mutex
	var dirs []string
	var fail atomic.Value
	fail.Store("")
	rotate := atomic.Bool{}
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		mu.Lock()
		dirs = append(dirs, dir)
		mu.Unlock()
		if msg := fail.Load().(string); msg != "" {
			return "", errors.New(msg)
		}
		if rotate.Load() {
			// as Claude Code refreshes a sign-in that is due
			writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "tok-2", "refreshToken": "r2-" + dir,
				"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}})
		}
		return text.Load().(string), nil
	})
	runs := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(dirs)
		dirs = nil
		slices.Sort(out)
		return out
	}
	max, team := "max@x.com", "team@x.com · Acme"
	// max@ on behind Claude Code's own; team@ left off, kept for later
	if err := SetLoginOn("claude", max, true); err != nil {
		t.Fatal(err)
	}
	both := []string{claudeAccountDir(max), claudeAccountDir(team)}
	slices.Sort(both)
	saved := func(user string) savedLogin {
		for _, l := range readLogins() {
			if l.Agent == "claude" && l.User == user {
				return l
			}
		}
		t.Fatalf("%s isn't saved", user)
		return savedLogin{}
	}
	of := func(rs []Renewal) map[string]Renewal {
		m := map[string]Renewal{}
		for _, r := range rs {
			m[r.User] = r
		}
		if len(m) != len(rs) {
			t.Fatalf("an account twice: %+v", rs)
		}
		return m
	}
	ctx := context.Background()

	// seen in Claude Code's hands a moment ago: not due, nothing run
	if rs := of(RenewLogins(ctx, keepAliveEvery)); len(rs) != 2 || rs[max].Renewed || rs[team].Renewed || len(runs()) != 0 {
		t.Fatalf("renewed what wasn't due: %+v", rs)
	}

	// due: Claude Code runs once in each saved account's own directory,
	// the one switched off too; pro@, Claude Code's own, is left alone
	rotate.Store(true)
	rs := of(RenewLogins(ctx, 0))
	for _, u := range []string{max, team} {
		if r := rs[u]; r.Agent != "claude" || !r.Renewed || r.Err != "" || r.Lapsed {
			t.Fatalf("renew %s: %+v", u, rs)
		}
	}
	if len(rs) != 2 {
		t.Fatalf("renewed: %+v", rs)
	}
	if got := runs(); !slices.Equal(got, both) {
		t.Fatalf("Claude Code ran in %q, want %q", got, both)
	}
	for _, u := range []string{max, team} {
		l := saved(u)
		if c, _ := parseClaudeCredentials(l.Auth); c.OAuth.RefreshToken != "r2-"+claudeAccountDir(u) {
			t.Fatalf("the sign-in Claude Code refreshed for %s isn't kept: %+v", u, c.OAuth)
		}
		if time.Since(l.Renewed) > time.Minute || renewalDue(l, keepAliveEvery, time.Now()) || !renewalDue(l, keepAliveEvery, time.Now().Add(keepAliveEvery+time.Hour)) {
			t.Fatalf("%s renewed at %v", u, l.Renewed)
		}
	}
	if saved(team).On {
		t.Fatal("the keep-alive switched team@ on")
	}
	if c, _ := parseClaudeCredentials(saved("pro@x.com").Auth); c.OAuth.RefreshToken != "r-pro@x.com" {
		t.Fatalf("Claude Code's own account changed: %+v", c.OAuth)
	}

	// a sign-in Claude Code needn't refresh yet: the run is the renewal,
	// so the account isn't run again every hour
	rotate.Store(false)
	if rs := of(RenewLogins(ctx, 0)); !rs[max].Renewed || !rs[team].Renewed || len(runs()) != 2 {
		t.Fatalf("a live sign-in: %+v", rs)
	}
	if rs := of(RenewLogins(ctx, keepAliveEvery)); rs[max].Renewed || rs[team].Renewed || len(runs()) != 0 {
		t.Fatalf("run again within its eight days: %+v", rs)
	}

	// no answer: it may yet renew, so nothing is marked
	fail.Store("Claude Code: network is unreachable")
	if rs := of(RenewLogins(ctx, 0)); rs[max].Err == "" || rs[max].Lapsed || rs[max].Renewed || saved(max).Lapsed != "" {
		t.Fatalf("a network failure lapsed it: %+v", rs)
	}
	runs()

	// refused: the account's row says to sign in again, as a request's
	// refusal does, and the keep-alive runs nothing on it after
	fail.Store("Claude Code: OAuth session expired and could not be refreshed. Please run /login.")
	rs = of(RenewLogins(ctx, 0))
	if r := rs[max]; !r.Lapsed || r.Renewed || !strings.Contains(r.Err, "sign in again") {
		t.Fatalf("refused: %+v", rs)
	}
	for _, lg := range Logins("claude") {
		if (lg.User == max || lg.User == team) != (lg.Lapsed != "") {
			t.Fatalf("lapsed: %+v", lg)
		}
	}
	runs()
	if rs := RenewLogins(ctx, 0); len(rs) != 0 || len(runs()) != 0 {
		t.Fatalf("a signed-out account was run again: %+v", rs)
	}
}

// resetsAhead moves a /usage text's "resets Nov 6 at 11am" three days past
// now: these tests read at the real time, and once that hour went by the
// week read as reset, Used 0 (#5481).
func resetsAhead(text string) string {
	dubai, err := time.LoadLocation("Asia/Dubai")
	if err != nil {
		panic(err)
	}
	return strings.ReplaceAll(text, "Nov 6 at 11am", time.Now().In(dubai).AddDate(0, 0, 3).Format("Jan 2 at 3pm"))
}

// keepAliveClean has the test start, and leave, with no renewal on record
// as failed (renewWaiting) and no reading kept from another test.
func keepAliveClean(t *testing.T) {
	t.Helper()
	clean := func() {
		renewFailed.Lock()
		renewFailed.m = nil
		renewFailed.Unlock()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		subscriptionUsageCache.Lock()
		subscriptionUsageCache.data = nil
		subscriptionUsageCache.Unlock()
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	clean()
	t.Cleanup(clean)
}

// #4532: under switch-only the keep-alive's run is the one reading a saved
// account gets, so it is kept as the account's reading, as a refresh of
// its card keeps one: beside the account, on its card, and on disk.
func TestRenewLoginsKeepsClaudeReading(t *testing.T) {
	claudeThree(t)
	keepAliveClean(t)
	var text atomic.Value
	text.Store(resetsAhead(claudeWeeklyOnly))
	fakeClaudeUsage(t, &text, nil)
	runs := atomic.Int32{}
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		runs.Add(1)
		return strings.Replace(resetsAhead(claudeWeeklyOnly), "(all models): 1%", "(all models): 37%", 1), nil
	})
	max := "max@x.com"
	var l Login
	for _, x := range Logins("claude") {
		if x.User == max {
			l = x
		}
	}
	// the Usage page has read max@'s card before, with nothing then
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.data = []SubscriptionQuota{{Provider: "claude", User: max, Name: "Claude Code", Windows: []QuotaWindow{}, Error: errClaudeSaved.Error()}}
	subscriptionUsageCache.Unlock()

	rs := RenewLogins(context.Background(), 0)
	if runs.Load() != 2 {
		t.Fatalf("%d runs: %+v", runs.Load(), rs)
	}
	used := func(ws []QuotaWindow) float64 {
		for _, w := range ws {
			if w.Name == "7 days" {
				return w.Used
			}
		}
		return -1
	}
	// beside the account (LoginUsage, the switch, warm-up)
	e, ok := loginReadingKnown(l)
	if !ok || used(e.read.Windows) != 37 || e.read.Error != "" || e.read.ReadAt == nil {
		t.Fatalf("the account's reading: %v %+v", ok, e.read)
	}
	// as Claude Code told it, for the next look that reads none
	if at := claudeReadAt(max); at == nil || time.Since(*at) > time.Minute {
		t.Fatalf("read at %v", at)
	}
	// on its card
	subscriptionUsageCache.Lock()
	cards := slices.Clone(subscriptionUsageCache.data)
	subscriptionUsageCache.Unlock()
	if len(cards) != 1 || used(cards[0].Windows) != 37 || cards[0].Error != "" || cards[0].User != max {
		t.Fatalf("the card: %+v", cards)
	}
	// and on disk, dated, for a magpie started after
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	q := keepLast(SubscriptionQuota{Provider: "claude", Windows: []QuotaWindow{}, Error: errClaudeSaved.Error()}, max)
	if used(q.Windows) != 37 {
		t.Fatalf("kept on disk: %+v", q)
	}
}

// #4532: a renewal that fails before anything is refreshed (the network,
// a proxy, Claude Code not runnable) left the account due, and with one
// renewal a look it took every look: the accounts after it never had
// theirs. One that failed waits keepAliveRetry, the others going first.
func TestRenewLoginsFailedWaitsItsTurn(t *testing.T) {
	claudeThree(t)
	keepAliveClean(t)
	var text atomic.Value
	text.Store("Current week (all models): 1% used")
	fakeClaudeUsage(t, &text, nil)
	max, team := "max@x.com", "team@x.com · Acme"
	// both due: last seen and renewed a month ago
	loginsMu.Lock()
	ls := readLogins()
	order := []string{}
	for i := range ls {
		if ls[i].Agent == "claude" && (ls[i].User == max || ls[i].User == team) {
			ls[i].Seen, ls[i].Renewed = time.Now().Add(-30*24*time.Hour), time.Now().Add(-30*24*time.Hour)
			order = append(order, ls[i].User)
		}
	}
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if len(order) != 2 {
		t.Fatalf("saved: %v", order)
	}
	first, second := order[0], order[1]
	var mu sync.Mutex
	var ran []string
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, dir)
		if dir == claudeAccountDir(first) {
			return "", errors.New("Claude Code: connect ECONNREFUSED 127.0.0.1:7890")
		}
		return text.Load().(string), nil
	})
	took := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := ran
		ran = nil
		return out
	}
	look := func() map[string]Renewal {
		m := map[string]Renewal{}
		for _, r := range renewLogins(context.Background(), keepAliveEvery, keepAliveAtOnce) {
			m[r.User] = r
		}
		return m
	}
	// the first look: the first account's turn, and it fails
	if rs := look(); rs[first].Err == "" || rs[first].Lapsed || rs[second].Renewed {
		t.Fatalf("look 1: %+v", rs)
	}
	if got := took(); !slices.Equal(got, []string{claudeAccountDir(first)}) {
		t.Fatalf("look 1 ran %q", got)
	}
	// the next: the second's turn, not the failed one's again
	if rs := look(); !rs[second].Renewed {
		t.Fatalf("look 2: %+v", rs)
	}
	if got := took(); !slices.Equal(got, []string{claudeAccountDir(second)}) {
		t.Fatalf("look 2 ran %q", got)
	}
	// the one after: nothing due but the failed one, which waits
	if rs := look(); rs[first].Renewed || rs[first].Err != "" || len(took()) != 0 {
		t.Fatalf("look 3: %+v", rs)
	}
	// past keepAliveRetry it is tried again
	renewFailed.Lock()
	for k, at := range renewFailed.m {
		renewFailed.m[k] = at.Add(-keepAliveRetry - time.Minute)
	}
	renewFailed.Unlock()
	if rs := look(); rs[first].Err == "" {
		t.Fatalf("look 4: %+v", rs)
	}
	if got := took(); !slices.Equal(got, []string{claudeAccountDir(first)}) {
		t.Fatalf("look 4 ran %q", got)
	}
	// `magpie accounts refresh` tries it at once, waiting or not
	if rs := RenewLogins(context.Background(), 0); len(rs) != 2 {
		t.Fatalf("refresh: %+v", rs)
	}
	if got := took(); !slices.Contains(got, claudeAccountDir(first)) {
		t.Fatalf("refresh ran %q", got)
	}
}

// Claude Code empties the sign-in in its directory when Anthropic refuses
// its refresh, and says no more of it: that too is the account signed out.
func TestRenewLoginsClaudeEmptiedSignIn(t *testing.T) {
	claudeThree(t)
	keepAliveClean(t)
	var text atomic.Value
	text.Store("Current week (all models): 1% used")
	fakeClaudeUsage(t, &text, nil)
	max := "max@x.com"
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "", "refreshToken": "", "expiresAt": 0}})
		return "", errors.New("Claude Code: Not logged in · Please run /login")
	})
	if err := SetLoginOn("claude", max, true); err != nil {
		t.Fatal(err)
	}
	rs := RenewLogins(context.Background(), 0)
	n := 0
	for _, r := range rs {
		if r.User == max {
			n++
			if !r.Lapsed || r.Renewed {
				t.Fatalf("emptied: %+v", rs)
			}
		}
	}
	if n != 1 {
		t.Fatalf("emptied: %+v", rs)
	}
	for _, lg := range Logins("claude") {
		if lg.User == max && lg.Lapsed == "" {
			t.Fatalf("not marked: %+v", lg)
		}
	}
}

// A Claude account added by setup-token has no refresh token to keep;
// one signed in, on or off, has.
func TestRenewLoginsLeavesClaudeSetupToken(t *testing.T) {
	l := savedLogin{Agent: "claude", User: "ci@x.com", On: true, Auth: []byte(`{"setupToken":"sk-ant-oat01-x"}`)}
	if claudeKeptAlive(l) {
		t.Fatal("a setup-token account is kept alive")
	}
	signedIn := []byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r"}}`)
	if !claudeKeptAlive(savedLogin{Agent: "claude", User: "x", Auth: signedIn}) {
		t.Fatal("an account switched off isn't kept alive")
	}
	if !claudeKeptAlive(savedLogin{Agent: "claude", User: "x", On: true, Auth: signedIn}) {
		t.Fatal("an account switched on isn't kept alive")
	}
}
