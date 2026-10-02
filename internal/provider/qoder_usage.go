package provider

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
	"errors"
	"fmt"
	"net/http"

	"github.com/yetone/magpie/internal/qoder"
)

// Account endpoints use the device token, independently of the chat job token.
func qoderLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	site := qoder.SiteOf(l.Agent)
	q := SubscriptionQuota{Provider: site.ID, User: l.User, Name: site.Name, Icon: "qoder", Plan: l.Plan, Windows: []QuotaWindow{}}
	c, err := QoderCredentialOf(ctx, site.ID, l.User)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	raw, err := qoder.FetchUsage(ctx, qoderClient, site.OpenAPI, c.DeviceToken)
	var status *qoder.UsageHTTPError
	if errors.As(err, &status) && (status.StatusCode == 401 || status.StatusCode == 403) {
		var token string
		token, err = qoderRefreshDevice(ctx, site.ID, l.User, c.DeviceToken)
		if err == nil {
			raw, err = qoder.FetchUsage(ctx, qoderClient, site.OpenAPI, token)
		}
	}
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q, err = parseQoderQuota(raw, q)
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

func qoderRefreshDevice(ctx context.Context, agent, user, attempted string) (string, error) {
	site := qoder.SiteOf(agent)
	qoderMu.Lock()
	defer qoderMu.Unlock()
	l, found := qoderLookup(agent, user)
	if !found {
		return "", fmt.Errorf("no %s account %q", site.Name, user)
	}
	c, ok, pending := qoderCurrent(l)
	if !ok {
		return "", fmt.Errorf("%s: unreadable sign-in", site.Name)
	}
	if c.DeviceToken != attempted {
		if pending {
			if err := qoderPersist(l, c, false); err != nil {
				return "", err
			}
		}
		return c.DeviceToken, nil
	}
	// the device refresh token rotates too: keep its reply past the caller
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), qoderRefreshTimeout)
	dt, err := qoder.RefreshDeviceToken(rctx, qoderClient, site.OpenAPI+qoder.DeviceTokenRefreshPath, c.DeviceRefresh)
	cancel()
	if err != nil && c.DeviceChat {
		// the device token is the chat token too: a refused one is the sign-in gone
		var refused *qoder.DeviceTokenRefreshHTTPError
		if errors.As(err, &refused) {
			err = &qoder.JobTokenRefreshHTTPError{StatusCode: refused.StatusCode}
		}
		return "", qoderRefreshFailed(agent, user, err)
	}
	if err != nil {
		// The device token serves only the account pages (usage); chat
		// runs on the job token, so a refused one doesn't lapse the account.
		var refused *qoder.DeviceTokenRefreshHTTPError
		if errors.As(err, &refused) && (refused.StatusCode == http.StatusUnauthorized || refused.StatusCode == http.StatusForbidden) {
			return "", fmt.Errorf("%[1]s usage is unavailable: %[1]s refused the account-page sign-in (chat still works) — sign in again to see usage (%[2]w)", site.Name, err)
		}
		return "", err
	}
	c.DeviceToken, c.DeviceRefresh = dt.Token, dt.RefreshToken
	if c.DeviceChat { // the pair it spent was the chat pair too
		c.Token, c.RefreshToken, c.ExpiresAt = dt.Token, dt.RefreshToken, qoder.DeviceExpiry(*dt).UnixMilli()
	}
	if err := qoderPersist(l, c, c.DeviceChat); err != nil {
		return "", err
	}
	return c.DeviceToken, nil
}

func parseQoderQuota(raw []byte, q SubscriptionQuota) (SubscriptionQuota, error) {
	var env struct {
		DisplayMode string                     `json:"displayMode"`
		Usage       map[string]json.RawMessage `json:"qoderUsage"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return q, err
	}
	if env.DisplayMode == "enterprise" {
		q.Plan = "Enterprise"
		return q, nil
	}
	if env.DisplayMode != "qoder" || env.Usage == nil {
		return q, fmt.Errorf("Qoder: missing quota data")
	}
	field := func(a, b string) json.RawMessage {
		if v := env.Usage[a]; len(v) > 0 {
			return v
		}
		return env.Usage[b]
	}
	_ = json.Unmarshal(field("userType", "user_type"), &q.Plan)
	var expiry any
	if json.Unmarshal(field("expiresAt", "expires_at"), &expiry) == nil {
		q.Until = cmdTime(expiry)
	}
	add := func(raw json.RawMessage, name string) {
		var b struct {
			Total     *float64 `json:"total"`
			Cap       *float64 `json:"cap"`
			Used      *float64 `json:"used"`
			Remaining *float64 `json:"remaining"`
			Name      string   `json:"name"`
			Unit      string   `json:"unit"`
		}
		if json.Unmarshal(raw, &b) != nil {
			return
		}
		if b.Total == nil {
			b.Total = b.Cap
		}
		if b.Total == nil || *b.Total <= 0 || b.Used == nil && b.Remaining == nil {
			return
		}
		used := *b.Total
		if b.Used != nil {
			used = *b.Used
		} else {
			used -= *b.Remaining
		}
		if used < 0 {
			return
		}
		if b.Name != "" {
			name = b.Name
		}
		if b.Unit == "" {
			b.Unit = "credits"
		}
		q.Windows = append(q.Windows, QuotaWindow{Name: name, Used: min(100, 100*used / *b.Total), Display: fmt.Sprintf("%g / %g %s", used, *b.Total, b.Unit)})
	}
	add(field("userQuota", "user_quota"), "Credits")
	add(field("addOnQuota", "add_on_quota"), "Add-on credits")
	add(field("orgResourcePackage", "org_resource_package"), "Shared credits")
	var dedicated []json.RawMessage
	_ = json.Unmarshal(field("dedicatedResourcePackages", "dedicated_resource_packages"), &dedicated)
	for _, b := range dedicated {
		add(b, "Dedicated credits")
	}
	return q, nil
}
