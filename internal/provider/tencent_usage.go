package provider

// Tencent Cloud's Token Plan 企业版 (#1574, undirectlookable) tells what a
// team's plan has used of its cycle's credits (专业套餐) or tokens (轻享套餐)
// only to TokenHub's cloud API (api/1823/132270): DescribeTokenPlanList
// and DescribeTokenPlan on tokenhub.tencentcloudapi.com, version
// 2026-03-22, signed TC3-HMAC-SHA256 with the account's API key, a
// SecretId and SecretKey (AccessKeyID, SecretAccessKey), never with the
// plan's own sk- key. The plan a provider is on is its endpoint's: /plan
// on tokenhub.tencentmaas.com is a Token Plan in Guangzhou, on
// tokenhub-intl.tencentmaas.com 企业版 in Singapore. Guangzhou's is 个人版
// too, at the same endpoints since Tencent moved it there from
// api.lkeap.cloud.tencent.com (ysm119, 11-02), and the key alone tells
// which: 个人版 has no API for its usage, only the console. So a provider
// there is asked for the access key, but given none it has no card
// saying so, which a personal plan would show for nothing; in Singapore,
// where 企业版 alone is sold, it has. TokenHub's pay as you go has no
// plan and no card.
//
// The request is signed as Tencent's signing doc (api/213/30654) says:
// the canonical request with content-type, host and x-tc-action signed,
// the scope date/service/tc3_request, and a key derived from "TC3" and
// the SecretKey. TestTC3SignMatchesDoc pins the doc's own example.
//
// Not tried against the real API: magpie has no Tencent Cloud account.
// What is read is the docs' reply: TotalUsed of CycleQuota (else
// TotalQuota), resetting at TokenSummary's CycleEndTime.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tcAPIHost     = "tokenhub.tencentcloudapi.com"
	tcAPIVersion  = "2026-03-22"
	tcAPIService  = "tokenhub"
	tcContentType = "application/json; charset=utf-8"
	tcPlanName    = "Token Plan Enterprise"
	// tcSharedRegion is the cloud API region whose /plan endpoints serve
	// Token Plan 个人版 as well as 企业版
	tcSharedRegion = "ap-guangzhou"
	tcKeyHint      = "check the SecretId and SecretKey in the provider's editor (Tencent Cloud console → Access management → API keys), and that the key may read TokenHub (CAM)"
)

// TakesTencentAccessKey says p is on Tencent Cloud's Token Plan 企业版 (at
// /plan on TokenHub's hosts), whose editor then asks for the account's
// SecretId and SecretKey.
func TakesTencentAccessKey(p Provider) bool {
	_, ok := tencentPlanRegion(p)
	return ok
}

// AccessKeyVendor is whose account access key p's plan is read with:
// "volcengine" (Ark's Coding and Agent Plans), "tencent" (Token Plan
// 企业版), "alibaba" (Token Plan on aliyuncs.com), or "" for none.
func AccessKeyVendor(p Provider) string {
	switch {
	case TakesVolcAccessKey(p):
		return "volcengine"
	case TakesTencentAccessKey(p):
		return "tencent"
	case TakesAlibabaAccessKey(p):
		return "alibaba"
	}
	return ""
}

// AsksAccessKey is whose account access key p's editor and Usage page
// ask for: AccessKeyVendor's for a preset's provider, and for a custom
// one only once it has one saved. A custom provider is the user's own
// address and key, sent as typed, whatever host it is on: one at
// tokenhub.tencentmaas.com/plan was asked for a SecretId and SecretKey
// it never needs (ysm119 on Discord).
func AsksAccessKey(p Provider) string {
	if p.Preset == "" && p.AccessKeyID == "" && p.SecretAccessKey == "" {
		return ""
	}
	return AccessKeyVendor(p)
}

// tencentPlanRegion is the cloud API region of the enterprise plan p's
// endpoint is on: ap-guangzhou for tokenhub.tencentmaas.com/plan (which
// is 个人版's too), ap-singapore for tokenhub-intl.tencentmaas.com/plan;
// ok false for TokenHub's pay as you go or another vendor.
func tencentPlanRegion(p Provider) (string, bool) {
	if p.Account != nil {
		return "", false
	}
	for _, base := range []string{p.Chat, p.Responses, p.Anthropic} {
		// both hosts below hold it: every other base, asked for each
		// provider on each Providers page, isn't parsed (ToLower makes
		// no copy of a base already lower-case)
		if !strings.Contains(strings.ToLower(base), "tencentmaas.com") {
			continue
		}
		u, err := url.Parse(base)
		if err != nil || !strings.HasPrefix(strings.TrimSuffix(u.Path, "/")+"/", "/plan/") {
			continue
		}
		switch strings.ToLower(u.Hostname()) {
		case "tokenhub.tencentmaas.com":
			return "ap-guangzhou", true
		case "tokenhub-intl.tencentmaas.com":
			return "ap-singapore", true
		}
	}
	return "", false
}

// tencentPlanQuotas is a card for each Token Plan 企业版 team plan the
// account of a provider in use has, read with its SecretId and SecretKey:
// each account and region asked once, its cards named for the first such
// provider. A region no provider on it has the access key for is a card
// that says so (accessKeyCard, #1821), as Volcengine's.
func tencentPlanQuotas(ctx context.Context) []SubscriptionQuota {
	type job struct {
		p      Provider
		region string
	}
	seen := map[string]bool{}
	var jobs []job
	keyed := map[string]bool{}
	var keyless []job
	for _, p := range planViews(All()) {
		if p.Hidden || p.Off {
			continue
		}
		region, ok := tencentPlanRegion(p)
		if !ok {
			continue
		}
		if p.AccessKeyID == "" || p.SecretAccessKey == "" {
			if AsksAccessKey(p) != "" {
				keyless = append(keyless, job{p, region})
			}
			continue
		}
		keyed[region] = true
		if seen[p.AccessKeyID+" "+region] || !wantsCard(ctx, p.ID, "") {
			continue
		}
		seen[p.AccessKeyID+" "+region] = true
		jobs = append(jobs, job{p, region})
	}
	got := make([][]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = tencentPlanCards(ctx, j.p, j.region, time.Now)
			if qs := got[i]; len(qs) == 1 && qs[0].Error == "" {
				// the account's one plan is what routing goes by, read
				// just now (KeyAllowance); of several, which a key is of
				// isn't told
				noteKeyAllowance(j.p, qs[0].Windows, time.Now())
			}
		}()
	}
	wg.Wait()
	out := []SubscriptionQuota{}
	for _, qs := range got {
		out = append(out, qs...)
	}
	for _, j := range keyless {
		if keyed[j.region] || seen["none "+j.region] || j.region == tcSharedRegion || !wantsCard(ctx, j.p.ID, "") {
			continue
		}
		seen["none "+j.region] = true
		out = append(out, accessKeyCard(j.p, tcPlanName))
	}
	return out
}

// tencentPlanCards is the account's team plans in region as cards, each
// showing what was last read when a read fails in passing.
func tencentPlanCards(ctx context.Context, p Provider, region string, now func() time.Time) []SubscriptionQuota {
	via := p.Via(ctx)
	card := func(user string) SubscriptionQuota {
		return SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, Plan: tcPlanName, User: user, Windows: []QuotaWindow{}}
	}
	raw, err := tencentAPICall(via, p.AccessKeyID, p.SecretAccessKey, region, "DescribeTokenPlanList", map[string]any{"Offset": 0, "Limit": 100}, now())
	var list struct {
		Response *struct {
			TokenPlanSet []tencentPlan `json:"TokenPlanSet"`
		} `json:"Response"`
	}
	if err == nil && (json.Unmarshal(raw, &list) != nil || list.Response == nil) {
		err = fmt.Errorf("Tencent Cloud DescribeTokenPlanList: a reply magpie can't read: %s", shapeOf(raw))
	}
	tag := keyTag("tencent "+region, p.AccessKeyID)
	if err != nil {
		q := card("")
		q.Error = err.Error()
		return []SubscriptionQuota{keepReading(ctx, q, tag)}
	}
	var plans []tencentPlan
	for _, t := range list.Response.TokenPlanSet {
		if t.TeamID != "" {
			plans = append(plans, t)
		}
	}
	if len(plans) == 0 {
		q := card("")
		q.Error = "this Tencent Cloud account has no Token Plan Enterprise in " + region + " that its API key can see (a sub-user sees only the plans it made)"
		return []SubscriptionQuota{keepReading(ctx, q, tag)}
	}
	out := make([]SubscriptionQuota, len(plans))
	var wg sync.WaitGroup
	for i, t := range plans {
		user := ""
		if len(plans) > 1 {
			if user = strings.TrimSpace(t.Name); user == "" {
				user = t.TeamID
			}
		}
		if !wantsCard(ctx, p.ID, user) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// the plan's own reply has its cycle's end (TokenSummary); the
			// list's package is enough when it can't be read
			told := raw
			if detail, err := tencentAPICall(via, p.AccessKeyID, p.SecretAccessKey, region, "DescribeTokenPlan", map[string]any{"TeamId": t.TeamID}, now()); err == nil {
				var one struct {
					Response *tencentPlan `json:"Response"`
				}
				if json.Unmarshal(detail, &one) == nil && one.Response != nil {
					// what it tells over the list's, each part it has
					d := one.Response
					if d.PackageInfo != nil {
						t.PackageInfo, told = d.PackageInfo, detail
					}
					if d.TokenSummary != nil {
						t.TokenSummary = d.TokenSummary
					}
					if d.Status != "" {
						t.Status, t.StopReason = d.Status, d.StopReason
					}
					if d.ProductType != "" {
						t.ProductType = d.ProductType
					}
				}
			}
			q := card(user)
			if w, ok := t.window(now()); ok {
				q.Windows = []QuotaWindow{w}
			}
			// a stopped plan (used up, in arrears, frozen) says so beside
			// its name: it is what Tencent answered, not a read that failed
			if t.Status != "" && !strings.EqualFold(t.Status, "enable") {
				q.Plan += " · " + t.Status + tcReason(t.StopReason)
			}
			if len(q.Windows) == 0 {
				q.Error = "Tencent Cloud told no quota for this Token Plan Enterprise: " + shapeOf(told)
			}
			out[i] = keepReading(ctx, q, keyTag("tencent "+region+" "+t.TeamID, p.AccessKeyID))
		}()
	}
	wg.Wait()
	var cards []SubscriptionQuota
	for _, q := range out {
		if q.Provider != "" {
			cards = append(cards, q)
		}
	}
	return cards
}

func tcReason(r string) string {
	if r = strings.TrimSpace(r); r == "" || strings.EqualFold(r, "NORMAL") || strings.EqualFold(r, "none") {
		return ""
	}
	return ", " + r
}

// tencentPlan is a team plan as DescribeTokenPlan and DescribeTokenPlanList
// tell it (the docs' TokenPlanListItem and DescribeTokenPlan's output).
type tencentPlan struct {
	TeamID      string `json:"TeamId"`
	Name        string `json:"Name"`
	Status      string `json:"Status"`
	StopReason  string `json:"StopReason"`
	ProductType string `json:"ProductType"`
	PackageInfo *struct {
		TotalQuota   any    `json:"TotalQuota"`
		TotalUsed    any    `json:"TotalUsed"`
		CycleQuota   any    `json:"CycleQuota"`
		CycleUnit    string `json:"CycleUnit"`
		CurrentCycle any    `json:"CurrentCycle"`
		StartTime    string `json:"StartTime"`
		ExpireTime   string `json:"ExpireTime"`
	} `json:"PackageInfo"`
	TokenSummary *struct {
		CycleStartTime string `json:"CycleStartTime"`
		CycleEndTime   string `json:"CycleEndTime"`
	} `json:"TokenSummary"`
}

// window is the plan's month: what it used of its cycle's quota, in
// credits (专业套餐, ProductType enterprise) or tokens (轻享套餐,
// enterprise-auto), and when the cycle ends; ok false when no quota is
// told.
func (t tencentPlan) window(now time.Time) (QuotaWindow, bool) {
	pk := t.PackageInfo
	if pk == nil {
		return QuotaWindow{}, false
	}
	limit, ok := volcNum(pk.CycleQuota)
	if !ok || limit <= 0 {
		limit, ok = volcNum(pk.TotalQuota)
	}
	used, uok := volcNum(pk.TotalUsed)
	if !ok || !uok || limit <= 0 {
		return QuotaWindow{}, false
	}
	w := QuotaWindow{Name: "Month", Span: 30 * 24 * time.Hour, Amount: used, Limit: limit, Unit: "credits",
		Used: max(0, min(100, used/limit*100))}
	if strings.EqualFold(t.ProductType, "enterprise-auto") {
		w.Unit = "tokens"
	}
	if t.TokenSummary != nil {
		if end, err := time.Parse(time.RFC3339, strings.TrimSpace(t.TokenSummary.CycleEndTime)); err == nil {
			w.ResetsAt = &end
		}
		if start, err := time.Parse(time.RFC3339, strings.TrimSpace(t.TokenSummary.CycleStartTime)); err == nil && w.ResetsAt != nil && w.ResetsAt.After(start) {
			w.Span = w.ResetsAt.Sub(start)
		}
	}
	if w.ResetsAt == nil {
		// the package's start and the cycle it is in: the cycle ends that
		// many months on, and never after the package does
		start, err := time.Parse(time.RFC3339, strings.TrimSpace(pk.StartTime))
		n, _ := volcNum(pk.CurrentCycle)
		if err == nil && (pk.CycleUnit == "" || strings.EqualFold(pk.CycleUnit, "month")) {
			end := start.AddDate(0, max(int(n), 1), 0)
			for !end.After(now) && n < 1200 {
				n++
				end = start.AddDate(0, int(n), 0)
			}
			if exp, err := time.Parse(time.RFC3339, strings.TrimSpace(pk.ExpireTime)); err == nil && exp.Before(end) {
				end = exp
			}
			w.ResetsAt = &end
		}
	}
	return w, true
}

// tc3Sign is the Authorization header of a POST to host's "/" with payload
// for action, at ts, as Tencent Cloud's TC3-HMAC-SHA256 makes it with the
// headers tencentAPICall sends: Content-Type, Host and X-TC-Action signed.
func tc3Sign(secretID, secretKey, service, host, action string, payload []byte, ts time.Time) string {
	date := ts.UTC().Format("2006-01-02")
	const signed = "content-type;host;x-tc-action"
	canonical := strings.Join([]string{
		http.MethodPost,
		"/",
		"",
		"content-type:" + tcContentType + "\n" +
			"host:" + host + "\n" +
			"x-tc-action:" + strings.ToLower(action) + "\n",
		signed,
		volcHex(payload),
	}, "\n")
	scope := date + "/" + service + "/tc3_request"
	toSign := strings.Join([]string{"TC3-HMAC-SHA256", strconv.FormatInt(ts.Unix(), 10), scope, volcHex([]byte(canonical))}, "\n")
	key := volcMAC([]byte("TC3"+secretKey), date)
	key = volcMAC(key, service)
	key = volcMAC(key, "tc3_request")
	sig := hex.EncodeToString(volcMAC(key, toSign))
	return "TC3-HMAC-SHA256 Credential=" + secretID + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig
}

// tencentAPICall asks TokenHub's cloud API action in region with params,
// signed with the SecretId and SecretKey, and gives the reply's body. The
// key is sent nowhere but tokenhub.tencentcloudapi.com. Tencent answers an
// error with 200 and Response.Error.
func tencentAPICall(ctx context.Context, secretID, secretKey, region, action string, params map[string]any, now time.Time) ([]byte, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+tcAPIHost+"/", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", tcContentType)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", tcAPIVersion)
	req.Header.Set("X-TC-Region", region)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("Authorization", tc3Sign(secretID, secretKey, tcAPIService, tcAPIHost, action, body, now))
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		Response struct {
			Error *struct{ Code, Message string } `json:"Error"`
		} `json:"Response"`
	}
	_ = json.Unmarshal(raw, &env)
	if e := env.Response.Error; e != nil && (e.Code != "" || e.Message != "") {
		msg := fmt.Sprintf("Tencent Cloud %s: %s %s", action, e.Code, e.Message)
		if tcRefusesKey(e.Code) {
			msg += "; " + tcKeyHint
		}
		return nil, fmt.Errorf("%s", msg)
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("Tencent Cloud %s: %s; %s", action, res.Status, tcKeyHint)
	case res.StatusCode >= 300:
		return nil, fmt.Errorf("Tencent Cloud %s: %s: %s", action, res.Status, volcHead(raw))
	}
	return raw, nil
}

// tcRefusesKey says a cloud API error code is about the API key or its
// rights (AuthFailure.SignatureFailure, AuthFailure.SecretIdNotFound,
// UnauthorizedOperation…), not the plan.
func tcRefusesKey(code string) bool {
	c := strings.ToLower(code)
	return strings.HasPrefix(c, "authfailure") || strings.Contains(c, "unauthorized")
}
