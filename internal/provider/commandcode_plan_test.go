package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

func TestCommandCodePlan(t *testing.T) {
	home := signIn(t)
	writeFile(t, filepath.Join(home, ".commandcode", "auth.json"), map[string]any{
		"apiKey": "own-key", "userId": "u1", "userName": "ownuser", "keyName": "cli", "authenticatedAt": "2026-09-01T00:00:00Z",
	})
	reset := time.Now().Add(2 * time.Hour).UnixMilli()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key != "own-key" && key != "two-key" {
			w.WriteHeader(401)
			return
		}
		ok := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/alpha/whoami":
			ok(map[string]any{"user": map[string]any{"id": "u2", "userName": "two", "email": "two@example.com"}})
		case "/alpha/billing/subscriptions":
			if key == "own-key" { // as for an account with no plan
				ok(map[string]any{"success": true, "data": nil})
				return
			}
			ok(map[string]any{"success": true, "data": map[string]any{"planId": "individual-max-monthly", "status": "active", "currentPeriodEnd": "2026-10-28T00:00:00Z", "cancelAtPeriodEnd": true}})
		case "/alpha/billing/credits":
			if key == "own-key" { // no plan: bought credits, no windows
				ok(map[string]any{"credits": map[string]any{"monthlyCredits": 0, "purchasedCredits": 7.25, "freeCredits": 0}})
				return
			}
			// as the live reply: no planId here, it is billing/subscriptions'
			ok(map[string]any{
				"credits": map[string]any{"monthlyCredits": 100.5, "purchasedCredits": 10, "freeCredits": 0},
				"windowLimits": map[string]any{"limited": true,
					"fiveHour": map[string]any{"used": 25, "cap": 100, "resetAt": reset},
					"weekly":   map[string]any{"used": 30, "cap": 300, "resetAt": time.Now().Add(72 * time.Hour).UnixMilli()},
				},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldAPI := cmdAPI
	cmdAPI = srv.URL
	defer func() { cmdAPI = oldAPI }()

	if who, a, ok := cmdOwn(); !ok || who != "ownuser" || a.APIKey != "own-key" {
		t.Fatalf("own: %v %q", ok, who)
	}

	st, err := StartSignIn(CommandCodePlanID)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	cb := u.Query().Get("callback")
	if u.Host != "commandcode.ai" || u.Path != "/studio/auth/cli" || u.Query().Get("mode") != "redirect" ||
		!strings.HasPrefix(cb, "http://127.0.0.1:") || !strings.HasSuffix(cb, "/callback") {
		t.Fatalf("sign-in page: %s", st.URL)
	}
	state := u.Query().Get("state")
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// the browser's preflight, from Studio
	pre, _ := http.NewRequest(http.MethodOptions, cb, nil)
	pre.Header.Set("Origin", "https://commandcode.ai")
	pre.Header.Set("Access-Control-Request-Private-Network", "true")
	if res, err := noFollow.Do(pre); err != nil || res.StatusCode != 204 ||
		res.Header.Get("Access-Control-Allow-Origin") != "https://commandcode.ai" || res.Header.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("preflight: %v %+v", err, res)
	}
	// someone else's state is turned away, and the sign-in goes on
	if res, err := noFollow.PostForm(cb, url.Values{"apiKey": {"evil"}, "state": {"nope"}}); err != nil || res.StatusCode != 403 {
		t.Fatalf("wrong state: %v %+v", err, res)
	}
	if res, _ := noFollow.Get(cb); res.StatusCode != 405 {
		t.Fatalf("GET callback: %d", res.StatusCode)
	}

	res, err := noFollow.PostForm(cb, url.Values{"apiKey": {"two-key"}, "state": {state}, "userId": {"u2"}, "userName": {"two"}, "keyName": {"magpie"}})
	if err != nil || res.StatusCode != 303 || !strings.HasPrefix(res.Header.Get("Location"), "/callback/complete?state=") {
		t.Fatalf("callback: %v %+v", err, res)
	}
	page, err := http.Get(strings.TrimSuffix(cb, "/callback") + res.Header.Get("Location"))
	if err != nil || page.StatusCode != 200 {
		t.Fatalf("complete page: %v %+v", err, page)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, _ = WaitSignIn(ctx, st.ID)
	if st.State != "done" || st.User != "two" || st.Plan != "Max" || st.Using {
		t.Fatalf("done: %+v", st)
	}

	var users []string
	for _, l := range Logins(CommandCodePlanID) {
		users = append(users, l.User+map[bool]string{true: "*", false: ""}[l.Active]+map[bool]string{true: "+", false: ""}[l.On])
	}
	if got := strings.Join(users, " "); got != "ownuser*+ two+" {
		t.Fatalf("logins: %s", got)
	}
	p, ok := find(All(), CommandCodePlanID)
	if !ok || p.Account.User != "ownuser" || p.Chat != srv.URL+"/provider/v1" || p.Anthropic != srv.URL+"/provider" {
		t.Fatalf("first: %+v", p)
	}
	also := p.AlsoOn()
	if len(also) != 1 || also[0].Account.User != "two" {
		t.Fatalf("also on: %+v", also)
	}
	req, _ := http.NewRequest("POST", also[0].Chat+"/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer magpie")
	if err := also[0].Sign(context.Background(), req, Chat, []byte(`{}`)); err != nil ||
		req.Header.Get("Authorization") != "Bearer two-key" || req.Header.Get("x-api-key") != "two-key" {
		t.Fatalf("signs with its own key: %v %v", err, req.Header)
	}

	q := LoginUsage(context.Background(), CommandCodePlanID)["two"]
	if q.Error != "" || q.Plan != "Max" || len(q.Windows) != 3 || q.Windows[0].Name != "5 hours" || q.Windows[0].Used != 25 ||
		q.Windows[1].Name != "Weekly" || q.Windows[1].Used != 10 || q.Windows[0].ResetsAt == nil || q.Windows[1].ResetsAt == nil ||
		q.Windows[0].ResetsAt.UnixMilli() != reset {
		t.Fatalf("usage: %+v", q)
	}
	// the credits are a window too, not a Balance, which every view shows
	// in place of the windows: Max's $150 a month, $100.50 of it left
	if q.Balance != "" || q.Windows[2].Name != "Credits" || q.Windows[2].Display != "$49.50 / $160.00" {
		t.Fatalf("credits: %q %+v", q.Balance, q.Windows[2])
	}
	if q.Until == nil || q.Until.Format("2006-01-02") != "2026-10-28" || q.Renew != "off" {
		t.Fatalf("period: %v %q", q.Until, q.Renew)
	}
	// an account with no subscription says so, and shows what it bought
	if q := LoginUsage(context.Background(), CommandCodePlanID)["ownuser"]; q.Error != "" || q.Plan != "No plan" || q.Balance != "$7.25" {
		t.Fatalf("no plan: %+v", q)
	}

	if err := SwitchLogin(CommandCodePlanID, "two"); err != nil {
		t.Fatal(err)
	}
	if p, _ := find(All(), CommandCodePlanID); p.Account.User != "two" {
		t.Fatalf("after switch: %+v", p.Account)
	}
	if err := SwitchLogin(CommandCodePlanID, "ownuser"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin(CommandCodePlanID, "two"); err != nil {
		t.Fatal(err)
	}
	if ls := Logins(CommandCodePlanID); len(ls) != 1 {
		t.Fatalf("after forget: %+v", ls)
	}

	// a sign-in the user turns down fails
	st, _ = StartSignIn(CommandCodePlanID)
	u, _ = url.Parse(st.URL)
	body, _ := json.Marshal(map[string]any{"state": u.Query().Get("state"), "error": "access_denied"})
	if res, err := http.Post(u.Query().Get("callback"), "application/json", strings.NewReader(string(body))); err != nil || res.StatusCode != 400 {
		t.Fatalf("denied: %v %+v", err, res)
	}
	st, _ = WaitSignIn(ctx, st.ID)
	if st.State != "failed" || st.Error != "the sign-in was denied" {
		t.Fatalf("denied: %+v", st)
	}

	// signed out of the CLI, with none of magpie's: no Command Code subscription
	os.Remove(filepath.Join(home, ".commandcode", "auth.json"))
	if _, ok := find(All(), CommandCodePlanID); ok {
		t.Fatal("command code without an account")
	}
}

func TestCommandCodePlanName(t *testing.T) {
	for id, want := range map[string]string{
		"individual-go": "Go", "individual-goat": "GOAT", "individual-pro-v1": "Pro", "individual_ultra": "Ultra",
		"teams-pro": "Teams Pro", "individual-provider": "Provider", "enterprise": "",
	} {
		if got := cmdPlanName(id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	if tm := cmdTime(float64(1_900_000_000_000)); tm == nil || tm.Unix() != 1_900_000_000 {
		t.Fatalf("ms: %v", tm)
	}
}

// A whoami that errs on a key just made doesn't fail the sign-in (tigger_ultra:
// "Command Code didn't take the new key: Internal Server Error"): it is asked
// again, then passed over for the name Studio sent; only a key it turns down
// fails.
func TestCommandCodeSignInWhoamiDown(t *testing.T) {
	signIn(t)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case key == "bad-key":
			w.WriteHeader(401)
		case r.URL.Path == "/alpha/whoami":
			asked.Add(1)
			w.WriteHeader(500)
		case r.URL.Path == "/alpha/billing/subscriptions":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"planId": "individual-pro-monthly", "status": "active"}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldAPI, oldWait := cmdAPI, cmdWhoamiWait
	cmdAPI, cmdWhoamiWait = srv.URL, 10*time.Millisecond
	defer func() { cmdAPI, cmdWhoamiWait = oldAPI, oldWait }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, c := range []struct{ key, user, state, errText string }{
		{"new-key", "tigger", "done", ""},
		{"bad-key", "evil", "failed", "Command Code didn't take the new key: Unauthorized"},
		{"new-key", "", "failed", "Command Code couldn't say which account signed in: Internal Server Error"},
	} {
		asked.Store(0)
		st, err := StartSignIn(CommandCodePlanID)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(st.URL)
		if _, err := noFollow.PostForm(u.Query().Get("callback"), url.Values{"apiKey": {c.key}, "state": {u.Query().Get("state")}, "userName": {c.user}}); err != nil {
			t.Fatal(err)
		}
		st, _ = WaitSignIn(ctx, st.ID)
		if st.State != c.state || st.Error != c.errText {
			t.Fatalf("%s %q: %+v", c.key, c.user, st)
		}
		if c.state == "done" && (st.User != c.user || st.Plan != "Pro" || asked.Load() != 3) {
			t.Fatalf("signed in: %+v, whoami asked %d times", st, asked.Load())
		}
	}
}

// The CLI's own account removed in magpie, then signed in to again from
// magpie (#320, ttbug): the sign-in said done, but the key was the CLI's
// own account's, let go as one, and that one stayed hidden, so magpie
// said "signed in, but magpie can't list it". Signing in brings it back.
func TestCommandCodeSignInAgainToRemovedOwn(t *testing.T) {
	home := signIn(t)
	writeFile(t, filepath.Join(home, ".commandcode", "auth.json"), map[string]any{
		"apiKey": "own-key", "userId": "u1", "userName": "ownuser", "keyName": "cli",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": "u1", "userName": "ownuser"}})
		case "/alpha/billing/subscriptions":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"planId": "individual-pro-monthly", "status": "active"}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldAPI := cmdAPI
	cmdAPI = srv.URL
	defer func() { cmdAPI = oldAPI }()

	if ls := Logins(CommandCodePlanID); len(ls) != 1 || ls[0].User != "ownuser" {
		t.Fatalf("own: %+v", ls)
	}
	if err := ForgetLogin(CommandCodePlanID, "ownuser"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), CommandCodePlanID); ok {
		t.Fatal("removed, still listed")
	}

	st, err := StartSignIn(CommandCodePlanID)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if _, err := noFollow.PostForm(u.Query().Get("callback"), url.Values{"apiKey": {"new-key"}, "state": {u.Query().Get("state")},
		"userId": {"u1"}, "userName": {"ownuser"}, "keyName": {"magpie"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if st, _ = WaitSignIn(ctx, st.ID); st.State != "done" || st.User != "ownuser" {
		t.Fatalf("sign-in: %+v", st)
	}
	if ls := Logins(CommandCodePlanID); len(ls) != 1 || ls[0].User != "ownuser" || !ls[0].Active {
		t.Fatalf("logins: %+v", ls)
	}
	if p, ok := find(All(), CommandCodePlanID); !ok || p.Account.User != "ownuser" {
		t.Fatalf("not listed after signing in again: %v %+v", ok, p)
	}
}

// Command Code answers a 200 with success false when it couldn't read the
// subscription ("write CONNECTION_CLOSED …"): the plan is unread, not none.
func TestCommandCodeSubscriptionUnread(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "write CONNECTION_CLOSED db.local:5432"})
	}))
	defer srv.Close()
	oldAPI := cmdAPI
	cmdAPI = srv.URL
	defer func() { cmdAPI = oldAPI }()
	if _, plan, _, _, ok := cmdSubscription(context.Background(), cmdAuth{APIKey: "k"}); ok || plan != "" {
		t.Fatalf("plan %q, ok %v: want it unread", plan, ok)
	}
}

// A Command Code model is told to the agents with a reply limit within the
// 200000 Command Code takes: models.dev gives DeepSeek V4 384000, and an
// agent asking for that was refused. Another provider's is left as it is.
func TestCommandCodeOutputCapped(t *testing.T) {
	m := catalog.Model{ID: "deepseek/deepseek-v4-pro", Context: 1_000_000, Output: 384_000}
	if got := entryFor(Provider{ID: CommandCodePlanID}, m, settings.Settings{}).Output; got != CommandCodeMaxOutput {
		t.Errorf("Command Code: output %d, want %d", got, CommandCodeMaxOutput)
	}
	if got := entryFor(Provider{ID: "deepseek"}, m, settings.Settings{}).Output; got != 384_000 {
		t.Errorf("deepseek: output %d, want 384000", got)
	}
	m.Output = 64_000
	if got := entryFor(Provider{ID: CommandCodePlanID}, m, settings.Settings{}).Output; got != 64_000 {
		t.Errorf("Command Code, within: output %d, want 64000", got)
	}
}
