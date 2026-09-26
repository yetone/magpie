package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
		{"siliconflow", readSiliconFlow("¥"), `{"code":20000,"data":{"balance":"0.88","totalBalance":"88.88"}}`, "¥88.88"},
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
		"https://api.deepseek.com/v1":    "https://api.deepseek.com/user/balance",
		"https://api.moonshot.cn/v1":     "https://api.moonshot.cn/v1/users/me/balance",
		"https://openrouter.ai/api/v1":   "https://openrouter.ai/api/v1/credits",
		"https://api.siliconflow.cn/v1":  "https://api.siliconflow.cn/v1/user/info",
		"https://relay.example.com/v1":   "",
		"https://api.deepseek.com.evil/": "",
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
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
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
