package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
		{"kimi", readMoonshot("¥"), `{"code":0,"data":{"available_balance":49.58894,"voucher_balance":46.5,"cash_balance":3.0},"status":true}`, "¥49.59"},
		{"openrouter", readOpenRouter, `{"data":{"total_credits":20,"total_usage":3.5}}`, "$16.50"},
		{"commandcode", readCommandCode, `{"credits":{"monthlyCredits":12.3,"purchasedCredits":2,"freeCredits":0},"windowLimits":{"limited":true,"fiveHour":{"used":4.2,"cap":10},"weekly":{"used":9,"cap":50}}}`, "$14.30"},
		{"commandcode credits only", readCommandCode, `{"credits":{"monthlyCredits":"70"},"windowLimits":null}`, "$70.00"},
		{"siliconflow", readSiliconFlow("¥"), `{"code":20000,"data":{"balance":"0.88","totalBalance":"88.88"}}`, "¥88.88"},
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
	for _, path := range []string{";", "5h: windowLimits.hour.used; $credits.monthlyCredits", "week:"} {
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

func TestBalanceSourceByHost(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.deepseek.com/v1":            "https://api.deepseek.com/user/balance",
		"https://api.moonshot.cn/v1":             "https://api.moonshot.cn/v1/users/me/balance",
		"https://openrouter.ai/api/v1":           "https://openrouter.ai/api/v1/credits",
		"https://api.siliconflow.cn/v1":          "https://api.siliconflow.cn/v1/user/info",
		"https://api.commandcode.ai/provider/v1": "https://api.commandcode.ai/alpha/billing/credits",
		"https://api.stepfun.com/step_plan/v1":   "https://api.stepfun.com/v1/accounts",
		"https://api.stepfun.ai/v1":              "https://api.stepfun.ai/v1/accounts",
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
