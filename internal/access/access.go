// Package access keeps the named API keys issued by the gateway.
package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

const Prefix = "sk-magpie-key-"
const legacyLANPrefix = "sk-magpie-"
const revokedLANPrefix = "sk-magpie-revoked-"

func Managed(secret string) bool {
	return strings.HasPrefix(secret, legacyLANPrefix)
}

type Key struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Off    bool   `json:"off,omitempty"`
	LAN    bool   `json:"lan,omitempty"` // default key and durable migration marker
	Secret string `json:"secret,omitempty"`
	Masked string `json:"masked,omitempty"`
}

type Identity struct{ KeyID, KeyName string }
type contextKey struct{}

func WithIdentity(ctx context.Context, who Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, who)
}
func Caller(ctx context.Context) Identity { who, _ := ctx.Value(contextKey{}).(Identity); return who }

var mu sync.Mutex

func Path() string { return filepath.Join(settings.Dir(), "caller-keys.json") }

func load() ([]Key, error) {
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return []Key{}, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []Key
	err = json.Unmarshal(b, &keys)
	return keys, err
}

// List never returns credentials; only the administrator's copy action does.
func List() ([]Key, error) {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return nil, err
	}
	for i := range keys {
		k := &keys[i]
		if len(k.Secret) > 8 {
			prefix := legacyLANPrefix
			if strings.HasPrefix(k.Secret, Prefix) {
				prefix = Prefix
			}
			k.Masked = prefix + "…" + k.Secret[len(k.Secret)-6:]
		}
		k.Secret = ""
	}
	return keys, nil
}

// Export returns credentials only for the encrypted backup bundle.
func Export() ([]Key, error) {
	mu.Lock()
	defer mu.Unlock()
	return load()
}

// Restore replaces the gateway credentials when restoring their settings.
func Restore(keys []Key) error {
	mu.Lock()
	defer mu.Unlock()
	return save(slices.Clone(keys))
}

func random(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type Change struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Update writes the named key store atomically.
func Update(action string, in Change) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if action == "add-key" || action == "rename-key" {
		if name == "" || utf8.RuneCountInString(name) > 120 {
			return "", errors.New("Use a name between 1 and 120 characters")
		}
	}
	var secret string
	var mirror *Key
	if action == "add-key" {
		id, err := random(12)
		if err != nil {
			return "", err
		}
		token, err := random(24)
		if err != nil {
			return "", err
		}
		secret = Prefix + token
		keys = append(keys, Key{ID: id, Name: name, Secret: secret})
	} else {
		i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == in.Key })
		if i < 0 {
			return "", errors.New("Key not found")
		}
		defaultKey := keys[i].LAN || keys[i].ID == settings.Load().LANKeyID
		if defaultKey {
			keys[i].LAN = true
		}
		switch action {
		case "rotate-key":
			token, err := random(24)
			if err != nil {
				return "", err
			}
			secret = Prefix + token
			keys[i].Secret = secret
		case "rename-key":
			keys[i].Name = name
		case "on-key", "off-key":
			keys[i].Off = action == "off-key"
		case "remove-key":
			if defaultKey {
				k := keys[i]
				k.Off = true
				mirror = &k
			}
			keys = slices.Delete(keys, i, i+1)
		case "copy-key":
			return keys[i].Secret, nil
		default:
			return "", fmt.Errorf("unknown key action %q", action)
		}
		if defaultKey && action != "remove-key" && action != "rename-key" {
			mirror = &keys[i]
		}
	}
	if mirror != nil {
		s := settings.Load()
		if err := setLegacyMirror(&s, *mirror); err != nil {
			return "", err
		}
		// Revoke the old-version mirror before changing the active key store.
		if err := settings.Save(s); err != nil {
			return "", fmt.Errorf("Cannot change the default gateway key without updating settings.json: older Magpie versions may still accept its old credential. Make the settings file writable and retry; the key is unchanged: %w", err)
		}
	}
	if err := save(keys); err != nil {
		return "", err
	}
	return secret, nil
}

func setLegacyMirror(s *settings.Settings, k Key) error {
	s.LANKeyID = k.ID
	if !k.Off {
		s.LANKey = k.Secret
		return nil
	}
	token, err := random(24)
	if err != nil {
		return err
	}
	s.LANKey = revokedLANPrefix + token
	return nil
}

// Authenticate reloads the store so revocation takes effect in running gateways.
func Authenticate(secret string) (Identity, bool) {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil || secret == "" {
		return Identity{}, false
	}
	for _, k := range keys {
		if subtle.ConstantTimeCompare([]byte(secret), []byte(k.Secret)) == 1 {
			if k.Off {
				return Identity{}, false
			}
			return Identity{k.ID, k.Name}, true
		}
	}
	s := settings.Load()
	// A read-only config directory may prevent the first store write. Never
	// fall back after either durable marker exists, or to a revoked mirror.
	if s.LAN && s.LANKeyID == "" && s.LANKey != "" && !strings.HasPrefix(s.LANKey, revokedLANPrefix) && !slices.ContainsFunc(keys, func(k Key) bool { return k.LAN }) && subtle.ConstantTimeCompare([]byte(secret), []byte(s.LANKey)) == 1 {
		k := legacyLANKey(s.LANKey)
		return Identity{k.ID, k.Name}, true
	}
	return Identity{}, false
}

func legacyLANKey(secret string) Key {
	// The fallback and eventual persisted key must share their usage identity.
	sum := sha256.Sum256([]byte(secret))
	return Key{ID: "lan-" + hex.EncodeToString(sum[:12]), Name: "Magpie", LAN: true, Secret: secret}
}

// MigrateLegacyLANKey makes the old Settings key a normal, revocable caller
// key. Keep LANKey for older Magpie versions; LANKeyID marks a completed
// migration so the retained credential cannot resurrect a removed key.
func MigrateLegacyLANKey() error {
	mu.Lock()
	defer mu.Unlock()
	return migrateLegacyLANKey()
}

func MigrateLegacyLANKeyBestEffort() {
	if err := MigrateLegacyLANKey(); err != nil {
		log.Printf("magpie: could not migrate LAN key: %v", err)
	}
}

func migrateLegacyLANKey() error {
	s := settings.Load()
	if s.LANKey == "" || s.LANKeyID != "" || strings.HasPrefix(s.LANKey, revokedLANPrefix) {
		return nil
	}
	keys, err := load()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(keys, func(k Key) bool { return k.LAN }) {
		return nil // the key-store write succeeded, even if settings were read-only
	}
	i := slices.IndexFunc(keys, func(k Key) bool {
		return subtle.ConstantTimeCompare([]byte(k.Secret), []byte(s.LANKey)) == 1
	})
	if i < 0 {
		keys = append(keys, legacyLANKey(s.LANKey))
		i = len(keys) - 1
	}
	keys[i].LAN = true
	if err := save(keys); err != nil {
		return err
	}
	s.LANKeyID = keys[i].ID
	return settings.Save(s)
}

// ConfigureLAN starts sharing with a named key. Rotation preserves the key's
// identity, name and enabled state.
func ConfigureLAN(on, rotate bool) error {
	mu.Lock()
	defer mu.Unlock()
	if err := migrateLegacyLANKey(); err != nil {
		return err
	}
	s := settings.Load()
	if on {
		keys, err := load()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == s.LANKeyID || k.LAN })
		if i < 0 || rotate {
			token, err := random(24)
			if err != nil {
				return err
			}
			if i < 0 {
				id, err := random(12)
				if err != nil {
					return err
				}
				keys = append(keys, Key{ID: id, Name: "Magpie", LAN: true})
				i = len(keys) - 1
			}
			keys[i].Secret = Prefix + token
			if err := save(keys); err != nil {
				return err
			}
			s.LANKeyID = keys[i].ID
		}
		if err := setLegacyMirror(&s, keys[i]); err != nil {
			return err
		}
	}
	s.LAN = on
	return settings.Save(s)
}

func save(keys []Key) error {
	b, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(Path(), 0o600); err != nil {
		return err
	}
	return edit.WriteAtomic(Path(), append(b, '\n'))
}
