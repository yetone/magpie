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

// Named: secret has a named gateway key's form (sk-magpie-…), or is the
// key's own value a user gave it (Change.Secret), so a request from this
// computer with one is counted and limited as that key, as a sk-magpie-
// key is.
func Named(secret string) bool {
	if Managed(secret) {
		return true
	}
	if secret == "" {
		return false
	}
	own := ownSecrets()
	_, ok := own[secret]
	return ok
}

// own caches the key store's own-value secrets by the file's size and
// time, so a request from this computer doesn't read it each time.
var own struct {
	sync.Mutex
	size    int64
	mod     int64
	secrets map[string]struct{}
}

func ownSecrets() map[string]struct{} {
	fi, err := os.Stat(Path())
	if err != nil {
		return nil
	}
	own.Lock()
	defer own.Unlock()
	if own.secrets != nil && own.size == fi.Size() && own.mod == fi.ModTime().UnixNano() {
		return own.secrets
	}
	mu.Lock()
	keys, err := load()
	mu.Unlock()
	if err != nil {
		return nil // unknown: tried again on the next request
	}
	m := map[string]struct{}{}
	for _, k := range keys {
		if k.Secret != "" && !Managed(k.Secret) {
			m[k.Secret] = struct{}{}
		}
	}
	own.size, own.mod, own.secrets = fi.Size(), fi.ModTime().UnixNano(), m
	return m
}

// ownSecret checks a key value a user brings (one their clients already
// send, from another gateway they move from, such as CLIProxyAPI's
// api-keys, which are any string): what an HTTP header carries, and no
// other key's.
func ownSecret(secret string, keys []Key) (string, error) {
	secret = strings.TrimSpace(secret)
	if n := len(secret); n < 8 || n > 256 {
		return "", errors.New("Use a key between 8 and 256 characters")
	}
	for i := 0; i < len(secret); i++ {
		if c := secret[i]; c <= ' ' || c > '~' {
			return "", errors.New("A key can have only letters, digits and ASCII symbols, no spaces")
		}
	}
	if strings.HasPrefix(secret, legacyLANPrefix) {
		return "", errors.New("A key of your own can't start with sk-magpie-: leave the key empty and magpie makes one")
	}
	for _, k := range keys {
		if subtle.ConstantTimeCompare([]byte(secret), []byte(k.Secret)) == 1 {
			return "", errors.New("Another gateway key already has this key")
		}
	}
	return secret, nil
}

type Key struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Off    bool   `json:"off,omitempty"`
	LAN    bool   `json:"lan,omitempty"` // default key and durable migration marker
	Secret string `json:"secret,omitempty"`
	Masked string `json:"masked,omitempty"`
	// Limit is the key's own budget (#585); nil for none.
	Limit *Limit `json:"limit,omitempty"`
	// Models are the models the key may use (#882): "<provider>/<model>",
	// "<provider>/*" or a group's "group/<name>"; none for every model.
	Models []string `json:"models,omitempty"`
	// Accounts are the accounts and keys the key may use (#905), as
	// "<provider>/<account>": a signed-in account by its stable id, a
	// key by its fingerprint; none for every account, as keys always did.
	Accounts []string `json:"accounts,omitempty"`
}

// Identity is the gateway key a request came with, its budget, the
// models and the accounts it may use.
type Identity struct {
	KeyID, KeyName string
	Limit          *Limit
	Models         []string
	Accounts       []string
}
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
		if k.Secret != "" && !Managed(k.Secret) { // a key of the user's own: only its end
			k.Masked = "…"
			if len(k.Secret) >= 12 {
				k.Masked += k.Secret[len(k.Secret)-4:]
			}
		}
		k.Secret = ""
	}
	return keys, nil
}

// LANSecret is the key magpie shares the gateway on the local network with
// (the default key ConfigureLAN made), for an agent of this computer's that
// reaches it from beyond loopback — one in a WSL distro under NAT, whose
// requests the gateway takes only with a named key; "" while the gateway
// isn't shared, or that key is off.
func LANSecret() string {
	mu.Lock()
	defer mu.Unlock()
	s := settings.Load()
	if !s.LAN {
		return ""
	}
	keys, err := load()
	if err != nil {
		return ""
	}
	if i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == s.LANKeyID || k.LAN }); i >= 0 {
		if keys[i].Off {
			return ""
		}
		return keys[i].Secret
	}
	if s.LANKeyID == "" && s.LANKey != "" && !strings.HasPrefix(s.LANKey, revokedLANPrefix) {
		return s.LANKey
	}
	return ""
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
	// Limit is what "limit-key" sets; nil or Unlimited takes the limit off.
	Limit *Limit `json:"limit,omitempty"`
	// Models is what "models-key" sets; none lets the key use every model.
	Models []string `json:"models,omitempty"`
	// Accounts is what "accounts-key" sets (#905); none lets the key use
	// every account and key.
	Accounts []string `json:"accounts,omitempty"`
	// Secret is the value "add-key" gives the new key, one its clients
	// already send (love1sbug on X: keys handed out from another gateway);
	// "" for one magpie makes.
	Secret string `json:"secret,omitempty"`
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
		if in.Secret != "" {
			if secret, err = ownSecret(in.Secret, keys); err != nil {
				return "", err
			}
		} else {
			token, err := random(24)
			if err != nil {
				return "", err
			}
			secret = Prefix + token
		}
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
		case "limit-key":
			lim, err := in.Limit.Valid()
			if err != nil {
				return "", err
			}
			keys[i].Limit = lim
		case "models-key":
			ms, err := CleanModels(in.Models)
			if err != nil {
				return "", err
			}
			keys[i].Models = ms
		case "accounts-key":
			as, err := CleanAccounts(in.Accounts)
			if err != nil {
				return "", err
			}
			keys[i].Accounts = as
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
		if defaultKey && action != "remove-key" && action != "rename-key" && action != "limit-key" && action != "models-key" && action != "accounts-key" {
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
			return Identity{KeyID: k.ID, KeyName: k.Name, Limit: k.Limit, Models: k.Models, Accounts: k.Accounts}, true
		}
	}
	s := settings.Load()
	// A read-only config directory may prevent the first store write. Never
	// fall back after either durable marker exists, or to a revoked mirror.
	if s.LAN && s.LANKeyID == "" && s.LANKey != "" && !strings.HasPrefix(s.LANKey, revokedLANPrefix) && !slices.ContainsFunc(keys, func(k Key) bool { return k.LAN }) && subtle.ConstantTimeCompare([]byte(secret), []byte(s.LANKey)) == 1 {
		k := legacyLANKey(s.LANKey)
		return Identity{KeyID: k.ID, KeyName: k.Name}, true
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
