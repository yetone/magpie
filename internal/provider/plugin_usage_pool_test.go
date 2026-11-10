package provider

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's usage says the pool a window is of and the family a model's
// window belongs to, as Antigravity's plugin says them: the pool's own
// windows are pools (PoolWindow) holding the models put in them, and a page
// of text names them with their pool in place of the models' own, as the
// built-in's quota did.
func TestPluginWindowsKeepPoolAndFamily(t *testing.T) {
	week := float64(7 * 24 * 3600)
	q := quotaOfPlugin(SubscriptionQuota{Provider: "antigravity", User: "a@gmail.com"}, plugin.Usage{Windows: []plugin.UsageWindow{
		{Name: "Gemini 3.8 Flash", Model: "gemini-3.8-flash", Family: "gemini", Pool: "Gemini", Used: fptr(20), Span: week},
		{Name: "Claude Sonnet 4.6", Model: "claude-sonnet-4-6", Family: "claude", Used: fptr(5)},
		{Name: "7 days", Pool: "Gemini", Used: fptr(20), Span: week, Aside: true},
	}})
	if len(q.Windows) != 3 {
		t.Fatalf("windows %+v", q.Windows)
	}
	m, c, p := q.Windows[0], q.Windows[1], q.Windows[2]
	if m.Pool != "Gemini" || m.Family != "gemini" || c.Pool != "" || c.Family != "claude" || p.Pool != "Gemini" || p.Family != "" {
		t.Fatalf("pool/family %q/%q %q/%q %q/%q", m.Pool, m.Family, c.Pool, c.Family, p.Pool, p.Family)
	}
	if !p.PoolWindow() || m.PoolWindow() {
		t.Fatalf("PoolWindow: pool %v, model %v", p.PoolWindow(), m.PoolWindow())
	}
	if ms := PoolMembers(q.Windows, p); len(ms) != 1 || ms[0].Model != "gemini-3.8-flash" {
		t.Fatalf("members %+v", ms)
	}
	var names []string
	for _, w := range PooledWindows(q.Windows) {
		names = append(names, w.Name)
	}
	if len(names) != 2 || names[0] != "Claude Sonnet 4.6" || names[1] != "Gemini · 7 days" {
		t.Fatalf("pooled %q", names)
	}
	if p.Span != 7*24*time.Hour {
		t.Fatalf("span %v", p.Span)
	}
}
