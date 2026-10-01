package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStepFun is a StepFun platform: it takes the session magpie keeps as
// its one cookie, renews it, and tells a plan's windows.
type fakeStepFun struct {
	srv      *httptest.Server
	access   atomic.Value // the access token it takes now
	renewed  atomic.Int32
	account  string // who a renewal says it is
	rateBody string
}

func newFakeStepFun(t *testing.T, site string) *fakeStepFun {
	t.Helper()
	f := &fakeStepFun{account: "4161", rateBody: `{"five_hour_usage_left_rate":0.6,"five_hour_usage_reset_time":"1790680000","weekly_usage_left_rate":"0.9","weekly_usage_reset_time":"1791200000","plan_family":"2","plan_credit_rate_limit":{"subscription_credit_left_rate":0.87,"subscription_credit_reset_time":"1792000000","topup_credit_left_rate":0}}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "{}" || r.Header.Get("Oasis-Platform") != "web" || r.Header.Get("Oasis-Webid") != "web-1" || r.Header.Get("Oasis-Appid") != stepfunSites[site].app {
			http.Error(w, `{"message":"bad request"}`, http.StatusBadRequest)
			return
		}
		// the one cookie, the two tokens joined by three dots
		c := r.Header.Get("Cookie")
		acc, ref, ok := strings.Cut(strings.TrimPrefix(c, "Oasis-Token="), "...")
		if !strings.HasPrefix(c, "Oasis-Token=") || strings.Contains(c, ";") || !ok || ref == "" {
			http.Error(w, `{"message":"token is malformed"}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/passport/proto.api.passport.v1.PassportService/RefreshToken":
			f.renewed.Add(1)
			n := fakeJWT(map[string]any{"activated": true, "oasis_id": f.account, "exp": time.Now().Add(30 * time.Minute).Unix()})
			f.access.Store(n)
			io.WriteString(w, `{"accessToken":{"raw":"`+n+`","duration":1800},"refreshToken":{"raw":"`+ref+`"}}`)
			return
		}
		if acc != f.access.Load().(string) {
			http.Error(w, `{"code":"unauthenticated","message":"auth failed: token is expired"}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/step.openapi.devcenter.Dashboard/QueryStepPlanRateLimit":
			io.WriteString(w, f.rateBody)
		case "/api/step.openapi.devcenter.Dashboard/GetStepPlanStatus":
			io.WriteString(w, `{"subscription":{"plan_type":"2","name":"Plus","expired_at":"1793000000","auto_renew":true},"plan_definition":{"price":"9900"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	old := stepfunSites
	stepfunSites = map[string]struct{ platform, app string }{site: {f.srv.URL, old[site].app}}
	t.Cleanup(func() { stepfunSites = old })
	return f
}

func stepfunTokens(account string, exp time.Time, activated bool) (string, string) {
	return fakeJWT(map[string]any{"activated": activated, "oasis_id": account, "exp": exp.Unix()}),
		fakeJWT(map[string]any{"device_id": "web-1", "oasis_id": account, "exp": exp.Add(30 * 24 * time.Hour).Unix()})
}

func TestStepFunSite(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.stepfun.ai/step_plan/v1": "ai",
		"https://api.stepfun.com/v1":          "com",
		"https://api.deepseek.com/v1":         "",
	} {
		if got := StepFunSite(Provider{Chat: base}); got != want {
			t.Errorf("%s: %q, want %q", base, got, want)
		}
	}
}

// The windows a Step Plan tells, what is left made what is used; a plan
// with no 5-hour or weekly window tells them as 0 and 0, which is none,
// not one used up.
func TestReadStepPlan(t *testing.T) {
	ws, err := readStepPlan([]byte(`{"five_hour_usage_left_rate":0.6,"five_hour_usage_reset_time":"1790680000","weekly_usage_left_rate":"0.9","weekly_usage_reset_time":"1791200000000","plan_credit_rate_limit":{"subscription_credit_left_rate":0.87,"subscription_credit_reset_time":"1792000000"}}`))
	if err != nil || len(ws) != 3 {
		t.Fatalf("%+v %v", ws, err)
	}
	want := []struct {
		name string
		used float64
		at   int64
	}{{"5 hours", 40, 1790680000}, {"7 days", 10, 1791200000}, {"Credits", 13, 1792000000}}
	for i, w := range want {
		g := ws[i]
		if g.Name != w.name || g.Used < w.used-0.01 || g.Used > w.used+0.01 || g.ResetsAt == nil || g.ResetsAt.Unix() != w.at {
			t.Errorf("window %d: %+v, want %+v", i, g, w)
		}
	}
	ws, _ = readStepPlan([]byte(`{"five_hour_usage_left_rate":"0","five_hour_usage_reset_time":"0","weekly_usage_left_rate":"0","weekly_usage_reset_time":"0","plan_credit_rate_limit":{"subscription_credit_left_rate":0.87,"subscription_credit_reset_time":"1792000000"}}`))
	if len(ws) != 1 || ws[0].Name != "Credits" {
		t.Errorf("no rolling windows: %+v", ws)
	}
	// a monthly Pro plan's credits never reset, they run out with the plan:
	// its reset is "0" and its bucket says next_reset_at "0", yet the credits
	// are there, 90% of them left
	ws, _ = readStepPlan([]byte(`{"status":1,"desc":"","five_hour_usage_left_rate":0,"five_hour_usage_reset_time":"0","weekly_usage_left_rate":0,"weekly_usage_reset_time":"0","plan_family":2,"plan_credit_rate_limit":{"subscription_credit_left_rate":0.9019661,"subscription_credit_reset_time":"0","topup_credit_left_rate":0,"credit_buckets":[{"type":1,"credit_total":"8000000000","credit_residual":"7215728427","expire_at":"1792061262","next_reset_at":"0"}]}}`))
	if len(ws) != 1 || ws[0].Name != "Credits" || ws[0].Used < 9.79 || ws[0].Used > 9.81 || ws[0].ResetsAt != nil {
		t.Errorf("credits that don't reset: %+v", ws)
	}
	// a plan without credits tells them as 0 and 0 with no bucket: none
	ws, _ = readStepPlan([]byte(`{"five_hour_usage_left_rate":0.6,"five_hour_usage_reset_time":"1790680000","weekly_usage_left_rate":0,"weekly_usage_reset_time":"0","plan_credit_rate_limit":{"subscription_credit_left_rate":0,"subscription_credit_reset_time":"0","credit_buckets":[]}}`))
	if len(ws) != 1 || ws[0].Name != "5 hours" {
		t.Errorf("no credits: %+v", ws)
	}
}

// A page not signed in yet has an anonymous session, which isn't kept;
// a signed-in one is, and the plan's card is read with it, renewed first
// when it is about to run out.
func TestStepFunSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	forgetPlanQuotas()
	f := newFakeStepFun(t, "ai")
	ctx := context.Background()

	anon, ref := stepfunTokens("999", time.Now().Add(time.Hour), false)
	f.access.Store(anon)
	if err := SaveStepFunSession(ctx, "ai", anon, ref, "web-1"); err == nil || StepFunSignedIn("ai") {
		t.Fatalf("an anonymous session was kept: %v", err)
	}

	// three minutes left: kept, then renewed before it is used
	acc, ref := stepfunTokens("4161", time.Now().Add(3*time.Minute), true)
	f.access.Store(acc)
	if err := SaveStepFunSession(ctx, "ai", acc, ref, "web-1"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(stepfunPath()); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("session file: %v %v", fi, err)
	}
	p := Provider{ID: "stepfun", Name: "StepFun", Icon: "stepfun-color", Chat: "https://api.stepfun.ai/step_plan/v1"}
	q := stepPlanQuota(ctx, "ai", p)
	if q.Error != "" || q.Plan != "Plus" || len(q.Windows) != 3 || q.Renew != "auto" || q.Until == nil || q.Until.Unix() != 1793000000 {
		t.Fatalf("card: %+v", q)
	}
	if f.renewed.Load() != 1 {
		t.Errorf("renewed %d times, want 1", f.renewed.Load())
	}
	if s := loadStepFun()["ai"]; s.Access != f.access.Load().(string) {
		t.Error("the renewed session wasn't kept")
	}

	// the platform expires the session early: renewed once, and asked again
	f.access.Store("something-else")
	if q := stepPlanQuota(ctx, "ai", p); q.Error != "" || len(q.Windows) != 3 {
		t.Fatalf("after an early expiry: %+v", q)
	}

	// past the refresh token's 30 days StepFun renews as someone else,
	// anonymous: signed out, not another account's windows
	f.account = "777"
	f.access.Store("expired")
	if q := stepPlanQuota(ctx, "ai", p); q.Error != errStepFunSession.Error() || len(q.Windows) != 0 {
		t.Fatalf("renewed as another: %+v", q)
	}

	if err := SignOutStepFun("ai"); err != nil || StepFunSignedIn("ai") {
		t.Fatalf("signed out: %v", err)
	}
}

// What the user pastes: the bookmarklet's copy for the right site, or the
// Oasis-Token cookie as developer tools show it; the other site's copy and
// anything else are refused, and nothing is kept.
func TestSaveStepFunPaste(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFakeStepFun(t, "com")
	ctx := context.Background()
	acc, ref := stepfunTokens("4161", time.Now().Add(time.Hour), true)
	f.access.Store(acc)

	for _, bad := range []string{
		"",
		"hello",
		stepfunPaste + `{"site":"ai","access":"` + acc + `","refresh":"` + ref + `"}`,
		stepfunPaste + `{not json`,
	} {
		if err := SaveStepFunPaste(ctx, "com", bad); err == nil || StepFunSignedIn("com") {
			t.Fatalf("%.40q was kept: %v", bad, err)
		}
	}
	// the page's web id is the refresh token's device, even when the
	// bookmarklet found none
	if err := SaveStepFunPaste(ctx, "com", "  "+stepfunPaste+`{"site":"com","access":"`+acc+`","refresh":"`+ref+`","webId":""}`+"\n"); err != nil || !StepFunSignedIn("com") {
		t.Fatalf("the bookmarklet's copy: %v", err)
	}
	if s := loadStepFun()["com"]; s.WebID != "web-1" || s.Account != "4161" {
		t.Errorf("kept %+v", s)
	}
	SignOutStepFun("com")
	if err := SaveStepFunPaste(ctx, "com", "Oasis-Token="+acc+"..."+ref+"; Path=/"); err != nil || !StepFunSignedIn("com") {
		t.Fatalf("the cookie: %v", err)
	}
}

// The bookmarklet knows both sites and the app id each page sends.
func TestStepFunBookmarklet(t *testing.T) {
	b := StepFunBookmarklet()
	for _, want := range []string{"javascript:", `"https://platform.stepfun.ai":["ai","20700"]`, `"https://platform.stepfun.com":["com","10300"]`, stepfunPaste, "PassportService/RefreshToken"} {
		if !strings.Contains(b, want) {
			t.Errorf("bookmarklet lacks %s", want)
		}
	}
}
