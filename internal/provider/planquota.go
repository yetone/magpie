package provider

// A plan bought with an API key — Zhipu's GLM Coding Plan (and Z.ai's),
// Kimi Code, OpenCode Go, a Command Code plan, MiniMax's Coding (Token)
// Plan, Ollama Cloud's included allowance, a Le Chat plan's month of Mistral
// Vibe, StepFun's Step Plan, Volcengine Ark's Coding and Agent Plans and
// Tencent Cloud's Token Plan 企业版 — has windows of allowance like a
// subscription's, which the vendor tells to the key (StepFun only to a
// sign-in, stepfun_plan.go; Volcengine and Tencent Cloud only to the
// account's access key, volcengine_usage.go and tencent_usage.go): the
// Usage page shows them beside the subscriptions'.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// planQuotaSource is where a provider's key tells its plan's windows.
type planQuotaSource struct {
	url    string
	bearer bool // Zhipu takes the bare key as the Authorization
	read   func(body []byte) (plan string, ws []QuotaWindow, err error)
	// sure is set when the provider is a plan and not just the vendor: a
	// pay-as-you-go GLM key has no windows to tell, and saying so on a card
	// would only be noise
	sure bool
}

func planQuotaSourceOf(p Provider) (planQuotaSource, bool) {
	for _, base := range []string{p.Chat, p.Anthropic, p.Responses} {
		coding := strings.Contains(base, "/api/coding/")
		if openCodeGateway(base) == "go" {
			// Go at Zen's gateway or at the inference API, the URL
			// models.dev and Go's docs give since October 2026: the same
			// key, told its windows at the same usage query
			return planQuotaSource{"https://opencode.ai/zen/go/v1/usage", true, readOpenCodeGo, true}, true
		}
		switch hostOf(base) {
		case "open.bigmodel.cn":
			return planQuotaSource{"https://open.bigmodel.cn/api/monitor/usage/quota/limit", false, readZhipuPlan, coding}, true
		case "api.z.ai":
			return planQuotaSource{"https://api.z.ai/api/monitor/usage/quota/limit", false, readZhipuPlan, coding}, true
		case "api.kimi.com", "api.kimi.ai":
			if strings.Contains(base, "/coding") {
				return planQuotaSource{strings.TrimSuffix(kimiCodeBase(base), "/") + "/usages", true, readKimiCode, true}, true
			}
		case "api.commandcode.ai":
			// a plan's key, from Studio or its sign-in, works on the keyed
			// preset too, and is told the plan's 5-hour and weekly windows;
			// a pay-as-you-go key has none, and no card
			return planQuotaSource{"https://api.commandcode.ai/alpha/billing/credits", true, readCommandCodePlan, false}, true
		case "api.minimaxi.com", "api.minimax.cn", "api.minimax.io":
			// a Coding Plan key (sk-cp-…) is told its windows; a
			// pay-as-you-go key isn't, and gets no card (#387).
			// api.minimax.cn is MiniMax China's host since mmx-cli
			// 8746d48f (2026-09-28), beside api.minimaxi.com, and
			// answers the same query
			return planQuotaSource{"https://" + hostOf(base) + "/v1/token_plan/remains", true, readMiniMaxPlan, false}, true
		case "ollama.com":
			// Ollama Cloud's included allowance (#1888); a key with none
			// gets no card
			return planQuotaSource{ollamaBalanceURL, true, readOllamaPlan, false}, true
		case "api.mistral.ai":
			// a Le Chat plan's Vibe key is told its month of Vibe
			// (mistral_plan.go); a key with no Vibe budget gets no card
			return planQuotaSource{mistralBudgetURL, true, readMistralPlan, false}, true
		}
	}
	return planQuotaSource{}, false
}

// readZhipuPlan reads
//
//	{"success":true,"data":{"level":"pro","limits":[
//	  {"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":12,"nextResetTime":1758800000000},
//	  {"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":40,"nextResetTime":…},
//	  {"type":"TIME_LIMIT","unit":5,"number":1,"percentage":3,"nextResetTime":…}]}}
//
// unit 3 is hours and 6 weeks (number 1 or 7 have both been seen for the
// week); TIME_LIMIT is the month's MCP tool calls, which don't stop the
// models.
func readZhipuPlan(b []byte) (string, []QuotaWindow, error) {
	var r struct {
		Success *bool  `json:"success"`
		Msg     string `json:"msg"`
		Data    *struct {
			Level  string `json:"level"`
			Limits []struct {
				Type          string   `json:"type"`
				Unit          int      `json:"unit"`
				Number        int      `json:"number"`
				Percentage    *float64 `json:"percentage"`
				NextResetTime int64    `json:"nextResetTime"`
			} `json:"limits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", nil, err
	}
	if code, msg, ok := zhipuEnvelope(b); ok && ZhipuKeyRefused(code) {
		// the key itself refused, which no plan can be read past
		return "", nil, zhipuKeyRefusal{msg}
	} else if ok && code != 1113 && zhipuCodeStatus(code) == http.StatusTooManyRequests {
		// /api/monitor answers its rate limits 200 in the envelope too
		// (1302 "并发数过高", 1303 "频率过高", …), in Chinese words a
		// passing failure isn't recognised by: said as the 429 it is, so
		// the card keeps what was last read (keepLast) instead of losing
		// it to a read that told nothing about the plan
		return "", nil, fmt.Errorf("429 rate limited (%d): %s", code, msg)
	}
	if r.Success != nil && !*r.Success || r.Data == nil {
		if r.Msg != "" {
			return "", nil, fmt.Errorf("%s", r.Msg)
		}
		return "", nil, fmt.Errorf("no plan in the reply")
	}
	out := []QuotaWindow{}
	for _, l := range r.Data.Limits {
		w := QuotaWindow{}
		if l.Percentage != nil {
			w.Used = *l.Percentage
		}
		if l.NextResetTime > 0 {
			t := time.UnixMilli(l.NextResetTime)
			w.ResetsAt = &t
		}
		switch {
		case strings.EqualFold(l.Type, "TIME_LIMIT"):
			w.Name, w.Aside = "MCP · Month", true
		case l.Unit == 3:
			n := max(l.Number, 1)
			if l.Number <= 0 { // a team's five hours come with no number
				n = 5
			}
			w.Name, w.Span = fmt.Sprintf("%d hours", n), time.Duration(n)*time.Hour
		case l.Unit == 6:
			w.Name, w.Span = "7 days", 7*24*time.Hour
		default:
			w.Name = "Allowance"
		}
		out = append(out, w)
	}
	return r.Data.Level, out, nil
}

// readCommandCodePlan reads a Command Code key's /alpha/billing/credits
// (see cmdCredits) for its plan's 5-hour and weekly windows. The key's
// card is cmdKeyWindows, which adds the month's credits once it knows the
// plan.
func readCommandCodePlan(b []byte) (string, []QuotaWindow, error) {
	var c cmdCredits
	if err := json.Unmarshal(b, &c); err != nil {
		return "", nil, err
	}
	return cmdPlanName(c.Credits.PlanID), cmdWindows(c), nil
}

// readOpenCodeGo reads
//
//	{"usage":{"rolling":{"status":"ok","percent":37,"resetsAt":"2026-08-26T14:12:03.000Z"},
//	          "weekly":{…},"monthly":{"status":"rate-limited","percent":100,…}}}
//
// A window at 0% gives now plus its length as resetsAt, a time nothing
// happens at, so it is left out.
func readOpenCodeGo(b []byte) (string, []QuotaWindow, error) {
	type window struct {
		Percent  *float64 `json:"percent"`
		ResetsAt string   `json:"resetsAt"`
	}
	var r struct {
		Usage map[string]window `json:"usage"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", nil, err
	}
	out := []QuotaWindow{}
	for _, x := range []struct {
		key, name string
		span      time.Duration
	}{{"rolling", "5 hours", 5 * time.Hour}, {"weekly", "7 days", 7 * 24 * time.Hour}, {"monthly", "Month", 0}} {
		w, ok := r.Usage[x.key]
		if !ok || w.Percent == nil {
			continue
		}
		q := QuotaWindow{Name: x.name, Used: *w.Percent, Span: x.span}
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil && *w.Percent > 0 {
			q.ResetsAt = &t
		}
		out = append(out, q)
	}
	if len(out) == 0 {
		return "", nil, fmt.Errorf("OpenCode Go's reply has no rolling, weekly or monthly window: %s", shapeOf(b))
	}
	return "", out, nil
}

// shapeOf names the fields of a JSON reply magpie couldn't read, two
// levels down, without their values: enough to see what the vendor sends
// now, with nothing of the account in it.
func shapeOf(b []byte) string {
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil {
		if len(b) == 0 {
			return "an empty reply"
		}
		return "a reply that isn't a JSON object"
	}
	var names []string
	for k, v := range top {
		var in map[string]json.RawMessage
		if json.Unmarshal(v, &in) == nil && len(in) > 0 {
			for k2 := range in {
				names = append(names, k+"."+k2)
			}
			continue
		}
		names = append(names, k)
	}
	if len(names) == 0 {
		return "an empty object"
	}
	slices.Sort(names)
	if len(names) > 12 {
		names = append(names[:12], "…")
	}
	return "fields " + strings.Join(names, ", ")
}

// readMiniMaxPlan reads MiniMax's /v1/token_plan/remains (#387):
//
//	{"model_remains":[{"model_name":"general",
//	   "start_time":…,"end_time":…,"current_interval_remaining_percent":100,
//	   "current_interval_status":1,"current_interval_total_count":0,
//	   "weekly_start_time":…,"weekly_end_time":…,"current_weekly_remaining_percent":100,
//	   "current_weekly_status":1,"current_weekly_total_count":0},
//	  {"model_name":"video",…}],
//	 "base_resp":{"status_code":0,"status_msg":"success"}}
//
// Each bucket is a quota with a rolling interval and a week, told as what
// remains. "general" is what the models draw on; another (video, image…)
// is metered apart and doesn't stop them. A bucket the plan doesn't have
// comes as both windows unlimited (status 3) with no totals, and is left
// out; status 2 is used up, whatever the percentage says. MiniMax answers
// 200 to a key it refuses, with the reason in base_resp.
//
// A bucket counted in items (video's 5 a day, 35 a week) also tells
// current_interval_usage_count and current_weekly_usage_count beside the
// totals, and the window carries them as a count, as MiniMax's own CLI
// (mmx quota show) shows "4 / 5" (#1366); see miniMaxCount.
func readMiniMaxPlan(b []byte) (string, []QuotaWindow, error) {
	type bucket struct {
		Model      string   `json:"model_name"`
		Start      int64    `json:"start_time"`
		End        int64    `json:"end_time"`
		Left       *float64 `json:"current_interval_remaining_percent"`
		Status     int      `json:"current_interval_status"`
		Total      *float64 `json:"current_interval_total_count"`
		Count      *float64 `json:"current_interval_usage_count"`
		WeekStart  int64    `json:"weekly_start_time"`
		WeekEnd    int64    `json:"weekly_end_time"`
		WeekLeft   *float64 `json:"current_weekly_remaining_percent"`
		WeekStatus int      `json:"current_weekly_status"`
		WeekTotal  *float64 `json:"current_weekly_total_count"`
		WeekCount  *float64 `json:"current_weekly_usage_count"`
	}
	var r struct {
		Remains []bucket `json:"model_remains"`
		Base    *struct {
			Code int    `json:"status_code"`
			Msg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", nil, err
	}
	if r.Base == nil {
		return "", nil, fmt.Errorf("no plan in the reply")
	}
	if r.Base.Code != 0 {
		if r.Base.Msg != "" {
			return "", nil, fmt.Errorf("%s", r.Base.Msg)
		}
		return "", nil, fmt.Errorf("MiniMax said %d", r.Base.Code)
	}
	at := func(n int64) time.Time {
		if n < 1e12 { // seconds
			return time.Unix(n, 0)
		}
		return time.UnixMilli(n)
	}
	zero := func(f *float64) bool { return f != nil && *f == 0 }
	out := []QuotaWindow{}
	for _, k := range r.Remains {
		name := strings.TrimSpace(k.Model)
		if name == "" || k.Status == 3 && k.WeekStatus == 3 && zero(k.Total) && zero(k.WeekTotal) {
			continue // not in the plan
		}
		general := strings.EqualFold(name, "general")
		for _, x := range []struct {
			left         *float64
			status       int
			start, end   int64
			week         bool
			count, total *float64
		}{{k.Left, k.Status, k.Start, k.End, false, k.Count, k.Total}, {k.WeekLeft, k.WeekStatus, k.WeekStart, k.WeekEnd, true, k.WeekCount, k.WeekTotal}} {
			if x.status == 3 || x.left == nil && x.status != 2 {
				continue // unlimited, or nothing told
			}
			w := QuotaWindow{Used: 100}
			if x.status != 2 {
				w.Used = max(0, min(100, 100-*x.left))
			}
			if x.start > 0 && x.end > x.start {
				w.Span = at(x.end).Sub(at(x.start))
			} else if x.week {
				w.Span = 7 * 24 * time.Hour
			}
			if x.end > 0 {
				t := at(x.end)
				w.ResetsAt = &t
			}
			switch span := w.Span; {
			case x.week:
				w.Name = "7 days"
			case span > 24*time.Hour && span%(24*time.Hour) == 0:
				w.Name = fmt.Sprintf("%d days", span/(24*time.Hour))
			case span >= time.Hour && span%time.Hour == 0:
				w.Name = fmt.Sprintf("%d hours", span/time.Hour)
			case span > 0:
				w.Name = fmt.Sprintf("%d minutes", span/time.Minute)
			default:
				w.Name = "Allowance"
			}
			if used, total, ok := miniMaxCount(x.count, x.total, x.left); ok {
				w.Amount, w.Limit = used, total
				if x.status == 2 {
					w.Amount = total
				}
				if strings.EqualFold(name, "video") {
					w.Unit = "videos"
				}
			}
			if !general {
				w.Name = strings.ToUpper(name[:1]) + name[1:] + " · " + w.Name
				w.Aside = true
			}
			out = append(out, w)
		}
	}
	return "", out, nil
}

// miniMaxCount is a MiniMax window's count, used of total, as mmx-cli's
// quota panel reads it (1.0.27, dist/mmx.mjs): the usage_count field has
// been seen to hold what remains rather than what is used, so it is
// taken as whichever of the two the remaining percentage agrees with,
// within a point, and as no count when neither does. A total of 0 (the
// general bucket, metered in tokens) or a count outside it is no count.
func miniMaxCount(count, total, left *float64) (used, of float64, ok bool) {
	if count == nil || total == nil || left == nil || *total <= 0 || *count < 0 || *count > *total {
		return 0, 0, false
	}
	c, t := *count, *total
	asUsed := math.Abs((t-c)/t*100 - *left) // c is what is used: t-c remains
	asLeft := math.Abs(c/t*100 - *left)     // c is what remains
	switch {
	case min(asUsed, asLeft) > 1:
		return 0, 0, false
	case asUsed < asLeft:
		return c, t, true
	default:
		return t - c, t, true
	}
}

// kimiCodeBase is Kimi Code's OpenAI endpoint, /coding/v1, for either of
// the provider's (its Anthropic one is /coding).
func kimiCodeBase(base string) string {
	u := strings.TrimSuffix(base, "/")
	if strings.HasSuffix(u, "/coding") {
		return u + "/v1"
	}
	return u
}

// KimiCodeSearch is where a Kimi Code plan's key searches the web, as
// kimi-cli's SearchWeb does (auth/platforms.py: the plan's search_url is
// its base_url, /coding/v1, with /search), "" for a provider that isn't a
// Kimi Code plan with a key.
func KimiCodeSearch(p Provider) string {
	if p.Account != nil || p.Key == "" {
		return ""
	}
	for _, base := range []string{p.Chat, p.Anthropic} {
		h := hostOf(base)
		if base != "" && ((h == "api.kimi.com" || h == "api.kimi.ai") && strings.Contains(base, "/coding") || strings.HasPrefix(p.Preset, "kimi-code")) {
			return strings.TrimSuffix(kimiCodeBase(base), "/") + "/search"
		}
	}
	return ""
}

// readKimiCode reads Kimi Code's /usages, as kimi-cli's /usage does:
//
//	{"usage":{"limit":"100","used":"12","resetTime":"2026-09-30T05:24:18.44Z"},
//	 "limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},
//	   "detail":{"limit":"100","remaining":"88","resetTime":"…"}}]}
//
// usage is the week's allowance, each of limits a shorter window; the
// numbers come as strings or numbers, used or what remains.
//
// A plan on Kimi's monthly allowance (lidazi2016 on Discord) is also, or
// only, told as shares in usages, as kimi-code reads them
// (packages/oauth/src/managed-usage.ts, usage-format.ts):
//
//	"usages":{"limit_5h":{"used_ratio":0.4372,"reset_time":"…"},
//	  "limit_7d":{…},"limit_month_total":{…},"limit_month_code":{…}}
//
// limit_month_total is the month's allowance. limit_month_code is code's
// share of it (kimi-code) or, on the membership of 2026-09, the coding
// allowance itself with no total beside it (GCMP's kimiUsage.ts), so it
// is the month only when limit_month_total isn't there. A window told
// both ways is taken from usages, as kimi-code does. Each share is read
// under its camelCase name too, a reset as text or epoch seconds or
// milliseconds (EchoBird's fixture), and a limits[] window counted in
// weeks or months by its length.
func readKimiCode(b []byte) (string, []QuotaWindow, error) {
	type row map[string]any
	var r struct {
		Usage  row            `json:"usage"`
		Usages map[string]row `json:"usages"`
		Limits []struct {
			Window row `json:"window"`
			Detail row `json:"detail"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", nil, err
	}
	num := func(v any) (float64, bool) {
		switch x := v.(type) {
		case float64:
			return x, true
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			return f, err == nil
		}
		return 0, false
	}
	// a reset as RFC 3339 text or as epoch seconds or milliseconds, under
	// either spelling of its name
	resetOf := func(d row) *time.Time {
		for _, k := range []string{"resetTime", "resetAt", "reset_at", "reset_time"} {
			if s, _ := d[k].(string); s != "" {
				if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
					return &t
				}
			}
			if n, ok := num(d[k]); ok && n > 0 {
				t := time.Unix(int64(n), 0)
				if n > 1e11 {
					t = time.UnixMilli(int64(n))
				}
				return &t
			}
		}
		return nil
	}
	window := func(d row, name string, span time.Duration) (QuotaWindow, bool) {
		limit, ok := num(d["limit"])
		if !ok || limit <= 0 {
			return QuotaWindow{}, false
		}
		used, ok := num(d["used"])
		if !ok {
			left, ok := num(d["remaining"])
			if !ok {
				return QuotaWindow{}, false
			}
			used = limit - left
		}
		w := QuotaWindow{Name: name, Span: span, Used: max(0, min(100, used/limit*100))}
		w.ResetsAt = resetOf(d)
		return w, true
	}
	out := []QuotaWindow{}
	for _, l := range r.Limits {
		d := l.Detail
		if d == nil {
			d = l.Window
		}
		n, _ := num(l.Window["duration"])
		unit, _ := l.Window["timeUnit"].(string)
		var span time.Duration
		switch {
		case strings.Contains(unit, "MINUTE"):
			span = time.Duration(n) * time.Minute
		case strings.Contains(unit, "HOUR"):
			span = time.Duration(n) * time.Hour
		case strings.Contains(unit, "DAY"):
			span = time.Duration(n) * 24 * time.Hour
		case strings.Contains(unit, "WEEK"):
			span = time.Duration(n) * 7 * 24 * time.Hour
		case strings.Contains(unit, "MONTH"):
			span = time.Duration(n) * 30 * 24 * time.Hour
		}
		name := "Allowance"
		switch {
		case span == 30*24*time.Hour:
			name = "Month"
		case span > 24*time.Hour && span%(24*time.Hour) == 0:
			name = fmt.Sprintf("%d days", span/(24*time.Hour))
		case span >= time.Hour && span%time.Hour == 0:
			name = fmt.Sprintf("%d hours", span/time.Hour)
		case span > 0:
			name = fmt.Sprintf("%d minutes", span/time.Minute)
		}
		if w, ok := window(d, name, span); ok {
			out = append(out, w)
		}
	}
	if w, ok := window(r.Usage, "7 days", 7*24*time.Hour); ok {
		out = append(out, w)
	}
	shares := []QuotaWindow{}
	for _, k := range []struct {
		key, name string
		span      time.Duration
	}{{"limit_5h", "5 hours", 5 * time.Hour}, {"limit_7d", "7 days", 7 * 24 * time.Hour},
		{"limit_month_total", "Month", 30 * 24 * time.Hour}, {"limit_month_code", "Month", 30 * 24 * time.Hour}} {
		d := r.Usages[k.key]
		if d == nil {
			d = r.Usages[camel(k.key)]
		}
		ratio, ok := num(d["used_ratio"])
		if !ok {
			ratio, ok = num(d["usedRatio"])
		}
		if d == nil || !ok || math.IsNaN(ratio) || math.IsInf(ratio, 0) ||
			slices.ContainsFunc(shares, func(s QuotaWindow) bool { return s.Span == k.span }) {
			continue
		}
		shares = append(shares, QuotaWindow{Name: k.name, Span: k.span, Used: max(0, min(100, ratio*100)), ResetsAt: resetOf(d)})
	}
	if len(shares) > 0 {
		out = slices.DeleteFunc(out, func(w QuotaWindow) bool {
			return slices.ContainsFunc(shares, func(s QuotaWindow) bool { return s.Span == w.Span })
		})
		out = append(out, shares...)
		// shortest first, as the counted windows come; one of no known
		// length last
		span := func(w QuotaWindow) time.Duration {
			if w.Span == 0 {
				return math.MaxInt64
			}
			return w.Span
		}
		slices.SortStableFunc(out, func(a, b QuotaWindow) int {
			switch sa, sb := span(a), span(b); {
			case sa < sb:
				return -1
			case sa > sb:
				return 1
			}
			return 0
		})
	}
	if len(out) == 0 {
		return "", nil, fmt.Errorf("no usage in the reply")
	}
	return "", out, nil
}

// planWindows asks the vendor for the plan key is on and its windows.
func planWindows(ctx context.Context, src planQuotaSource, key string) (plan string, ws []QuotaWindow, err error) {
	b, err := planBody(ctx, src, key)
	if err != nil {
		return "", nil, err
	}
	plan, ws, err = src.read(b)
	if r, ok := err.(zhipuKeyRefusal); ok {
		err = zhipuRefused(src.url, r)
	}
	return plan, ws, err
}

// planBody is the vendor's reply to a key asking for its plan's windows,
// or what it said when it refused.
func planBody(ctx context.Context, src planQuotaSource, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
	if err != nil {
		return nil, err
	}
	if src.bearer {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("Authorization", key)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en")
	// Go's usage is asked at its gateway, which routes nothing that names
	// no session
	OpenCodeSession(req.Header, src.url)
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	openCode := strings.Contains(src.url, "opencode.ai")
	switch {
	case res.StatusCode == http.StatusForbidden && openCode:
		// OpenCode keeps Go on the member who subscribed, in the workspace
		// they subscribed in, and asks for it by the key's own member and
		// workspace (zen/go/v1/usage.ts in sst/opencode): a key another
		// member made, or one from another workspace, has none
		return nil, fmt.Errorf("this key has no OpenCode Go subscription: Go belongs to the member who subscribed, in that workspace, so use a key that member made there (opencode.ai, the workspace's API Keys)")
	case res.StatusCode == http.StatusUnauthorized && openCode:
		return nil, fmt.Errorf("OpenCode didn't take this key (%s): it was deleted, or is mistyped", vendorSaid(b, res.Status))
	case res.StatusCode == http.StatusUnauthorized && zhipuHost(hostOf(src.url)):
		return nil, zhipuRefused(src.url, zhipuKeyRefusal{vendorSaid(b, res.Status)})
	case res.StatusCode >= 300:
		return nil, fmt.Errorf("%s", vendorSaid(b, res.Status))
	}
	return b, nil
}

// zhipuKeyRefusal is Zhipu or Z.ai refusing a key itself (codes
// 1000–1005: not taken, expired), in their words.
type zhipuKeyRefusal struct{ said string }

func (e zhipuKeyRefusal) Error() string { return e.said }

// zhipuRefused says a refused key's card in words that name the key and
// what to do (@xanahopper on X: a GLM Coding Plan key Zhipu refused showed
// only "Allowance unavailable"), keeping the vendor's own words.
func zhipuRefused(quotaURL string, r zhipuKeyRefusal) error {
	vendor, keys := "Zhipu", "https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys"
	if hostOf(quotaURL) == "api.z.ai" {
		vendor, keys = "Z.ai", "https://z.ai/manage-apikey/apikey-list"
	}
	said := strings.TrimSpace(r.said)
	if said == "" {
		said = "key refused"
	}
	return refusedKeyErr{fmt.Sprintf("%s didn't take this key (%s): it has expired, was deleted, or is mistyped; paste a current one from %s in the provider's settings", vendor, said, keys)}
}

// refusedKeyErr is a card's error for a key Zhipu or Z.ai refused.
type refusedKeyErr struct{ msg string }

func (e refusedKeyErr) Error() string { return e.msg }

func zhipuKeyRefusedErr(err error) bool {
	_, ok := err.(refusedKeyErr)
	return ok
}

// vendorSaid is the status, and the reason in a vendor's error body
// ({"error":{"message":…}} or {"message":…}) when it gives one, so a card
// that can't be read says why, not only a number.
func vendorSaid(b []byte, status string) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(b, &e) != nil {
		if said, ok := Unsaid(b); ok {
			return status + ": " + said
		}
		return status
	}
	msg := e.Message
	var inner struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Error, &inner) == nil && inner.Message != "" {
		msg = inner.Message
	} else if s := ""; json.Unmarshal(e.Error, &s) == nil && s != "" {
		msg = s
	}
	if msg = strings.TrimSpace(msg); msg == "" {
		return status
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return status + ": " + msg
}

// planKeyWindows asks the vendor for the plan key is on and its windows:
// a Zhipu or Z.ai key with no plan of its own is a team's, whose windows
// are asked with type=2 (zcode_team.go), and team says so.
func planKeyWindows(ctx context.Context, p Provider, src planQuotaSource, key string) (plan string, ws []QuotaWindow, team bool, err error) {
	if hostOf(src.url) == "api.commandcode.ai" {
		plan, ws, err = cmdKeyWindows(ctx, src, key)
		return plan, ws, false, err
	}
	if src.url == mistralBudgetURL {
		plan, ws, err = mistralKeyWindows(ctx, src, key)
		return plan, ws, false, err
	}
	plan, ws, err = planWindows(ctx, src, key)
	// a key refused is refused as a team's too
	if zhipu := strings.HasSuffix(src.url, "/api/monitor/usage/quota/limit"); zhipu && (err != nil || len(ws) == 0) && !zhipuKeyRefusedErr(err) {
		if tplan, tws, terr := zhipuKeyTeamWindows(ctx, src.url, key, p.ZhipuTeam); terr == nil && len(tws) > 0 {
			return tplan, tws, true, nil
		}
	}
	return plan, ws, false, err
}

var planQuotaCache struct {
	sync.Mutex
	at   time.Time
	data []SubscriptionQuota
}

// ForgetPlanQuotas has the next PlanQuotas ask again, after what a plan's
// windows are read with changed (a Volcengine or Tencent Cloud access key).
func ForgetPlanQuotas() { forgetPlanQuotas() }

// PlanQuotas is the windows of every plan magpie has a key for, each key
// on a card of its own when a provider has several. What was asked less
// than a minute ago is not asked again.
func PlanQuotas(ctx context.Context) []SubscriptionQuota {
	c := &planQuotaCache
	_, again := refreshing(ctx) // one card read again (RefreshUsage)
	c.Lock()
	// with allowances read only when asked, the cards stand as they were
	// last read till the user asks (#1518)
	if !again && c.data != nil && (time.Since(c.at) < time.Minute || heldRead(ctx)) {
		defer c.Unlock()
		return c.data
	}
	c.Unlock()
	ctx, seq := quotaReading(ctx)
	type job struct {
		p    Provider
		src  planQuotaSource
		key  string
		user string
	}
	var jobs []job
	for _, p := range All() {
		if p.Hidden || p.Off || p.Account != nil || p.Key == "" {
			continue
		}
		keys := []KeyAccount{p.first()}
		for _, k := range p.Keys {
			if !k.Off && k.Key != "" && k.Key != p.Key {
				keys = append(keys, k)
			}
		}
		for _, k := range keys {
			// each key told at its own plan's source: a GLM Coding Plan
			// key beside a pay-as-you-go one (KeyAccount.Plan)
			q := p.atKeyPlan(k.Plan)
			src, ok := planQuotaSourceOf(q)
			if !ok {
				continue
			}
			user := ""
			if len(keys) > 1 {
				if user = k.Name; user == "" {
					user = Mask(k.Key)
				}
			}
			if wantsCard(ctx, p.ID, user) {
				jobs = append(jobs, job{q, src, k.Key, user})
			}
		}
	}
	got := make([]*SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	var slots keySlots
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer slots.take(ctx, j.p.ID)()
			q := SubscriptionQuota{Provider: j.p.ID, Name: j.p.Name, Icon: j.p.Icon, User: j.user, Windows: []QuotaWindow{}}
			plan, ws, team, err := planKeyWindows(j.p.Via(ctx), j.p, j.src, j.key)
			// a vendor failing a while (Command Code answers billing/credits
			// 503 at times) shows what was last read, as a subscription's
			// card does, rather than no card or "Usage unavailable"
			tag := keyTag("plan", j.key)
			switch {
			case err == nil && len(ws) == 0:
				return // a key with no plan
			case err != nil && !j.src.sure:
				// no plan, unless one was read before: a key whose windows
				// were read is a plan's, and a read of it that fails is
				// unknown, not "no plan" — its card stays, with what was
				// last read when the failure passes, else with why
				// (larchsis on X: the second of two GLM Coding Plan keys
				// lost its card on each refresh)
				q.Error = err.Error()
				known := hadReading(q.Provider, tag)
				if q = keepReading(ctx, q, tag); q.AsOf != nil || known {
					got[i] = &q
				}
				return
			case err != nil:
				q.Error = err.Error()
			default:
				q.Plan, q.Windows = plan, ws
				// what routing goes by, read just now (KeyAllowance)
				k := j.p
				k.Key = j.key
				noteKeyAllowance(k, ws, time.Now())
				if strings.HasSuffix(j.src.url, "/api/monitor/usage/quota/limit") && !team { // Zhipu, Z.ai
					// the plan's term and its resets (#1191), asked together
					root := zcodeRoot(j.src.url)
					resets := make(chan *ResetCredits, 1)
					go func() { resets <- zhipuPersonalResets(ctx, root, j.key) }()
					q.Until, q.Renew = zhipuTerm(ctx, root, j.key)
					q.Resets = <-resets
				}
			}
			// read just now: the card says when (jiabubu: Kimi Code's
			// said nothing while every other card did)
			q = keepReading(ctx, readNow(q), tag)
			got[i] = &q
		}()
	}
	stepfun := make(chan []SubscriptionQuota, 1)
	go func() { stepfun <- stepPlanQuotas(ctx) }()
	volc := make(chan []SubscriptionQuota, 1)
	go func() { volc <- volcPlanQuotas(ctx) }()
	tencent := make(chan []SubscriptionQuota, 1)
	go func() { tencent <- tencentPlanQuotas(ctx) }()
	wg.Wait()
	out := []SubscriptionQuota{}
	for i, q := range got {
		if q != nil {
			// Set this after keepReading, which can restore a card from disk.
			q.glmPlan = strings.HasSuffix(jobs[i].src.url, "/api/monitor/usage/quota/limit")
			out = append(out, *q)
		}
	}
	// StepFun's, Volcengine's and Tencent's, read just now too
	for _, qs := range [][]SubscriptionQuota{<-stepfun, <-volc, <-tencent} {
		for _, q := range qs {
			out = append(out, readNow(q))
		}
	}
	if ctx.Err() == nil {
		noteQuotaHistory(out, time.Now())
		c.Lock()
		c.data = cacheCards(c.data, out, seq, again)
		if !again {
			c.at, out = time.Now(), c.data
		}
		c.Unlock()
	}
	return out
}

// ZhipuTeam is the team a Zhipu or Z.ai key's GLM Coding Plan belongs to:
// its organization and project IDs, as the BigModel console shows them.
type ZhipuTeam struct {
	Org     string `json:"org,omitempty"`
	Project string `json:"project,omitempty"`
}

// normal is t as it is kept: trimmed, nil when neither is given.
func (t *ZhipuTeam) normal() *ZhipuTeam {
	if t == nil {
		return nil
	}
	n := ZhipuTeam{strings.TrimSpace(t.Org), strings.TrimSpace(t.Project)}
	if n.Org == "" && n.Project == "" {
		return nil
	}
	return &n
}

// TakesZhipuTeam says p is a key of Zhipu's or Z.ai's, whose editor then
// offers the team's organization and project (#236).
func TakesZhipuTeam(p Provider) bool {
	if p.Account != nil {
		return false
	}
	src, ok := planQuotaSourceOf(p)
	return ok && strings.HasSuffix(src.url, "/api/monitor/usage/quota/limit")
}

// zhipuKeyTeamWindows is a GLM key's windows on a team's GLM Coding Plan:
// the quota asked with type=2 where ZCode asks it (bigmodel.cn, api.z.ai),
// with the team's organization and project in the headers: those the user
// gave the provider, else those of a ZCode account's team key magpie holds
// (a key pasted alone is asked without them, which is a guess: ZCode
// always sends them).
func zhipuKeyTeamWindows(ctx context.Context, quotaURL, key string, team *ZhipuTeam) (plan string, ws []QuotaWindow, err error) {
	// the key's own host first (open.bigmodel.cn, as CC Switch asks it),
	// then where ZCode asks it
	for _, root := range zhipuTeamRoots(quotaURL) {
		if plan, ws, err = zhipuKeyTeamAt(ctx, root, quotaURL, key, team); err == nil && len(ws) > 0 {
			return
		}
	}
	return
}

// zhipuTeamRoots are the hosts a team's quota is asked at, each once.
func zhipuTeamRoots(quotaURL string) []string {
	own := strings.TrimSuffix(quotaURL, "/api/monitor/usage/quota/limit")
	if biz := zcodeBizRoot(quotaURL); biz != own {
		return []string{own, biz}
	}
	return []string{own}
}

func zhipuKeyTeamAt(ctx context.Context, root, quotaURL, key string, team *ZhipuTeam) (string, []QuotaWindow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/monitor/usage/quota/limit?type=2", nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en")
	org, project := zhipuTeamOf(key)
	if team != nil && team.Org != "" && team.Project != "" {
		org, project = team.Org, team.Project
	}
	if org != "" {
		for k, v := range zcodeTeamHeaders(quotaURL, org, project) {
			req.Header.Set(k, v)
		}
	}
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return "", nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", nil, fmt.Errorf("%s", res.Status)
	}
	_, ws, err := readZhipuPlan(b)
	return "GLM Coding Team", ws, err
}

// camel spells a snake_case name in camelCase: limit_month_total is
// limitMonthTotal.
func camel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
