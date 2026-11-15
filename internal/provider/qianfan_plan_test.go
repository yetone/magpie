package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// qianfanResourceBody is the console's tokenPlanPersonal/resource reply in
// the shape Baidu's console page reads it (its bundle's item mapping:
// planType, resourceStatus, billingMode "credit", usedCredits and
// totalCredits as the page's Number() takes them, expiresAt, autoRenew,
// apiKey), with an item Released beside the running one.
const qianfanResourceBody = `{"success":true,"status":200,"result":{"items":[` +
	`{"resourceId":"tp-old","planType":"lite","resourceStatus":"Released","billingMode":"credit","usedCredits":"6600","totalCredits":"6600","expiresAt":"2026-10-01T00:00:00Z","autoRenew":false,"apiKey":"bce-v3/ALTAK-old/secret-old"},` +
	`{"resourceId":"tp-1","planType":"pro","resourceStatus":"Running","billingMode":"credit","usedCredits":"11250.5","totalCredits":45000,"usedTokens":0,"totalTokens":0,"expiresAt":"2026-12-01T00:00:00Z","autoRenew":true,"renewTimeUnit":"month","renewTime":1,"apiKey":"bce-v3/ALTAK-live/secret-live"}]}}`

// fakeQianfanConsole is Baidu Cloud's console: it answers the plan's
// resource to its one session's cookies, asked as the page asks, and sends
// anything else to the sign-in page, as the real console does.
type fakeQianfanConsole struct {
	srv    *httptest.Server
	cookie atomic.Value // the Cookie header it takes now
	body   atomic.Value
	asked  atomic.Int32
}

func newFakeQianfanConsole(t *testing.T) *fakeQianfanConsole {
	t.Helper()
	f := &fakeQianfanConsole{}
	f.cookie.Store("BDUSS=bd-1; bce-session=s-1; bce-user-info=u-1")
	f.body.Store(qianfanResourceBody)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.asked.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != qianfanResourcePath || r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Cookie") != f.cookie.Load().(string) {
			http.Redirect(w, r, "https://login.bce.baidu.com/?redirect=x", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(f.body.Load().(string)))
	}))
	t.Cleanup(f.srv.Close)
	old := qianfanConsole
	qianfanConsole = f.srv.URL
	t.Cleanup(func() { qianfanConsole = old })
	oldNow := qianfanNow
	qianfanNow = func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { qianfanNow = oldNow })
	return f
}

func TestQianfanPlan(t *testing.T) {
	for base, want := range map[string]bool{
		"https://qianfan.baidubce.com/v2/tokenplan/personal":        true,
		"https://qianfan.baidubce.com/anthropic/tokenplan/personal": true,
		"https://qianfan.baidubce.com/v2/tokenplan/team":            false,
		"https://qianfan.baidubce.com/v2":                           false,
		"https://example.com/v2/tokenplan/personal":                 false,
	} {
		if got := QianfanPlan(Provider{Chat: base}); got != want {
			t.Errorf("%s: %v, want %v", base, got, want)
		}
	}
	p := Provider{ID: "baidu-qianfan", Key: "bce-v3/k", Chat: "https://qianfan.baidubce.com/v2/tokenplan/personal"}
	if got := accountPlanOf(p); got != "qianfan" {
		t.Errorf("accountPlanOf: %q, want qianfan", got)
	}
}

// The running plan's month: what it used of its credits (or tokens, a
// token plan's), named as the console names it, to its end. A plan
// Released or past its end is none; a reply with no plan, or without the
// amounts, is an error and never a window with nothing left.
func TestReadQianfanPlan(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	var r struct{ Result json.RawMessage }
	if err := json.Unmarshal([]byte(qianfanResourceBody), &r); err != nil {
		t.Fatal(err)
	}
	plan, until, renew, w, err := readQianfanPlan(r.Result, now)
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if err != nil || plan != "Token Plan Pro" || renew != "auto" || until == nil || !until.Equal(end) {
		t.Fatalf("%q %v %q %v", plan, until, renew, err)
	}
	if w.Name != "Month" || w.Unit != "credits" || w.Amount != 11250.5 || w.Limit != 45000 || w.Used < 24.99 || w.Used > 25.01 || w.ResetsAt == nil || !w.ResetsAt.Equal(end) {
		t.Fatalf("window: %+v", w)
	}

	// a token plan, its end in the console's own "2006-01-02 15:04:05",
	// Beijing time
	_, until, renew, w, err = readQianfanPlan([]byte(`{"items":[{"planType":"max","resourceStatus":"Running","billingMode":"token","usedTokens":70000000,"totalTokens":700000000,"expiresAt":"2026-12-01 00:00:00","autoRenew":false}]}`), now)
	if err != nil || w.Unit != "tokens" || w.Used < 9.99 || w.Used > 10.01 || renew != "off" || until == nil || !until.Equal(time.Date(2026, 11, 30, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("token plan: %+v %v %q %v", w, until, renew, err)
	}

	for name, body := range map[string]string{
		"no plan":      `{"items":[]}`,
		"released":     `{"items":[{"planType":"pro","resourceStatus":"Released","billingMode":"credit","usedCredits":0,"totalCredits":45000,"expiresAt":"2026-12-01T00:00:00Z"}]}`,
		"past its end": `{"items":[{"planType":"pro","resourceStatus":"Running","billingMode":"credit","usedCredits":0,"totalCredits":45000,"expiresAt":"2026-11-01T00:00:00Z"}]}`,
		"no amounts":   `{"items":[{"planType":"pro","resourceStatus":"Running","billingMode":"credit","expiresAt":"2026-12-01T00:00:00Z"}]}`,
		"no total":     `{"items":[{"planType":"pro","resourceStatus":"Running","billingMode":"credit","usedCredits":"5","totalCredits":"0","expiresAt":"2026-12-01T00:00:00Z"}]}`,
		"not JSON":     `<html>`,
	} {
		if _, _, _, w, err := readQianfanPlan([]byte(body), now); err == nil || w.Limit != 0 || w.Used != 0 {
			t.Errorf("%s: %+v %v, want an error", name, w, err)
		}
	}
}

// What the user pastes: the bookmarklet's copy, the Cookie header as
// developer tools show it, or the request copied as cURL; the console is
// asked with it before it is kept, and anything else is refused with
// nothing kept.
func TestSaveQianfanPaste(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFakeQianfanConsole(t)
	ctx := context.Background()
	cookie := f.cookie.Load().(string)

	for _, bad := range []string{
		"",
		"hello",
		qianfanPaste + "{",
		"BDUSS=other; bce-session=gone", // the console sends it to sign in
		"Cookie: BDUSS=bd-1\r\nX-Evil: 1",
	} {
		if err := SaveQianfanPaste(ctx, "baidu-qianfan", bad); err == nil {
			t.Errorf("%q was kept", bad)
		}
		if QianfanSignedIn("baidu-qianfan") {
			t.Fatalf("%q signed in", bad)
		}
	}
	if err := SaveQianfanPaste(ctx, "baidu-qianfan", "BDUSS=other"); err == nil || !strings.Contains(err.Error(), "developer tools") {
		t.Errorf("a refused bookmarklet copy should say where the whole cookie is: %v", err)
	}

	copied, _ := json.Marshal(map[string]string{"cookie": cookie})
	for _, good := range []string{
		qianfanPaste + string(copied),
		"Cookie: " + cookie,
		"cookie: " + cookie + "\n",
		cookie,
		`curl 'https://console.bce.baidu.com/api/qianfan/charge/tokenPlanPersonal/resource' -H 'accept: application/json' -b '` + cookie + `' -H 'x-requested-with: XMLHttpRequest'`,
		`curl 'https://console.bce.baidu.com/api/qianfan/charge/tokenPlanPersonal/resource' -H 'Cookie: ` + cookie + `'`,
	} {
		if err := SignOutQianfan("baidu-qianfan"); err != nil {
			t.Fatal(err)
		}
		if err := SaveQianfanPaste(ctx, "baidu-qianfan", good); err != nil || !QianfanSignedIn("baidu-qianfan") {
			t.Fatalf("%.40q: %v", good, err)
		}
		if got := loadQianfan()["baidu-qianfan"].Cookie; got != cookie {
			t.Fatalf("%.40q kept %q", good, got)
		}
	}
	if fi, err := os.Stat(qianfanPath()); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("session file: %v %v", fi, err)
	}
	// the plan's own key, in the console's reply, is never kept
	if b, _ := os.ReadFile(qianfanPath()); strings.Contains(string(b), "bce-v3") || strings.Contains(string(b), "secret") {
		t.Fatalf("the plan's key was kept: %s", b)
	}
}

// The card is read with the provider's own sign-in. A session the console
// sends to sign in again is told as that, with no window: unknown, never
// nothing left.
func TestQianfanPlanCard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	forgetPlanQuotas()
	f := newFakeQianfanConsole(t)
	ctx := context.Background()
	p := Provider{ID: "baidu-qianfan", Name: "Baidu Qianfan", Icon: "baiducloud-color", Key: "bce-v3/k", Chat: "https://qianfan.baidubce.com/v2/tokenplan/personal"}

	if q := qianfanPlanQuota(ctx, p); q.Error == "" || len(q.Windows) != 0 {
		t.Fatalf("no sign-in: %+v", q)
	}
	if err := SaveQianfanPaste(ctx, p.ID, f.cookie.Load().(string)); err != nil {
		t.Fatal(err)
	}
	q := qianfanPlanQuota(ctx, p)
	if q.Error != "" || q.Plan != "Token Plan Pro" || q.Renew != "auto" || q.Until == nil || len(q.Windows) != 1 || q.Windows[0].Limit != 45000 {
		t.Fatalf("card: %+v", q)
	}
	if b, _ := json.Marshal(q); strings.Contains(string(b), "bce-v3") || strings.Contains(string(b), "secret") {
		t.Fatalf("the plan's key reached the card: %s", b)
	}
	kr, err := accountPlanWindows(ctx, p)
	if err != nil || len(kr.ws) != 1 || kr.plan != "Token Plan Pro" {
		t.Fatalf("routing's reading: %+v %v", kr, err)
	}

	// Baidu ends the session: the console answers with its sign-in page
	f.cookie.Store("BDUSS=new-one")
	q = qianfanPlanQuota(ctx, p)
	if q.Error != errQianfanSession.Error() || len(q.Windows) != 0 {
		t.Fatalf("session ended: %+v", q)
	}
	if _, err := accountPlanWindows(ctx, p); err == nil {
		t.Fatal("routing read an ended session as a reading")
	}

	// the console answers, but not success
	f.cookie.Store(loadQianfan()[p.ID].Cookie)
	f.body.Store(`{"success":false,"message":{"global":"系统繁忙"}}`)
	if q := qianfanPlanQuota(ctx, p); !strings.Contains(q.Error, "系统繁忙") || len(q.Windows) != 0 {
		t.Fatalf("refused: %+v", q)
	}

	// the sign-in goes with the provider's id, and with the provider
	if err := renameQianfan(p.ID, "qf2"); err != nil || QianfanSignedIn(p.ID) || !QianfanSignedIn("qf2") {
		t.Fatalf("renamed: %v", err)
	}
	if err := forgetQianfan("qf2"); err != nil || QianfanSignedIn("qf2") {
		t.Fatalf("forgotten: %v", err)
	}
}
