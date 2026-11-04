package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// pluginCheckinSandbox is the fake plugin with its own check-in, signed in
// with each key; it says the file each press is written to.
func pluginCheckinSandbox(t *testing.T, keys ...string) string {
	t.Helper()
	bun := testenv.Bun(t)
	home := claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	presses := filepath.Join(home, "presses")
	t.Setenv("FAKE_CHECKIN", presses)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, k); err != nil {
			t.Fatal(err)
		}
	}
	return presses
}

// pressed is the keys the plugin's check-in was pressed for, in order.
func pressed(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// A plugin's own check-in (auth.checkin; Lemon on Discord) runs as magpie's
// own do: each account once a Beijing day while it is on, a failure asked
// again after wbCheckinRetry, a captcha left for the user (never retried
// that day), and what came of each kept, on its card and in Settings.
func TestPluginCheckinRunsAsTheBuiltinsDo(t *testing.T) {
	keys := []string{"ck-claim", "ck-done", "ck-captcha", "ck-throw", "ck-odd"}
	presses := pluginCheckinSandbox(t, keys...)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	ps := pluginCheckinProviders()
	if len(ps) != 1 || ps[0].ID != "fakeco" || ps[0].Name != "FakeCo" || len(ps[0].accounts) != len(keys) {
		t.Fatalf("the plugins that check in: %+v", ps)
	}
	if !HasPluginCheckin() {
		t.Fatal("HasPluginCheckin is false")
	}

	now := time.Date(2026, 10, 6, 10, 0, 0, 0, beijing)
	loop := pluginCheckiner{now: func() time.Time { return now }, only: func(id string) bool { return PluginCheckinOn(settings.Load(), id) }}

	// off (as it is by default): nothing pressed
	if rs := loop.checkinNow(ctx, false); len(rs) != 0 || len(pressed(t, presses)) != 0 {
		t.Fatalf("while off: %+v, pressed %q", rs, pressed(t, presses))
	}
	s := settings.Load()
	s.PluginCheckins = map[string]bool{"fakeco": true}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	rs := loop.checkinNow(ctx, false)
	if len(rs) != len(keys) {
		t.Fatalf("on: %d answers, want %d: %+v", len(rs), len(keys), rs)
	}
	// by what each answered: one account per key
	got := map[string]WorkBuddyCheckin{}
	for _, r := range rs {
		if r.By != "plugin:fakeco" || r.Vendor != "FakeCo" || !r.Asked || r.Day != "2026-10-06" {
			t.Fatalf("an answer: %+v", r)
		}
		k := r.Outcome
		if k == CheckinFailed {
			k += ":" + r.Msg
		}
		got[k] = r
	}
	if r := got[CheckinClaimed]; r.Credit != 50 || r.Streak != 3 {
		t.Fatalf("ck-claim: %+v (%+v)", r, rs)
	}
	if r := got[CheckinDone]; r.Credit != 50 {
		t.Fatalf("ck-done: %+v (%+v)", r, rs)
	}
	if r := got[CheckinCaptcha]; r.Msg != "slide the puzzle" {
		t.Fatalf("ck-captcha: %+v (%+v)", r, rs)
	}
	failed := 0
	for k := range got {
		if strings.Contains(k, "check-in is down") || strings.Contains(k, "the plugin answered") {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("the failures (ck-throw, ck-odd): %+v", rs)
	}
	// an outcome magpie doesn't know keeps the plugin's message, marked as
	// the plugin's own word, so "failed" can't read as contradicting it (#3650)
	if _, ok := got[CheckinFailed+`:the plugin answered "yes": the daily credits were refreshed`]; !ok {
		t.Fatalf("ck-odd's message: %+v", rs)
	}
	if p := pressed(t, presses); len(p) != len(keys) {
		t.Fatalf("pressed %q", p)
	}

	// a minute on: nothing asked again, each answered as kept
	now = now.Add(time.Minute)
	if rs := loop.checkinNow(ctx, false); len(rs) != len(keys) || len(pressed(t, presses)) != len(keys) {
		t.Fatalf("a minute on: %+v, pressed %q", rs, pressed(t, presses))
	}
	// past wbCheckinRetry: the failures, and the done given before noon
	// (#3326), not the captcha
	now = now.Add(wbCheckinRetry)
	loop.checkinNow(ctx, false)
	if p := pressed(t, presses)[len(keys):]; strings.Join(slices.Sorted(slices.Values(p)), ",") != "ck-done,ck-odd,ck-throw" {
		t.Fatalf("past the retry, pressed again %q", p)
	}
	// the next Beijing day: each again
	now = time.Date(2026, 10, 7, 0, 5, 0, 0, beijing)
	loop.checkinNow(ctx, false)
	if p := pressed(t, presses); len(p) != 2*len(keys)+3 {
		t.Fatalf("the next day, pressed %q", p)
	}

	// kept, for Settings
	pcs := PluginCheckins()
	if len(pcs) != 1 || pcs[0].ID != "fakeco" || !pcs[0].On || len(pcs[0].Checkins) != len(keys) {
		t.Fatalf("PluginCheckins = %+v", pcs)
	}
	// and on each account's card
	var qs []SubscriptionQuota
	for _, a := range ps[0].accounts {
		qs = append(qs, SubscriptionQuota{Provider: PluginID("fakeco"), User: a.User})
	}
	marked := WithCheckins(qs)
	for i, q := range marked {
		if !q.Checkins || q.CheckinBy != "plugin:fakeco" || q.Checkin == nil || q.Checkin.Day != "2026-10-07" {
			t.Fatalf("card %d: %+v %+v", i, q, q.Checkin)
		}
	}
	if qs[0].Checkins {
		t.Fatal("WithCheckins changed the cards it was given")
	}
	// a press now (Check in now, the CLI) asks the failure again at once,
	// whatever the switch; another provider's press asks none of these
	before := len(pressed(t, presses))
	if rs := CheckInPlugins(ctx, "someone-else"); len(rs) != 0 || len(pressed(t, presses)) != before {
		t.Fatalf("another provider's press: %+v", rs)
	}
}

// A plugin that checks in itself takes over from magpie's own check-in
// through its fetch, so an account isn't checked in twice; and its switch
// follows the vendor's, which it took over, until set on its own.
func TestPluginCheckinTakesOverTheVendors(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_ID", "qoder")
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "qoder", 0, nil, "ck-claim", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	// a plugin without auth.checkin: magpie claims Qoder's credits itself
	if n := len(qoderCheckinAccounts()); n != 1 || HasPluginCheckin() {
		t.Fatalf("without the hook: %d Qoder accounts, plugins that check in %v", n, HasPluginCheckin())
	}
	// with it, the plugin does, and magpie's own lists none
	t.Setenv("FAKE_CHECKIN", filepath.Join(t.TempDir(), "presses"))
	plugin.Restart()
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(qoderCheckinAccounts()); n != 0 || !HasPluginCheckin() {
		t.Fatalf("with the hook: %d Qoder accounts, plugins that check in %v", n, HasPluginCheckin())
	}
	for _, c := range []struct {
		s    settings.Settings
		id   string
		want bool
	}{
		{settings.Settings{QoderCheckin: true}, "qoder", true},
		{settings.Settings{QoderCheckin: true}, QoderCNID, true},
		{settings.Settings{QoderCheckin: true, PluginCheckins: map[string]bool{"qoder": false}}, "qoder", false},
		{settings.Settings{WorkBuddyCheckin: true}, "workbuddy", true},
		{settings.Settings{TraeCheckin: true}, TraeCNID, true},
		{settings.Settings{MiniMaxCheckin: true}, MiniMaxCodeGlobalID, true},
		{settings.Settings{QoderCheckin: true}, "fakeco", false},
		{settings.Settings{PluginCheckins: map[string]bool{"fakeco": true}}, "fakeco", true},
	} {
		if got := PluginCheckinOn(c.s, c.id); got != c.want {
			t.Errorf("PluginCheckinOn(%+v, %s) = %v, want %v", c.s, c.id, got, c.want)
		}
	}
}

// pluginCheckinTeams is the fake plugin with its own check-in, signed in
// through its browser sign-in as each team (name/uid): the reporter's case
// in plugins#254 is two AutoClaw display names with two sign-ins each,
// told apart only by their uid. It says the file each press is written to,
// as name@fake/uid, and each team's sign-in key.
func pluginCheckinTeams(t *testing.T, teams ...string) (string, map[string]string) {
	t.Helper()
	presses := pluginCheckinSandbox(t)
	for _, team := range teams {
		st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{} // name@fake/uid → its sign-in's key
	for k, a := range plugin.Auths("fakeco") {
		id, _ := a["accountId"].(string)
		uid, _ := a["uid"].(string)
		keys[id+"/"+uid] = k
	}
	if len(keys) != len(teams) {
		t.Fatalf("signed in as %v, want %d sign-ins", keys, len(teams))
	}
	return presses, keys
}

// pluginCheckinOn turns fakeco's daily check-in on.
func pluginCheckinOn(t *testing.T) {
	t.Helper()
	s := settings.Load()
	s.PluginCheckins = map[string]bool{"fakeco": true}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// pluginCards are a card for each of p's accounts, as the Usage page has.
func pluginCards(p pluginCheckinProvider) []SubscriptionQuota {
	var qs []SubscriptionQuota
	for _, a := range p.accounts {
		qs = append(qs, SubscriptionQuota{Provider: PluginID(p.ID), User: a.User})
	}
	return qs
}

// Two sign-ins of one plugin that share a display name are two accounts
// to check in (plugins#254: four AutoClaw sign-ins under two default
// names, which AutoClaw doesn't let the user change, and only the first of
// each name was ever checked in, by the loop or Check in now). Each is
// checked in once a day, kept, carded and switched off on its own.
func TestPluginCheckinTellsSignInsOfOneNameApart(t *testing.T) {
	presses, keys := pluginCheckinTeams(t, "A/u1", "A/u2", "B/u3", "B/u4")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pluginCheckinOn(t)
	ps := pluginCheckinProviders()
	if len(ps) != 1 || len(ps[0].accounts) != 4 {
		t.Fatalf("the plugins that check in: %+v", ps)
	}
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, beijing)
	loop := pluginCheckiner{now: func() time.Time { return now }, only: func(id string) bool { return PluginCheckinOn(settings.Load(), id) }}
	all := "A@fake/u1,A@fake/u2,B@fake/u3,B@fake/u4"
	sorted := func(p []string) string { return strings.Join(slices.Sorted(slices.Values(p)), ",") }

	if rs := loop.checkinNow(ctx, false); len(rs) != 4 || sorted(pressed(t, presses)) != all {
		t.Fatalf("the loop: %+v, pressed %q", rs, pressed(t, presses))
	}
	// kept for each: nothing asked again that day
	now = now.Add(time.Minute)
	if rs := loop.checkinNow(ctx, false); len(rs) != 4 || len(pressed(t, presses)) != 4 {
		t.Fatalf("a minute on: %+v, pressed %q", rs, pressed(t, presses))
	}
	st := shownCheckins(pluginCheckinPath())
	for who, k := range keys {
		if r, ok := st[pluginCheckinKey("fakeco", k)]; !ok || r.Day != "2026-10-06" || r.Outcome != CheckinInactive {
			t.Errorf("%s (%s) kept as %+v, ok %v; file %+v", who, k, r, ok, st)
		}
	}
	if len(st) != 4 {
		t.Errorf("plugin-checkin.json holds %d entries, want 4: %+v", len(st), st)
	}
	// Settings lists each, and each card says today's
	if pcs := PluginCheckins(); len(pcs) != 1 || len(pcs[0].Checkins) != 4 {
		t.Fatalf("PluginCheckins = %+v", pcs)
	}
	for i, q := range WithCheckins(pluginCards(ps[0])) {
		if !q.Checkins || q.Checkin == nil || q.Checkin.Day != "2026-10-06" {
			t.Fatalf("card %d (%s): %+v", i, q.User, q.Checkin)
		}
	}
	// each its own row in Settings, and one switched off is left out
	var rows []string
	for _, a := range CheckinAccounts() {
		if a.By == "plugin:fakeco" {
			rows = append(rows, a.Key)
		}
	}
	if len(rows) != 4 {
		t.Fatalf("Settings' rows: %q", rows)
	}
	s := settings.Load()
	s.CheckinOff = []string{pluginCheckinKey("fakeco", keys["A@fake/u2"])}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	// Check in now, the next day: each but the one switched off
	now = time.Date(2026, 10, 7, 9, 0, 0, 0, beijing)
	press := pluginCheckiner{now: func() time.Time { return now }, only: func(id string) bool { return id == "fakeco" }}
	press.checkinNow(ctx, true)
	if p := sorted(pressed(t, presses)[4:]); p != "A@fake/u1,B@fake/u3,B@fake/u4" {
		t.Fatalf("Check in now with A@fake/u2 off pressed %q", p)
	}
	for _, q := range WithCheckins(pluginCards(ps[0])) {
		if q.CheckinOff != (q.User == userOf(ps[0], keys["A@fake/u2"])) {
			t.Fatalf("card %s: off %v", q.User, q.CheckinOff)
		}
	}
}

// userOf is the name the account signed in under key is shown by.
func userOf(p pluginCheckinProvider, key string) string {
	for _, a := range p.accounts {
		if a.key == pluginCheckinKey(p.ID, key) {
			return a.User
		}
	}
	return ""
}

// An upgrade from a plugin-checkin.json kept by display name (before
// plugins#254) checks no account in twice and keeps the user's own Off: a
// name checked in today is carried to the one sign-in the old key asked,
// the first in use, so only its namesake is asked; a name switched off
// stays off for each sign-in of it; another vendor's Off is left alone.
func TestPluginCheckinCarriesOverDisplayNameEntries(t *testing.T) {
	presses, keys := pluginCheckinTeams(t, "A/u1", "A/u2", "B/u3", "B/u4")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, beijing)
	legacy := map[string]WorkBuddyCheckin{
		"fakeco|A@fake": {Day: "2026-10-06", At: now.Add(-time.Hour), Outcome: CheckinClaimed, Credit: 400},
		"fakeco|B@fake": {Day: "2026-10-06", At: now.Add(-time.Hour), Outcome: CheckinClaimed, Credit: 400},
		// another plugin's, whose accounts aren't signed in: left as kept
		"other|C": {Day: "2026-10-05", At: now.Add(-25 * time.Hour), Outcome: CheckinDone},
	}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(pluginCheckinPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.PluginCheckins = map[string]bool{"fakeco": true}
	s.CheckinOff = []string{"workbuddy|123", "fakeco|B@fake"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	ps := pluginCheckinProviders()
	if len(ps) != 1 || len(ps[0].accounts) != 4 {
		t.Fatalf("the plugins that check in: %+v", ps)
	}
	// the sign-in of A the old key asked: the first of A's in use
	var heir, namesake string
	for _, a := range ps[0].accounts {
		if a.legacy == "fakeco|A@fake" && a.On {
			if heir == "" {
				heir = a.key
			} else if namesake == "" {
				namesake = a.key
			}
		}
	}
	if heir == "" || namesake == "" {
		t.Fatalf("A's sign-ins: %+v", ps[0].accounts)
	}
	st := shownCheckins(pluginCheckinPath())
	if r, ok := st[heir]; !ok || r.Outcome != CheckinClaimed || r.Credit != 400 || r.Day != "2026-10-06" {
		t.Fatalf("A's check-in went to %s as %+v (ok %v): %+v", heir, r, ok, st)
	}
	if _, ok := st[namesake]; ok {
		t.Fatalf("A's check-in was carried to both its sign-ins: %+v", st)
	}
	if _, ok := st["fakeco|A@fake"]; ok {
		t.Fatalf("A's old entry was kept: %+v", st)
	}
	if _, ok := st["other|C"]; !ok {
		t.Fatalf("another plugin's entry went: %+v", st)
	}
	off := settings.Load().CheckinOff
	want := []string{"workbuddy|123", pluginCheckinKey("fakeco", keys["B@fake/u3"]), pluginCheckinKey("fakeco", keys["B@fake/u4"])}
	if !slices.Equal(slices.Sorted(slices.Values(off)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("CheckinOff = %q, want %q", off, want)
	}

	// the loop asks only A's namesake: the heir is in, B's are off
	loop := pluginCheckiner{now: func() time.Time { return now }, only: func(id string) bool { return PluginCheckinOn(settings.Load(), id) }}
	loop.checkinNow(ctx, false)
	var who string
	for w, k := range keys {
		if pluginCheckinKey("fakeco", k) == namesake {
			who = w
		}
	}
	if p := pressed(t, presses); len(p) != 1 || p[0] != who {
		t.Fatalf("after the upgrade pressed %q, want only %s", p, who)
	}
	// and nothing is carried again, or asked again, on the next look
	pluginCheckinProviders()
	loop.checkinNow(ctx, false)
	if p := pressed(t, presses); len(p) != 1 {
		t.Fatalf("a second look pressed %q", p)
	}
	for _, a := range CheckinAccounts() {
		if a.By == "plugin:fakeco" && a.On == strings.HasPrefix(a.User, "B@fake") {
			t.Fatalf("Settings' row %+v: B's should be off, A's on", a)
		}
	}
}

// A plugin-checkin.json that doesn't parse is unknown: nothing is carried
// over or written over, and it is carried once it reads.
func TestPluginCheckinCarriesNothingOverAnUnreadableFile(t *testing.T) {
	path := filepath.Join(claudeHome(t), "plugin-checkin.json")
	order, heirs := []string{"fakeco|A"}, map[string][]string{"fakeco|A": {"fakeco|fakeco", "fakeco|fakeco#x1"}}
	bad := []byte(`{"fakeco|A": {"day": "2026-10-06",`)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if adoptLegacyCheckinFile(path, order, heirs) {
		t.Fatal("a file that doesn't parse was taken as carried over")
	}
	if b, _ := os.ReadFile(path); string(b) != string(bad) {
		t.Fatalf("the file was written over: %s", b)
	}
	if err := os.WriteFile(path, []byte(`{"fakeco|A": {"day": "2026-10-06", "outcome": "claimed"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !adoptLegacyCheckinFile(path, order, heirs) {
		t.Fatal("not carried once the file reads")
	}
	st := shownCheckins(path)
	if _, ok := st["fakeco|fakeco"]; !ok || len(st) != 1 {
		t.Fatalf("carried as %+v", st)
	}
}

// pluginCheckinCounter is a plugin provider with one account whose
// check-in answers what *outcome holds and counts the asks in *calls.
func pluginCheckinCounter(id string, outcome *string, calls *int) pluginCheckinProvider {
	return pluginCheckinProvider{ID: id, accounts: []pluginCheckinAcct{{User: "ann", On: true, key: id + "|ann", card: id, do: func(context.Context) WorkBuddyCheckin {
		*calls++
		return WorkBuddyCheckin{Outcome: *outcome}
	}}}}
}

// #2696 (LoKi, mufanmu): the Qoder plugins' check-in is rechecked as
// magpie's own Qoder check-in is. A campaign can open mid-day, so a done or
// inactive read before it was kept for the whole Beijing day, even on a
// press. Now the loop asks again 30 minutes after the last answer, not
// before, and a press asks at once. ZCode's gift plans are rechecked the
// same way (plugins#503).
func TestPluginCheckinQoderRechecksCampaigns(t *testing.T) {
	for _, id := range []string{"qoder", QoderCNID, "zcode"} {
		for _, first := range []string{CheckinInactive, CheckinDone} {
			// each: wait this long, press or not, then the outcome shown
			// and the asks so far
			type step struct {
				wait      time.Duration
				press     bool
				want      string
				wantCalls int
			}
			for name, steps := range map[string][]step{
				"loop": {
					{0, false, first, 1},
					{29 * time.Minute, false, first, 1},
					{time.Minute, false, CheckinClaimed, 2},
					{29 * time.Minute, false, CheckinClaimed, 2},
					{time.Minute, false, CheckinClaimed, 3}, // a claim is rechecked too
				},
				"press": {
					{0, false, first, 1},
					{10 * time.Minute, true, CheckinClaimed, 2},
					{0, false, CheckinClaimed, 2}, // the loop keeps what the press read
					{0, true, CheckinClaimed, 3},
				},
			} {
				t.Run(id+"/"+first+"/"+name, func(t *testing.T) {
					claudeHome(t)
					now := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC) // 09:00 Beijing
					outcome, calls := first, 0
					p := pluginCheckinCounter(id, &outcome, &calls)
					for i, s := range steps {
						now = now.Add(s.wait)
						got := p.checkiner(func() time.Time { return now }).checkinNow(context.Background(), s.press)
						if len(got) != 1 || got[0].Outcome != s.want || calls != s.wantCalls {
							t.Fatalf("step %d (press=%v at %s): got %+v after %d asks; want %s after %d", i, s.press, now.Format(time.TimeOnly), got, calls, s.want, s.wantCalls)
						}
						outcome = CheckinClaimed
					}
				})
			}
		}
	}
}

// Every other plugin's check-in is settled for the Beijing day by a claim,
// or by a done, inactive or ineligible answered from noon Beijing on: the
// loop doesn't ask again after 30 minutes (#2696 changes Qoder's, plugins#503 ZCode's).
// A press asks again for any of those but a claim: the record is not the
// vendor's answer now (#3326).
func TestPluginCheckinOthersSettledForTheDay(t *testing.T) {
	for _, first := range []string{CheckinInactive, CheckinDone, CheckinIneligible, CheckinClaimed} {
		t.Run(first, func(t *testing.T) {
			claudeHome(t)
			now := time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC) // 12:00 Beijing
			outcome, calls := first, 0
			p := pluginCheckinCounter("fakeco", &outcome, &calls)
			p.checkiner(func() time.Time { return now }).checkinNow(context.Background(), false)
			outcome = CheckinClaimed
			for _, step := range []time.Duration{31 * time.Minute, 5 * time.Hour} {
				now = now.Add(step)
				got := p.checkiner(func() time.Time { return now }).checkinNow(context.Background(), false)
				if len(got) != 1 || got[0].Outcome != first || got[0].Again || calls != 1 {
					t.Fatalf("loop at %s: got %+v after %d asks; want %s kept after 1", now, got, calls, first)
				}
			}
			got := p.checkiner(func() time.Time { return now }).checkinNow(context.Background(), true)
			want, wantCalls := CheckinClaimed, 2
			if first == CheckinClaimed {
				wantCalls = 1
			}
			if len(got) != 1 || got[0].Outcome != want || calls != wantCalls {
				t.Fatalf("press at %s: got %+v after %d asks; want %s after %d", now, got, calls, want, wantCalls)
			}
		})
	}
}

// #3326 (connoryang331): at 00:00:28 Beijing AutoClaw CN, WorkBuddy,
// CodeArts, Trae CN, MiniMax and Raccoon answered "already in" (done) for
// accounts whose day's credits were still to claim; magpie kept it as the
// day's, the loop never asked again, and a press answered from the record.
// An answer magpie saw no claim for, given before noon Beijing, is asked
// again every 30 minutes; a press asks at once.
func TestPluginCheckinEarlyDoneAskedAgain(t *testing.T) {
	early := time.Date(2026, 10, 23, 0, 0, 28, 0, beijing)
	run := func(p pluginCheckinProvider, now time.Time, press bool) WorkBuddyCheckin {
		t.Helper()
		got := p.checkiner(func() time.Time { return now }).checkinNow(context.Background(), press)
		if len(got) != 1 {
			t.Fatalf("at %s: %+v", now, got)
		}
		return got[0]
	}
	t.Run("loop", func(t *testing.T) {
		claudeHome(t)
		outcome, calls := CheckinDone, 0
		p := pluginCheckinCounter("autoclaw-cn", &outcome, &calls)
		now := early
		if r := run(p, now, false); r.Outcome != CheckinDone || !r.Again || calls != 1 {
			t.Fatalf("at midnight: %+v after %d asks", r, calls)
		}
		// shown so on the card too, and in Settings
		if r := shownCheckins(pluginCheckinPath())["autoclaw-cn|ann"]; !r.Again {
			t.Fatalf("kept: %+v", r)
		}
		if r := run(p, now.Add(29*time.Minute), false); r.Outcome != CheckinDone || !r.Again || calls != 1 {
			t.Fatalf("29 minutes on: %+v after %d asks", r, calls)
		}
		now = now.Add(wbCheckinEvery)
		if r := run(p, now, false); r.Outcome != CheckinDone || !r.Again || calls != 2 {
			t.Fatalf("30 minutes on: %+v after %d asks", r, calls)
		}
		// the vendor's day turns
		outcome = CheckinClaimed
		now = now.Add(wbCheckinEvery)
		if r := run(p, now, false); r.Outcome != CheckinClaimed || r.Again || calls != 3 {
			t.Fatalf("after the vendor's turn: %+v after %d asks", r, calls)
		}
		// a claim settles the day, for the loop and a press
		for _, step := range []time.Duration{wbCheckinEvery, 5 * time.Hour} {
			now = now.Add(step)
			for _, press := range []bool{false, true} {
				if r := run(p, now, press); r.Outcome != CheckinClaimed || calls != 3 {
					t.Fatalf("press=%v at %s: %+v after %d asks", press, now, r, calls)
				}
			}
		}
	})
	t.Run("press", func(t *testing.T) {
		claudeHome(t)
		outcome, calls := CheckinDone, 0
		p := pluginCheckinCounter("autoclaw-cn", &outcome, &calls)
		run(p, early, false)
		outcome = CheckinClaimed
		if r := run(p, time.Date(2026, 10, 23, 10, 54, 0, 0, beijing), true); r.Outcome != CheckinClaimed || !r.Asked || calls != 2 {
			t.Fatalf("pressed at 10:54: %+v after %d asks", r, calls)
		}
	})
	t.Run("done all day", func(t *testing.T) {
		claudeHome(t)
		outcome, calls := CheckinDone, 0
		p := pluginCheckinCounter("autoclaw-cn", &outcome, &calls)
		var last WorkBuddyCheckin
		for now := early; wbCheckinDay(now) == "2026-10-23"; now = now.Add(wbCheckinEvery) {
			last = run(p, now, false)
		}
		// 00:00:28 to 11:30:28 provisional, 12:00:28 the day's
		if calls != 25 || last.Again || last.Outcome != CheckinDone {
			t.Fatalf("%d asks, last %+v", calls, last)
		}
	})
	t.Run("done after magpie's claim", func(t *testing.T) {
		claudeHome(t)
		outcome, calls := CheckinClaimed, 0
		// Qoder's is rechecked after a claim, and then answers done
		p := pluginCheckinCounter("qoder", &outcome, &calls)
		run(p, early, false)
		outcome = CheckinDone
		if r := run(p, early.Add(wbCheckinEvery), false); r.Outcome != CheckinDone || r.Again || r.Claimed.IsZero() || calls != 2 {
			t.Fatalf("done after the claim: %+v after %d asks", r, calls)
		}
	})
}

// A plugin whose vendor's day turns at another hour says when (until):
// Qoder's daily credits open at 10:00 UTC+8 and stay open until 09:59 the
// next day (sdytman on Discord). A check-in before 10:00 meets the day
// before's campaign; magpie asks again as the plugin said, a minute past
// 10:00, not the next Beijing day, and not before, whatever the machine's
// zone; a failure in the gap is asked again then too.
func TestPluginCheckinAsksAgainAtUntil(t *testing.T) {
	presses := pluginCheckinSandbox(t, "ck-until")
	s := settings.Load()
	s.PluginCheckins = map[string]bool{"fakeco": true}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	answer := func(outcome string, until time.Time) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"outcome": outcome, "credit": 100, "until": until.UTC().Format(time.RFC3339)})
		if err := os.WriteFile(presses+".until", b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// the plugin's answer at t: before 10:00 Beijing, the day before's
	// campaign (claimed at its first ask, done after), until today's 10:01
	opens := func(at time.Time) time.Time {
		b := at.In(beijing)
		o := time.Date(b.Year(), b.Month(), b.Day(), 10, 1, 0, 0, beijing)
		if !at.Before(o) {
			o = o.AddDate(0, 0, 1)
		}
		return o
	}
	defer func(l *time.Location) { time.Local = l }(time.Local)
	for _, zone := range []*time.Location{time.UTC, time.FixedZone("UTC-8", -8*3600), beijing} {
		t.Run(zone.String(), func(t *testing.T) {
			time.Local = zone
			if err := os.Remove(pluginCheckinPath()); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			_ = os.Remove(presses)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			now := time.Date(2026, 10, 28, 9, 27, 0, 0, beijing).In(zone)
			c := pluginCheckiner{now: func() time.Time { return now }, only: func(id string) bool { return PluginCheckinOn(settings.Load(), id) }}
			var l wbCheckinLoop
			var at []string
			claimed := map[string]bool{}
			end := time.Date(2026, 10, 29, 10, 30, 0, 0, beijing)
			for ; now.Before(end); now = now.Add(time.Minute) {
				o := opens(now)
				if campaign := o.AddDate(0, 0, -1).Format("01-02"); claimed[campaign] {
					answer(CheckinDone, o)
				} else {
					answer(CheckinClaimed, o)
				}
				for _, r := range l.tick(ctx, c) {
					if !r.Asked {
						continue
					}
					at = append(at, now.In(beijing).Format("01-02 15:04"))
					if r.ValidUntil.IsZero() || !r.ValidUntil.Equal(o) {
						t.Fatalf("at %v: until %v, want %v (%+v)", now, r.ValidUntil, o, r)
					}
					if r.Outcome == CheckinClaimed {
						claimed[o.AddDate(0, 0, -1).Format("01-02")] = true
					}
				}
			}
			// 09:27 claims the 10-27 campaign, 10:01 the 10-28 one, the
			// Beijing day's turn asks once (done), 10:01 claims 10-29's
			if want := []string{"10-28 09:27", "10-28 10:01", "10-29 00:00", "10-29 10:01"}; !slices.Equal(at, want) {
				t.Fatalf("asked at %q, want %q", at, want)
			}
			if p := pressed(t, presses); len(p) != 4 {
				t.Fatalf("pressed %q", p)
			}
			if len(claimed) != 3 {
				t.Fatalf("claimed %v", claimed)
			}

			// in the gap (09:59:30, yesterday's closed, today's not open):
			// a failure, asked again at 10:01, not half an hour on
			_ = os.Remove(presses)
			l = wbCheckinLoop{}
			now = time.Date(2026, 10, 30, 9, 59, 30, 0, beijing).In(zone)
			b, _ := json.Marshal(map[string]any{"outcome": CheckinFailed, "message": "today's credits aren't offered yet", "until": opens(now).UTC().Format(time.RFC3339)})
			if err := os.WriteFile(presses+".until", b, 0o600); err != nil {
				t.Fatal(err)
			}
			if rs := l.tick(ctx, c); len(rs) != 1 || rs[0].Outcome != CheckinFailed || !rs[0].Asked {
				t.Fatalf("the gap: %+v", rs)
			}
			answer(CheckinClaimed, opens(now).AddDate(0, 0, 1))
			var again []string
			for now = now.Add(30 * time.Second); now.Before(time.Date(2026, 10, 30, 10, 2, 0, 0, beijing)); now = now.Add(30 * time.Second) {
				for _, r := range l.tick(ctx, c) {
					if r.Asked {
						again = append(again, now.In(beijing).Format("15:04:05"))
					}
				}
			}
			if !slices.Equal(again, []string{"10:01:00"}) {
				t.Fatalf("after the gap, asked at %q, want 10:01:00", again)
			}
			if p := pressed(t, presses); len(p) != 2 {
				t.Fatalf("after the gap, pressed %q", p)
			}
		})
	}
}

// The provider editor's account rows have each account's check-in as its
// Usage card does, with the key its own Check in now presses by
// (xwoffice_91592): a press of one account's asks that account alone.
func TestLoginCheckinsPressOneAccount(t *testing.T) {
	keys := []string{"ck-claim", "ck-done"}
	presses := pluginCheckinSandbox(t, keys...)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ps := pluginCheckinProviders()
	if len(ps) != 1 || len(ps[0].accounts) != len(keys) {
		t.Fatalf("the plugins that check in: %+v", ps)
	}
	for _, agent := range []string{PluginID("fakeco"), "plugin:fakeco"} {
		m := map[string]SubscriptionQuota{}
		for _, a := range ps[0].accounts {
			m[a.User] = SubscriptionQuota{Plan: "Credits"}
		}
		got := LoginCheckins(agent, m)
		for u, q := range got {
			if !q.Checkins || q.CheckinBy != "plugin:fakeco" || q.CheckinKey == "" || q.Plan != "Credits" {
				t.Fatalf("%s: %s: %+v", agent, u, q)
			}
		}
		if m[ps[0].accounts[0].User].Checkins {
			t.Fatal("LoginCheckins changed the map it was given")
		}
	}
	a := ps[0].accounts[1]
	q := LoginCheckins(PluginID("fakeco"), map[string]SubscriptionQuota{a.User: {}})[a.User]
	rs := CheckInAccount(ctx, q.CheckinBy, q.CheckinKey)
	if len(rs) != 1 || rs[0].Outcome != CheckinDone && rs[0].Outcome != CheckinClaimed {
		t.Fatalf("the press: %+v", rs)
	}
	if p := pressed(t, presses); len(p) != 1 || p[0] != "ck-done" {
		t.Fatalf("pressed %q", p)
	}
	// another provider's accounts are not marked
	if q := LoginCheckins("codex", map[string]SubscriptionQuota{a.User: {}})[a.User]; q.Checkins || q.CheckinKey != "" {
		t.Fatalf("codex: %+v", q)
	}
}
