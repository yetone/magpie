package qoder

import (
	"context"
	"encoding/json"
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
}

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
	jt, err := RefreshJobToken(ctx, client, c.RefreshToken)
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
