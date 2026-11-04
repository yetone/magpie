package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// The signature is the one Tencent Cloud's signing doc (api/213/30654)
// works through: DescribeInstances on cvm.tencentcloudapi.com at
// 1551113065, its payload byte for byte.
func TestTC3SignMatchesDoc(t *testing.T) {
	// the doc sends the name as JSON escapes (a backslash and u672a…), not UTF-8
	esc := strings.ReplaceAll("#u672a#u547d#u540d", "#", `\`)
	payload := []byte(`{"Limit": 1, "Filters": [{"Values": ["` + esc + `"], "Name": "instance-name"}]}`)
	if got := volcHex(payload); got != "35e9c5b0e3ae67532d3c9f17ead6c90222632e5b1ff7f6e89887f1398934f064" {
		t.Fatalf("payload hash %s", got)
	}
	id, key := "AKID"+strings.Repeat("*", 32), strings.Repeat("*", 32)
	got := tc3Sign(id, key, "cvm", "cvm.tencentcloudapi.com", "DescribeInstances", payload, time.Unix(1551113065, 0))
	want := "TC3-HMAC-SHA256 Credential=" + id + "/2019-02-25/cvm/tc3_request, SignedHeaders=content-type;host;x-tc-action, Signature=10b1a37a7301a02ca19a647ad722d5e43b4b3cff309d421d85b46093f6ab6c4f"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	// a time in another zone signs with the UTC date
	cst := time.Unix(1551113065, 0).In(time.FixedZone("CST", 8*3600))
	if tc3Sign(id, key, "cvm", "cvm.tencentcloudapi.com", "DescribeInstances", payload, cst) != want {
		t.Error("CST time signed differently")
	}
}

// Only the enterprise plan's endpoints take an access key: /plan on
// TokenHub's hosts. Its pay as you go, the personal plan, another vendor
// and an account don't; Volcengine's stay Volcengine's.
func TestTencentPlanRegion(t *testing.T) {
	for _, c := range []struct {
		p      Provider
		region string
	}{
		{Provider{Chat: "https://tokenhub.tencentmaas.com/plan/v3"}, "ap-guangzhou"},
		{Provider{Anthropic: "https://tokenhub.tencentmaas.com/plan/anthropic"}, "ap-guangzhou"},
		{Provider{Responses: "https://TokenHub-Intl.tencentmaas.com/plan/v3/"}, "ap-singapore"},
		{Provider{Chat: "https://tokenhub.tencentmaas.com/v1"}, ""},
		{Provider{Chat: "https://api.lkeap.cloud.tencent.com/plan/v3"}, ""},
		{Provider{Chat: "https://relay.example.com/plan/v3"}, ""},
		{Provider{Chat: "https://tokenhub.tencentmaas.com/planx/v3"}, ""},
		{Provider{Chat: "https://tokenhub.tencentmaas.com/plan/v3", Account: &Account{}}, ""},
	} {
		region, ok := tencentPlanRegion(c.p)
		if region != c.region || ok != (c.region != "") || TakesTencentAccessKey(c.p) != ok {
			t.Errorf("%+v: %q %v", c.p, region, ok)
		}
	}
	for _, c := range []struct {
		p      Provider
		vendor string
	}{
		{Provider{Chat: "https://tokenhub.tencentmaas.com/plan/v3"}, "tencent"},
		{Provider{Chat: "https://ark.cn-beijing.volces.com/api/coding/v3"}, "volcengine"},
		{Provider{Chat: "https://api.lkeap.cloud.tencent.com/plan/v3"}, ""},
	} {
		if got := AccessKeyVendor(c.p); got != c.vendor {
			t.Errorf("AccessKeyVendor(%s) = %q", c.p.Chat, got)
		}
	}
}

// The docs' PackageInfo (quotas as strings) and TokenSummary: used of the
// cycle's quota, credits for 专业套餐 and tokens for 轻享套餐, resetting at
// the cycle's end; with no TokenSummary the cycle is counted from the
// package's start, never past its expiry; no quota is no window.
func TestTencentPlanWindow(t *testing.T) {
	var p tencentPlan
	if err := json.Unmarshal([]byte(`{"TeamId":"team-a1b2c3d4","ProductType":"enterprise",
		"PackageInfo":{"TotalQuota":"1000000","TotalUsed":"100000","TotalCycles":1,"CycleUnit":"month","StartTime":"2026-04-01T00:00:00Z","ExpireTime":"2027-04-01T00:00:00Z",
			"ExclusiveAllocated":"500000","ExclusiveUsed":"100000","SharedPool":"500000","SharedUsed":"50000","CycleQuota":"1000000","CurrentCycle":1,"RemainCycles":0},
		"TokenSummary":{"CycleSeq":1,"CycleStartTime":"2026-04-01T00:00:00+08:00","CycleEndTime":"2026-05-01T00:00:00+08:00",
			"BillingItems":[{"BillingItem":"input","TotalQty":"60000"},{"BillingItem":"output","TotalQty":"40000"}]}}`), &p); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	w, ok := p.window(now)
	if !ok || w.Name != "Month" || w.Used != 10 || w.Amount != 100000 || w.Limit != 1000000 || w.Unit != "credits" ||
		w.ResetsAt == nil || !w.ResetsAt.Equal(time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)) || w.Span != 30*24*time.Hour {
		t.Fatalf("%+v %v", w, ok)
	}
	p.ProductType = "enterprise-auto"
	if w, _ := p.window(now); w.Unit != "tokens" {
		t.Errorf("轻享套餐 unit %q", w.Unit)
	}
	// no TokenSummary: month 1 from StartTime, and on past it as time goes
	p.TokenSummary = nil
	if w, _ := p.window(now); w.ResetsAt == nil || !w.ResetsAt.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("from StartTime: %+v", w.ResetsAt)
	}
	if w, _ := p.window(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)); w.ResetsAt == nil || !w.ResetsAt.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("July: %+v", w.ResetsAt)
	}
	p.PackageInfo.ExpireTime = "2026-07-20T00:00:00Z"
	if w, _ := p.window(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)); w.ResetsAt == nil || !w.ResetsAt.Equal(time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("capped at expiry: %+v", w.ResetsAt)
	}
	// no cycle quota: the total's; used past it shows full
	p.PackageInfo.CycleQuota, p.PackageInfo.TotalUsed = nil, 2000000.0
	if w, ok := p.window(now); !ok || w.Limit != 1000000 || w.Used != 100 {
		t.Errorf("TotalQuota: %+v", w)
	}
	for _, pk := range []string{`{}`, `{"TotalQuota":"0","TotalUsed":"1"}`, `{"TotalQuota":"10"}`} {
		var q tencentPlan
		json.Unmarshal([]byte(`{"PackageInfo":`+pk+`}`), &q)
		if _, ok := q.window(now); ok {
			t.Errorf("%s gave a window", pk)
		}
	}
	if _, ok := (tencentPlan{}).window(now); ok {
		t.Error("no PackageInfo gave a window")
	}
}

// The Usage page has a card for each team plan the SecretId can see, read
// from tokenhub.tencentcloudapi.com only, each request signed as TC3 with
// the SecretKey and never with the plan's own key; a refused key says
// what to check, an account with no plan says so, and the personal plan,
// a provider with no SecretKey and one switched off have none.
func TestTencentPlanQuotas(t *testing.T) {
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
	secrets := map[string]string{"AKIDone": "SK1", "AKIDtwo": "SK2", "AKIDnone": "SK4", "AKIDbad": "wrong"}
	var mu sync.Mutex
	asked := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		action := r.Header.Get("X-TC-Action")
		if r.Header.Get("X-Host") != tcAPIHost || r.Method != http.MethodPost || r.URL.Path != "/" ||
			r.Header.Get("X-TC-Version") != "2026-03-22" || r.Header.Get("Content-Type") != tcContentType {
			t.Errorf("asked %s %s%s %s %v", r.Method, r.Header.Get("X-Host"), r.URL, action, r.Header)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "sk-tp") || strings.Contains(string(body), "sk-tp") {
			t.Errorf("the plan's key was sent: %s %s", auth, body)
		}
		cred, _, _ := strings.Cut(strings.TrimPrefix(auth, "TC3-HMAC-SHA256 Credential="), "/")
		ts, _ := strconv.ParseInt(r.Header.Get("X-TC-Timestamp"), 10, 64)
		region := r.Header.Get("X-TC-Region")
		if want := tc3Sign(cred, secrets[cred], "tokenhub", tcAPIHost, action, body, time.Unix(ts, 0)); cred == "AKIDbad" || auth != want {
			io.WriteString(w, `{"Response":{"Error":{"Code":"AuthFailure.SignatureFailure","Message":"The provided credentials could not be validated."},"RequestId":"e1"}}`)
			return
		}
		mu.Lock()
		asked[cred+" "+region+" "+action]++
		mu.Unlock()
		var params map[string]any
		json.Unmarshal(body, &params)
		switch {
		case action == "DescribeTokenPlanList" && cred == "AKIDnone":
			io.WriteString(w, `{"Response":{"RequestId":"r0","TokenPlanSet":[],"TotalCount":0}}`)
		case action == "DescribeTokenPlanList" && cred == "AKIDone":
			// the docs' example
			io.WriteString(w, `{"Response":{"RequestId":"be515245-e05f-4334-9f9d-6d55db1aa8b5","TokenPlanSet":[{"ApiKeyMax":10000,"AppId":"176623905","CreatedAt":"2026-04-05T21:06:09+08:00","Creator":"176623905","Name":"my-team-test-1","PrepayResourceID":"pre-123456","Status":"enable","StopReason":"","TeamId":"team-a1b2c3d4","Uin":"214508345","UpdatedAt":"2026-04-07T23:26:01+08:00","ProductType":"enterprise"}],"TotalCount":1}}`)
		case action == "DescribeTokenPlanList" && cred == "AKIDtwo":
			io.WriteString(w, `{"Response":{"RequestId":"r2","TokenPlanSet":[
				{"Name":"alpha","Status":"enable","TeamId":"team-a","ProductType":"enterprise-auto","PackageInfo":{"TotalQuota":"5000000","TotalUsed":"1250000","CycleQuota":"5000000","StartTime":"2026-04-01T00:00:00Z","ExpireTime":"2027-04-01T00:00:00Z","CurrentCycle":1}},
				{"Name":"beta","Status":"isolate","StopReason":"arrears","TeamId":"team-b","ProductType":"enterprise"}],"TotalCount":2}}`)
		case action == "DescribeTokenPlan" && params["TeamId"] == "team-a1b2c3d4":
			io.WriteString(w, `{"Response":{"ApiKeyCount":10,"ApiKeyMax":10000,"Name":"my-team-test-1","Status":"enable","TeamId":"team-a1b2c3d4","RequestId":"45b1f117",
				"PackageInfo":{"TotalQuota":"1000000","TotalUsed":"100000","CycleUnit":"month","StartTime":"2026-04-01T00:00:00Z","ExpireTime":"2027-04-01T00:00:00Z","CycleQuota":"1000000","CurrentCycle":1},
				"TokenSummary":{"CycleSeq":1,"CycleStartTime":"2026-04-01T00:00:00+08:00","CycleEndTime":"2026-05-01T00:00:00+08:00","BillingItems":[]}}}`)
		case action == "DescribeTokenPlan" && params["TeamId"] == "team-a":
			// the docs' own example: no PackageInfo, the list's is kept
			io.WriteString(w, `{"Response":{"ApiKeyCount":1,"Name":"alpha","Status":"enable","TeamId":"team-a","RequestId":"45b1f117"}}`)
		case action == "DescribeTokenPlan" && params["TeamId"] == "team-b":
			io.WriteString(w, `{"Response":{"Name":"beta","Status":"isolate","StopReason":"arrears","TeamId":"team-b","RequestId":"r3"}}`)
		default:
			t.Errorf("action %q %s by %s", action, body, cred)
		}
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	forgetPlanQuotas()
	t.Cleanup(forgetPlanQuotas)

	ent, intl := "https://tokenhub.tencentmaas.com/plan/v3", "https://tokenhub-intl.tencentmaas.com/plan/v3"
	for _, p := range []Provider{
		{ID: "tencent-cloud", Name: "Tencent Cloud", Chat: ent, Anthropic: "https://tokenhub.tencentmaas.com/plan/anthropic", Key: "sk-tp-1", AccessKeyID: "AKIDone", SecretAccessKey: "SK1"},
		// the same key and region again: asked once
		{ID: "tc-copy", Name: "Tencent copy", Chat: ent, Key: "sk-tp-1", AccessKeyID: "AKIDone", SecretAccessKey: "SK1"},
		{ID: "tc-intl", Name: "Tencent intl", Chat: intl, Key: "sk-tp-2", AccessKeyID: "AKIDtwo", SecretAccessKey: "SK2"},
		{ID: "tc-none", Name: "Tencent none", Chat: ent, Key: "sk-tp-3", AccessKeyID: "AKIDnone", SecretAccessKey: "SK4"},
		{ID: "tc-bad", Name: "Tencent bad", Chat: ent, Key: "sk-tp-4", AccessKeyID: "AKIDbad", SecretAccessKey: "wrong"},
		{ID: "tc-personal", Name: "Tencent personal", Chat: "https://api.lkeap.cloud.tencent.com/plan/v3", Key: "sk-tp-5", AccessKeyID: "AKIDone", SecretAccessKey: "SK1"},
		{ID: "tc-nosecret", Name: "Tencent half", Chat: ent, Key: "sk-tp-6", AccessKeyID: "AKIDhalf"},
		{ID: "tc-off", Name: "Tencent off", Chat: ent, Key: "sk-tp-7", AccessKeyID: "AKIDoff", SecretAccessKey: "SKoff", Off: true},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	var cards []SubscriptionQuota
	for _, q := range PlanQuotas(context.Background()) {
		if q.Plan != "" && (strings.HasPrefix(q.Provider, "tc-") || q.Provider == "tencent-cloud") {
			cards = append(cards, q)
		}
	}
	by := map[string]SubscriptionQuota{}
	for _, q := range cards {
		by[q.Provider+"|"+q.User] = q
	}
	if len(cards) != 5 || len(by) != 5 {
		t.Fatalf("cards: %+v", cards)
	}
	one, ok := by["tencent-cloud|"]
	if !ok {
		one = by["tc-copy|"]
	}
	if one.Plan != "Token Plan Enterprise" || one.Error != "" || len(one.Windows) != 1 || one.Windows[0].Used != 10 || one.Windows[0].Unit != "credits" ||
		one.Windows[0].ResetsAt == nil || !one.Windows[0].ResetsAt.Equal(time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("one plan: %+v", one)
	}
	alpha := by["tc-intl|alpha"]
	if alpha.Error != "" || len(alpha.Windows) != 1 || alpha.Windows[0].Used != 25 || alpha.Windows[0].Unit != "tokens" || alpha.Windows[0].ResetsAt == nil {
		t.Errorf("alpha: %+v", alpha)
	}
	beta := by["tc-intl|beta"]
	if beta.Plan != "Token Plan Enterprise · isolate, arrears" || !strings.Contains(beta.Error, "told no quota") {
		t.Errorf("beta: %+v", beta)
	}
	if q := by["tc-none|"]; !strings.Contains(q.Error, "no Token Plan Enterprise in ap-guangzhou") {
		t.Errorf("no plan: %+v", q)
	}
	if q := by["tc-bad|"]; !strings.Contains(q.Error, "AuthFailure.SignatureFailure") || !strings.Contains(q.Error, "SecretId and SecretKey") {
		t.Errorf("refused: %+v", q)
	}
	mu.Lock()
	defer mu.Unlock()
	for k, want := range map[string]int{
		"AKIDone ap-guangzhou DescribeTokenPlanList": 1, "AKIDone ap-guangzhou DescribeTokenPlan": 1,
		"AKIDtwo ap-singapore DescribeTokenPlanList": 1, "AKIDtwo ap-singapore DescribeTokenPlan": 2,
		"AKIDnone ap-guangzhou DescribeTokenPlanList": 1,
	} {
		if asked[k] != want {
			t.Errorf("%s asked %d times, want %d (%v)", k, asked[k], want, asked)
		}
	}
	if len(asked) != 5 {
		t.Errorf("asked %v", asked)
	}
}

// A custom provider at the Token Plan's address is the user's own (ysm119):
// its key goes as typed, as a Bearer token on chat and Responses, with no
// Tencent Cloud signing, and nothing asks it for a SecretId and SecretKey.
func TestCustomTokenHubIsSentAsTyped(t *testing.T) {
	p := Provider{ID: "mine", Chat: "https://tokenhub.tencentmaas.com/plan/v3", Key: "sk-tFAKEueJF"}
	for _, proto := range []Protocol{Chat, Responses} {
		if h := AuthHeaders(p, proto); len(h) != 1 || h["Authorization"] != "Bearer sk-tFAKEueJF" {
			t.Errorf("%s: %v", proto, h)
		}
	}
	if v := AsksAccessKey(p); v != "" {
		t.Errorf("a custom provider is asked for %s's access key", v)
	}
	p.Preset = "tencent-cloud"
	if v := AsksAccessKey(p); v != "tencent" {
		t.Errorf("the preset's provider: %q", v)
	}
	p.Preset, p.AccessKeyID = "", "AKID"
	if v := AsksAccessKey(p); v != "tencent" {
		t.Errorf("a custom provider with one saved: %q", v)
	}
}
