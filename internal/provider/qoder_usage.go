package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yetone/magpie/internal/qoder"
)

// Account endpoints use the device token, independently of the chat job token.
func qoderLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "qoder", User: l.User, Name: "Qoder", Icon: "qoder", Plan: l.Plan, Windows: []QuotaWindow{}}
	c, err := QoderCredential(ctx, l.User)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	raw, err := qoder.FetchUsage(ctx, qoderClient, "", c.DeviceToken)
	var status *qoder.UsageHTTPError
	if errors.As(err, &status) && (status.StatusCode == 401 || status.StatusCode == 403) {
		var token string
		token, err = qoderRefreshDevice(ctx, l.User, c.DeviceToken)
		if err == nil {
			raw, err = qoder.FetchUsage(ctx, qoderClient, "", token)
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

func qoderRefreshDevice(ctx context.Context, user, attempted string) (string, error) {
	qoderMu.Lock()
	defer qoderMu.Unlock()
	l, found := qoderLookup(user)
	if !found {
		return "", fmt.Errorf("no Qoder account %q", user)
	}
	c, ok, pending := qoderCurrent(l)
	if !ok {
		return "", fmt.Errorf("Qoder: unreadable sign-in")
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
	dt, err := qoder.RefreshDeviceToken(rctx, qoderClient, "", c.DeviceRefresh)
	cancel()
	if err != nil {
		return "", qoderRefreshFailed(l.User, err)
	}
	c.DeviceToken, c.DeviceRefresh = dt.Token, dt.RefreshToken
	if err := qoderPersist(l, c, false); err != nil {
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
