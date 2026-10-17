package provider

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

func TestBalanceReaders(t *testing.T) {
	for _, c := range []struct {
		name string
		read func([]byte) (string, error)
		body string
		want string
	}{
		{"deepseek", readDeepSeek, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00"}]}`, "¥110.00"},
		{"deepseek two", readDeepSeek, `{"balance_infos":[{"currency":"CNY","total_balance":"1"},{"currency":"USD","total_balance":"2.5"}]}`, "¥1.00 · $2.50"},
		{"deepseek two, USD first", readDeepSeek, `{"balance_infos":[{"currency":"USD","total_balance":"2.5"},{"currency":"CNY","total_balance":"1"}]}`, "¥1.00 · $2.50"},
		{"deepseek, the CNY spent", readDeepSeek, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"-0.05","granted_balance":"0.00","topped_up_balance":"-0.05"},{"currency":"USD","total_balance":"11.12","granted_balance":"0.00","topped_up_balance":"11.12"}]}`, "$11.12 · ¥-0.05"},
		{"kimi", readMoonshot("¥"), `{"code":0,"data":{"available_balance":49.58894,"voucher_balance":46.5,"cash_balance":3.0},"status":true}`, "¥49.59"},
		{"openrouter", readOpenRouter, `{"data":{"total_credits":20,"total_usage":3.5}}`, "$16.50"},
		{"commandcode", readCommandCode, `{"credits":{"monthlyCredits":12.3,"purchasedCredits":2,"freeCredits":0},"windowLimits":{"limited":true,"fiveHour":{"used":4.2,"cap":10},"weekly":{"used":9,"cap":50}}}`, "$14.30"},
		{"commandcode credits only", readCommandCode, `{"credits":{"monthlyCredits":"70"},"windowLimits":null}`, "$70.00"},
		{"siliconflow", readSiliconFlow("¥"), `{"code":20000,"data":{"balance":"0.88","totalBalance":"88.88"}}`, "¥88.88"},
		{"siliconflow international", readSiliconFlow("$"), `{"code":20000,"data":{"balance":"0.88","chargeBalance":"88.00","totalBalance":"88.88"}}`, "$88.88"},
		{"stepfun", readStepFun("¥"), `{"object":"account","type":"prepaid","balance":26.00,"total_cash_balance":0.00,"total_voucher_balance":26.00}`, "¥26.00"},
		{"stepfun intl", readStepFun("$"), `{"object":"account","type":"prepaid","balance":0.00,"total_cash_balance":0.00,"total_voucher_balance":0.00}`, "$0.00"},
	} {
		got, err := c.read([]byte(c.body))
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
	if _, err := readDeepSeek([]byte(`{"error":{"message":"bad key"}}`)); err == nil {
		t.Error("deepseek: no balance read as one")
	}
}

func TestReadBalancePath(t *testing.T) {
	body := []byte(`{"code":true,"credits":{"monthlyCredits":42},"data":{"total_available":2500000,"name":"x","list":[{"left":"7.5"}]}}`)
	for path, want := range map[string]string{
		"data.total_available":            "2500000.00",
		"$ data.total_available / 500000": "$5.00",
		"¥data.list.0.left":               "¥7.50",
		"data.name":                       "x",
		" data.total_available/1000000 ":  "2.50",
		"(1-credits.monthlyCredits/70)%":  "40%",
		"$ (data.total_available - data.list.0.left * 100000) / 500000": "$3.50",
		"credits.monthlyCredits * 2 + 1":                                "85.00",
		"-credits.monthlyCredits":                                       "-42.00",
		"credits.monthlyCredits / 70 %":                                 "60%",
	} {
		if got, err := readBalancePath(body, path); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", path, got, err, want)
		}
	}
	cc := []byte(`{"credits":{"monthlyCredits":70.0},"windowLimits":{"fiveHour":{"used":0,"cap":14},"weekly":{"used":1.19,"cap":35}}}`)
	for path, want := range map[string]string{
		"5h: windowLimits.fiveHour.used / windowLimits.fiveHour.cap %; week: windowLimits.weekly.used/windowLimits.weekly.cap %; $credits.monthlyCredits": "5h 0% · week 3.4% · $70.00",
		"credits.monthlyCredits;":         "70.00",
		" left : $credits.monthlyCredits": "left $70.00",
	} {
		if got, err := readBalancePath(cc, path); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", path, got, err, want)
		}
	}
	for _, path := range []string{";", "5h: windowLimits.hour.used; week: windowLimits.weekly.used / 0", "week:"} {
		if got, err := readBalancePath(cc, path); err == nil {
			t.Errorf("%q: read %q, want an error", path, got)
		}
	}
	for _, path := range []string{"", "data.missing", "data.list.3.left", "data.total_available / zero", "data.list", "data.name + 1", "(data.total_available", "data.total_available / 0", "data.total_available 2", "%"} {
		if got, err := readBalancePath(body, path); err == nil {
			t.Errorf("%q: read %q, want an error", path, got)
		}
	}
}

// A relay's subscriptions are a list: a balance path sums, or takes the
// max of, every item's or those that match (#1993, RicardoWYP's
// /api/subscription/self, as pasted), and one amount of several that the
// reply hasn't (a second subscription that isn't there, a weekly cap of 0)
// is "—" while the others still show.
func TestBalancePathLists(t *testing.T) {
	one := []byte(`{
  "data": {
    "subscriptions": [
      { "subscription": { "status": "active", "amount_total": 50000000, "amount_used": 31593277, "weekly_amount_total": 0, "weekly_amount_used": 0 } }
    ],
    "all_subscriptions": [ { "subscription": { "status": "expired", "...": "..." } } ]
  }
}`)
	two := []byte(`{"data":{"subscriptions":[
  {"subscription":{"status":"active","amount_total":50000000,"amount_used":31593277,"weekly_amount_total":0,"weekly_amount_used":0}},
  {"subscription":{"status":"active","amount_total":"10000000","amount_used":5000000,"weekly_amount_total":4000000,"weekly_amount_used":1000000}},
  {"subscription":{"status":"expired","amount_total":90000000,"amount_used":90000000,"weekly_amount_total":1000000,"weekly_amount_used":1000000}}]}}`)
	const (
		used  = "sum(data.subscriptions[subscription.status=active].subscription.amount_used) / sum(data.subscriptions[subscription.status=active].subscription.amount_total) %"
		week  = "max(data.subscriptions[subscription.status=active].subscription.weekly_amount_used / data.subscriptions[subscription.status=active].subscription.weekly_amount_total) %"
		count = "count(data.subscriptions.*.subscription[status='active'])"
	)
	for _, c := range []struct {
		body []byte
		path string
		want string
	}{
		{one, used, "63.2%"},
		{two, used, "61%"},
		{one, "$ sum(data.subscriptions.*.subscription.amount_used) / 500000", "$63.19"},
		{two, "max(data.subscriptions.*.subscription.amount_used)", "90000000.00"},
		{two, "min(data.subscriptions.*.subscription.amount_used)", "5000000.00"},
		{two, "count(data.subscriptions[subscription.status!=active])", "1.00"},
		{one, "sum(data.all_subscriptions.*.subscription.amount_used)", "0.00"},
		{two, week, "25%"},
		{two, count, "2.00"},
		{two, "sum(data.subscriptions.*.subscription[status=active].amount_total) / 1000000", "60.00"},
		{two, "max(data.subscriptions.*.subscription.weekly_amount_used / data.subscriptions.*.subscription.weekly_amount_total) %", "100%"},
		{two, "sum(data.subscriptions[subscription.status=active].subscription.amount_used) * 2 / 1000000", "73.19"},
		// all of a list's items, as they are
		{[]byte(`[{"left":1},{"left":2.5}]`), "sum(*.left)", "3.50"},
		{[]byte(`{"a":{"x":{"n":1},"y":{"n":4}}}`), "max(a.*.n)", "4.00"},
		// one amount the reply hasn't is "—", the others still show
		{one, "used: " + used + "; week: " + week, "used 63.2% · week —"},
		{one, "first: data.subscriptions.0.subscription.amount_used; second: data.subscriptions.1.subscription.amount_used", "first 31593277.00 · second —"},
		{one, "all: data.subscriptions.0.subscription.amount_used + data.subscriptions.1.subscription.amount_used; used: " + used, "all — · used 63.2%"},
	} {
		if got, err := readBalancePath(c.body, c.path); err != nil || got != c.want {
			t.Errorf("%q: %q %v, want %q", c.path, got, err, c.want)
		}
	}
	for _, path := range []string{
		// several values where one is wanted: a sum, not a guess
		"data.subscriptions.*.subscription.amount_used",
		"data.subscriptions[subscription.status=active].subscription.amount_used / 2",
		"data.subscriptions.*.subscription.amount_used %",
		// a max of nothing, a list beside one of another length
		"max(data.all_subscriptions.*.subscription.amount_used)",
		// one alone that can't be read still fails, as does every one of several
		week,
		"week: " + week + "; second: data.subscriptions.1.subscription.amount_used",
		"sum(data.subscriptions.*.subscription.amount_used",
		"sum(data.subscriptions[status.amount_used)",
		"sum(data.subscriptions[status].amount_used)",
	} {
		if got, err := readBalancePath(one, path); err == nil {
			t.Errorf("%q: read %q, want an error", path, got)
		}
	}
	if got, err := readBalancePath(two, "sum(data.subscriptions.*.subscription.amount_used + data.subscriptions[subscription.status=active].subscription.amount_total)"); err == nil {
		t.Errorf("lists of 3 and 2 put together: read %q, want an error", got)
	}
	// the card's amount says why it is "—"
	parts, err := readBalanceParts(one, "used: "+used+"; week: "+week)
	if err != nil || len(parts) != 2 || parts[1].Text != "—" || !strings.Contains(parts[1].Error, "nothing to take it of") || parts[0].Error != "" {
		t.Errorf("parts: %+v %v", parts, err)
	}
}

func TestBalanceSourceByHost(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.deepseek.com/v1":            "https://api.deepseek.com/user/balance",
		"https://api.moonshot.cn/v1":             "https://api.moonshot.cn/v1/users/me/balance",
		"https://openrouter.ai/api/v1":           "https://openrouter.ai/api/v1/credits",
		"https://api.siliconflow.cn/v1":          "https://api.siliconflow.cn/v1/user/info",
		"https://api.siliconflow.com/v1":         "https://api.siliconflow.com/v1/user/info",
		"https://api.commandcode.ai/provider/v1": "https://api.commandcode.ai/alpha/billing/credits",
		"https://api.stepfun.com/step_plan/v1":   "https://api.stepfun.com/v1/accounts",
		"https://api.stepfun.ai/v1":              "https://api.stepfun.ai/v1/accounts",
		"https://ollama.com/v1":                  "https://ollama.com/api/balance",
		"http://localhost:11434/v1":              "",
		"https://relay.example.com/v1":           "",
		"https://api.deepseek.com.evil/":         "",
	} {
		src, ok := balanceSourceOf(Provider{Chat: base})
		if ok != (want != "") || src.url != want {
			t.Errorf("%s: %q %v, want %q", base, src.url, ok, want)
		}
	}
	// the provider's own endpoint wins over the host's
	if src, _ := balanceSourceOf(Provider{Chat: "https://api.deepseek.com/v1", BalanceURL: "https://x.example/b"}); src.url != "https://x.example/b" {
		t.Errorf("own endpoint: %q", src.url)
	}
}

// A relay's own endpoint, asked with each key it has on; a key the relay
// refuses says so on its card instead of failing the others.
func TestKeyBalances(t *testing.T) {
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
	keyBalanceCache.data = nil
	t.Cleanup(func() { keyBalanceCache.data = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Org") != "acme" {
			t.Errorf("custom header not sent: %v", r.Header)
		}
		switch r.Header.Get("Authorization") {
		case "Bearer sk-one":
			w.Write([]byte(`{"data":{"total_available":1000000}}`))
		case "Bearer sk-two":
			w.Write([]byte(`{"data":{"total_available":250000}}`))
		default:
			http.Error(w, `{"message":"invalid token"}`, http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-one", KeyName: "main",
		Keys:       []KeyAccount{{Name: "spare", Key: "sk-two"}, {Name: "gone", Key: "sk-bad"}, {Name: "off", Key: "sk-off", Off: true}},
		Headers:    map[string]string{"X-Org": "acme"},
		BalanceURL: srv.URL + "/api/usage/token", BalancePath: "$data.total_available / 500000"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "plain", Name: "Plain", Chat: "https://plain.example.com/v1", Key: "sk-p"}); err != nil {
		t.Fatal(err)
	}
	got := KeyBalances(context.Background())
	var lines []string
	for _, q := range got {
		lines = append(lines, q.Provider+"|"+q.User+"|"+q.Balance+"|"+strings.SplitN(q.Error, ":", 2)[0])
	}
	want := "relay|main|$2.00|,relay|spare|$0.50|,relay|gone||401 Unauthorized"
	if strings.Join(lines, ",") != want {
		t.Fatalf("balances:\n%s\nwant\n%s", strings.Join(lines, ","), want)
	}
	if got[0].Windows == nil {
		t.Fatal("windows is null in the JSON")
	}
	// each key's balance is kept over time, a failed one not
	bh := readBalanceHist()
	if pts := bh[quotaHistKey("relay", "main")]; len(pts) != 1 || pts[0].Amount != 2 {
		t.Fatalf("main's balance history = %+v", pts)
	}
	if pts := bh[quotaHistKey("relay", "spare")]; len(pts) != 1 || pts[0].Amount != 0.5 {
		t.Fatalf("spare's balance history = %+v", pts)
	}
	if len(bh) != 2 {
		t.Fatalf("balance history = %+v", bh)
	}
}

func TestReadAiHubMix(t *testing.T) {
	if got, err := readAiHubMix([]byte(`{"object":"list","total_usage":12.5}`)); err != nil || got != "$12.50" {
		t.Fatalf("got %q, %v", got, err)
	}
	// a key without a limit: -1 of AiHubMix's units
	if _, err := readAiHubMix([]byte(`{"object":"list","total_usage":-0.000002}`)); err == nil {
		t.Fatal("an unlimited key read as a balance")
	}
}

func TestAiHubMixAccountBalance(t *testing.T) {
	if got, err := readAiHubMixAccount([]byte(`{"success":true,"data":{"username":"x","quota":2500000}}`)); err != nil || got != "$5.00" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := readAiHubMixAccount([]byte(`{"success":false,"message":"no such token"}`)); err == nil || err.Error() != "no such token" {
		t.Fatalf("a refused token: %v", err)
	}
	p := Provider{Chat: "https://aihubmix.com/v1", Key: "sk-1"}
	if !TakesBalanceToken(p) || TakesBalanceToken(Provider{Chat: "https://api.deepseek.com"}) {
		t.Fatal("TakesBalanceToken")
	}
	if src, _ := balanceSourceOf(p); src.token != "" || !strings.HasSuffix(src.url, "/dashboard/billing/remain") {
		t.Fatalf("without a token: %+v", src)
	}
	p.BalanceToken = "tok"
	if src, _ := balanceSourceOf(p); src.token != "tok" || !strings.HasSuffix(src.url, "/api/user/self") {
		t.Fatalf("with a token: %+v", src)
	}
}

// A new-api relay tells the account's quota at /api/user/self to its
// access token and the user's id in New-Api-User, not to a key.
func TestNamedBalanceWithAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/self" || r.Header.Get("Authorization") != "tok" || r.Header.Get("New-Api-User") != "42" {
			http.Error(w, `{"success":false}`, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"success":true,"data":{"quota":1500000,"used_quota":10}}`))
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-one", Headers: map[string]string{"New-Api-User": "42"},
		BalanceURL: srv.URL + "/api/user/self", BalancePath: "$data.quota / 500000"}
	if !TakesBalanceToken(p) {
		t.Fatal("a named endpoint takes a token")
	}
	if _, _, err := Balance(context.Background(), p); err == nil {
		t.Fatal("the key was taken for the access token")
	}
	p.BalanceToken = "tok"
	if got, ok, err := Balance(context.Background(), p); err != nil || !ok || got != "$3.00" {
		t.Fatalf("balance = %q %v %v", got, ok, err)
	}
}

// A sub2api panel tells the account's balance to its login JWT, as a bearer.
func TestNamedBalanceWithSub2APIJWT(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user/profile" || r.Header.Get("Authorization") != "Bearer "+jwt {
			http.Error(w, `{"code":401}`, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"code":0,"data":{"balance":12.5}}`))
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-one",
		BalanceURL: srv.URL + "/api/v1/user/profile", BalancePath: "$data.balance", BalanceToken: jwt}
	if got, ok, err := Balance(context.Background(), p); err != nil || !ok || got != "$12.50" {
		t.Fatalf("balance = %q %v %v", got, ok, err)
	}
	p.BalanceToken = "Bearer " + jwt // pasted with its scheme
	if got, _, err := Balance(context.Background(), p); err != nil || got != "$12.50" {
		t.Fatalf("balance = %q %v", got, err)
	}
	if balanceAuthorization("tok") != "tok" {
		t.Fatal("a new-api access token goes as it is")
	}
}

// An access token beside new-api's /api/usage/token, which takes only the
// key, is never sent there: what to name in its place is said instead.
func TestKeyUsageWithAccessToken(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"success":false,"message":"无效的令牌"}`))
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-one", BalanceToken: "tok",
		BalanceURL: srv.URL + "/api/usage/token/", BalancePath: "$data.total_available / 500000"}
	_, ok, err := Balance(context.Background(), p)
	if !ok || err == nil || asked {
		t.Fatalf("ok %v, err %v, asked %v", ok, err, asked)
	}
	if want := srv.URL + "/api/user/self (Balance field $data.quota / 500000)"; !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "takes the API key, not the access token") {
		t.Fatalf("err = %v", err)
	}
	// the key alone still asks it
	p.BalanceToken = ""
	if _, _, err := Balance(context.Background(), p); !asked || err == nil {
		t.Fatalf("without the token: asked %v, err %v", asked, err)
	}
}

// A login token beside sub2api's /v1/usage, which takes only the key, is
// never sent there either: the panel's query for the account is named in
// its place, as new-api's is above.
func TestSub2APIKeyUsageWithLoginToken(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid API key"}`))
	}))
	defer srv.Close()
	p := Provider{ID: "panel", Chat: srv.URL, Key: "sk-two",
		BalanceToken: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzIn0.sig",
		BalanceURL:   srv.URL + "/v1/usage"}
	_, ok, err := Balance(context.Background(), p)
	if !ok || err == nil || asked {
		t.Fatalf("ok %v, err %v, asked %v", ok, err, asked)
	}
	if want := srv.URL + "/api/v1/user/profile (Balance field $data.balance)"; !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "takes the API key, not the login token") {
		t.Fatalf("err = %v", err)
	}
	// the key alone still asks it
	p.BalanceToken = ""
	if _, _, err := Balance(context.Background(), p); !asked || err == nil {
		t.Fatalf("without the token: asked %v, err %v", asked, err)
	}
}

// new-api's /api/user/self with its field left out reads the quota as
// new-api counts it, $1 to 500000.
func TestUserSelfDefaultField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"quota":1000000}}`))
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-one", BalanceToken: "tok", BalanceURL: srv.URL + "/api/user/self"}
	if got, _, err := Balance(context.Background(), p); err != nil || got != "$2.00" {
		t.Fatalf("balance = %q %v", got, err)
	}
	p.BalancePath = "data.quota"
	if got, _, err := Balance(context.Background(), p); err != nil || got != "1000000.00" {
		t.Fatalf("a field given is kept: %q %v", got, err)
	}
}

// new-api turns an access token without New-Api-User down with a 401 (and
// some builds, or a bad token, with a 200) of {"success":false,…}: the
// message is the error, with what to add, not a field missing.
func TestNewAPIUserRefusal(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   []string
	}{
		{401, `{"success":false,"message":"无权进行此操作，未提供 New-Api-User"}`, []string{"401 Unauthorized: ", "add the header New-Api-User = your user ID", "未提供 New-Api-User"}},
		{200, `{"success":false,"message":"Unauthorized, New-Api-User header not provided"}`, []string{"add the header New-Api-User = your user ID", "header not provided"}},
		{401, `{"success":false,"message":"无权进行此操作，New-Api-User 与登录用户不匹配"}`, []string{"add the header New-Api-User", "不匹配"}},
		{200, `{"success":false,"message":"无权进行此操作，access token 无效"}`, []string{"access token 无效"}},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		}))
		p := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-one", BalanceToken: "tok",
			BalanceURL: srv.URL + "/api/user/self", BalancePath: "$data.quota / 500000"}
		_, ok, err := Balance(context.Background(), p)
		srv.Close()
		if !ok || err == nil {
			t.Fatalf("%s: ok %v, err %v", c.body, ok, err)
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q lacks %q", c.body, err, w)
			}
		}
		if strings.Contains(err.Error(), "nothing at") {
			t.Errorf("%s: read as a missing field: %v", c.body, err)
		}
		if !strings.Contains(c.body, "New-Api-User") && strings.Contains(err.Error(), "add the header") {
			t.Errorf("%s: told to add the header: %v", c.body, err)
		}
	}
}

// A vendor that takes the key in the Balance URL's query, or in a header of
// its own, is asked with each key in its place, so every key's card tells
// its own balance, not the one a key pasted into the URL has (#266).
func TestBalanceURLNamesTheKey(t *testing.T) {
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
	keyBalanceCache.data = nil
	t.Cleanup(func() { keyBalanceCache.data = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Key") != r.URL.Query().Get("apikey") {
			t.Errorf("header %q, query %q", r.Header.Get("X-Key"), r.URL.Query().Get("apikey"))
		}
		switch r.URL.Query().Get("apikey") {
		case "sk-one":
			w.Write([]byte(`{"balance":3}`))
		case "sk-two+/=":
			w.Write([]byte(`{"balance":7}`))
		default:
			http.Error(w, `{"message":"no such key"}`, http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-one", KeyName: "main",
		Keys:       []KeyAccount{{Name: "spare", Key: "sk-two+/="}},
		Headers:    map[string]string{"X-Key": "{apiKey}"},
		BalanceURL: srv.URL + "/query?apikey={key}", BalancePath: "$balance"}); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, q := range KeyBalances(context.Background()) {
		lines = append(lines, q.User+"|"+q.Balance+"|"+q.Error)
	}
	if want := "main|$3.00|,spare|$7.00|"; strings.Join(lines, ",") != want {
		t.Fatalf("balances:\n%s\nwant\n%s", strings.Join(lines, ","), want)
	}
	// a URL that can't be asked doesn't show the key in what it says
	_, _, err := Balance(context.Background(), Provider{Chat: "http://127.0.0.1:1/v1", Key: "sk-secret-123456",
		BalanceURL: "http://127.0.0.1:1/q?key={key}", BalancePath: "balance"})
	if err == nil || strings.Contains(err.Error(), "sk-secret-123456") {
		t.Fatalf("error: %v", err)
	}
}

// A known balance query read with its field left empty (#881): new-api's
// for a key and for the account, OpenAI's old credit grants, a sub2api
// panel's profile and its query for a key; a query magpie doesn't know
// still asks for the field.
func TestKnownBalanceFieldLeftEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/usage/token", "/api/usage/token/":
			w.Write([]byte(`{"code":true,"message":"ok","data":{"object":"token_usage","total_granted":2000000,"total_used":500000,"total_available":1500000,"unlimited_quota":false}}`))
		case "/api/user/self":
			w.Write([]byte(`{"success":true,"data":{"quota":1000000}}`))
		case "/v1/dashboard/billing/credit_grants", "/dashboard/billing/credit_grants":
			w.Write([]byte(`{"object":"credit_summary","total_granted":20,"total_used":7.5,"total_available":12.5}`))
		case "/api/v1/user/profile":
			w.Write([]byte(`{"code":0,"data":{"balance":4.2}}`))
		case "/v1/usage":
			// sub2api's reply for a key on the wallet, its key alone asked
			if r.Header.Get("Authorization") != "Bearer sk-one" {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":{"type":"authentication_error","message":"Invalid API key"}}`))
				return
			}
			w.Write([]byte(`{"mode":"unrestricted","isValid":true,"planName":"钱包余额","remaining":7.25,"unit":"USD","balance":7.25}`))
		default:
			w.Write([]byte(`{"left":1}`))
		}
	}))
	defer srv.Close()
	for _, c := range []struct{ path, token, want string }{
		{"/api/usage/token", "", "$3.00"},
		{"/api/usage/token/", "", "$3.00"},
		{"/api/user/self", "tok", "$2.00"},
		{"/v1/dashboard/billing/credit_grants", "", "$12.50"},
		{"/dashboard/billing/credit_grants", "", "$12.50"},
		{"/api/v1/user/profile", "eyJ.a.b", "$4.20"},
		{"/v1/usage", "", "$7.25"},
	} {
		p := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-one", BalanceURL: srv.URL + c.path, BalanceToken: c.token}
		if got, ok, err := Balance(context.Background(), p); err != nil || !ok || got != c.want {
			t.Errorf("%s: %q %v %v, want %q", c.path, got, ok, err, c.want)
		}
	}
	// a field the user gave still wins
	p := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-one", BalanceURL: srv.URL + "/api/usage/token", BalancePath: "data.total_used"}
	if got, _, err := Balance(context.Background(), p); err != nil || got != "500000.00" && got != "500000" {
		t.Errorf("own field: %q %v", got, err)
	}
	p = Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-one", BalanceURL: srv.URL + "/other"}
	if _, _, err := Balance(context.Background(), p); err == nil || !strings.Contains(err.Error(), "no balance path") {
		t.Errorf("unknown query: %v", err)
	}
}

// SiliconFlow retired /v1/user/info (#1094): its 410, as the reporter's card
// showed it, says the query is gone and where the balance and vouchers are
// read now, not the raw reply; a key it refuses still says so as it came.
func TestSiliconFlowRetiredBalanceQuery(t *testing.T) {
	var status int
	var body string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/user/info" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	for _, c := range []struct {
		base, console string
	}{
		{"https://api.siliconflow.cn/v1", "https://cloud.siliconflow.cn"},
		{"https://api.siliconflow.com/v1", "https://cloud.siliconflow.com"},
	} {
		p := Provider{ID: "siliconflow", Chat: c.base, Key: "sk-test"}
		status, body = http.StatusGone, `{"code":20092,"message":"This endpoint is deprecated and is no longer available.","data":null}`
		_, ok, err := Balance(context.Background(), p)
		if !ok || err == nil || !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), c.console) || strings.Contains(err.Error(), "410 Gone") {
			t.Errorf("%s 410: %v %v, want it retired and %s named", c.base, ok, err, c.console)
		}
		status, body = http.StatusUnauthorized, `{"code":30014,"data":null,"message":"Token is invalid."}`
		if _, _, err := Balance(context.Background(), p); err == nil || !strings.Contains(err.Error(), "Token is invalid.") || strings.Contains(err.Error(), "retired") {
			t.Errorf("%s 401: %v, want the refusal as it came", c.base, err)
		}
		status, body = http.StatusOK, `{"code":20000,"message":"OK","status":true,"data":{"id":"userid","balance":"0.88","chargeBalance":"88.00","totalBalance":"88.88"}}`
		if got, _, err := Balance(context.Background(), p); err != nil || !strings.HasSuffix(got, "88.88") {
			t.Errorf("%s 200: %q %v", c.base, got, err)
		}
	}
}

// A key usage query that takes only "Authorization: Bearer <key>"
// (lx_78654 on Discord: a header of "Bearer {token}" went as written, and
// the vendor said "Authorization header format must be 'Bearer {token}'").
// The key is put in for each of its names, and the key pasted as the
// balance token goes as a Bearer too, not bare as an access token.
func TestBalanceKeyAsBearer(t *testing.T) {
	const key = "sk-tt-1"
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":"INVALID_AUTH_HEADER","message":"Authorization header format must be 'Bearer {token}'"}`))
			return
		}
		w.Write([]byte(`{"data":{"remain":8.5}}`))
	}))
	defer srv.Close()
	base := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: key, BalanceURL: srv.URL + "/key/usage", BalancePath: "$data.remain"}
	for _, c := range []struct {
		name    string
		headers map[string]string
		token   string
	}{
		{name: "no header"},
		{name: "{token}", headers: map[string]string{"Authorization": "Bearer {token}"}},
		{name: "{key}", headers: map[string]string{"Authorization": "Bearer {key}"}},
		{name: "{API_KEY}", headers: map[string]string{"authorization": "Bearer {API_KEY}"}},
		{name: "{{apikey}}", headers: map[string]string{"Authorization": "Bearer {{apikey}}"}},
		{name: "${API_KEY}", headers: map[string]string{"Authorization": "Bearer ${API_KEY}"}},
		{name: "key as token", token: key},
		{name: "Bearer key as token", token: "Bearer " + key},
	} {
		p := base
		p.Headers, p.BalanceToken = c.headers, c.token
		got = nil
		if amount, ok, err := Balance(context.Background(), p); err != nil || !ok || amount != "$8.50" {
			t.Errorf("%s: balance = %q %v %v, sent %q", c.name, amount, ok, err, got)
		}
	}
	// the key in the URL is escaped there, under any of its names
	p := base
	p.BalanceURL = srv.URL + "/key/usage?k={TOKEN}"
	p.Key = "sk-tt 1"
	if u := withBalanceKey(p.BalanceURL, p.Key, true); !strings.HasSuffix(u, "?k=sk-tt+1") {
		t.Fatalf("url = %q", u)
	}
	// a token of the account's own, not the key, still goes as it is
	p = base
	p.BalanceToken = "tok"
	got = nil
	if _, _, err := Balance(context.Background(), p); err == nil || len(got) != 1 || got[0] != "tok" {
		t.Fatalf("an access token went as %q (%v)", got, err)
	}
}

// A balance endpoint that answers only a POST is asked with one, with no
// body, when the provider says so (#2368, yaofeng); a GET is still what
// every other Balance URL gets, and a method that is neither is refused
// rather than sent. The reply's amount comes as a string, read as any other
// field is.
func TestBalanceMethod(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		asked = append(asked, r.Method+" "+string(b))
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			w.Write([]byte(`{"error":"Method not allowed"}`))
			return
		}
		w.Write([]byte(`{"usd_balance":"0.94306040","nano_balance":"1.5"}`))
	}))
	defer srv.Close()
	base := Provider{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-1", BalanceURL: srv.URL + "/api/check-balance", BalancePath: "$usd_balance"}
	for _, c := range []struct {
		method, want string
		ok           bool
	}{
		{"POST", "POST ", true},
		{" post ", "POST ", true},
		{"", "GET ", false},
		{"GET", "GET ", false},
	} {
		p := base
		p.BalanceMethod = c.method
		asked = nil
		amount, ok, err := Balance(context.Background(), p)
		if len(asked) != 1 || asked[0] != c.want {
			t.Errorf("%q: asked %q, want %q", c.method, asked, c.want)
		}
		if c.ok && (err != nil || !ok || amount != "$0.94") {
			t.Errorf("%q: balance = %q %v %v", c.method, amount, ok, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: a 405 read as %q", c.method, amount)
		}
	}
	for _, m := range []string{"PUT", "DELETE", "POST\r\nX: y"} {
		p := base
		p.BalanceMethod = m
		asked = nil
		if _, _, err := Balance(context.Background(), p); err == nil || len(asked) != 0 {
			t.Errorf("%q: err %v, asked %q", m, err, asked)
		}
	}
}

// DeepSeek's card tells its granted credit apart from the money topped
// up (wwcraft_ on Discord), under the total, which stays the figure the
// trend follows and an alert reads; an account with nothing granted has
// no split, all of it topped up.
func TestDeepSeekBalanceSplit(t *testing.T) {
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
	keyBalanceCache.data = nil
	t.Cleanup(func() { keyBalanceCache.data = nil })

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.deepseek.com" || r.URL.Path != "/user/balance" {
			http.NotFound(w, r)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer sk-gift":
			w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"}]}`))
		case "Bearer sk-two":
			w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"0.00","granted_balance":"0.00","topped_up_balance":"0.00"},{"currency":"CNY","total_balance":"7.50","granted_balance":"2.50","topped_up_balance":"5.00"}]}`))
		default:
			w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"42.00","granted_balance":"0.00","topped_up_balance":"42.00"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	if err := Save(Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com", Key: "sk-gift", KeyName: "gift",
		Keys: []KeyAccount{{Name: "two", Key: "sk-two"}, {Name: "paid", Key: "sk-paid"}}}); err != nil {
		t.Fatal(err)
	}
	got := map[string]SubscriptionQuota{}
	for _, q := range KeyBalances(context.Background()) {
		got[q.User] = q
	}
	gift := got["gift"]
	if gift.Error != "" || gift.Balance != "¥110.00" || len(gift.BalanceParts) != 0 {
		t.Fatalf("gift = %+v", gift)
	}
	if w := "granted ¥10.00 · topped up ¥100.00"; SplitWords(gift.BalanceSplit) != w {
		t.Fatalf("gift's split = %q, want %q", SplitWords(gift.BalanceSplit), w)
	}
	// two currencies: the one with money first, its split alone
	if two := got["two"]; two.Balance != "¥7.50 · $0.00" || SplitWords(two.BalanceSplit) != "granted ¥2.50 · topped up ¥5.00" {
		t.Fatalf("two = %q %q", two.Balance, SplitWords(two.BalanceSplit))
	}
	if paid := got["paid"]; paid.Balance != "¥42.00" || paid.BalanceSplit != nil {
		t.Fatalf("paid = %q %+v, want no split", paid.Balance, paid.BalanceSplit)
	}
	// the total is still kept over time
	if pts := readBalanceHist()[quotaHistKey("deepseek", "gift")]; len(pts) != 1 || pts[0].Amount != 110 {
		t.Fatalf("gift's balance history = %+v", pts)
	}
}
