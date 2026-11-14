package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// The replies below are the shapes the QianWen CLI (@qianwenai/qianwen-cli
// 1.10.0) reads: device/code's is a real one (its token changed), the
// rest are what its loginPoll, GenerateCLIAccessToken and cs-data usage
// readers take, with BailianGateway.Login.NotLogined and ConsoleNeedLogin
// as the platform answered them to a request with no sign-in.

const qianwenCodeReply = `{"Data":{"Token":"devtok1","ExpiresIn":300,"VerificationUrl":"https://account.qianwenai.com/sso/OIDCAuth?app_type=qianwenai_cli&lang=en&auth_token=devtok1","Interval":2},"RequestId":"01A1D8E9-1E96-5A84-B19B-1EB6C862D2F9","Success":true}`

func qianwenUsageReply(month float64) string {
	b, _ := json.Marshal(map[string]any{
		"code": "200", "successResponse": true,
		"data": map[string]any{"success": true, "DataV2": map[string]any{
			"ret":  []string{"SUCCESS::调用成功"},
			"data": map[string]any{"code": "SUCCESS", "success": true, "data": map[string]any{"per1MonthPercentage": month, "per1MonthResetTime": 1780000000000}},
		}},
	})
	return string(b)
}

const qianwenNotLogged = `{"code":"200","successResponse":true,"data":{"success":false,"errorCode":"BailianGateway.Login.NotLogined","errorMsg":"not logined"}}`

// fakeQianwen is the platform's three hosts. polls is how many times
// device/token answers authorization_pending before its last reply.
type fakeQianwen struct {
	mu        sync.Mutex
	t         *testing.T
	challenge string
	polls     int
	last      string // device/token's reply once pending is done
	lastCode  int
	mints     map[string]string // access token → console token ("" refuses)
	usage     map[string]string // console token → reply
	minted    int
	reads     int
	asked     []string
}

func newFakeQianwen(t *testing.T) *fakeQianwen {
	f := &fakeQianwen{t: t, mints: map[string]string{}, usage: map[string]string{}, lastCode: 200}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	return f
}

func (f *fakeQianwen) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.t
	host := r.Header.Get("X-Host")
	f.asked = append(f.asked, host+r.URL.Path)
	if r.Method != http.MethodPost || r.Header.Get("User-Agent") != qianwenUA {
		t.Errorf("%s %s%s as %q", r.Method, host, r.URL.Path, r.Header.Get("User-Agent"))
	}
	q := r.URL.Query()
	switch host + r.URL.Path {
	case qianwenAuthHost + "/cli/device/code":
		if q.Get("client_id") == "" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			t.Errorf("device/code %s", r.URL.RawQuery)
		}
		f.challenge = q.Get("code_challenge")
		io.WriteString(w, qianwenCodeReply)
	case qianwenAuthHost + "/cli/device/token":
		sum := sha256.Sum256([]byte(q.Get("code_verifier")))
		if q.Get("token") != "devtok1" || q.Get("client_id") == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			t.Errorf("device/token %s (challenge %s)", r.URL.RawQuery, f.challenge)
		}
		if f.polls > 0 {
			f.polls--
			io.WriteString(w, `{"Data":{"Status":"authorization_pending"},"Success":true}`)
			return
		}
		w.WriteHeader(f.lastCode)
		io.WriteString(w, f.last)
	case qianwenAPIHost + "/data/v2/api.json":
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if r.Header.Get("Content-Type") != "application/json" || m["product"] != "ModelStudioRegional" || m["action"] != "GenerateCLIAccessToken" || m["region"] != "cn-beijing" || m["version"] != "2026-02-10" {
			t.Errorf("mint %s %q", r.Header.Get("Content-Type"), body)
		}
		access := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if strings.HasPrefix(access, "sk-sp-") {
			t.Errorf("the plan's key was sent: %s", access)
		}
		tok, ok := f.mints[access]
		if !ok || tok == "" {
			io.WriteString(w, `{"code":"ConsoleNeedLogin","message":"Please login first","successResponse":false}`)
			return
		}
		f.minted++
		io.WriteString(w, `{"code":"200","successResponse":true,"data":{"cliAccessToken":"`+tok+`"}}`)
	case qianwenDataHost + "/cli/api.json":
		f.reads++
		form, _ := url.ParseQuery(string(body))
		if q.Get("action") != "BroadScopeAspnGateway" || q.Get("product") != "sfm_bailian" || q.Get("api") != aliUsageAPI ||
			form.Get("region") != "cn-beijing" || !strings.Contains(form.Get("params"), "QIANWENAI") || !strings.Contains(form.Get("params"), aliUsageAPI) {
			t.Errorf("usage %s with %q", r.URL, body)
		}
		reply, ok := f.usage[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			reply = qianwenNotLogged
		}
		io.WriteString(w, reply)
	default:
		t.Errorf("asked %s %s", host, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func qianwenHome(t *testing.T) {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	old := qianwenPoll
	qianwenPoll = 5 * time.Millisecond
	t.Cleanup(func() { qianwenPoll = old })
	qianwenTokensMu.Lock()
	qianwenTokens = map[string]aliToken{}
	qianwenTokensMu.Unlock()
	forgetPlanQuotas()
	t.Cleanup(forgetPlanQuotas)
}

// waitQianwen waits for the provider's sign-in to end, as the GUI does.
func waitQianwen(t *testing.T, id string) (string, string) {
	t.Helper()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if s, why := QianwenSignInState(id); s != "pending" {
			return s, why
		}
	}
	t.Fatal("the sign-in never ended")
	return "", ""
}

func TestQianwenPlanHost(t *testing.T) {
	for base, want := range map[string]bool{
		"https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1":        true,
		"https://token-plan.maas.qianwenaiapi.com/apps/anthropic":            true,
		"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1": false,
		"https://token-plan.maas.qwencloudapi.com/compatible-mode/v1":        false,
		"https://dashscope.aliyuncs.com/compatible-mode/v1":                  false,
	} {
		if got := QianwenPlan(Provider{Chat: base}); got != want {
			t.Errorf("%s: %v", base, got)
		}
	}
	if QianwenPlan(Provider{Chat: "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1", Account: &Account{}}) {
		t.Error("an account is a plan's")
	}
	p, err := FromPreset("qwen-token-plan")
	if err != nil || !QianwenPlan(p) {
		t.Fatalf("the preset isn't the platform's plan: %v %+v", err, p)
	}
	p.Key = "sk-sp-x"
	if accountPlanOf(p) != "qianwen" {
		t.Errorf("accountPlanOf %q", accountPlanOf(p))
	}
}

func TestQianwenSignIn(t *testing.T) {
	qianwenHome(t)
	f := newFakeQianwen(t)
	f.polls = 2
	f.last = `{"Data":{"Status":"complete","Credentials":{"AccessToken":"acc1","ExpireTime":"2099-01-02T03:04:05Z","User":{"Id":"u1","Email":"a@example.com","AliyunId":"x"}}},"Success":true}`
	u, err := StartQianwenSignIn(context.Background(), "qwen-token-plan")
	if err != nil || !strings.HasPrefix(u, "https://account.qianwenai.com/sso/OIDCAuth?") {
		t.Fatalf("%q %v", u, err)
	}
	if s, why := waitQianwen(t, "qwen-token-plan"); s != "done" {
		t.Fatalf("%s %s", s, why)
	}
	if !QianwenSignedIn("qwen-token-plan") || QianwenAccount("qwen-token-plan") != "a@example.com" || QianwenSignedIn("other") {
		t.Fatal("not kept for the provider alone")
	}
	s, _ := qianwenSignIn("qwen-token-plan")
	if s.Access != "acc1" || s.User != "u1" || !s.Expires.Equal(time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("%+v", s)
	}
	if fi, err := os.Stat(qianwenPath()); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("qianwen-auth.json readable by others: %v %v", fi, err)
	}

	for name, c := range map[string]struct {
		code  int
		reply string
		want  string
	}{
		"refused":           {200, `{"Data":{"Status":"access_denied"},"Success":true}`, "refused"},
		"ran out":           {200, `{"Data":{"Status":"expired_token"},"Success":true}`, "ran out"},
		"ran out, as error": {400, `{"Code":"DeviceCode.Expired","Message":"token expired"}`, "ran out"},
		"no token":          {200, `{"Data":{"Status":"complete"},"Success":true}`, "gave no token"},
		"other error":       {500, `{"Code":"InternalError"}`, "500"},
	} {
		f.mu.Lock()
		f.polls, f.lastCode, f.last = 1, c.code, c.reply
		f.mu.Unlock()
		if _, err := StartQianwenSignIn(context.Background(), "p2"); err != nil {
			t.Fatal(err)
		}
		if s, why := waitQianwen(t, "p2"); s != "failed" || !strings.Contains(why, c.want) {
			t.Errorf("%s: %s %q", name, s, why)
		}
		if QianwenSignedIn("p2") {
			t.Errorf("%s: kept a sign-in", name)
		}
	}
	// the first provider's sign-in is still there
	if !QianwenSignedIn("qwen-token-plan") {
		t.Fatal("another provider's failed sign-in dropped this one")
	}

	// cancelled: nothing is kept, and the state is gone
	f.mu.Lock()
	f.polls = 1 << 20
	f.mu.Unlock()
	if _, err := StartQianwenSignIn(context.Background(), "p3"); err != nil {
		t.Fatal(err)
	}
	CancelQianwenSignIn("p3")
	if s, _ := QianwenSignInState("p3"); s != "" {
		t.Errorf("cancelled sign-in is %q", s)
	}

	if err := SignOutQianwen("qwen-token-plan"); err != nil || QianwenSignedIn("qwen-token-plan") {
		t.Fatalf("sign-out: %v", err)
	}
}

func saveQianwenSession(t *testing.T, id string, s qianwenSession) {
	t.Helper()
	qianwenMu.Lock()
	defer qianwenMu.Unlock()
	all := loadQianwen()
	all[id] = s
	if err := saveQianwen(all); err != nil {
		t.Fatal(err)
	}
}

func TestQianwenPlanQuotas(t *testing.T) {
	qianwenHome(t)
	f := newFakeQianwen(t)
	f.mints["acc1"] = "cli2"
	f.usage["cli2"] = qianwenUsageReply(0.25)
	// cli1 is a console token kept from before that the console no longer
	// takes: the reader makes another once
	qianwenTokensMu.Lock()
	qianwenTokens["qwen-token-plan"] = aliToken{"cli1", time.Now()}
	qianwenTokensMu.Unlock()

	const base = "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1"
	for _, p := range []Provider{
		{ID: "qwen-token-plan", Name: "Qwen Token Plan", Chat: base, Key: "sk-sp-key"},
		// not signed in: a card that says where to sign in
		{ID: "qwen-two", Name: "Qwen Two", Chat: base, Key: "sk-sp-key2"},
		// signed in, but the platform no longer takes the sign-in
		{ID: "qwen-old", Name: "Qwen Old", Chat: base, Key: "sk-sp-key3"},
		// signed in, past the time the platform gave it
		{ID: "qwen-late", Name: "Qwen Late", Chat: base, Key: "sk-sp-key4"},
		// Alibaba Cloud's plan: not this one
		{ID: "bailian", Name: "Bailian", Chat: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", Key: "sk-sp-key5"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	saveQianwenSession(t, "qwen-token-plan", qianwenSession{Access: "acc1", Email: "a@example.com", Expires: time.Now().Add(time.Hour)})
	saveQianwenSession(t, "qwen-old", qianwenSession{Access: "gone"})
	saveQianwenSession(t, "qwen-late", qianwenSession{Access: "acc1", Expires: time.Now().Add(-time.Minute)})

	got := map[string]SubscriptionQuota{}
	for _, q := range PlanQuotas(context.Background()) {
		if strings.HasPrefix(q.Provider, "qwen-") {
			got[q.Provider] = q
		}
	}
	if len(got) != 4 {
		t.Fatalf("%+v", got)
	}
	q := got["qwen-token-plan"]
	if q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Name != "Month" || q.Windows[0].Used != 25 ||
		q.Windows[0].ResetsAt.UnixMilli() != 1780000000000 || q.User != "a@example.com" || q.NeedsAccessKey {
		t.Fatalf("%+v", q)
	}
	if q := got["qwen-two"]; !q.NeedsAccessKey || !strings.HasPrefix(q.Error, "this plan's usage is told only to a sign-in on the Qwen AI platform") || !strings.Contains(q.Error, "Qwen Two") {
		t.Errorf("not signed in: %+v", q)
	}
	for _, id := range []string{"qwen-old", "qwen-late"} {
		if q := got[id]; !q.NeedsAccessKey || q.Error != errQianwenSession.Error() {
			t.Errorf("%s: %+v", id, q)
		}
	}
	f.mu.Lock()
	minted, reads := f.minted, f.reads
	f.mu.Unlock()
	// cli1 refused, cli2 made and read; "gone" refused at the mint, the
	// late one never asked
	if minted != 1 || reads != 2 {
		t.Errorf("minted %d, reads %d", minted, reads)
	}

	// the console token is kept: a second read makes none
	ws, err := qianwenReadUsage(context.Background(), "qwen-token-plan", time.Now)
	if err != nil || len(ws) != 1 {
		t.Fatalf("%v %+v", err, ws)
	}
	f.mu.Lock()
	if f.minted != 1 {
		t.Errorf("minted again: %d", f.minted)
	}
	// a fresh console token the console refuses is a sign-in ended, not a
	// loop
	f.usage = map[string]string{}
	f.mu.Unlock()
	qianwenForgetToken("qwen-token-plan")
	if _, err := qianwenReadUsage(context.Background(), "qwen-token-plan", time.Now); !errors.Is(err, errQianwenSession) {
		t.Errorf("fresh token refused: %v", err)
	}
	// an account with no Token Plan tells no window: said, not a 0% card
	f.mu.Lock()
	f.usage["cli2"] = strings.Replace(qianwenUsageReply(0), `"per1MonthPercentage":0,"per1MonthResetTime":1780000000000`, `"planName":null`, 1)
	f.mu.Unlock()
	if _, err := qianwenReadUsage(context.Background(), "qwen-token-plan", time.Now); err == nil || !strings.Contains(err.Error(), "told no window") {
		t.Errorf("no window: %v", err)
	}
}

// the sign-in goes with its provider: renamed, it follows; removed, it is
// gone, and no provider added later under the id has it
func TestQianwenFollowsProvider(t *testing.T) {
	qianwenHome(t)
	const base = "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1"
	if err := Save(Provider{ID: "qwen-a", Name: "Qwen A", Chat: base, Key: "sk-sp-a"}); err != nil {
		t.Fatal(err)
	}
	saveQianwenSession(t, "qwen-a", qianwenSession{Access: "acc1"})
	saveQianwenSession(t, "keep", qianwenSession{Access: "acc2"})
	if err := Rename("qwen-a", "qwen-b"); err != nil {
		t.Fatal(err)
	}
	if QianwenSignedIn("qwen-a") || !QianwenSignedIn("qwen-b") {
		t.Fatal("the sign-in didn't follow the rename")
	}
	if err := Delete("qwen-b"); err != nil {
		t.Fatal(err)
	}
	if QianwenSignedIn("qwen-b") || !QianwenSignedIn("keep") {
		t.Fatal("removing the provider didn't drop its sign-in alone")
	}
}

func TestQianwenTime(t *testing.T) {
	want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	for _, v := range []any{float64(want.Unix()), float64(want.UnixMilli()), "2026-12-01T00:00:00Z", strconvInt(want.UnixMilli())} {
		if got := qianwenTime(v); !got.Equal(want) {
			t.Errorf("%v: %v", v, got)
		}
	}
	if !qianwenTime("").IsZero() || !qianwenTime(nil).IsZero() || !qianwenTime("soon").IsZero() {
		t.Error("a time from nothing")
	}
}

func strconvInt(n int64) string { b, _ := json.Marshal(n); return string(b) }
