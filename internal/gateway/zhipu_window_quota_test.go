package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Zhipu's window refusals as they reached magpie. The 1308's words are
// wf2311's on #3013 (v0.1.3533, 2026-11-10), the 1310's the issue's own
// (2026-10-20); through Zhipu's Anthropic endpoint each is typed
// rate_limit_error.
const (
	zhipu1308Zh = "[1308][已达到 5 小时的使用上限。您的限额将在 2026-11-10 16:01:04 重置。][202611101433583360720d4c25418e]"
	zhipu1310Zh = "[1310][您已达到每周/每月使用上限，您的限额将在 2026-10-24 20:44:45 重置。][20261020091843ac61fce173a347a5]"
)

func anthropicRateError(msg string) string {
	return `{"type":"error","error":{"type":"rate_limit_error","message":"` + msg + `"}}`
}

// ColinWOoOOooo on X, #3013: a GLM Coding Plan's 5-hour window spent,
// then reset on Zhipu's side, was still refused by magpie with the 5-hour
// limit until the account was turned off and on. Through the Anthropic
// endpoint the refusal is typed rate_limit_error, and its Chinese words
// read as a rate limit, which no reading of the plan's windows lifts. It
// is out of quota in every envelope, as its English words already were;
// Zhipu's per-minute 1302 and the other plans' window refusals read as
// they did.
func TestZhipuWindowSpentIsQuota(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       string
	}{
		{"1308 zh, Anthropic", anthropicRateError(zhipu1308Zh), failQuota},
		{"1310 zh, Anthropic", anthropicRateError(zhipu1310Zh), failQuota},
		{"1308 zh, Anthropic, escaped", `{"type":"error","error":{"type":"rate_limit_error","message":` + strconv.QuoteToASCII(zhipu1308Zh) + `}}`, failQuota},
		{"1308 zh, OpenAI, escaped", escaped(zhipu1308Zh, "1308"), failQuota},
		{"1308 zh, OpenAI", `{"error":{"code":"1308","message":"` + zhipu1308Zh + `"}}`, failQuota},
		{"1308 zh, its words without the reset", anthropicRateError("[1308][已达到 5 小时的使用上限。]"), failQuota},
		{"1308 en, Anthropic", anthropicRateError(zai1308), failQuota},
		{"1310 en, Anthropic", anthropicRateError(zai1310), failQuota},
		// a rate limit stays one; the minute's words are made up, to keep
		// 使用上限 from swallowing a cap per minute
		{"1302, Anthropic", anthropicRateError("[1302][您的账户已达到速率限制，请您控制请求频率][202611051732017a8c6e62fa7d42f5]"), failRate},
		{"1302, OpenAI", zcodeWallCode, failRate},
		{"a minute's cap", anthropicRateError("每分钟使用上限已达到，请稍后再试"), failRate},
		// the siblings' windows, as before
		{"Kimi Code", `{"error":{"message":"You've reached your usage limit","type":"rate_limit_error"}}`, failQuota},
		{"Volcengine Ark", `{"error":{"code":"AccountQuotaExceeded","message":"You have exceeded the monthly usage quota. It will reset at 2026-10-25 23:59:59 +0800 CST.","type":"TooManyRequests"}}`, failQuota},
	} {
		if got := failure(http.StatusTooManyRequests, []byte(c.body)); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// A GLM Coding Plan key refused through the Anthropic endpoint with
// Zhipu's Chinese 1308 rests out of its window till the time Zhipu
// named, its windows read again as it is planned; once a reading finds
// the window renewed (a reset spent on Zhipu's side), the next request
// asks Zhipu again rather than being refused here till that time.
func TestZhipuAnthropicWindowRenewedIsAsked(t *testing.T) {
	fresh(t)
	provider.ForgetBalances()
	t.Cleanup(provider.ForgetBalances)
	old := keyAllowance
	keyAllowance = provider.KeyAllowance // the key's windows as read, here
	t.Cleanup(func() { keyAllowance = old })
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	var mu sync.Mutex
	used := 800
	reading := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		u := used
		mu.Unlock()
		fmt.Fprintf(w, `{"isValid":true,"mode":"quota_limited","status":"active","rate_limits":[{"window":"5h","limit":800,"used":%d,"remaining":%d,"reset_at":%q}]}`, u, 800-u, reset.Format(time.RFC3339))
	}))
	defer reading.Close()
	if err := provider.Save(provider.Provider{ID: "glmz", Name: "GLM", Key: "sk-z", Models: []string{"glm-5.3"},
		Anthropic: reading.URL, BalanceURL: reading.URL + "/v1/usage"}); err != nil {
		t.Fatal(err)
	}
	saved, err := provider.Find("glmz")
	if err != nil {
		t.Fatal(err)
	}
	c := perKey(*saved, "glm-5.3", provider.Anthropic)[0]
	t.Cleanup(func() { clearRest(c.restKey()) })
	provider.KeyBalances(context.Background()) // its five hours read: used up
	s := &Server{}
	refusal := []byte(anthropicRateError(strings.Replace(zhipu1308Zh, "2026-11-10 16:01:04", reset.In(zhipuZone).Format("2006-01-02 15:04:05"), 1)))
	r := s.restAfter(c, http.StatusTooManyRequests, http.Header{}, refusal)
	if r.Why != failQuota || r.By != "resets" {
		t.Fatalf("refused out of its 5 hours till %s: rests %s by %s", reset.Format(time.TimeOnly), r.Why, r.By)
	}
	if d := r.Until.Sub(reset); d < -time.Second || d > time.Second {
		t.Errorf("rests till %s, want Zhipu's %s", r.Until, reset)
	}
	held := func() bool {
		_, _, ok := timedHeld([]candidate{c})
		return ok
	}
	if !held() {
		t.Fatal("refused till a named time: not held")
	}
	read := func(u int) {
		mu.Lock()
		used = u
		mu.Unlock()
		provider.ForgetBalances()
		provider.KeyBalances(context.Background())
	}
	read(800)
	if !held() {
		t.Fatal("a reading that still finds the window full let the hold go")
	}
	read(0) // the window reset on Zhipu's side
	if held() {
		t.Fatal("the window read renewed, and the key is still refused here till Zhipu's old time")
	}
}
