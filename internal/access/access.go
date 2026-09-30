// Package access keeps the users and API keys issued by the gateway.
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

const Prefix = "sk-magpie-user-"

type Key struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Off    bool   `json:"off,omitempty"`
	Secret string `json:"secret,omitempty"`
	Masked string `json:"masked,omitempty"`
}

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Off  bool   `json:"off,omitempty"`
	Keys []Key  `json:"keys"`
}

type Identity struct {
	UserID, UserName, KeyID, KeyName string
}

type contextKey struct{}

func WithIdentity(ctx context.Context, who Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, who)
}

func Caller(ctx context.Context) Identity {
	who, _ := ctx.Value(contextKey{}).(Identity)
	return who
}

var mu sync.Mutex

func Path() string { return filepath.Join(settings.Dir(), "users.json") }

func load() ([]User, error) {
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return []User{}, nil
	}
	if err != nil {
		return nil, err
	}
	var users []User
	err = json.Unmarshal(b, &users)
	return users, err
}

// List never returns credentials; only the administrator's copy action does.
func List() ([]User, error) {
	mu.Lock()
	defer mu.Unlock()
	users, err := load()
	for i := range users {
		for j := range users[i].Keys {
			k := &users[i].Keys[j]
			if len(k.Secret) > 8 {
				k.Masked = Prefix + "…" + k.Secret[len(k.Secret)-6:]
			}
			k.Secret = ""
		}
	}
	return users, err
}

func random(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type Change struct {
	User string `json:"user"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Update modifies one user or key atomically. A new key's secret is returned
// to the administrator, never to the usage ledger.
func Update(action string, in Change) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	users, err := load()
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if action == "add-user" || action == "rename-user" || action == "add-key" || action == "rename-key" {
		if name == "" || utf8.RuneCountInString(name) > 120 {
			return "", errors.New("Use a name between 1 and 120 characters")
		}
	}
	var secret string
	if action == "add-user" {
		id, err := random(12)
		if err != nil {
			return "", err
		}
		users = append(users, User{ID: id, Name: name, Keys: []Key{}})
	} else {
		i := slices.IndexFunc(users, func(u User) bool { return u.ID == in.User })
		if i < 0 {
			return "", errors.New("User not found")
		}
		u := &users[i]
		switch action {
		case "rename-user":
			u.Name = name
		case "on-user", "off-user":
			u.Off = action == "off-user"
		case "remove-user":
			users = slices.Delete(users, i, i+1)
		case "add-key":
			id, err := random(12)
			if err != nil {
				return "", err
			}
			token, err := random(24)
			if err != nil {
				return "", err
			}
			secret = Prefix + token
			u.Keys = append(u.Keys, Key{ID: id, Name: name, Secret: secret})
		case "rename-key", "on-key", "off-key", "remove-key", "copy-key":
			j := slices.IndexFunc(u.Keys, func(k Key) bool { return k.ID == in.Key })
			if j < 0 {
				return "", errors.New("Key not found")
			}
			switch action {
			case "rename-key":
				u.Keys[j].Name = name
			case "on-key", "off-key":
				u.Keys[j].Off = action == "off-key"
			case "remove-key":
				u.Keys = slices.Delete(u.Keys, j, j+1)
			case "copy-key":
				return u.Keys[j].Secret, nil
			}
		default:
			return "", fmt.Errorf("unknown user action %q", action)
		}
	}
	b, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return "", err
	}
	// WriteAtomic preserves the existing mode; pre-create with 0600 so a
	// fresh credential file is private from the first write.
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	f.Close()
	if err := os.Chmod(Path(), 0o600); err != nil {
		return "", err
	}
	return secret, edit.WriteAtomic(Path(), append(b, '\n'))
}

// Authenticate reloads the file so disabling or deleting a key takes effect
// in a gateway already running, including one in a separate process.
func Authenticate(secret string) (Identity, bool) {
	mu.Lock()
	defer mu.Unlock()
	users, err := load()
	if err != nil || secret == "" {
		return Identity{}, false
	}
	for _, u := range users {
		for _, k := range u.Keys {
			if subtle.ConstantTimeCompare([]byte(secret), []byte(k.Secret)) == 1 && !u.Off && !k.Off {
				return Identity{u.ID, u.Name, k.ID, k.Name}, true
			}
		}
	}
	return Identity{}, false
}
