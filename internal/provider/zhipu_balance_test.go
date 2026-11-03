package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// zhipuReport is BigModel's account report as it answered a real key on
// 2026-11-03 (open.bigmodel.cn, www.bigmodel.cn and api.z.ai alike), the
// account never paid in.
const zhipuReport = `{"code":200,"msg":"操作成功","data":{"balance":0E-9,"rechargeAmount":0.000000,"giveAmount":0.000000,"totalSpendAmount":0E-9,"todaySpendAmount":null,"availableBalance":0E-9,"frozenBalance":0E-9,"creditBalance":null,"availableCreditBalance":null,"creditStatus":"NOT_OPEN","modelSpendAmountList":null,"isKA":false},"success":true}`

// zhipuReportFunded is the same reply of an account with money on it,
// some of it frozen.
const zhipuReportFunded = `{"code":200,"msg":"操作成功","data":{"balance":15.500000000,"rechargeAmount":20.000000,"giveAmount":0.000000,"totalSpendAmount":4.500000000,"todaySpendAmount":0.120000,"availableBalance":12.500000000,"frozenBalance":3.000000000,"creditBalance":null,"availableCreditBalance":null,"creditStatus":"NOT_OPEN","modelSpendAmountList":null,"isKA":false},"success":true}`

// zhipuReportRefused is its answer to a key it doesn't take, 200 too.
const zhipuReportRefused = `{"code":1000,"msg":"身份验证失败。","success":false}`

// fakeVendors sends every TLS request to srv, whatever host it names.
func fakeVendors(t *testing.T, srv *httptest.Server) {
	t.Helper()
	addr := srv.Listener.Addr().String()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

// nam.haid on Discord: a Zhipu key had no balance card at all. A
// pay-as-you-go key's card reads the account report at its own host, in
// that host's currency, with the key as a Bearer; a refused key says so
// in the words the plan's card uses; a GLM Coding Plan's key isn't asked,
// and one asked at only the Anthropic endpoint that was never paid in has
// no card.
func TestZhipuBalance(t *testing.T) {
	var mu sync.Mutex
	body := zhipuReport
	var asked []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Host+r.URL.Path+" "+r.Header.Get("Authorization"))
		b := body
		mu.Unlock()
		if r.URL.Path != zhipuAccountReport {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	fakeVendors(t, srv)
	set := func(b string) {
		mu.Lock()
		body, asked = b, nil
		mu.Unlock()
	}

	zhipu := Provider{ID: "zhipu", Preset: "zhipu", Chat: "https://open.bigmodel.cn/api/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic", Key: "abc.def"}
	zai := Provider{ID: "zai", Preset: "zai", Chat: "https://api.z.ai/api/paas/v4", Anthropic: "https://api.z.ai/api/anthropic", Key: "abc.def"}
	for _, c := range []struct {
		p    Provider
		body string
		want string
	}{
		{zhipu, zhipuReport, "¥0.00"},
		{zhipu, zhipuReportFunded, "¥12.50"},
		{zai, zhipuReportFunded, "$12.50"},
	} {
		set(c.body)
		got, ok, err := Balance(context.Background(), c.p)
		if !ok || err != nil || got != c.want {
			t.Errorf("%s: %q %v %v, want %q", c.p.ID, got, ok, err, c.want)
		}
		host := hostOf(c.p.Chat)
		if want := host + zhipuAccountReport + " Bearer abc.def"; len(asked) != 1 || asked[0] != want {
			t.Errorf("%s asked %q, want %q", c.p.ID, asked, want)
		}
	}

	set(zhipuReportRefused)
	for _, c := range []struct {
		p      Provider
		vendor string
	}{{zhipu, "Zhipu"}, {zai, "Z.ai"}} {
		_, ok, err := Balance(context.Background(), c.p)
		if !ok || err == nil || !strings.HasPrefix(err.Error(), c.vendor+" didn't take this key (身份验证失败。)") {
			t.Errorf("%s refused: %v %v", c.p.ID, ok, err)
		}
	}
	set(`{"code":500,"msg":"系统繁忙","success":false}`)
	if _, _, err := Balance(context.Background(), zhipu); err == nil || !strings.Contains(err.Error(), "code 500: 系统繁忙") {
		t.Errorf("other code: %v", err)
	}
	// a reply in another shape is unread, never a balance
	set(`{"code":200,"msg":"操作成功","data":{"cash":1},"success":true}`)
	if got, _, err := Balance(context.Background(), zhipu); err == nil || got != "" {
		t.Errorf("no availableBalance: %q %v", got, err)
	}

	// a GLM Coding Plan's key: its windows are its card, nothing asked
	set(zhipuReportFunded)
	for _, coding := range []Provider{
		{ID: "glm", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic", Key: "k"},
		{ID: "glm", Anthropic: "https://api.z.ai/api/coding/paas/v4", Key: "k"},
		zhipu.atKeyPlan("coding"),
	} {
		if _, ok, err := Balance(context.Background(), coding); ok || err != nil {
			t.Errorf("coding %s: %v %v", coding.Chat+coding.Anthropic, ok, err)
		}
	}
	if len(asked) != 0 {
		t.Errorf("coding plan asked %q", asked)
	}

	// at only the Anthropic endpoint, which a plan's key shares: an
	// account never paid in has no card, one with money its balance
	anth := Provider{ID: "glm", Anthropic: "https://open.bigmodel.cn/api/anthropic", Key: "k"}
	set(zhipuReport)
	if _, _, err := Balance(context.Background(), anth); !errors.Is(err, errNoBalance) {
		t.Errorf("anthropic, never paid in: %v", err)
	}
	set(zhipuReportFunded)
	if got, _, err := Balance(context.Background(), anth); err != nil || got != "¥12.50" {
		t.Errorf("anthropic, funded: %q %v", got, err)
	}
}

// The Usage page's cards: a Zhipu provider's pay-as-you-go key has its
// balance card and its Coding Plan key none; a SiliconFlow key, whose
// vendor retired its balance query, says so with its console to open
// (SeeAt) instead of a read that failed for now (nam.haid on Discord).
func TestZhipuAndSiliconFlowBalanceCards(t *testing.T) {
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
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == zhipuAccountReport && r.Header.Get("Authorization") == "Bearer payg.key":
			w.Write([]byte(zhipuReportFunded))
		case r.URL.Path == zhipuAccountReport:
			w.Write([]byte(zhipuReportRefused))
		case r.URL.Path == "/v1/user/info":
			w.WriteHeader(http.StatusGone)
			w.Write([]byte(`{"code":20092,"message":"This endpoint is deprecated and is no longer available.","data":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	fakeVendors(t, srv)

	if err := Save(Provider{ID: "zhipu", Name: "Zhipu GLM", Preset: "zhipu", Chat: "https://open.bigmodel.cn/api/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic",
		Key: "payg.key", KeyName: "payg", Keys: []KeyAccount{{Name: "plan", Key: "plan.key", Plan: "coding"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "siliconflow", Name: "SiliconFlow", Preset: "siliconflow", Chat: "https://api.siliconflow.cn/v1", Key: "sk-sf"}); err != nil {
		t.Fatal(err)
	}
	var lines []string
	var sf *SubscriptionQuota
	for _, q := range KeyBalances(context.Background()) {
		lines = append(lines, q.Provider+"|"+q.User+"|"+q.Balance)
		if q.Provider == "siliconflow" {
			sf = &q
		}
	}
	if got, want := strings.Join(lines, ","), "zhipu|payg|¥12.50,siliconflow||"; got != want {
		t.Errorf("cards:\n%s\nwant\n%s", got, want)
	}
	if sf == nil || sf.SeeAt != "https://cloud.siliconflow.cn" || !strings.HasPrefix(sf.Error, "SiliconFlow retired its balance query") {
		t.Errorf("SiliconFlow's card: %+v", sf)
	}
}
