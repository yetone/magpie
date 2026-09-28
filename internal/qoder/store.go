package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Credential is a signed-in Qoder account kept by magpie, with its job token
// (which serves the models), the refresh token that rotates it, and who it is.
type Credential struct {
	UID           string `json:"uid"`
	Email         string `json:"email,omitempty"`
	Name          string `json:"name,omitempty"`
	Token         string `json:"token"`         // the jt- job token
	RefreshToken  string `json:"refresh_token"` // rotates the job token
	DeviceToken   string `json:"device_token"`  // the dt- device token, for account endpoints
	DeviceRefresh string `json:"device_refresh,omitempty"`
	ExpiresAt     int64  `json:"expires_at"` // unix ms, when the job token lapses
}

// Store keeps the signed-in account. magpie runs its own device flow, so the
// account is magpie's, not the CLI's, and lives under magpie's config dir.
type Store struct {
	dir  string
	mu   sync.Mutex
	c    *Credential
	path string
}

// NewStore points a store at magpie's config directory.
func NewStore(dir string) *Store {
	return &Store{dir: dir, path: filepath.Join(dir, "qoder.json")}
}

// Load reads the saved account, or nil when there is none.
func (s *Store) Load() (*Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil {
		return s.c, nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("qoder: read %s: %w", s.path, err)
	}
	if c.Token == "" || c.UID == "" {
		return nil, nil
	}
	s.c = &c
	return &c, nil
}

// Save writes an account, readable only by its owner, atomically.
func (s *Store) Save(c Credential) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.mu.Lock()
	cp := c
	s.c = &cp
	s.mu.Unlock()
	return nil
}

// Forget drops the saved account: magpie leaves it alone, though the sign-in
// at Qoder still stands.
func (s *Store) Forget() error {
	s.mu.Lock()
	s.c = nil
	s.mu.Unlock()
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// refreshLead is how soon before expiry a job token is rotated: Qoder's
// authenticator refreshes five minutes early.
const refreshLead = 5 * time.Minute

// Valid reports whether the account can serve now: it has a token that is not
// about to lapse.
func (c *Credential) Valid() bool {
	return c != nil && c.Token != "" && c.ExpiresAt-time.Now().UnixMilli() > int64(refreshLead/time.Millisecond)
}

// EnsureFresh returns a usable account, rotating the job token when it is near
// expiry. The refreshed pair replaces the saved one, since the old refresh
// token is spent on the round.
func (s *Store) EnsureFresh(ctx context.Context, client *http.Client) (*Credential, error) {
	c, err := s.Load()
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("qoder: not signed in")
	}
	if c.Valid() {
		return c, nil
	}
	if c.RefreshToken == "" {
		return nil, fmt.Errorf("qoder: the sign-in lapsed; sign in again")
	}
	jt, err := RefreshJobToken(ctx, client, c.RefreshToken)
	if err != nil {
		return nil, err
	}
	c.Token = jt.Token
	c.RefreshToken = jt.RefreshToken
	life := jt.Expiry()
	if life <= 0 {
		life = 24 * time.Hour // Qoder advertises a day when it names no expiry
	}
	c.ExpiresAt = time.Now().Add(life).UnixMilli()
	if err := s.Save(*c); err != nil {
		return nil, err
	}
	return c, nil
}
