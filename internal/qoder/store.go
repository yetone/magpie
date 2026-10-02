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
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Credential is kept in the account's auth blob in magpie's logins.json.
type Credential struct {
	UID           string          `json:"uid"`
	Email         string          `json:"email,omitempty"`
	Name          string          `json:"name,omitempty"`
	Token         string          `json:"token"`
	RefreshToken  string          `json:"refresh_token"`
	DeviceToken   string          `json:"device_token"`
	DeviceRefresh string          `json:"device_refresh,omitempty"`
	ExpiresAt     int64           `json:"expires_at"`
	MachineID     string          `json:"machine_id"`
	Models        json.RawMessage `json:"models,omitempty"`
	// Site is the Qoder site the account is on: CNProviderKey for Qoder CN,
	// empty for the global site (every credential saved before there were two).
	Site string `json:"site,omitempty"`
	// DeviceChat says Token is the device token itself, as Qoder CN's CLI
	// uses it, because the site refused to trade it for a job token; it is
	// then refreshed as a device token.
	DeviceChat bool `json:"device_chat,omitempty"`
}

// OnSite is the Qoder site the credential belongs to.
func (c *Credential) OnSite() *Site { return SiteOf(c.Site) }

const refreshLead = 5 * time.Minute

func (c *Credential) Valid() bool {
	return c != nil && c.Token != "" && c.ExpiresAt-time.Now().UnixMilli() > int64(refreshLead/time.Millisecond)
}

// Refresh returns a new credential value. The caller serializes the entire
// check, refresh and save operation, since refresh tokens rotate on use.
func (c Credential) Refresh(ctx context.Context, client *http.Client) (Credential, error) {
	if c.RefreshToken == "" {
		return Credential{}, fmt.Errorf("qoder: the sign-in lapsed; sign in again")
	}
	if c.DeviceChat {
		dt, err := RefreshDeviceToken(ctx, client, c.OnSite().OpenAPI+DeviceTokenRefreshPath, c.RefreshToken)
		if err != nil {
			// a refused refresh is as final as a refused job refresh
			var refused *DeviceTokenRefreshHTTPError
			if errors.As(err, &refused) {
				return Credential{}, &JobTokenRefreshHTTPError{StatusCode: refused.StatusCode}
			}
			return Credential{}, err
		}
		c.Token, c.RefreshToken = dt.Token, dt.RefreshToken
		c.DeviceToken, c.DeviceRefresh = dt.Token, dt.RefreshToken
		c.ExpiresAt = DeviceExpiry(*dt).UnixMilli()
		return c, nil
	}
	jt, err := RefreshJobToken(ctx, client, c.OnSite(), c.RefreshToken)
	if err != nil {
		return Credential{}, err
	}
	c.Token, c.RefreshToken = jt.Token, jt.RefreshToken
	life := jt.Expiry()
	if life <= 0 {
		life = 24 * time.Hour
	}
	c.ExpiresAt = time.Now().Add(life).UnixMilli()
	return c, nil
}

// DeviceExpiry is when a device token used for chat is taken to lapse: what
// Qoder said, else a day, as a job token without a lifetime is.
func DeviceExpiry(t DeviceToken) time.Time {
	if at := t.Expiry(); !at.IsZero() {
		return at
	}
	return time.Now().Add(24 * time.Hour)
}
