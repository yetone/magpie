// Package backup packs what the user set up in magpie into one file, sealed
// with a passphrase, to carry to another machine: the providers (their keys
// too, unless left out), the pictures picked for them, the settings, the
// profiles, every agent's model and the library (the instructions, MCP
// servers and skills magpie gives the agents). Subscriptions are not in it: each is
// the sign-in of an agent on this machine, so each machine signs in on its
// own.
//
// The file is JSON: a header naming how it is sealed, and the bundle
// encrypted with AES-256-GCM under a key derived from the passphrase with
// PBKDF2-SHA256. Nothing in it can be read without the passphrase.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Ext is the extension backups are saved with.
const Ext = ".magpie-backup"

const (
	format     = "magpie-backup"
	iterations = 600_000
)

// Bundle is what a backup holds.
type Bundle struct {
	Version     int                        `json:"version"`
	Created     time.Time                  `json:"created"`
	App         string                     `json:"app,omitempty"` // the magpie that made it
	Keys        bool                       `json:"keys"`          // whether credentials are included
	Providers   []provider.Provider        `json:"providers"`
	Icons       map[string][]byte          `json:"icons,omitempty"`  // pictures picked for providers, by file name
	Groups      []provider.Group           `json:"groups,omitempty"` // the user's model groups
	Settings    *settings.Settings         `json:"settings,omitempty"`
	GatewayKeys *[]access.Key              `json:"gatewayKeys,omitempty"`
	Profiles    map[string]profile.Profile `json:"profiles,omitempty"`
	Agents      map[string]string          `json:"agents,omitempty"`  // every agent's fields as they are now
	Library     *library.Bundle            `json:"library,omitempty"` // nil from a magpie before it, or with none
	// Searches are the web search APIs (#419), their keys with the
	// providers'; nil from a magpie before them.
	Searches *[]provider.SearchAPI `json:"searches,omitempty"`
	// Order is the order the user put the providers in (#499), by id;
	// none from a magpie before it went, or when they were never arranged.
	Order []string `json:"order,omitempty"`
}

type envelope struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Data       []byte `json:"data"`
}

// ErrPassphrase is a passphrase that does not open the backup.
var ErrPassphrase = errors.New("wrong passphrase, or the file was changed")

// Collect gathers the bundle; without keys, providers carry none, nor any
// header that looks like one, and the library's servers no environment
// variable or header that looks like one (their names stay).
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: 1, Created: time.Now().UTC(), App: app, Keys: keys}
	// a providers.json that can't be read stops the backup: carried as no
	// providers, it would take them all away where it is put back, or where
	// sync mirrors it
	stored, err := provider.Stored()
	if err != nil {
		return b, err
	}
	if b.Groups, err = provider.StoredGroups(); err != nil {
		return b, err
	}
	if b.Order, err = provider.StoredOrder(); err != nil {
		return b, err
	}
	if keys {
		access.MigrateLegacyLANKeyBestEffort()
		if _, err := os.Stat(access.Path()); err == nil {
			gatewayKeys, err := access.Export()
			if err != nil {
				return b, err
			}
			b.GatewayKeys = &gatewayKeys
		} else if !errors.Is(err, os.ErrNotExist) {
			return b, err
		}
	}
	for _, p := range stored {
		if !keys {
			p = withoutKeys(p)
		}
		b.Providers = append(b.Providers, p)
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			if f := provider.IconFile(name); f != "" {
				if data, err := os.ReadFile(f); err == nil {
					if b.Icons == nil {
						b.Icons = map[string][]byte{}
					}
					b.Icons[name] = data
				}
			}
		}
	}
	searches := []provider.SearchAPI{}
	for _, a := range provider.StoredSearchAPIs() {
		if !keys {
			a.Key = ""
		}
		searches = append(searches, a)
	}
	b.Searches = &searches
	if _, err := os.Stat(settings.Path()); err == nil {
		s := settings.Load()
		if !keys {
			s.LANKey, s.LANKeyID = "", ""
			s.OTel.Headers = nil
			s.GitHubToken = ""
		}
		b.Settings = &s
	}
	ps, err := profile.Load()
	if err != nil {
		return b, err
	}
	if len(ps) > 0 {
		b.Profiles = ps
	}
	if snap := profile.Fields(); len(snap) > 0 {
		b.Agents = snap
	}
	if b.Library, err = library.Collect(); err != nil {
		return b, err
	}
	if !keys && b.Library != nil {
		b.Library.WithoutSecrets(Secret)
	}
	return b, nil
}

var secretHeader = regexp.MustCompile(`(?i)auth|key|token|secret|cookie|session|password`)

// Secret reports whether a header or an environment variable by that name
// looks like it holds a key: what a backup without keys leaves empty.
func Secret(name string) bool { return secretHeader.MatchString(name) }

func withoutKeys(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.Keys, p.KeyProtocol = "", "", nil, ""
	p.BalanceToken = ""
	if len(p.Headers) > 0 {
		h := map[string]string{}
		for k, v := range p.Headers {
			if !Secret(k) {
				h[k] = v
			}
		}
		p.Headers = h
	}
	return p
}

// Seal encrypts the bundle under the passphrase.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("a backup needs a passphrase")
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	e := envelope{Format: format, Version: 1, KDF: "pbkdf2-sha256", Iterations: iterations,
		Salt: make([]byte, 16), Nonce: make([]byte, 12)}
	rand.Read(e.Salt)
	rand.Read(e.Nonce)
	gcm, err := aead(pass, e)
	if err != nil {
		return nil, err
	}
	e.Data = gcm.Seal(nil, e.Nonce, plain, header(e))
	return json.MarshalIndent(e, "", "  ")
}

// Open decrypts a backup.
func Open(data []byte, pass string) (Bundle, error) {
	var e envelope
	if json.Unmarshal(data, &e) != nil || e.Format != format {
		return Bundle{}, errors.New("not a magpie backup")
	}
	if e.Version != 1 || e.KDF != "pbkdf2-sha256" {
		return Bundle{}, errors.New("this backup was made by a newer magpie; update magpie to open it")
	}
	if e.Iterations < 100_000 || e.Iterations > 10_000_000 || len(e.Nonce) != 12 || len(e.Salt) < 16 {
		return Bundle{}, errors.New("not a magpie backup")
	}
	gcm, err := aead(pass, e)
	if err != nil {
		return Bundle{}, err
	}
	plain, err := gcm.Open(nil, e.Nonce, e.Data, header(e))
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// header is what the sealing covers besides the bundle: the envelope's
// own fields, so none of them can be changed unnoticed.
func header(e envelope) []byte {
	return fmt.Appendf(nil, "%s/%d/%s/%d/%x", e.Format, e.Version, e.KDF, e.Iterations, e.Salt)
}

func aead(pass string, e envelope) (cipher.AEAD, error) {
	key, err := deriveKey(pass, e)
	if err != nil {
		return nil, err
	}
	return gcmOf(key)
}

func deriveKey(pass string, e envelope) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, e.Salt, e.Iterations, 32)
}

func gcmOf(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Parts picks what a restore puts back.
type Parts struct {
	Providers, Settings, Profiles, Agents, Library bool
}

// All is every part.
var All = Parts{true, true, true, true, true}

// Result says what a restore did.
type Result struct {
	Added, Replaced int      // providers
	NeedKey         []string // providers that came without a key and have none here
	Settings        bool
	Profiles        int
	Agents          int               // agent fields changed
	Skipped         []string          // agent fields left alone: the agent is not on this machine, or the value failed
	Library         bool              // the library was put in
	LibraryProblems []library.Problem `json:",omitempty"` // what of it couldn't be given to an agent
}

// Restore puts the chosen parts of the bundle in. Providers come first, so
// the agents' models that go through them resolve. Only agents on this
// machine are set.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	if parts.Providers {
		for name, data := range b.Icons {
			if f := provider.IconFile(name); f != "" && len(data) <= provider.MaxIcon {
				if _, err := os.Stat(f); err != nil {
					os.MkdirAll(filepath.Dir(f), 0o755)
					edit.WriteAtomic(f, data)
				}
			}
		}
		var err error
		if r.Added, r.Replaced, err = provider.Restore(b.Providers); err != nil {
			return r, err
		}
		if err := provider.RestoreGroups(b.Groups); err != nil {
			return r, err
		}
		if b.Searches != nil {
			if err := provider.RestoreSearchAPIs(*b.Searches); err != nil {
				return r, err
			}
			for _, a := range provider.StoredSearchAPIs() {
				if !a.Ready() && slices.ContainsFunc(*b.Searches, func(x provider.SearchAPI) bool { return x.Vendor == a.Vendor }) {
					r.NeedKey = append(r.NeedKey, a.Name())
				}
			}
		}
		stored, err := provider.Stored()
		if err != nil {
			return r, err
		}
		for _, p := range stored {
			if !p.Ready() && slices.ContainsFunc(b.Providers, func(q provider.Provider) bool { return q.ID == p.ID }) {
				r.NeedKey = append(r.NeedKey, p.Name)
			}
		}
	}
	if parts.Settings && (b.Settings != nil || b.GatewayKeys != nil) {
		// the window's size, the proxy, the menu bar's usage are this machine's own
		cur := settings.Load()
		s := cur
		if b.Settings != nil {
			s = *b.Settings
		}
		s.KeepOwn(cur)
		if !b.Keys {
			s.LANKey, s.LANKeyID = cur.LANKey, cur.LANKeyID
			s.GitHubToken = cur.GitHubToken
			s.OTel.Headers = nil
			if strings.TrimRight(strings.TrimSpace(s.OTel.Endpoint), "/") == cur.OTel.Endpoint {
				s.OTel.Headers = cur.OTel.Headers
			}
		} else if b.GatewayKeys == nil {
			// An older backup may carry a marker without its named-key store.
			s.LANKeyID = ""
			keys, err := access.List()
			if err != nil {
				return r, err
			}
			if slices.ContainsFunc(keys, func(k access.Key) bool { return k.LAN }) {
				// Its missing store must not detach this machine's default key.
				s.LANKey, s.LANKeyID = cur.LANKey, cur.LANKeyID
			}
		}
		if err := settings.Save(s); err != nil {
			return r, err
		}
		if b.Keys && b.GatewayKeys != nil {
			if err := access.Restore(*b.GatewayKeys); err != nil {
				// Settings must be writable before replacing credentials. If the
				// store refuses the write, restore their original association.
				if rollback := settings.Save(cur); rollback != nil {
					return r, errors.Join(err, fmt.Errorf("could not restore original settings after gateway-key restore failed: %w", rollback))
				}
				return r, err
			}
		}
		if b.Keys && b.GatewayKeys == nil {
			access.MigrateLegacyLANKeyBestEffort()
		}
		r.Settings = true
	}
	if parts.Profiles && len(b.Profiles) > 0 {
		for name, p := range b.Profiles {
			if err := profile.Save(name, p); err != nil {
				return r, err
			}
			r.Profiles++
		}
	}
	if parts.Agents && len(b.Agents) > 0 {
		here := map[string]bool{}
		for _, a := range agent.Detected() {
			here[a.ID] = true
		}
		// one value at a time, so one that fails (a model of a subscription
		// not signed in here) leaves the rest to go in
		for _, k := range profileKeys(b.Agents) {
			id := k[:max(strings.LastIndex(k, "."), 0)] // codex@wsl:Ubuntu-24.04.model
			if !here[id] {
				r.Skipped = append(r.Skipped, k)
				continue
			}
			n, err := profile.ApplyFields(map[string]string{k: b.Agents[k]})
			if err != nil {
				r.Skipped = append(r.Skipped, k)
				continue
			}
			r.Agents += n
		}
	}
	if parts.Library && b.Library != nil {
		lib := b.Library
		if !b.Keys { // the servers' keys kept here stay
			have, err := library.Collect()
			if err != nil {
				return r, err
			}
			lib = lib.WithSecrets(have, Secret)
		}
		res, err := library.Put(lib)
		if err != nil {
			return r, fmt.Errorf("the library: %w", err)
		}
		r.Library, r.LibraryProblems = true, res.Problems
	}
	return r, nil
}

// profileKeys orders a profile's fields the way ApplyFields does: providers, then
// models, then the rest.
func profileKeys(p map[string]string) []string {
	rank := func(k string) int {
		switch {
		case strings.HasSuffix(k, ".provider"):
			return 0
		case strings.HasSuffix(k, ".model"):
			return 1
		}
		return 2
	}
	keys := slices.Collect(maps.Keys(p))
	slices.SortFunc(keys, func(a, b string) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a, b)
	})
	return keys
}
