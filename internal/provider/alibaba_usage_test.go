package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// the signature is fixed by the algorithm: this one was made by hand with
// openssl from the canonical request below
func TestAliACSSignIsACS3(t *testing.T) {
	now := time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC)
	h := aliACSSign("AKID", "SECRET", "modelstudio.cn-beijing.aliyuncs.com", aliACSPath, aliACSAction, aliACSVersion, nil, now, "nonce1")
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if h["x-acs-content-sha256"] != empty || h["x-acs-date"] != "2026-11-12T01:02:03Z" {
		t.Fatalf("%v", h)
	}
	signed := "content-type;host;x-acs-action;x-acs-content-sha256;x-acs-date;x-acs-signature-nonce;x-acs-version"
	pre := "ACS3-HMAC-SHA256 Credential=AKID,SignedHeaders=" + signed + ",Signature="
	auth := h["authorization"]
	if !strings.HasPrefix(auth, pre) || len(auth) != len(pre)+64 {
		t.Fatalf("Authorization %q", auth)
	}
	// another secret, another signature
	if aliACSSign("AKID", "OTHER", "modelstudio.cn-beijing.aliyuncs.com", aliACSPath, aliACSAction, aliACSVersion, nil, now, "nonce1")["authorization"] == auth {
		t.Fatal("the secret doesn't sign")
	}
}

func TestAliReadWindows(t *testing.T) {
	ws := aliReadWindows([]byte(`{"per1MonthPercentage":"0.25","per1MonthResetTime":1780000000000,"per5HourPercentage":0.1,"per5HourResetTime":1770000000000,"per1WeekPercentage":0.5,"planName":"x","per1DayPercentage":null}`))
	if len(ws) != 3 || ws[0].Name != "5 hours" || ws[1].Name != "Weekly" || ws[2].Name != "Month" {
		t.Fatalf("%+v", ws)
	}
	if ws[0].Used != 10 || ws[2].Used != 25 || ws[0].Span != 5*time.Hour || ws[2].ResetsAt.UnixMilli() != 1780000000000 {
		t.Fatalf("%+v", ws)
	}
	if len(aliReadWindows([]byte(`{"x":1}`))) != 0 {
		t.Fatal("windows from nothing")
	}
}

func TestAliPlanSite(t *testing.T) {
	for url, region := range map[string]string{
		"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1": "cn-beijing",
		"https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic": "ap-southeast-1",
		"https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1":        "",
		"https://token-plan.maas.qwencloudapi.com/compatible-mode/v1":        "",
		"https://dashscope-intl.aliyuncs.com/compatible-mode/v1":             "",
		"https://ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1":        "",
	} {
		s, ok := aliPlanSite(Provider{Chat: url})
		if ok != (region != "") || s.region != region {
			t.Errorf("%s: %v %q", url, ok, s.region)
		}
	}
	if _, ok := aliPlanSite(Provider{Chat: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", Account: &Account{}}); ok {
		t.Error("an account is a plan's")
	}
	for _, id := range []string{"bailian-token-plan", "alibaba-token-plan"} {
		p, err := FromPreset(id)
		if err != nil || AccessKeyVendor(p) != "alibaba" {
			t.Errorf("%s: %v", id, err)
		}
	}
}

func TestAliPlanQuotas(t *testing.T) {
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
	var mu sync.Mutex
	mints, reads := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		host := r.Header.Get("X-Host")
		mu.Lock()
		defer mu.Unlock()
		switch host {
		case "modelstudio.cn-beijing.aliyuncs.com":
			if r.Method != http.MethodPost || r.URL.Path != aliACSPath || r.URL.RawQuery != "" || len(body) != 0 ||
				r.Header.Get("x-acs-action") != aliACSAction || r.Header.Get("x-acs-version") != aliACSVersion {
				t.Errorf("mint %s %s with %q", r.Method, r.URL, body)
			}
			auth := r.Header.Get("Authorization")
			if strings.Contains(auth, "sk-sp-key") {
				t.Errorf("the plan's key was sent: %s", auth)
			}
			if !strings.Contains(auth, "Credential=AK1,") {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"Code":"InvalidAccessKeyId.NotFound","Message":"Specified access key is not found."}`)
				return
			}
			date, _ := time.Parse("2006-01-02T15:04:05Z", r.Header.Get("x-acs-date"))
			want := aliACSSign("AK1", "SK1", host, aliACSPath, aliACSAction, aliACSVersion, nil, date, r.Header.Get("x-acs-signature-nonce"))["authorization"]
			if auth != want {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"Code":"SignatureDoesNotMatch","Message":"bad"}`)
				return
			}
			mints++
			io.WriteString(w, `{"cliAccessToken":"tok2"}`)
		case "bailian-cs.console.aliyun.com":
			reads++
			q := r.URL.Query()
			f, _ := url.ParseQuery(string(body))
			if r.Method != http.MethodPost || r.URL.Path != "/cli/api.json" || q.Get("action") != "BroadScopeAspnGateway" || q.Get("product") != "sfm_bailian" ||
				q.Get("api") != aliUsageAPI || f.Get("region") != "cn-beijing" || !strings.Contains(f.Get("params"), aliUsageAPI) {
				t.Errorf("usage %s %s with %q", r.Method, r.URL, body)
			}
			// the first token is refused once: the reader makes another
			if r.Header.Get("Authorization") == "Bearer tok1" {
				io.WriteString(w, `{"code":"200","data":{"success":false,"errorCode":"BailianGateway.Login.NotLogined"}}`)
				return
			}
			if r.Header.Get("Authorization") != "Bearer tok2" {
				t.Errorf("Authorization %q", r.Header.Get("Authorization"))
			}
			io.WriteString(w, `{"code":"200","data":{"success":true,"DataV2":{"data":{"data":{"per5HourPercentage":0.1,"per5HourResetTime":1770000000000,"per1WeekPercentage":0.2,"per1MonthPercentage":0.3,"per1MonthResetTime":1780000000000}}}}}`)
		default:
			t.Errorf("asked %s %s", host, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	aliTokensMu.Lock()
	// a token kept from before, which the console no longer takes
	aliTokens = map[string]aliToken{"AK1 cn-beijing": {"tok1", time.Now()}}
	aliTokensMu.Unlock()
	forgetPlanQuotas()
	t.Cleanup(forgetPlanQuotas)

	const cn = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
	for _, p := range []Provider{
		{ID: "bailian-token-plan", Name: "Bailian Token Plan", Chat: cn, Key: "sk-sp-key", AccessKeyID: "AK1", SecretAccessKey: "SK1"},
		// the same access key: one card
		{ID: "bailian-copy", Name: "Bailian copy", Chat: cn, Key: "sk-sp-key", AccessKeyID: "AK1", SecretAccessKey: "SK1"},
		{ID: "bailian-bad", Name: "Bailian bad", Chat: cn, Key: "sk-sp-key", AccessKeyID: "AKbad", SecretAccessKey: "SKbad"},
		// no access key for the Singapore region: a card that asks for it
		{ID: "alibaba-token-plan", Name: "Alibaba Cloud Token Plan", Preset: "alibaba-token-plan", Chat: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", Key: "sk-sp-key"},
		// not a plan's host
		{ID: "qwen", Name: "Qwen", Chat: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Key: "sk-x", AccessKeyID: "AK1", SecretAccessKey: "SK1"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]SubscriptionQuota{}
	for _, q := range PlanQuotas(context.Background()) {
		if q.Provider == "bailian-token-plan" || q.Provider == "bailian-copy" || q.Provider == "bailian-bad" || q.Provider == "alibaba-token-plan" || q.Provider == "qwen" {
			got[q.Provider] = q
		}
	}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	q := got["bailian-token-plan"]
	if q.Error != "" || len(q.Windows) != 3 || q.Windows[0].Name != "5 hours" || q.Windows[0].Used != 10 || q.Windows[2].Name != "Month" || q.Windows[2].Used != 30 {
		t.Fatalf("%+v", q)
	}
	if mints != 1 || reads != 2 {
		// tok1 refused, tok2 made and read once; AKbad's mint is refused
		t.Errorf("mints %d, reads %d", mints, reads)
	}
	if b := got["bailian-bad"]; !strings.Contains(b.Error, "AccessKey") || strings.Contains(b.Error, "SKbad") {
		t.Errorf("bad key: %+v", b)
	}
	if a := got["alibaba-token-plan"]; a.Error == "" || !strings.Contains(a.Error, "AccessKey") {
		t.Errorf("keyless: %+v", a)
	}
}
