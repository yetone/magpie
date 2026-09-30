// Package access keeps the named API keys issued by the gateway.
package access

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
const legacyPrefix = "sk-magpie-user-"
const legacyLANPrefix = "sk-magpie-"

func Managed(secret string) bool {
	return strings.HasPrefix(secret, legacyLANPrefix)
}

type Key struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Off    bool   `json:"off,omitempty"`
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

// load reads legacy user/key data only until the first write of the flat store.
func load() ([]Key, error) {
	b, err := os.ReadFile(Path())
	if err == nil {
		var keys []Key
		err = json.Unmarshal(b, &keys)
		return keys, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b, err = os.ReadFile(filepath.Join(settings.Dir(), "users.json"))
	if errors.Is(err, os.ErrNotExist) {
		return []Key{}, nil
	}
	if err != nil {
		return nil, err
	}
	var users []struct {
		Off  bool  `json:"off"`
		Keys []Key `json:"keys"`
	}
	if err := json.Unmarshal(b, &users); err != nil {
		return nil, err
	}
	keys := []Key{}
	for _, u := range users {
		for _, k := range u.Keys {
			k.Off = k.Off || u.Off
			keys = append(keys, k)
		}
	}
	return keys, nil
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
			} else if strings.HasPrefix(k.Secret, legacyPrefix) {
				prefix = legacyPrefix
			}
			k.Masked = prefix + "…" + k.Secret[len(k.Secret)-6:]
		}
		k.Secret = ""
	}
	return keys, nil
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

// Update writes the flat store atomically. The old user store is left intact
// as a backup; all subsequent reads use the flat store.
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
		switch action {
		case "rename-key":
			keys[i].Name = name
		case "on-key", "off-key":
			keys[i].Off = action == "off-key"
		case "remove-key":
			keys = slices.Delete(keys, i, i+1)
		case "copy-key":
			return keys[i].Secret, nil
		default:
			return "", fmt.Errorf("unknown key action %q", action)
		}
	}
	return secret, save(keys)
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
		if subtle.ConstantTimeCompare([]byte(secret), []byte(k.Secret)) == 1 && !k.Off {
			return Identity{k.ID, k.Name}, true
		}
	}
	return Identity{}, false
}

// MigrateLegacyLANKey makes the old Settings key a normal, revocable caller
// key. Persist the key before clearing Settings so a failed migration never
// silently revokes a working client. Repeating it after a partial write is safe.
func MigrateLegacyLANKey() error {
	mu.Lock()
	defer mu.Unlock()
	return migrateLegacyLANKey()
}

func migrateLegacyLANKey() error {
	s := settings.Load()
	if s.LANKey == "" {
		return nil
	}
	keys, err := load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k Key) bool {
		return subtle.ConstantTimeCompare([]byte(k.Secret), []byte(s.LANKey)) == 1
	})
	if i < 0 {
		id, err := random(12)
		if err != nil {
			return err
		}
		keys = append(keys, Key{ID: id, Name: "Local network (legacy)", Secret: s.LANKey})
		if err := save(keys); err != nil {
			return err
		}
		i = len(keys) - 1
	}
	s.LANKeyID = keys[i].ID
	s.LANKey = ""
	return settings.Save(s)
}

// ConfigureLAN keeps Settings' copy/rotate shortcut on an ordinary named key.
// Rotation preserves its ID, name and enabled state, and leaves other keys alone.
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
		i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == s.LANKeyID })
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
				keys = append(keys, Key{ID: id, Name: "Local network"})
				i = len(keys) - 1
			}
			keys[i].Secret = Prefix + token
			if err := save(keys); err != nil {
				return err
			}
			s.LANKeyID = keys[i].ID
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
