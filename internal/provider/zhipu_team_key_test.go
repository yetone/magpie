package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// #236: a Zhipu key on a team's GLM Coding Plan, added as a key and not
// through ZCode, has the team's organization and project typed into its
// provider: its windows are asked with type=2 and those two headers. A
// key without them is asked as before, with no team headers.
func TestZhipuKeyTeamFields(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		t.Setenv(v, "")
	}
	asked := map[string]http.Header{}
	hosts := map[string][]string{} // where each key's team quota was asked
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := r.Header.Get("Authorization")
		switch r.Header.Get("X-Host") + r.URL.Path {
		case "open.bigmodel.cn/api/monitor/usage/quota/limit", "bigmodel.cn/api/monitor/usage/quota/limit":
			if r.URL.Query().Get("type") != "2" {
				// a team's key has no plan of its own
				w.Write([]byte(`{"code":500,"msg":"no plan","success":false}`))
				return
			}
			hosts[key] = append(hosts[key], r.Header.Get("X-Host"))
			asked[key] = r.Header.Clone()
			if r.Header.Get("Bigmodel-Organization") != "org-1" || r.Header.Get("Bigmodel-Project") != "proj-1" {
				w.Write([]byte(`{"code":1001,"msg":"no team","success":false}`))
				return
			}
			w.Write([]byte(`{"code":200,"success":true,"data":{"limits":[
				{"type":"CREDIT_LIMIT","unit":3,"percentage":42,"currentValue":420,"usage":1000,"nextResetTime":1790000000000},
				{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":10,"nextResetTime":1790500000000}]}}`))
		default:
			t.Errorf("asked %s%s with %q", r.Header.Get("X-Host"), r.URL.Path, key)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	forget := func() {
		planQuotaCache.Lock()
		planQuotaCache.data = nil
		planQuotaCache.Unlock()
	}
	forget()
	t.Cleanup(forget)

	for _, p := range []Provider{
		{ID: "team", Name: "Team", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "team-k", ZhipuTeam: &ZhipuTeam{" org-1 ", "proj-1\n"}},
		{ID: "bare", Name: "Bare", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "bare-k"},
		{ID: "blank", Name: "Blank", Chat: "https://open.bigmodel.cn/api/paas/v4", Key: "blank-k", ZhipuTeam: &ZhipuTeam{" ", ""}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := Find("team"); p.ZhipuTeam == nil || *p.ZhipuTeam != (ZhipuTeam{"org-1", "proj-1"}) {
		t.Fatalf("kept: %+v", p.ZhipuTeam)
	}
	if p, _ := Find("blank"); p.ZhipuTeam != nil {
		t.Fatalf("blank kept: %+v", p.ZhipuTeam)
	}
	if p, _ := Find("team"); !TakesZhipuTeam(*p) {
		t.Fatal("a Zhipu key takes a team")
	}
	if TakesZhipuTeam(Provider{Chat: "https://api.z.ai/api/coding/paas/v4"}) != true ||
		TakesZhipuTeam(Provider{Chat: "https://api.deepseek.com/v1"}) {
		t.Fatal("takes a team: Z.ai does, DeepSeek doesn't")
	}

	got := map[string]SubscriptionQuota{}
	for _, q := range PlanQuotas(context.Background()) {
		got[q.Provider] = q
	}
	mu.Lock()
	defer mu.Unlock()
	q, ok := got["team"]
	if !ok || q.Error != "" || q.Plan != "GLM Coding Team" || len(q.Windows) != 2 ||
		q.Windows[0].Name != "5 hours" || q.Windows[0].Used != 42 || q.Windows[0].Span != 5*time.Hour ||
		q.Windows[1].Name != "7 days" || q.Windows[1].Used != 10 {
		t.Fatalf("team: %+v", q)
	}
	if hd := asked["team-k"]; hd.Get("Bigmodel-Organization") != "org-1" || hd.Get("Bigmodel-Project") != "proj-1" || hd.Get("Set-Language") != "zh" {
		t.Fatalf("team asked with %v", hd)
	}
	// at the key's own host, as CC Switch asks it; bigmodel.cn only when
	// that one has no windows
	if h := strings.Join(hosts["team-k"], ","); h != "open.bigmodel.cn" {
		t.Fatalf("team asked at %s", h)
	}
	if h := strings.Join(hosts["bare-k"], ","); h != "open.bigmodel.cn,bigmodel.cn" {
		t.Fatalf("bare asked at %s", h)
	}
	// without them nothing changes: asked with no team headers, no windows
	if hd, ok := asked["bare-k"]; !ok || hd.Get("Bigmodel-Organization") != "" || hd.Get("Bigmodel-Project") != "" {
		t.Fatalf("bare asked with %v (%v)", hd, ok)
	}
	if q := got["bare"]; len(q.Windows) != 0 || q.Error == "" {
		t.Fatalf("bare: %+v", q)
	}
	if _, ok := got["blank"]; ok {
		t.Fatalf("a pay-as-you-go key with no team has no card: %+v", got["blank"])
	}
}
