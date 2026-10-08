package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's accounts show their allowance as the built-ins' do: beside
// each account, on the usage page, and to the gateway, a window counting
// only some models holding back those alone.
func TestPluginUsage(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
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
	for _, team := range []string{"a", "full"} {
		st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "k-123456"); err != nil {
		t.Fatal(err)
	}

	for _, agent := range []string{"fakeco", "plugin:fakeco"} {
		u := LoginUsage(ctx, agent)
		if len(u) != 3 {
			t.Fatalf("%s: %d accounts' usage, want 3: %+v", agent, len(u), u)
		}
		q := u["a@fake"]
		if q.Error != "" || q.Plan != "Fake Pro" || q.Provider != "fakeco" || q.Name != "FakeCo" || len(q.Windows) != 3 {
			t.Fatalf("%s: a@fake = %+v", agent, q)
		}
		if q.Until == nil || q.Until.Year() != 2030 || q.Renew != "auto" {
			t.Fatalf("%s: plan period %v %q", agent, q.Until, q.Renew)
		}
		w, week, extra := q.Windows[0], q.Windows[1], q.Windows[2]
		if w.Name != "5 hours" || w.Used != 25 || w.Span != 5*time.Hour || w.ResetsAt == nil || time.Until(*w.ResetsAt) < 50*time.Minute {
			t.Fatalf("%s: five hours = %+v", agent, w)
		}
		// a reset in seconds reads as one
		if week.ResetsAt == nil || time.Until(*week.ResetsAt) < 23*time.Hour || time.Until(*week.ResetsAt) > 25*time.Hour {
			t.Fatalf("%s: week = %+v", agent, week)
		}
		// its count, through the host (#659)
		if week.Amount != 120 || week.Limit != 1200 || week.Unit != "credits" || w.Limit != 0 {
			t.Fatalf("%s: week = %+v", agent, week)
		}
		if extra.Used != 250 || extra.Display != "$2.50" || !extra.Aside {
			t.Fatalf("%s: extra = %+v", agent, extra)
		}
		if e := u["API key"].Error; e != "an API key has no plan" {
			t.Fatalf("%s: the key's usage error = %q (%v)", agent, e, u)
		}
	}

	cards := 0
	for _, q := range fetchSubscriptionUsage(context.Background()) {
		if q.Provider == "fakeco" {
			cards++
			if q.Name != "FakeCo" || q.User == "" {
				t.Fatalf("usage card %+v", q)
			}
		}
	}
	if cards != 3 {
		t.Fatalf("%d usage cards for the plugin's accounts, want 3", cards)
	}
	// one account's card read again from its refresh button (#840): that
	// account's alone is asked
	again := context.WithValue(context.Background(), cardRefreshKey{}, cardRefresh{"fakeco", "A@fake"})
	if qs := fetchSubscriptionUsage(again); len(qs) != 1 || qs[0].User != "a@fake" || qs[0].Error != "" {
		t.Fatalf("a@fake read again: %+v", qs)
	}

	// a model the plugin says the plan serves at no cost shows as free,
	// as WorkBuddy's built-in marked its x0.00 models
	free := map[string]bool{}
	for _, pp := range plugin.Cached() {
		if pp.ID == "fakeco" {
			for _, m := range pluginCatalog(pp) {
				free[m.ID] = m.Free
			}
		}
	}
	if !free["fake-1"] || free["fake-claude"] {
		t.Fatalf("free models %v, want fake-1 alone", free)
	}

	// the gateway asks the allowance by the account's UsageAgent
	var agent string
	for _, p := range All() {
		if p.IsPlugin() && p.ID == "fakeco" {
			agent = p.Account.UsageAgent()
		}
	}
	if agent != "plugin:fakeco" {
		t.Fatalf("UsageAgent = %q", agent)
	}
	// the plan the usage told stays with the account, shown beside it
	plan := false
	for _, p := range All() {
		if p.IsPlugin() && p.ID == "fakeco" && p.Account.User == "a@fake" {
			plan = p.Account.Plan == "Fake Pro"
		}
	}
	if !plan {
		t.Fatal("a@fake's provider doesn't carry its plan")
	}
	if q := LoginUsage(ctx, agent)["full@fake"]; q.Resets == nil || !q.Resets.ByWindow || q.Resets.FiveHour != 2 || q.Resets.Weekly != 1 || q.Resets.Count != 3 || q.User != "Full@Fake.example" {
		t.Fatalf("full@fake = %+v, resets %+v", q, q.Resets)
	}
	full := allowanceOf(LoginUsage(ctx, agent)["full@fake"].Windows, time.Now())
	if used, _ := full.For("fake-claude", time.Now()); used != 100 {
		t.Fatalf("fake-claude on full@fake: used %v, want 100", used)
	}
	if used, _ := full.For("fake-1", time.Now()); used != 10 {
		t.Fatalf("fake-1 on full@fake: used %v, want 10 (the five hours don't count it)", used)
	}
	if full.Full("fake-claude", 100, time.Now()).IsZero() || !full.Full("fake-1", 100, time.Now()).IsZero() {
		t.Fatal("full@fake is used up for fake-claude alone")
	}
}

// A usage read that finds the sign-in refused marks the account, as the
// built-ins' did, and a clean one takes the mark off; one the network
// failed shows the last reading, as a built-in's did.
func TestPluginUsageLapse(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	auth := func(user, refresh string) map[string]any {
		return map[string]any{"type": "oauth", "refresh": refresh, "access": "a", "expires": 9e15, "accountId": user}
	}
	keys := map[string]string{}
	var rows []savedLogin
	for user, refresh := range map[string]string{"gone@fake": "r-gone", "ok@fake": "r-ok", "off@fake": "r-offline", "kept@fake": "r-kept", "renewed@fake": "r-renewed"} {
		k, err := plugin.Import(ctx, "fakeco", auth(user, refresh))
		if err != nil {
			t.Fatal(err)
		}
		keys[user] = k
		rows = append(rows, savedLogin{Agent: "plugin:fakeco", User: user, Home: k, On: true})
	}
	saveLogins(t, rows...)
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pp := mustPlugin(t)
	for _, u := range []string{"ok@fake", "renewed@fake"} {
		notePluginLapse(pp, keys[u], http.StatusUnauthorized)
	}
	lapsed := func() map[string]bool {
		out := map[string]bool{}
		for _, l := range pluginLogins(mustPlugin(t)) {
			out[l.User] = l.Lapsed != ""
		}
		return out
	}
	if l := lapsed(); !l["ok@fake"] || l["gone@fake"] {
		t.Fatalf("before: %v", l)
	}
	for _, u := range []string{"gone@fake", "ok@fake", "kept@fake", "renewed@fake"} {
		pluginLoginQuota(ctx, Login{Agent: "plugin:fakeco", User: u})
	}
	// a plugin that says what the read means is taken at its word: kept
	// though its error says to sign in again, renewed though the read failed
	if l := lapsed(); l["ok@fake"] || !l["gone@fake"] || l["kept@fake"] || l["renewed@fake"] {
		t.Fatalf("after reading usage: %v", l)
	}
	if !passing.MatchString(pluginLoginQuota(ctx, Login{Agent: "plugin:fakeco", User: "off@fake"}).Error) {
		t.Fatal("a failed fetch isn't taken for the network")
	}
}

// A usage read begun before a request was answered doesn't overrule what
// the answer told of the sign-in: one that ends clean leaves the mark of a
// 401 that came meanwhile, and one that ends refused doesn't mark an
// account a request went through on meanwhile. A read begun afterwards
// marks or clears as before. TestPluginFailover's refused account lost
// its mark to a reading of its allowance begun just before the 401.
func TestPluginUsageReadOlderThanAnswer(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	asked, answer := make(chan struct{}), make(chan int)
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case asked <- struct{}{}:
		case <-r.Context().Done():
			return
		}
		select {
		case status := <-answer:
			w.WriteHeader(status)
			fmt.Fprint(w, "Fake Pro")
		case <-r.Context().Done():
		}
	}))
	defer vendor.Close()
	t.Setenv("FAKE_USAGE", vendor.URL)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	key, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-ok", "access": "a", "expires": 9e15, "accountId": "ok@fake"})
	if err != nil {
		t.Fatal(err)
	}
	saveLogins(t, savedLogin{Agent: "plugin:fakeco", User: "ok@fake", Home: key, On: true})
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pp := mustPlugin(t)
	lapsed := func() bool {
		for _, l := range pluginLogins(mustPlugin(t)) {
			return l.Lapsed != ""
		}
		t.Fatal("no account")
		return false
	}
	// read reads the account's usage, the vendor answering it status
	// once meanwhile has run
	read := func(status int, meanwhile func()) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			pluginLoginQuota(ctx, Login{Agent: "plugin:fakeco", User: "ok@fake"})
		}()
		select {
		case <-asked:
		case <-ctx.Done():
			t.Fatal("the usage was never asked")
		}
		meanwhile()
		answer <- status
		<-done
	}
	// as the gateway notes a request's answer that says nothing of it
	answered := func(status int) func() {
		return func() { notePluginSaid(pp, key, "", status) }
	}
	nothing := func() {}

	read(http.StatusOK, answered(http.StatusUnauthorized))
	if !lapsed() {
		t.Fatal("a usage read begun before the 401 took its mark off")
	}
	read(http.StatusOK, nothing)
	if lapsed() {
		t.Fatal("a clean usage read begun after the 401 left its mark")
	}
	read(http.StatusUnauthorized, answered(http.StatusOK))
	if lapsed() {
		t.Fatal("a refused usage read begun before a request went through marked the account")
	}
	read(http.StatusUnauthorized, nothing)
	if !lapsed() {
		t.Fatal("a refused usage read didn't mark the account")
	}
}

// Each of a plugin provider's accounts has the models its own plan
// serves, as each of a built-in's accounts had: a key that has fewer
// doesn't take on the first account's.
func TestPluginAccountModels(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
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
	st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, "good"); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "few"); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	pp := mustPlugin(t)
	for _, l := range pluginLogins(pp) {
		for _, m := range pluginProvider(pp, l).Available() {
			got[l.User] = append(got[l.User], m.ID)
		}
	}
	if len(got) != 2 || len(got["a@fake"]) < 2 || strings.Join(got["API key"], " ") != "fake-1" {
		t.Fatalf("each account's models: %v", got)
	}
}

// A built-in moved onto its plugin keeps the name, icon and site its
// provider had, whatever the plugin calls itself.
func TestMovedProviderName(t *testing.T) {
	claudeHome(t)
	for id, want := range map[string]string{CommandCodePlanID: "Command Code Plan", "grok": "Grok (SuperGrok)", "kiro": "Kiro"} {
		if err := setMigration(id, func(m *Migration) { m.State = MovePlugin }); err != nil {
			t.Fatal(err)
		}
		p := pluginProvider(plugin.Provider{ID: id, Name: "Plugin's own " + id}, pluginLogin{})
		if p.Name != want || p.Icon != movedCards[id].icon || p.Website != movedCards[id].site {
			t.Fatalf("%s: %q %q %q, want %q", id, p.Name, p.Icon, p.Website, want)
		}
	}
}

// A models hook that finds the sign-in refused and says so marks the
// account, as a built-in whose model list the vendor refused marked it.
func TestPluginModelsSayExpired(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	var rows []savedLogin
	for user, refresh := range map[string]string{"ok@fake": "r-ok", "gone@fake": "r-models-gone", "dead@fake": "r-dead"} {
		k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": refresh, "access": "a", "expires": 9e15, "accountId": user})
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, savedLogin{Agent: "plugin:fakeco", User: user, Home: k, On: true})
	}
	saveLogins(t, rows...)
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		lapsed := map[string]bool{}
		for _, l := range pluginLogins(mustPlugin(t)) {
			lapsed[l.User] = l.Lapsed != ""
		}
		if lapsed["gone@fake"] && !lapsed["ok@fake"] && !lapsed["dead@fake"] {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lapsed after reading the models: %v, want gone@fake alone (dead@fake's hook said nothing)", lapsed)
		}
	}
}
