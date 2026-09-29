package provider

// A plan bought with an API key — Zhipu's GLM Coding Plan (and Z.ai's),
// Kimi Code, OpenCode Go and a Command Code plan — has windows of allowance like a subscription's, which the
// vendor tells to the key: the Usage page shows them beside the
// subscriptions'.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		case "opencode.ai":
			if u := strings.TrimSuffix(base, "/"); strings.HasSuffix(u, "/zen/go") || strings.Contains(u, "/zen/go/") {
				return planQuotaSource{"https://opencode.ai/zen/go/v1/usage", true, readOpenCodeGo, true}, true
			}
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
// (see cmdCredits) for its plan's 5-hour and weekly windows. The dollars
// left are the key's balance card's (readCommandCode), so not a window
// here as on the signed-in subscription's card.
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
		return "", nil, fmt.Errorf("no usage in the reply")
	}
	return "", out, nil
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

// readKimiCode reads Kimi Code's /usages, as kimi-cli's /usage does:
//
//	{"usage":{"limit":"100","used":"12","resetTime":"2026-09-30T05:24:18.44Z"},
//	 "limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},
//	   "detail":{"limit":"100","remaining":"88","resetTime":"…"}}]}
//
// usage is the week's allowance, each of limits a shorter window; the
// numbers come as strings or numbers, used or what remains.
func readKimiCode(b []byte) (string, []QuotaWindow, error) {
	type row map[string]any
	var r struct {
		Usage  row `json:"usage"`
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
		for _, k := range []string{"resetTime", "resetAt", "reset_at", "reset_time"} {
			if s, _ := d[k].(string); s != "" {
				if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
					w.ResetsAt = &t
					break
				}
			}
		}
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
		}
		name := "Allowance"
		switch {
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
	if len(out) == 0 {
		return "", nil, fmt.Errorf("no usage in the reply")
	}
	return "", out, nil
}

// planWindows asks the vendor for the plan key is on and its windows.
func planWindows(ctx context.Context, src planQuotaSource, key string) (plan string, ws []QuotaWindow, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
	if err != nil {
		return "", nil, err
	}
	if src.bearer {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("Authorization", key)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == http.StatusForbidden && strings.Contains(src.url, "opencode.ai"):
		return "", nil, fmt.Errorf("this key has no OpenCode Go subscription")
	case res.StatusCode >= 300:
		return "", nil, fmt.Errorf("%s", res.Status)
	}
	return src.read(b)
}

var planQuotaCache struct {
	sync.Mutex
	at   time.Time
	data []SubscriptionQuota
}

// PlanQuotas is the windows of every plan magpie has a key for, each key
// on a card of its own when a provider has several. What was asked less
// than a minute ago is not asked again.
func PlanQuotas(ctx context.Context) []SubscriptionQuota {
	c := &planQuotaCache
	c.Lock()
	if c.data != nil && time.Since(c.at) < time.Minute {
		defer c.Unlock()
		return c.data
	}
	c.Unlock()
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
		src, ok := planQuotaSourceOf(p)
		if !ok {
			continue
		}
		keys := []string{p.Key}
		names := []string{p.KeyName}
		for _, k := range p.Keys {
			if !k.Off && k.Key != "" && k.Key != p.Key {
				keys, names = append(keys, k.Key), append(names, k.Name)
			}
		}
		for i, k := range keys {
			user := ""
			if len(keys) > 1 {
				if user = names[i]; user == "" {
					user = Mask(k)
				}
			}
			jobs = append(jobs, job{p, src, k, user})
		}
	}
	got := make([]*SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := SubscriptionQuota{Provider: j.p.ID, Name: j.p.Name, Icon: j.p.Icon, User: j.user, Windows: []QuotaWindow{}}
			plan, ws, err := planWindows(ctx, j.src, j.key)
			switch {
			case err != nil && !j.src.sure, err == nil && len(ws) == 0:
				return // a key with no plan
			case err != nil:
				q.Error = err.Error()
			default:
				q.Plan, q.Windows = plan, ws
				if strings.HasSuffix(j.src.url, "/api/monitor/usage/quota/limit") { // Zhipu, Z.ai
					q.Until, q.Renew = zhipuTerm(ctx, zcodeRoot(j.src.url), j.key)
				}
			}
			got[i] = &q
		}()
	}
	wg.Wait()
	out := []SubscriptionQuota{}
	for _, q := range got {
		if q != nil {
			out = append(out, *q)
		}
	}
	if ctx.Err() == nil {
		c.Lock()
		c.at, c.data = time.Now(), out
		c.Unlock()
	}
	return out
}
