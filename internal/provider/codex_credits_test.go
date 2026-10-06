package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The credits a ChatGPT account holds besides its windows are its balance
// (#571), shown when it has some to spend and they are not unlimited.
func TestCodexCreditsBalance(t *testing.T) {
	for _, c := range []struct {
		name    string
		credits any
		want    string
	}{
		{"none said", nil, ""},
		{"held", map[string]any{"has_credits": true, "unlimited": false, "balance": "1234.5"}, "1234.5 credits"},
		{"a few", map[string]any{"has_credits": true, "unlimited": false, "balance": "42"}, "42 credits"},
		{"none held", map[string]any{"has_credits": false, "unlimited": false, "balance": "0"}, ""},
		{"spent", map[string]any{"has_credits": true, "unlimited": false, "balance": "0"}, ""},
		{"unlimited", map[string]any{"has_credits": true, "unlimited": true, "balance": ""}, ""},
		{"unreadable", map[string]any{"has_credits": true, "balance": "lots"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
					"primary_window": map[string]any{"used_percent": 100, "limit_window_seconds": 18000}}}
				if c.credits != nil {
					body["credits"] = c.credits
				}
				json.NewEncoder(w).Encode(body)
			}))
			defer fake.Close()
			old := CodexBase
			CodexBase = fake.URL + "/backend-api/codex"
			defer func() { CodexBase = old }()
			plan, windows, _, credits, _, err := codexWindows(context.Background(), "tok", "acct-1")
			if err != nil || plan != "plus" || len(windows) != 1 {
				t.Fatalf("plan %q, windows %v, err %v", plan, windows, err)
			}
			if credits != c.want {
				t.Errorf("credits %q, want %q", credits, c.want)
			}
		})
	}
}
