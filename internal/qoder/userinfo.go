package qoder

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// UserInfoPath is the account endpoint, asked with the dt- device token (not
// the jt- job token). It answers who the signed-in account is.
const UserInfoPath = "/api/v1/userinfo"

// UserInfo is what the account endpoint says about a signed-in account.
type UserInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	Avatar      string `json:"avatar"`
	Source      string `json:"source"`
	OrgName     string `json:"organization_name"`
	HighestTier bool   `json:"is_highest_tier"`
}

// FetchUserInfo asks site's account endpoint (nil is the global one) with a
// device token. The device token is short-lived but outlasts the sign-in that
// just made it, so this runs during the flow while it is fresh.
func FetchUserInfo(ctx context.Context, client *http.Client, site *Site, deviceToken string) (*UserInfo, error) {
	if strings.TrimSpace(deviceToken) == "" {
		return nil, fmt.Errorf("qoder userinfo: missing device token")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if site == nil {
		site = Global
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, site.OpenAPI+UserInfoPath, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder userinfo: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder userinfo: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("qoder userinfo: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("qoder userinfo: HTTP %d: %s", resp.StatusCode, sanitize(body))
	}
	var u UserInfo
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("qoder userinfo: decode: %w", err)
	}
	return &u, nil
}
