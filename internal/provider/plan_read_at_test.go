package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// jiabubu (Discord #feedback): on Usage → Overview the Kimi Code card
// said nothing of when it was read, while every other card said "Updated
// 3 minutes ago". A plan key's card (PlanQuotas: Kimi Code, OpenCode Go,
// a GLM Coding Plan, MiniMax, Ollama) was handed out with no readAt, so
// the page's readWhen had nothing to show. One read just now says now.
func TestPlanKeyCardSaysWhenRead(t *testing.T) {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Host")+r.URL.Path != "api.kimi.com/coding/v1/usages" {
			t.Errorf("asked %s%s", r.Header.Get("X-Host"), r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"usage":{"limit":"100","used":"12","resetTime":"2026-11-10T00:00:00Z"},
			"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","used":"11","resetTime":"2026-11-04T11:25:00Z"}}]}`))
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	if err := Save(FromPresetKey(t, "kimi-code-cn", "sk-kimi-cn")); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	var q *SubscriptionQuota
	for _, c := range PlanQuotas(context.Background()) {
		if c.Provider == "kimi-code-cn" {
			q = &c
		}
	}
	if q == nil || q.Error != "" || len(q.Windows) == 0 {
		t.Fatalf("Kimi Code's card: %+v", q)
	}
	if q.ReadAt == nil || q.ReadAt.Before(before) || q.ReadAt.After(time.Now()) {
		t.Fatalf("Kimi Code's card says no time it was read: readAt %v, asOf %v", q.ReadAt, q.AsOf)
	}
}
