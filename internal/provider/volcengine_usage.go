package provider

// Volcengine Ark's Coding Plan and Agent Plan (#1427, yayoinoyume) have
// 5-hour, weekly and monthly windows, which Ark tells only to the
// account's control plane: GetCodingPlanUsage and GetAFPUsage on
// open.volcengineapi.com, signed with the account's AccessKey ID and
// Secret (AccessKeyID, SecretAccessKey), never to the plan's inference
// key. The plan a provider is on is its endpoint's: /api/coding is the
// Coding Plan's, /api/plan the Agent Plan's; pay-as-you-go (/api/v3) has
// no windows and no card.
//
// The request is signed as Volcengine's own SDK signs it
// (volc-sdk-golang base/sign.go GetSignRequest, the signer of
// volcengine-go-sdk's volcenginequery): HMAC-SHA256 over the canonical
// request with the signed headers sorted, the scope
// date/region/ark/request, and a key derived from the bare Secret.
// TestVolcSignMatchesSDK pins a signature that SDK made.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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
	volcOpenAPIHost   = "open.volcengineapi.com"
	volcAPIVersion    = "2024-01-01"
	volcDefaultRegion = "cn-beijing"
	volcService       = "ark"
	volcContentType   = "application/json"
	volcAKSKHint      = "check the AccessKey ID and Secret in the provider's editor, and that the access key may query Ark (IAM)"
)

// TakesVolcAccessKey says p is on Volcengine Ark (ark.<region>.volces.com),
// whose editor then asks for the account's AccessKey ID and Secret.
func TakesVolcAccessKey(p Provider) bool {
	if p.Account != nil {
		return false
	}
	for _, base := range []string{p.Chat, p.Responses, p.Anthropic} {
		if h := hostOf(base); strings.HasPrefix(h, "ark.") && strings.HasSuffix(h, ".volces.com") {
			return true
		}
	}
	return false
}

// volcPlanOf is the plan p's endpoint is on: the action that tells its
// windows, the plan's name and the region it is in; ok false for
// pay-as-you-go or another vendor.
func volcPlanOf(p Provider) (action, plan, region string, ok bool) {
	if !TakesVolcAccessKey(p) {
		return "", "", "", false
	}
	for _, base := range []string{p.Chat, p.Anthropic, p.Responses} {
		u, err := url.Parse(base)
		if err != nil || !strings.HasSuffix(strings.ToLower(u.Host), ".volces.com") {
			continue
		}
		switch path := strings.TrimSuffix(u.Path, "/") + "/"; {
		case strings.HasPrefix(path, "/api/coding/"):
			return "GetCodingPlanUsage", "Coding Plan", volcRegion(u.Host), true
		case strings.HasPrefix(path, "/api/plan/"):
			return "GetAFPUsage", "Agent Plan", volcRegion(u.Host), true
		}
	}
	return "", "", "", false
}

// volcRegion is the region of an Ark host: ark.cn-beijing.volces.com is
// cn-beijing; cn-beijing when it names none.
func volcRegion(host string) string {
	labels := strings.Split(strings.ToLower(host), ".")
	if len(labels) == 4 && labels[0] == "ark" && labels[1] != "" {
		return labels[1]
	}
	return volcDefaultRegion
}

// volcPlanQuotas is a card for each Volcengine Ark plan a provider in use
// is on and has the account's AccessKey ID and Secret for: one for each
// access key and plan, named for the first such provider. A plan no
// provider on it has the access key for is a card that says so
// (accessKeyCard, #1821): it was left out with nothing said.
func volcPlanQuotas(ctx context.Context) []SubscriptionQuota {
	type job struct {
		p                    Provider
		action, plan, region string
	}
	seen := map[string]bool{}
	var jobs []job
	keyed := map[string]bool{} // the plans a provider has the access key for
	var keyless []job
	for _, p := range planViews(All()) {
		if p.Hidden || p.Off {
			continue
		}
		action, plan, region, ok := volcPlanOf(p)
		if !ok {
			continue
		}
		if p.AccessKeyID == "" || p.SecretAccessKey == "" {
			// a custom provider is asked for none (AsksAccessKey)
			if AsksAccessKey(p) != "" {
				keyless = append(keyless, job{p, action, plan, region})
			}
			continue
		}
		keyed[action] = true
		if seen[p.AccessKeyID+" "+action] || !wantsCard(ctx, p.ID, "") {
			continue
		}
		seen[p.AccessKeyID+" "+action] = true
		jobs = append(jobs, job{p, action, plan, region})
	}
	var missing []SubscriptionQuota
	for _, j := range keyless {
		if keyed[j.action] || seen["none "+j.action] || !wantsCard(ctx, j.p.ID, "") {
			continue
		}
		seen["none "+j.action] = true
		missing = append(missing, accessKeyCard(j.p, j.plan))
	}
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = volcPlanQuota(j.p.Via(ctx), j.p, j.action, j.plan, j.region)
			if out[i].Error == "" {
				// what routing goes by, read just now (KeyAllowance)
				noteKeyAllowance(j.p, out[i].Windows, time.Now())
			}
			// a failed read shows what was last read, as a key's plan does
			out[i] = keepReading(ctx, out[i], keyTag("volc "+j.action, j.p.AccessKeyID))
		}()
	}
	wg.Wait()
	return append(out, missing...)
}

// volcPlanQuota is p's plan as a card, read with its access key.
func volcPlanQuota(ctx context.Context, p Provider, action, plan, region string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, Plan: plan, Windows: []QuotaWindow{}}
	raw, err := volcOpenAPICall(ctx, p.AccessKeyID, p.SecretAccessKey, region, action, time.Now())
	if err != nil {
		q.Error = err.Error()
		return q
	}
	var env struct {
		Result json.RawMessage `json:"Result"`
	}
	_ = json.Unmarshal(raw, &env)
	var ws []QuotaWindow
	if action == "GetAFPUsage" {
		var tier string
		ws, tier = volcReadAFP(env.Result)
		if tier != "" {
			q.Plan = plan + " " + tier
		}
	} else {
		ws = volcReadCodingPlan(env.Result)
	}
	if len(ws) == 0 {
		q.Error = fmt.Sprintf("this Volcengine account has no %s, or Ark answered %s in a form magpie doesn't know: %s", plan, action, volcHead(raw))
		return q
	}
	q.Windows = ws
	return q
}

// volcHead is the head of a reply, for a card to say what was answered.
func volcHead(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// volcSign is the Authorization header of a POST to the control plane's
// "/" with query and body, at now, as volc-sdk-golang's GetSignRequest
// makes it for the headers volcOpenAPICall sends: Content-Type, Host,
// X-Content-Sha256 and X-Date, signed in that (sorted) order.
func volcSign(ak, sk, region string, query url.Values, body []byte, now time.Time) (auth, xDate, contentSHA string) {
	xDate = now.UTC().Format("20060102T150405Z")
	date := xDate[:8]
	contentSHA = volcHex(body)
	const signed = "content-type;host;x-content-sha256;x-date"
	canonical := strings.Join([]string{
		http.MethodPost,
		"/",
		volcQuery(query),
		"content-type:" + volcContentType + "\n" +
			"host:" + volcOpenAPIHost + "\n" +
			"x-content-sha256:" + contentSHA + "\n" +
			"x-date:" + xDate + "\n",
		signed,
		contentSHA,
	}, "\n")
	scope := date + "/" + region + "/" + volcService + "/request"
	toSign := strings.Join([]string{"HMAC-SHA256", xDate, scope, volcHex([]byte(canonical))}, "\n")
	key := volcMAC([]byte(sk), date)
	for _, s := range []string{region, volcService, "request"} {
		key = volcMAC(key, s)
	}
	sig := hex.EncodeToString(volcMAC(key, toSign))
	return "HMAC-SHA256 Credential=" + ak + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig, xDate, contentSHA
}

// volcQuery is a query as the SDK signs and sends it: sorted by key,
// percent-encoded, a space as %20 (its normquery).
func volcQuery(v url.Values) string {
	return strings.ReplaceAll(v.Encode(), "+", "%20")
}

func volcMAC(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func volcHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// volcOpenAPICall asks the control plane's action, signed with the access
// key ak and its Secret sk, and gives the reply's body. The access key is
// sent nowhere but open.volcengineapi.com.
func volcOpenAPICall(ctx context.Context, ak, sk, region, action string, now time.Time) ([]byte, error) {
	query := url.Values{"Action": {action}, "Version": {volcAPIVersion}}
	body := []byte("{}")
	auth, xDate, contentSHA := volcSign(ak, sk, region, query, body, now)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+volcOpenAPIHost+"/?"+volcQuery(query), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", volcContentType)
	req.Header.Set("X-Date", xDate)
	req.Header.Set("X-Content-Sha256", contentSHA)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		ResponseMetadata struct {
			Error *struct{ Code, Message string } `json:"Error"`
		} `json:"ResponseMetadata"`
	}
	_ = json.Unmarshal(raw, &env)
	if e := env.ResponseMetadata.Error; e != nil && (e.Code != "" || e.Message != "") {
		msg := fmt.Sprintf("Volcengine %s: %s %s", action, e.Code, e.Message)
		if volcRefusesKey(e.Code) || res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			msg += "; " + volcAKSKHint
		}
		return nil, fmt.Errorf("%s", msg)
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("Volcengine %s: %s; %s", action, res.Status, volcAKSKHint)
	case res.StatusCode >= 300:
		return nil, fmt.Errorf("Volcengine %s: %s: %s", action, res.Status, volcHead(raw))
	}
	return raw, nil
}

// volcRefusesKey says a control-plane error code is about the access key
// or its rights (InvalidAccessKey, SignatureDoesNotMatch, AccessDenied…),
// not the plan.
func volcRefusesKey(code string) bool {
	c := strings.ToLower(code)
	for _, w := range []string{"accesskey", "signature", "accessdenied", "unauthorized", "forbidden", "credential", "authfailure"} {
		if strings.Contains(c, w) {
			return true
		}
	}
	return false
}

// volcReadCodingPlan reads GetCodingPlanUsage's Result:
//
//	{"QuotaUsage":[{"Level":"session","Percent":10.5,"ResetTimestamp":1758100000},
//	  {"Level":"weekly",…},{"Level":"monthly",…}]}
//
// Percent is the share used; ResetTimestamp is in seconds.
func volcReadCodingPlan(result json.RawMessage) []QuotaWindow {
	var r struct {
		QuotaUsage []struct {
			Level          string `json:"Level"`
			Percent        any    `json:"Percent"`
			ResetTimestamp any    `json:"ResetTimestamp"`
		} `json:"QuotaUsage"`
	}
	if json.Unmarshal(result, &r) != nil {
		return nil
	}
	var out []QuotaWindow
	for _, u := range r.QuotaUsage {
		w, ok := volcWindow(u.Level)
		if !ok {
			continue
		}
		used, ok := volcNum(u.Percent)
		if !ok {
			continue
		}
		w.Used, w.ResetsAt = used, volcTime(u.ResetTimestamp)
		out = append(out, w)
	}
	return out
}

// volcReadAFP reads GetAFPUsage's Result: AFPFiveHour, AFPWeekly and
// AFPMonthly (AFPDaily isn't shown by Ark's own console either), each
// with what was used of its quota and when it resets, and the plan's
// Tier. What each bucket carries is from tools built on it, not from a
// reply seen here: Used of Quota (lordqyxz/dsh-ark-quota), Used of Total
// or Percent (arkcli's struct tags, HaydenSmith1121/dsh-ark-plans), and a
// reset in ResetTime or ResetTimestamp, seconds or milliseconds.
func volcReadAFP(result json.RawMessage) ([]QuotaWindow, string) {
	type bucket struct {
		Used, Quota, Total, Percent, ResetTime, ResetTimestamp any
	}
	var r struct {
		Tier        string  `json:"Tier"`
		AFPFiveHour *bucket `json:"AFPFiveHour"`
		AFPWeekly   *bucket `json:"AFPWeekly"`
		AFPMonthly  *bucket `json:"AFPMonthly"`
	}
	if json.Unmarshal(result, &r) != nil {
		return nil, ""
	}
	var out []QuotaWindow
	for _, b := range []struct {
		level string
		b     *bucket
	}{{"session", r.AFPFiveHour}, {"weekly", r.AFPWeekly}, {"monthly", r.AFPMonthly}} {
		if b.b == nil {
			continue
		}
		w, _ := volcWindow(b.level)
		used, ok := volcNum(b.b.Percent)
		if !ok {
			n, nok := volcNum(b.b.Used)
			of, ook := volcNum(b.b.Quota)
			if !ook {
				of, ook = volcNum(b.b.Total)
			}
			if !nok || !ook || of <= 0 {
				continue // no quota: not on this plan
			}
			used = n / of * 100
			w.Amount, w.Limit = n, of
		}
		w.Used = used
		if w.ResetsAt = volcTime(b.b.ResetTime); w.ResetsAt == nil {
			w.ResetsAt = volcTime(b.b.ResetTimestamp)
		}
		out = append(out, w)
	}
	return out, strings.TrimSpace(r.Tier)
}

// volcWindow is the window a level names, as other plans' are named.
func volcWindow(level string) (QuotaWindow, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "session":
		return QuotaWindow{Name: "5 hours", Span: 5 * time.Hour}, true
	case "weekly":
		return QuotaWindow{Name: "Weekly", Span: 7 * 24 * time.Hour}, true
	case "monthly":
		return QuotaWindow{Name: "Month", Span: 30 * 24 * time.Hour}, true
	}
	return QuotaWindow{}, false
}

// volcNum is a JSON number, or a string of one.
func volcNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// volcTime is a reset as Ark gives it: epoch seconds (Coding Plan),
// milliseconds (Agent Plan) or RFC 3339; nil for none.
func volcTime(v any) *time.Time {
	if s, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s)); err == nil {
			return &t
		}
	}
	n, ok := volcNum(v)
	if !ok || n <= 0 {
		return nil
	}
	// 1e11 seconds is the year 5138, 1e11 ms 1973: no reset is on the
	// wrong side of it
	var t time.Time
	if n > 1e11 {
		t = time.UnixMilli(int64(n))
	} else {
		t = time.Unix(int64(n), 0)
	}
	return &t
}
