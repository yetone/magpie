package provider

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

func TestPlanQuotaSource(t *testing.T) {
	for _, c := range []struct {
		p        Provider
		url      string
		ok, sure bool
	}{
		{Provider{Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic"}, "https://open.bigmodel.cn/api/monitor/usage/quota/limit", true, true},
		{Provider{Chat: "https://open.bigmodel.cn/api/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic"}, "https://open.bigmodel.cn/api/monitor/usage/quota/limit", true, false},
		{Provider{Anthropic: "https://api.z.ai/api/anthropic"}, "https://api.z.ai/api/monitor/usage/quota/limit", true, false},
		{Provider{Chat: "https://opencode.ai/zen/go/v1", Anthropic: "https://opencode.ai/zen/go"}, "https://opencode.ai/zen/go/v1/usage", true, true},
		{Provider{Anthropic: "https://opencode.ai/zen/go"}, "https://opencode.ai/zen/go/v1/usage", true, true},
		{Provider{Chat: "https://opencode.ai/zen/v1"}, "", false, false}, // Zen is pay as you go
		// Go at the inference API, as models.dev and Go's docs give it
		// since October 2026 (ttmrmx on X: no card)
		{Provider{Chat: "https://opencode.ai/inference/go/openai/v1"}, "https://opencode.ai/zen/go/v1/usage", true, true},
		{Provider{Chat: "https://opencode.ai/inference/go/v1", Anthropic: "https://opencode.ai/inference/go"}, "https://opencode.ai/zen/go/v1/usage", true, true},
		{Provider{Anthropic: "https://opencode.ai/inference/go/anthropic"}, "https://opencode.ai/zen/go/v1/usage", true, true},
		{Provider{Chat: "https://opencode.ai/inference/openai/v1"}, "", false, false},      // Zen's
		{Provider{Chat: "https://opencode.ai/inference/custom/acme/v1"}, "", false, false}, // the user's own provider
		{Provider{Chat: "https://example.com/zen/go/v1"}, "", false, false},
		{Provider{Chat: "https://api.kimi.com/coding/v1", Anthropic: "https://api.kimi.com/coding"}, "https://api.kimi.com/coding/v1/usages", true, true},
		{Provider{Anthropic: "https://api.kimi.ai/coding/"}, "https://api.kimi.ai/coding/v1/usages", true, true},
		{Provider{Chat: "https://api.commandcode.ai/provider/v1", Anthropic: "https://api.commandcode.ai/provider"}, "https://api.commandcode.ai/alpha/billing/credits", true, false},
		{Provider{Chat: "https://api.minimaxi.com/v1", Anthropic: "https://api.minimaxi.com/anthropic"}, "https://api.minimaxi.com/v1/token_plan/remains", true, false},
		{Provider{Anthropic: "https://api.minimax.io/anthropic"}, "https://api.minimax.io/v1/token_plan/remains", true, false},
		// MiniMax China's host since mmx-cli 8746d48f
		{Provider{Chat: "https://api.minimax.cn/v1", Anthropic: "https://api.minimax.cn/anthropic"}, "https://api.minimax.cn/v1/token_plan/remains", true, false},
		{Provider{Chat: "https://api.deepseek.com"}, "", false, false},
		{Provider{Chat: "https://ollama.com/v1", Anthropic: "https://ollama.com"}, "https://ollama.com/api/balance", true, false},
		{Provider{Chat: "http://localhost:11434/v1"}, "", false, false}, // a local Ollama
	} {
		src, ok := planQuotaSourceOf(c.p)
		if ok != c.ok || src.url != c.url || src.sure != c.sure {
			t.Errorf("%+v: got %q ok=%v sure=%v", c.p, src.url, ok, src.sure)
		}
	}
}

func TestReadZhipuPlan(t *testing.T) {
	plan, ws, err := readZhipuPlan([]byte(`{"code":200,"success":true,"data":{"level":"pro","limits":[
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":12,"nextResetTime":1790000000000},
		{"type":"TOKENS_LIMIT","unit":6,"number":7,"percentage":40,"nextResetTime":1790500000000},
		{"type":"TIME_LIMIT","unit":5,"number":1,"percentage":3,"usageDetails":[{"modelCode":"search-prime","usage":2}]}]}}`))
	if err != nil || len(ws) != 3 || plan != "pro" {
		t.Fatalf("%v %q %+v", err, plan, ws)
	}
	if ws[0].Name != "5 hours" || ws[0].Used != 12 || ws[0].Span != 5*time.Hour || !ws[0].ResetsAt.Equal(time.UnixMilli(1790000000000)) {
		t.Errorf("5h: %+v", ws[0])
	}
	if ws[1].Name != "7 days" || ws[1].Used != 40 || ws[1].Span != 7*24*time.Hour {
		t.Errorf("week: %+v", ws[1])
	}
	if ws[2].Name != "MCP · Month" || !ws[2].Aside || ws[2].ResetsAt != nil {
		t.Errorf("mcp: %+v", ws[2])
	}
	if _, _, err := readZhipuPlan([]byte(`{"code":401,"msg":"令牌已过期或验证不正确","success":false}`)); err == nil || err.Error() != "令牌已过期或验证不正确" {
		t.Errorf("a refusal reads as %v", err)
	}
	// a pay-as-you-go key: no windows, which PlanQuotas leaves off the page
	if _, ws, err := readZhipuPlan([]byte(`{"success":true,"data":{"limits":[]}}`)); err != nil || len(ws) != 0 {
		t.Errorf("no plan: %v %+v", err, ws)
	}
}

// cmdCreditsReply is /alpha/billing/credits as command-code 1.66's /usage
// reads it: the credits, the windows beside them (resetAt in ms), and the
// sandbox's minutes, which magpie has no use for.
const cmdCreditsReply = `{"credits":{"planId":"individual-goat-monthly","monthlyCredits":41.2,"purchasedCredits":5,"freeCredits":0},
	"windowLimits":{"limited":true,"fiveHour":{"used":3,"cap":10,"resetAt":1790000000000},"weekly":{"used":12,"cap":40,"resetAt":1790400000000}},
	"sandboxMinutes":{"limitMinutes":0,"usedMinutes":0},"sandboxAccess":false}`

func TestReadCommandCodePlan(t *testing.T) {
	plan, ws, err := readCommandCodePlan([]byte(cmdCreditsReply))
	if err != nil || plan != "GOAT" || len(ws) != 2 {
		t.Fatalf("%v %q %+v", err, plan, ws)
	}
	if ws[0].Name != "5 hours" || ws[0].Used != 30 || ws[0].Span != 5*time.Hour || !ws[0].ResetsAt.Equal(time.UnixMilli(1790000000000)) {
		t.Errorf("5h: %+v", ws[0])
	}
	if ws[1].Name != "Weekly" || ws[1].Used != 30 || ws[1].Span != 7*24*time.Hour || !ws[1].ResetsAt.Equal(time.UnixMilli(1790400000000)) {
		t.Errorf("week: %+v", ws[1])
	}
	// a pay-as-you-go key: no windows, and so no card
	if _, ws, err := readCommandCodePlan([]byte(`{"credits":{"purchasedCredits":20},"windowLimits":null}`)); err != nil || len(ws) != 0 {
		t.Errorf("no plan: %v %+v", err, ws)
	}
	// the keyed preset's balance card is the dollars alone
	if got, err := readCommandCode([]byte(cmdCreditsReply)); err != nil || got != "$46.20" {
		t.Errorf("balance: %q %v", got, err)
	}
}

// TestCommandCodeQuotaShowsWindows: the signed-in plan's card carries its
// windows and not a Balance, which every view shows in their place.
func TestCommandCodeQuotaShowsWindows(t *testing.T) {
	var c cmdCredits
	if err := json.Unmarshal([]byte(cmdCreditsReply), &c); err != nil {
		t.Fatal(err)
	}
	q := cmdQuotaOf(SubscriptionQuota{}, c, nil)
	if q.Balance != "" || q.Plan != "GOAT" || len(q.Windows) != 3 {
		t.Fatalf("%+v", q)
	}
	// GOAT's month is 70 usage-value credits, not $70 (they are "$100 on
	// DeepSeek V4.1 Flash", $70 on GLM-5.2): 41.20 of them left, and the
	// $5 bought, dollars at model cost, said beside them, not in the pool
	cr := q.Windows[2]
	if cr.Name != "Credits" || cr.Display != "28.80 / 70.00 credits + $5.00 extra" || math.Abs(cr.Used-100*28.8/70) > 1e-9 || cr.Span != 0 {
		t.Errorf("credits: %+v", cr)
	}
	// Max's credits are dollars: the CLI's pool, the month and the bought
	var maxPlan cmdCredits
	_ = json.Unmarshal([]byte(strings.Replace(cmdCreditsReply, "individual-goat-monthly", "individual-max-monthly", 1)), &maxPlan)
	if q := cmdQuotaOf(SubscriptionQuota{}, maxPlan, nil); q.Plan != "Max" || len(q.Windows) != 3 || q.Windows[2].Display != "$108.80 / $155.00" {
		t.Errorf("max: %+v", q)
	}
	// Provider is pay as you go: its $15 carries no credits, so no month
	var provider cmdCredits
	_ = json.Unmarshal([]byte(strings.Replace(cmdCreditsReply, "individual-goat-monthly", "individual-provider", 1)), &provider)
	if q := cmdQuotaOf(SubscriptionQuota{}, provider, nil); q.Plan != "Provider" || len(q.Windows) != 2 {
		t.Errorf("provider: %+v", q)
	}
	// no plan and no windows: what is left is all there is to show
	var none cmdCredits
	_ = json.Unmarshal([]byte(`{"credits":{"purchasedCredits":"20","freeCredits":1}}`), &none)
	if q := cmdQuotaOf(SubscriptionQuota{}, none, nil); q.Balance != "$21.00" || len(q.Windows) != 0 {
		t.Errorf("no plan: %+v", q)
	}
}

func TestReadOpenCodeGo(t *testing.T) {
	_, ws, err := readOpenCodeGo([]byte(`{"usage":{
		"rolling":{"status":"ok","percent":0,"resetsAt":"2026-09-25T20:00:00.000Z"},
		"weekly":{"status":"ok","percent":62,"resetsAt":"2026-09-28T00:00:00.000Z"},
		"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-10-11T00:00:00.000Z"}}}`))
	if err != nil || len(ws) != 3 {
		t.Fatalf("%v %+v", err, ws)
	}
	if ws[0].Name != "5 hours" || ws[0].Used != 0 || ws[0].ResetsAt != nil {
		t.Errorf("an idle window keeps its placeholder reset: %+v", ws[0])
	}
	if ws[1].Name != "7 days" || ws[1].Used != 62 || ws[1].ResetsAt == nil || ws[1].ResetsAt.Day() != 28 {
		t.Errorf("week: %+v", ws[1])
	}
	if ws[2].Name != "Month" || ws[2].Used != 100 {
		t.Errorf("month: %+v", ws[2])
	}
	if _, _, err := readOpenCodeGo([]byte(`{"rollingUsage":{}}`)); err == nil {
		t.Error("a reply of another shape reads as no windows, silently")
	}
}

// MiniMax's Coding Plan tells what remains of each bucket's interval and
// week (#387): the general bucket is the models', another is aside, one
// the plan doesn't have is left out, and a used-up window is full.
func TestReadMiniMaxPlan(t *testing.T) {
	h := int64(3600 * 1000)
	_, ws, err := readMiniMaxPlan([]byte(`{"model_remains":[
		{"model_name":"general","start_time":1790800000000,"end_time":` + jsonInt(1790800000000+5*h) + `,
		 "current_interval_remaining_percent":72,"current_interval_status":1,"current_interval_total_count":0,
		 "weekly_start_time":1790500000000,"weekly_end_time":` + jsonInt(1790500000000+168*h) + `,
		 "current_weekly_remaining_percent":90,"current_weekly_status":1,"current_weekly_total_count":0},
		{"model_name":"video","start_time":1790800000000,"end_time":` + jsonInt(1790800000000+24*h) + `,
		 "current_interval_remaining_percent":40,"current_interval_status":2,"current_interval_total_count":3,
		 "current_weekly_status":3,"current_weekly_total_count":0},
		{"model_name":"music","current_interval_remaining_percent":100,"current_interval_status":3,"current_interval_total_count":0,
		 "current_weekly_remaining_percent":100,"current_weekly_status":3,"current_weekly_total_count":0}],
		"base_resp":{"status_code":0,"status_msg":"success"}}`))
	if err != nil || len(ws) != 3 {
		t.Fatalf("%v %+v", err, ws)
	}
	if w := ws[0]; w.Name != "5 hours" || math.Abs(w.Used-28) > 1e-9 || w.Span != 5*time.Hour || w.Aside || w.ResetsAt == nil || w.ResetsAt.UnixMilli() != 1790800000000+5*h {
		t.Errorf("interval: %+v", w)
	}
	if w := ws[1]; w.Name != "7 days" || math.Abs(w.Used-10) > 1e-9 || w.Span != 7*24*time.Hour || w.Aside {
		t.Errorf("week: %+v", w)
	}
	if w := ws[2]; w.Name != "Video · 24 hours" || w.Used != 100 || !w.Aside {
		t.Errorf("video, used up: %+v", w)
	}
	if _, _, err := readMiniMaxPlan([]byte(`{"base_resp":{"status_code":1004,"status_msg":"login fail"}}`)); err == nil || !strings.Contains(err.Error(), "login fail") {
		t.Errorf("a refused key: %v", err)
	}
	if _, ws, err := readMiniMaxPlan([]byte(`{"model_remains":[],"base_resp":{"status_code":0}}`)); err != nil || len(ws) != 0 {
		t.Errorf("no plan: %v %+v", err, ws)
	}
}

// A Token Plan's video bucket is counted in videos, and MiniMax's CLI
// shows it so, "4 / 5" left of the day and "34 / 35" of the week (#1366,
// the reporter's mmx quota show): the window carries the count, used of
// total, whichever way usage_count is told, and the general bucket,
// counted in no items, carries none.
func TestReadMiniMaxPlanVideoCounts(t *testing.T) {
	h := int64(3600 * 1000)
	_, ws, err := readMiniMaxPlan([]byte(`{"model_remains":[
		{"model_name":"general","start_time":1791100800000,"end_time":` + jsonInt(1791100800000+5*h) + `,"remains_time":120000,
		 "current_interval_total_count":0,"current_interval_usage_count":0,"current_interval_remaining_percent":94,"current_interval_status":1,
		 "weekly_start_time":1791043200000,"weekly_end_time":` + jsonInt(1791043200000+168*h) + `,"weekly_remains_time":259200000,
		 "current_weekly_total_count":0,"current_weekly_usage_count":0,"current_weekly_remaining_percent":93,"current_weekly_status":1},
		{"model_name":"video","start_time":1791043200000,"end_time":` + jsonInt(1791043200000+24*h) + `,"remains_time":50520000,
		 "current_interval_total_count":5,"current_interval_usage_count":1,"current_interval_remaining_percent":80,"current_interval_status":1,
		 "weekly_start_time":1791043200000,"weekly_end_time":` + jsonInt(1791043200000+168*h) + `,"weekly_remains_time":259200000,
		 "current_weekly_total_count":35,"current_weekly_usage_count":34,"current_weekly_remaining_percent":97.14,"current_weekly_status":1}],
		"base_resp":{"status_code":0,"status_msg":"success"}}`))
	if err != nil || len(ws) != 4 {
		t.Fatalf("%v %+v", err, ws)
	}
	for _, w := range ws[:2] {
		if w.Limit != 0 || w.Unit != "" {
			t.Errorf("general, counted in no items, has a count: %+v", w)
		}
	}
	// the day's usage_count (1) is what is used, the week's (34) what remains
	if w := ws[2]; w.Name != "Video · 24 hours" || w.Amount != 1 || w.Limit != 5 || w.Unit != "videos" || w.Count(true) != "4 / 5 videos" || math.Abs(w.Used-20) > 1e-9 {
		t.Errorf("video, the day: %+v", w)
	}
	if w := ws[3]; w.Name != "Video · 7 days" || w.Amount != 1 || w.Limit != 35 || w.Unit != "videos" || w.Count(true) != "34 / 35 videos" || w.Count(false) != "1 / 35 videos" {
		t.Errorf("video, the week: %+v", w)
	}

	// a count the percentage doesn't agree with is no count; a used-up
	// window is all of it used
	_, ws, err = readMiniMaxPlan([]byte(`{"model_remains":[{"model_name":"video",
		"start_time":1791043200000,"end_time":` + jsonInt(1791043200000+24*h) + `,
		"current_interval_total_count":5,"current_interval_usage_count":2,"current_interval_remaining_percent":50,"current_interval_status":1,
		"weekly_start_time":1791043200000,"weekly_end_time":` + jsonInt(1791043200000+168*h) + `,
		"current_weekly_total_count":35,"current_weekly_usage_count":0,"current_weekly_remaining_percent":0,"current_weekly_status":2}],
		"base_resp":{"status_code":0}}`))
	if err != nil || len(ws) != 2 {
		t.Fatalf("%v %+v", err, ws)
	}
	if w := ws[0]; w.Limit != 0 || w.Count(false) != "" || w.Used != 50 {
		t.Errorf("a count against the percentage: %+v", w)
	}
	if w := ws[1]; w.Amount != 35 || w.Limit != 35 || w.Used != 100 {
		t.Errorf("used up: %+v", w)
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestPlanWindowsAuth(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path == "/go" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":1}]}}`))
	}))
	defer srv.Close()
	_, ws, err := planWindows(context.Background(), planQuotaSource{url: srv.URL + "/z", read: readZhipuPlan}, "k1")
	if err != nil || len(ws) != 1 || auth != "k1" {
		t.Errorf("zhipu: %v %+v auth %q", err, ws, auth)
	}
	_, _, err = planWindows(context.Background(), planQuotaSource{url: srv.URL + "/go", bearer: true, read: readOpenCodeGo}, "k2")
	if err == nil || auth != "Bearer k2" {
		t.Errorf("go: %v auth %q", err, auth)
	}
}

// rewrite sends every request to srv, keeping its path, as if srv were
// the vendor's host.
type rewrite struct{ srv *httptest.Server }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := *req.URL
	u.Scheme, u.Host = "http", strings.TrimPrefix(r.srv.URL, "http://")
	req = req.Clone(req.Context())
	req.Header.Set("X-Host", req.URL.Host)
	req.URL, req.Host = &u, ""
	return http.DefaultTransport.RoundTrip(req)
}

// #68: the Usage page shows a GLM Coding Plan's windows and OpenCode Go's,
// a card for each key when a provider has several, and nothing for a
// pay-as-you-go GLM key.
func TestPlanQuotas(t *testing.T) {
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
	var unavailable atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		key := r.Header.Get("Authorization")
		switch r.Header.Get("X-Host") + r.URL.Path + " " + key {
		case "open.bigmodel.cn/api/monitor/usage/quota/limit glm-a":
			w.Write([]byte(`{"success":true,"data":{"level":"pro","limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":20,"nextResetTime":4102444800000}]}}`))
		case "open.bigmodel.cn/api/monitor/usage/quota/limit glm-b":
			w.Write([]byte(`{"success":true,"data":{"level":"lite","limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":90,"nextResetTime":4102444800000}]}}`))
		case "open.bigmodel.cn/api/monitor/usage/quota/limit glm-payg":
			w.Write([]byte(`{"success":true,"data":{"limits":[]}}`))
		case "bigmodel.cn/api/monitor/usage/quota/limit glm-payg":
			// no plan of its own: asked as a team's key too (#236), and not one
			if r.URL.Query().Get("type") != "2" {
				t.Errorf("team quota asked without type=2: %s", r.URL)
			}
			w.Write([]byte(`{"code":500,"msg":"no team","success":false}`))
		case "open.bigmodel.cn/api/biz/subscription/list glm-a":
			w.Write([]byte(`{"code":200,"success":true,"data":[{"productName":"GLM Coding Pro","status":"VALID","autoRenew":1,"nextRenewTime":"2026-10-18 10:00:00"}]}`))
		case "open.bigmodel.cn/api/biz/subscription/list glm-b":
			w.WriteHeader(http.StatusInternalServerError)
		case "open.bigmodel.cn/api/biz/customer-package-reset/list glm-a":
			if r.URL.Query().Get("targetType") != "PERSONAL" {
				t.Errorf("resets asked as %s", r.URL)
			}
			w.Write([]byte(zhipuResetList))
		case "open.bigmodel.cn/api/biz/customer-package-reset/list glm-b":
			w.Write([]byte(`{"code":1000,"msg":"身份验证失败。","success":false}`))
		case "api.minimaxi.com/v1/token_plan/remains Bearer sk-cp-k":
			w.Write([]byte(`{"model_remains":[{"model_name":"general","current_interval_remaining_percent":75,"current_interval_status":1,
				"current_weekly_remaining_percent":96,"current_weekly_status":1}],"base_resp":{"status_code":0,"status_msg":"success"}}`))
		case "api.minimaxi.com/v1/token_plan/remains Bearer sk-api-k":
			// a pay-as-you-go key: MiniMax answers 200 with the refusal
			w.Write([]byte(`{"base_resp":{"status_code":2049,"status_msg":"invalid api key"}}`))
		case "opencode.ai/zen/go/v1/usage Bearer go-k":
			w.Write([]byte(`{"usage":{"rolling":{"percent":5,"resetsAt":"2026-09-25T20:00:00Z"}}}`))
		default:
			t.Errorf("asked %s%s with %q", r.Header.Get("X-Host"), r.URL.Path, key)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	for _, p := range []Provider{
		{ID: "glm", Name: "GLM", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "glm-a", KeyName: "work",
			Keys: []KeyAccount{{Key: "glm-a"}, {Key: "glm-b", Name: "home"}, {Key: "glm-off", Off: true}}},
		{ID: "glm-api", Name: "GLM API", Chat: "https://open.bigmodel.cn/api/paas/v4", Key: "glm-payg"},
		{ID: "go", Name: "OpenCode Go", Chat: "https://opencode.ai/zen/go/v1", Key: "go-k"},
		// Go at models.dev's URL for it: the same card (ttmrmx on X)
		{ID: "go-inference", Name: "OpenCode Go", Chat: "https://opencode.ai/inference/go/openai/v1", Key: "go-k"},
		{ID: "zen", Name: "Zen", Chat: "https://opencode.ai/zen/v1", Key: "zen-k"},
		{ID: "minimax-cn", Name: "MiniMax (China)", Chat: "https://api.minimaxi.com/v1", Anthropic: "https://api.minimaxi.com/anthropic", Key: "sk-cp-k"},
		{ID: "minimax-payg", Name: "MiniMax API", Anthropic: "https://api.minimaxi.com/anthropic", Key: "sk-api-k"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]SubscriptionQuota{}
	for _, q := range PlanQuotas(context.Background()) {
		got[q.Provider+"/"+q.User] = q
	}
	if len(got) != 5 {
		t.Fatalf("cards: %+v", got)
	}
	if q := got["minimax-cn/"]; len(q.Windows) != 2 || q.Windows[0].Name != "5 hours" && q.Windows[0].Name != "Allowance" || q.Windows[0].Used != 25 || q.Windows[1].Name != "7 days" || q.Windows[1].Used != 4 {
		t.Errorf("minimax: %+v", q)
	}
	if q := got["glm/work"]; q.Plan != "pro" || len(q.Windows) != 1 || q.Windows[0].Used != 20 ||
		q.Until == nil || q.Until.UTC().Hour() != 2 || q.Renew != "auto" {
		t.Errorf("first key: %+v", q)
	}
	if q := got["glm/home"]; q.Plan != "lite" || q.Windows[0].Used != 90 || q.Until != nil || q.Error != "" {
		t.Errorf("second key: %+v", q)
	}
	// its resets (#1191): a list read is counted, one refused shows none
	if r := got["glm/work"].Resets; r == nil || r.Count != 2 || r.FiveHour != 1 || r.Weekly != 1 {
		t.Errorf("first key's resets: %+v", r)
	}
	if r := got["glm/home"].Resets; r != nil {
		t.Errorf("second key's resets: %+v", r)
	}
	if q := got["go/"]; q.Name != "OpenCode Go" || len(q.Windows) != 1 || q.Windows[0].Name != "5 hours" {
		t.Errorf("go: %+v", q)
	}
	if q := got["go-inference/"]; len(q.Windows) != 1 || q.Windows[0].Name != "5 hours" || q.Error != "" {
		t.Errorf("go at the inference API: %+v", q)
	}
	// The custom GLM provider is recognized by its endpoint, not its id
	// or key labels, and the provenance survives PlanQuotas' cache.
	reset := time.UnixMilli(4102444800000)
	sub := SubscriptionQuota{Provider: "zcode", User: "me@example.com",
		Windows: []QuotaWindow{{Span: 5 * time.Hour, ResetsAt: &reset}}}
	for _, q := range got {
		if hidden := len(notShown([]SubscriptionQuota{q}, []SubscriptionQuota{sub})) == 0; hidden != (q.Provider == "glm") {
			t.Errorf("%s/%s: hidden=%v", q.Provider, q.User, hidden)
		}
	}
	if qs := notShown(PlanQuotas(context.Background()), []SubscriptionQuota{sub}); len(qs) != 3 {
		t.Fatalf("cached plans after deduplication: %+v", qs)
	}
	// A restart loses the in-memory provenance; a failed read restores
	// the old windows from disk and must still recognize the GLM source.
	unavailable.Store(true)
	planQuotaCache.Lock()
	planQuotaCache.data = nil
	planQuotaCache.Unlock()
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	qs := PlanQuotas(context.Background())
	if len(qs) != 5 {
		t.Fatalf("restored plans: %+v", qs)
	}
	if kept := notShown(qs, []SubscriptionQuota{sub}); len(kept) != 3 {
		t.Fatalf("restored plans after deduplication: %+v", kept)
	}
}

func TestReadKimiCode(t *testing.T) {
	_, ws, err := readKimiCode([]byte(`{"usage":{"limit":"100","used":"12","resetTime":"2026-09-30T05:24:18.443553353Z"},
		"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"50","remaining":"40","resetTime":"2026-09-27T09:00:00Z"}},
		          {"window":{"duration":1,"timeUnit":"TIME_UNIT_DAY"},"detail":{"limit":200,"used":200}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[0].Name != "5 hours" || ws[0].Used != 20 || ws[0].Span != 5*time.Hour || ws[0].ResetsAt == nil ||
		ws[1].Name != "24 hours" || ws[1].Used != 100 || ws[1].ResetsAt != nil ||
		ws[2].Name != "7 days" || ws[2].Used != 12 || ws[2].ResetsAt == nil || ws[2].ResetsAt.Day() != 30 {
		t.Fatalf("%+v", ws)
	}
	if _, _, err := readKimiCode([]byte(`{"error":{"message":"bad key"}}`)); err == nil {
		t.Fatal("nothing read")
	}
}

// A plan on Kimi's monthly allowance tells it in usages, as shares
// (lidazi2016 on Discord: 5h and weekly shown, the month not). The first
// body is a live reply of 2026-09-23 (narumiruna/pi-extensions#1390): no
// weekly usage, the 5h window told both ways. The second has every
// window kimi-code reads (managed-usage.ts); each is taken from usages,
// limit_month_code not shown beside limit_month_total.
func TestReadKimiCodeMonth(t *testing.T) {
	_, ws, err := readKimiCode([]byte(`{
  "limits": [
    {
      "window": { "duration": 300, "timeUnit": "TIME_UNIT_MINUTE" },
      "detail": { "limit": "100", "used": "44", "remaining": "56", "resetTime": "2026-09-23T06:47:09.122865Z" }
    }
  ],
  "usages": {
    "limit_5h":          { "used_ratio": 0.4372, "reset_time": "2026-09-23T06:47:08Z" },
    "limit_month_total": { "used_ratio": 0.7877, "reset_time": "2026-10-17T00:00:00Z" },
    "limit_month_code":  { "used_ratio": 0.7877, "reset_time": "2026-10-17T00:00:00Z" }
  },
  "booster_wallet": {}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[0].Name != "5 hours" || math.Abs(ws[0].Used-43.72) > 1e-9 || ws[0].Span != 5*time.Hour || ws[0].ResetsAt == nil || ws[0].ResetsAt.Second() != 8 ||
		ws[1].Name != "Month" || math.Abs(ws[1].Used-78.77) > 1e-9 || ws[1].Span != 30*24*time.Hour || ws[1].ResetsAt == nil || ws[1].ResetsAt.Day() != 17 {
		t.Fatalf("%+v", ws)
	}

	_, ws, err = readKimiCode([]byte(`{"usage":{"limit":"100","used":"30","resetTime":"2026-10-31T05:24:18.44Z"},
		"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","remaining":"88","resetTime":"2026-10-29T09:00:00Z"}}],
		"usages":{"limit_month_code":{"used_ratio":0.1},"limit_month_total":{"used_ratio":"0.4","reset_time":"2026-11-18T00:00:00Z"},
		  "limit_7d":{"used_ratio":0.3012,"reset_time":"2026-10-31T05:24:18Z"},"limit_5h":{"used_ratio":0.1234}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[0].Name != "5 hours" || math.Abs(ws[0].Used-12.34) > 1e-9 || ws[0].ResetsAt != nil ||
		ws[1].Name != "7 days" || math.Abs(ws[1].Used-30.12) > 1e-9 || ws[1].Span != 7*24*time.Hour ||
		ws[2].Name != "Month" || math.Abs(ws[2].Used-40) > 1e-9 || ws[2].ResetsAt == nil || ws[2].ResetsAt.Day() != 18 {
		t.Fatalf("%+v", ws)
	}

	// a share that isn't a number is no window; the counted ones stand
	_, ws, err = readKimiCode([]byte(`{"usage":{"limit":"100","used":"12"},"usages":{"limit_month_total":{"used_ratio":null},"limit_7d":{"reset_time":"2026-10-31T05:24:18Z"}}}`))
	if err != nil || len(ws) != 1 || ws[0].Name != "7 days" || ws[0].Used != 12 {
		t.Fatalf("%+v %v", ws, err)
	}
}

// The month however Kimi tells it (lidazi2016 on Discord, still no month
// on v0.1.2023 with a Kimi Code China key): the membership of 2026-09
// sends limit_5h and limit_month_code with no total (GCMP's
// kimiUsage.ts), a share may come in camelCase with its reset in epoch
// milliseconds (EchoBird's fixture), and a counted window may be a
// month or a week long.
func TestReadKimiCodeMonthEveryWay(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"code share only", `{"usages":{"limit_5h":{"used_ratio":0.25,"reset_time":"2026-10-29T11:25:00Z"},
			"limit_month_code":{"used_ratio":0.5,"reset_time":"2026-11-17T00:00:00Z"}}}`},
		{"camelCase", `{"usages":{"limit5h":{"usedRatio":0.25,"resetTime":"2026-10-29T11:25:00Z"},
			"limitMonthTotal":{"usedRatio":"0.5","resetTime":1794873600000}}}`},
		{"counted", `{"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","used":"25","resetTime":"2026-10-29T11:25:00Z"}},
			{"window":{"duration":1,"timeUnit":"TIME_UNIT_MONTH"},"detail":{"limit":"100","remaining":"50","resetTime":1794873600}}]}`},
	} {
		_, ws, err := readKimiCode([]byte(c.body))
		if err != nil || len(ws) != 2 || ws[0].Name != "5 hours" || ws[0].Used != 25 || ws[0].ResetsAt == nil ||
			ws[1].Name != "Month" || ws[1].Used != 50 || ws[1].Span != 30*24*time.Hour ||
			ws[1].ResetsAt == nil || ws[1].ResetsAt.UTC().Day() != 17 || ws[1].ResetsAt.UTC().Month() != time.November {
			t.Errorf("%s: %+v %v", c.name, ws, err)
		}
	}
	// a counted week is the week, beside the month the shares tell
	_, ws, err := readKimiCode([]byte(`{"limits":[{"window":{"duration":1,"timeUnit":"TIME_UNIT_WEEK"},"detail":{"limit":10,"used":4}}],
		"usages":{"limit_month_total":{"used_ratio":0.1},"limit_month_code":{"used_ratio":0.05}}}`))
	if err != nil || len(ws) != 2 || ws[0].Name != "7 days" || ws[0].Used != 40 || ws[1].Name != "Month" || math.Abs(ws[1].Used-10) > 1e-9 {
		t.Errorf("week and month: %+v %v", ws, err)
	}
}

// Every way a Kimi Code key is added shows the month Kimi tells: the
// China and global presets, a provider of its own at the Anthropic base
// alone, and a key imported from OpenCode's auth.json. Each card is read
// from /usages on its own host with its own key.
func TestPlanQuotasKimiCodeMonthEveryRoute(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	month := `{"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","used":"11","resetTime":"2026-10-29T11:25:00Z"}}],
		"usages":{"limit_5h":{"used_ratio":0.11,"reset_time":"2026-10-29T11:25:00Z"},"limit_month_code":{"used_ratio":0.3,"reset_time":"2026-11-17T00:00:00Z"}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Host") + r.URL.Path + " " + r.Header.Get("Authorization") {
		case "api.kimi.com/coding/v1/usages Bearer sk-kimi-cn",
			"api.kimi.ai/coding/v1/usages Bearer sk-kimi-global",
			"api.kimi.com/coding/v1/usages Bearer sk-kimi-own",
			"api.kimi.com/coding/v1/usages Bearer sk-kimi-oc":
			w.Write([]byte(month))
		default:
			t.Errorf("asked %s%s with %q", r.Header.Get("X-Host"), r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	auth := filepath.Join(h, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"kimi-code-plan-cn":{"type":"api","key":"sk-kimi-oc"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := readOpenCodeAuth(auth)
	if err != nil || len(items) != 1 || items[0].Skip != "" {
		t.Fatalf("OpenCode import: %+v %v", items, err)
	}
	oc := items[0].Provider
	oc.ID = "kimi-oc"
	for _, p := range []Provider{
		FromPresetKey(t, "kimi-code-cn", "sk-kimi-cn"),
		FromPresetKey(t, "kimi-code", "sk-kimi-global"),
		{ID: "kimi-own", Name: "My Kimi", Anthropic: "https://api.kimi.com/coding/", Key: "sk-kimi-own"},
		oc,
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]SubscriptionQuota{}
	for _, q := range PlanQuotas(context.Background()) {
		got[q.Provider] = q
	}
	for _, id := range []string{"kimi-code-cn", "kimi-code", "kimi-own", "kimi-oc"} {
		q := got[id]
		if len(q.Windows) != 2 || q.Windows[0].Name != "5 hours" || q.Windows[1].Name != "Month" ||
			math.Abs(q.Windows[1].Used-30) > 1e-9 || q.Windows[1].ResetsAt == nil {
			t.Errorf("%s: %+v", id, q)
		}
	}
}

// An OpenCode Go key whose windows can't be read says why, with the way
// out (sakulalalalalal on X: three Go keys, each "Allowance unavailable"
// and nothing more). The bodies are OpenCode's own, as zen/go/v1/usage
// answered on 2026-10-10: a key whose member has no Go, and a key it
// doesn't know.
func TestOpenCodeGoSaysWhyNoWindows(t *testing.T) {
	bodies := map[string]struct {
		status int
		body   string
	}{
		"/nogo":    {http.StatusForbidden, `{"type":"error","error":{"type":"EntitlementError","message":"OpenCode Go subscription required."}}`},
		"/unknown": {http.StatusUnauthorized, `{"type":"error","error":{"type":"AuthError","message":"Unauthorized"}}`},
		"/down":    {http.StatusServiceUnavailable, `{"error":{"type":"api_error","message":"Inference routing is unavailable. Please retry later."}}`},
		"/other":   {http.StatusOK, `{"usage":{"fiveHour":{"used":3}},"plan":"go"}`},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := bodies[r.URL.Path]
		w.WriteHeader(b.status)
		w.Write([]byte(b.body))
	}))
	defer srv.Close()
	read := func(path string) string {
		// the vendor's host, so the OpenCode rules apply
		_, _, err := planWindows(context.Background(), planQuotaSource{url: srv.URL + path + "?opencode.ai", bearer: true, read: readOpenCodeGo}, "k")
		if err == nil {
			t.Fatalf("%s read as windows", path)
		}
		return err.Error()
	}
	for path, want := range map[string][]string{
		"/nogo":    {"no OpenCode Go subscription", "member who subscribed"},
		"/unknown": {"OpenCode didn't take this key", "401 Unauthorized: Unauthorized"},
		"/down":    {"503 Service Unavailable", "Inference routing is unavailable"},
		"/other":   {"no rolling, weekly or monthly window", "usage.fiveHour", "plan"},
	} {
		got := read(path)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q doesn't say %q", path, got, w)
			}
		}
	}
	if got := read("/other"); strings.Contains(got, "3") || strings.Contains(got, "go\"") {
		t.Errorf("an unread reply's values are kept out of the error: %q", got)
	}
}

// Kimi Code's booster wallet (加油包) is read beside the plan's windows
// (#5721, the reporter's reply of 2026-11-08): its balance shows as Extra
// usage, set aside from routing. With money left, Kimi goes on past the
// plan's windows on it, so each of them is on credits; used up (no
// amountLeft, which kimi-code reads as 0), they are not.
func TestReadKimiCodeBooster(t *testing.T) {
	body, err := os.ReadFile("testdata/kimi-booster-5721.json")
	if err != nil {
		t.Fatal(err)
	}
	_, ws, err := readKimiCode(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[0].Name != "5 hours" || ws[0].Used != 0 || ws[0].Credits ||
		ws[1].Name != "Month" || math.Abs(ws[1].Used-98.85) > 1e-9 || ws[1].Credits ||
		ws[2].Name != "Extra usage" || ws[2].Balance == nil || *ws[2].Balance != 0 || ws[2].Unit != "CNY" || !ws[2].Aside {
		t.Fatalf("%+v", ws)
	}

	left := strings.Replace(string(body), `"amount": "17500000000",`, `"amount": "17500000000", "amountLeft": "5000000000",`, 1)
	camel := strings.Replace(left, `"booster_wallet"`, `"boosterWallet"`, 1)
	for name, b := range map[string]string{"snake_case": left, "camelCase": camel} {
		_, ws, err = readKimiCode([]byte(b))
		if err != nil || len(ws) != 3 || !ws[0].Credits || !ws[1].Credits ||
			ws[2].Balance == nil || *ws[2].Balance != 50 || ws[2].Unit != "CNY" || ws[2].Credits {
			t.Fatalf("%s: %+v %v", name, ws, err)
		}
	}

	// a wallet that isn't a booster, or holds nothing, is no row
	for _, b := range []string{
		strings.Replace(left, `"type": "BOOSTER"`, `"type": "GIFT"`, 1),
		strings.Replace(left, `"amount": "17500000000"`, `"amount": "0"`, 1),
	} {
		if _, ws, err = readKimiCode([]byte(b)); err != nil || len(ws) != 2 || ws[0].Credits || ws[1].Credits {
			t.Fatalf("%+v %v", ws, err)
		}
	}
}
