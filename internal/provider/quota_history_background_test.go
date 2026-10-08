package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A background reading of an account is a point of the account's quota
// history too, as a Usage-page reading is: a magpie only ever serving
// other magpies has nobody on its Usage page, so its routing read each
// account every few minutes while GET /v1/magpie/quotas/history stayed
// empty (#1313; yetone on #1317).
func TestBackgroundLoginReadNotesQuotaHistory(t *testing.T) {
	azureHome(t)
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "solo@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-solo"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-solo", "account_id": "acct-solo",
		},
	})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 18000}}})
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// rememberLogins is memoized for 30s process-wide; another test may
	// have just run it, and then this account is never saved and read.
	loginsMu.Lock()
	loginsSeenAt = time.Time{}
	loginsMu.Unlock()

	// routing's read, no Usage page involved
	if got := LoginUsage(ctx, "codex"); len(got) == 0 {
		t.Fatal("LoginUsage read no account")
	}

	hs := QuotaHistories(time.Now().Add(-time.Hour), "codex", "solo@example.com")
	if len(hs) == 0 || len(hs[0].Lines) == 0 || len(hs[0].Lines[0].Points) == 0 {
		t.Fatalf("background read noted no history: %+v", hs)
	}
}
