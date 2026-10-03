package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Discovery and routing agree when a new model is available only to a
// secondary account, including the family id clients actually request.
func TestAntigravityPoolRoutesDiscoveredModels(t *testing.T) {
	fresh(t)
	var logins []map[string]any
	for _, user := range []string{"old", "new"} {
		logins = append(logins, map[string]any{
			"agent": "antigravity", "user": user + "@example.com", "on": true,
			"auth": map[string]any{"access_token": user, "refresh_token": "pool-test-" + user,
				"project": "project-" + user, "expiry_date": time.Now().Add(time.Hour).UnixMilli()},
		})
	}
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), mustJSON(logins), 0o600); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasSuffix(r.URL.Path, "latest-arm64-mac.yml"):
			body = "version: 2.9.1\n"
		case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			if r.Header.Get("Authorization") == "Bearer old" {
				body = `{"models":{"claude-opus-4-6-thinking":{}}}`
			} else {
				body = `{"models":{"claude-opus-5-5-high":{},"claude-opus-5-5-low":{}}}`
			}
		default:
			return nil, fmt.Errorf("unexpected request to %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = old })
	p, err := provider.Find("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for model, user := range map[string]string{
		"claude-opus-5-5": "new@example.com", "claude-opus-5-5-high": "new@example.com",
		"claude-opus-4-6-thinking": "old@example.com",
	} {
		on, aside, left := perKeyOf(*p, model, provider.Chat)
		if len(on) != 1 || on[0].p.Account.User != user || len(aside) != 0 || len(left) != 1 {
			t.Errorf("%s: eligible=%d, aside=%d, not listed=%d; expected only %s", model, len(on), len(aside), len(left), user)
		}
		if model == "claude-opus-5-5" && len(on) == 1 {
			if !takesEffort(on[0], "high") || takesEffort(on[0], "xhigh") {
				t.Error("routing did not use the account's own reasoning levels")
			}
		}
	}
}
