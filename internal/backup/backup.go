// Package backup packs what the user set up in magpie into one file, sealed
// with a passphrase, to carry to another machine: the providers (their keys
// too, unless left out), the pictures picked for them, the settings, the
// profiles, every agent's model and the library (the instructions, MCP
// servers and skills magpie gives the agents). Sign-ins (subscription
// accounts and the plugins') are in it only when the user asks for them
// (WithSignIns): each is the sign-in of an agent on this machine, and
// without them each machine signs in on its own.
//
// The file is JSON: a header naming how it is sealed, and the bundle
// encrypted with AES-256-GCM under a key derived from the passphrase with
// PBKDF2-SHA256. Nothing in it can be read without the passphrase.
package backup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/memory"
	"github.com/yetone/magpie/internal/plugin"
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

// isBackupJSON says whether data begins like a sealed backup: Seal writes
// the envelope with MarshalIndent, so its head is a { then the format
// field. Whitespace is dropped so the check holds whatever the indent. A
// body that starts this way but won't parse whole is a backup cut short,
// not some other answer a server gave.
func isBackupJSON(data []byte) bool {
	head := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, string(data))
	return strings.HasPrefix(head, `{"format":"`+format+`"`)
}

// Bundle is what a backup holds.
type Bundle struct {
	Version     int                 `json:"version"`
	Created     time.Time           `json:"created"`
	App         string              `json:"app,omitempty"` // the magpie that made it
	Keys        bool                `json:"keys"`          // whole-backup credential policy
	Providers   []provider.Provider `json:"providers"`
	Icons       map[string][]byte   `json:"icons,omitempty"`  // pictures picked for providers, by file name
	Groups      []provider.Group    `json:"groups,omitempty"` // the user's model groups
	Settings    *settings.Settings  `json:"settings,omitempty"`
	GatewayKeys *[]access.Key       `json:"gatewayKeys,omitempty"`
	// AccountGroups are the account groups gateway keys name (LeiWang_2000
	// on X), carried with the keys; nil from a magpie before them.
	AccountGroups *[]access.AccountGroup     `json:"accountGroups,omitempty"`
	Profiles      map[string]profile.Profile `json:"profiles,omitempty"`
	Agents        map[string]string          `json:"agents,omitempty"`  // every agent's fields as they are now
	Library       *library.Bundle            `json:"library,omitempty"` // nil from a magpie before it, or with none
	// Searches are the web search APIs (#419), their keys with the
	// providers'; nil from a magpie before them.
	Searches *[]provider.SearchAPI `json:"searches,omitempty"`
	// Order is the order the user put the providers in (#499), by id;
	// none from a magpie before it went, or when they were never arranged.
	Order []string `json:"order,omitempty"`
	// GroupOrder is the order the user put the routing groups in (#779),
	// by id; none from a magpie before it, or when never arranged.
	GroupOrder []string `json:"groupOrder,omitempty"`
	// Plugins are the OpenCode plugins and middlewares added from npm or
	// git (provider.Carried), installed where the backup is put back:
	// the providers they serve come with them. Their sign-ins don't, as a
	// subscription's don't. Nil from a magpie before them.
	Plugins *[]plugin.Entry `json:"plugins,omitempty"`
	// Proxies are the proxies kept by name, which providers, accounts and
	// Settings name as "@id". They travel with the providers, passwords
	// only when the providers' keys do; nil from a magpie before them.
	Proxies *[]settings.NamedProxy `json:"proxies,omitempty"`
	// Sync can carry one part's credentials without changing the others'
	// Keys policy; these are absent from older backups and whole collects.
	ProvidersKeys *bool `json:"providersKeys,omitempty"`
	SettingsKeys  *bool `json:"settingsKeys,omitempty"`
	LibraryKeys   *bool `json:"libraryKeys,omitempty"`
	// SignIns are the subscription accounts and the plugins' sign-ins,
	// with their credentials: only when the user asked for them
	// (WithSignIns). Nil otherwise, and from a magpie before them.
	SignIns *provider.SignIns `json:"signIns,omitempty"`
	// Memory is what the agents know (memory.Collect): merged where it is
	// put back, memory by memory, never put over what is there. Nil from
	// a magpie before it, or with no memory.
	Memory *memory.Bundle `json:"memory,omitempty"`
}

// WithSignIns puts this computer's sign-ins in b. One that can't be read
// is an error, never none.
func WithSignIns(b *Bundle) error {
	s, err := provider.CollectSignIns()
	if err != nil {
		return err
	}
	b.SignIns = s
	return nil
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

// ErrCorrupt is a file that isn't a backup at all, or one cut short: its
// JSON, format, or key-derivation fields don't read. It is told from a
// wrong passphrase and from a newer magpie's file, so a sync can rebuild
// the damaged one from this computer rather than fail on it forever.
var ErrCorrupt = errors.New("not a magpie backup")

// ErrNotBackup is a file that doesn't begin like a backup at all: an empty
// one, a page a server answered with, another app's file. It reads as
// ErrCorrupt does, but is never rebuilt over by a sync on its own.
var ErrNotBackup = errors.New("not a magpie backup")

// BundleVersion is the bundle's structure: 2 carries the per-part
// credential markers (ProvidersKeys, SettingsKeys, LibraryKeys) as their
// own three-state flags; 1 and before had the whole-bundle Keys alone.
const BundleVersion = 2

// Flag is a per-part credential marker as a bundle field takes one: the
// part's own, written when a sync merged it. Absent (nil) is a whole
// collect's or an older magpie's.
func Flag(keys bool) *bool { return &keys }

// scopedKeys is whether a part's credentials are in the bundle: the part's
// own marker when the bundle carries one — a sync writes it for every part
// it merges — and the whole-bundle bit for a whole collect or an older
// file, which have no per-part ones. An older writer can also drop the markers
// from a v2 file, so a version number alone never implies a credential scope.
func (b Bundle) scopedKeys(part string) bool {
	var own *bool
	switch part {
	case "settings":
		own = b.SettingsKeys
	case "library":
		own = b.LibraryKeys
	case "providers":
		own = b.ProvidersKeys
	}
	if own != nil {
		return *own
	}
	return b.Keys
}

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
	if b.GroupOrder, err = provider.StoredGroupOrder(); err != nil {
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
		if _, err := os.Stat(access.GroupsPath()); err == nil {
			groups, err := access.ExportGroups()
			if err != nil {
				return b, err
			}
			b.AccountGroups = &groups
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
	plugins := provider.Carried(keys)
	if plugins == nil {
		plugins = []plugin.Entry{} // none, as against a file that carries no list
	}
	b.Plugins = &plugins
	searches := []provider.SearchAPI{}
	for _, a := range provider.StoredSearchAPIs() {
		if !keys {
			a = a.WithoutKeys() // those switched off too
		}
		searches = append(searches, a)
	}
	b.Searches = &searches
	// the saved proxies ride with the providers that name them
	proxies := settings.Load().Proxies
	if !keys {
		proxies = settings.WithoutProxyPasswords(proxies)
	}
	if len(proxies) > 0 {
		b.Proxies = &proxies
	}
	// a settings.json that can't be read stops it too: carried as the
	// defaults, it would put them over the settings where it is put back,
	// or on every other computer sync mirrors it to (#2269)
	if _, err := os.Stat(settings.Path()); err == nil {
		s, err := settings.LoadChecked()
		if err != nil {
			return b, err
		}
		// the saved proxies hold passwords and are this computer's own (KeepOwn)
		s.Proxies = nil
		if !keys {
			s.LANKey, s.LANKeyID = "", ""
			s.OTel.Headers = nil
			s.GitHubToken = ""
			s.BarkKey = ""
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
	// a memory.json that can't be read stops it too: unknown, not none
	if b.Memory, err = memory.Collect(); err != nil {
		return b, fmt.Errorf("memory: %w", err)
	}
	return b, nil
}

// Secret reports whether a header or an environment variable by that name
// looks like it holds a key: what a backup without keys leaves empty.
func Secret(name string) bool { return provider.SecretHeader(name) }

func withoutKeys(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.Keys, p.KeyProtocol, p.KeyWeight, p.KeyPlan = "", "", nil, "", 0, ""
	p.BalanceToken = ""
	p.AccessKeyID, p.SecretAccessKey = "", ""
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
		Salt: saltFor(pass), Nonce: make([]byte, 12)}
	rand.Read(e.Nonce)
	gcm, err := aead(pass, e)
	if err != nil {
		return nil, err
	}
	e.Data = gcm.Seal(nil, e.Nonce, plain, header(e))
	return json.MarshalIndent(e, "", "  ")
}

// Open decrypts a backup. A byte order mark in front of the JSON is read
// through: the file is written back through edit.WriteAtomic, which keeps
// the mark a Windows tool saved the file with (#1534), and a backup moved
// through an editor that writes one opens as it is.
func Open(data []byte, pass string) (Bundle, error) {
	data = edit.TrimBOM(data)
	var e envelope
	if json.Unmarshal(data, &e) != nil {
		// The JSON doesn't read whole. A backup a relay cut short ends in
		// the middle of its sealed data and fails here, so this is the
		// damaged-backup case — but so does any other non-JSON body a
		// server answers with (a captive portal's sign-in page), and that
		// isn't damage to rebuild over. Only a body that begins like a
		// magpie backup counts as one; anything else is what it is.
		if isBackupJSON(data) {
			return Bundle{}, ErrCorrupt
		}
		return Bundle{}, ErrNotBackup
	}
	if e.Format != format {
		return Bundle{}, ErrNotBackup
	}
	if e.Version != 1 || e.KDF != "pbkdf2-sha256" {
		return Bundle{}, errors.New("this backup was made by a newer magpie; update magpie to open it")
	}
	if e.Iterations < 100_000 || e.Iterations > 10_000_000 || len(e.Nonce) != 12 || len(e.Salt) < 16 {
		return Bundle{}, ErrCorrupt
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
	key, err := derived(pass, e)
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
	// SignIns puts the backup's sign-ins in, when it has them
	// (provider.RestoreSignIns: nothing renewed later here is written over).
	SignIns bool
	// Memory merges the backup's memory into this computer's
	// (memory.Put): a memory here is never taken away by it.
	Memory bool
}

// All is every part.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true, Library: true, SignIns: true, Memory: true}

// Result says what a restore did.
type Result struct {
	Added, Replaced int      // providers
	NeedKey         []string // providers that came without a key and have none here
	// SignIn is the subscriptions whose settings came with no account
	// here: a backup carries no sign-in, so they are listed once one is
	SignIn []string `json:",omitempty"`
	// Plugins are the plugins installed for it, PluginErrors why the others
	// couldn't be, by spec
	Plugins         []string          `json:",omitempty"`
	PluginErrors    map[string]string `json:",omitempty"`
	Settings        bool
	Profiles        int
	Agents          int               // agent fields changed
	Skipped         []string          // agent fields left alone: the agent is not on this machine, or the value failed
	Library         bool              // the library was put in
	Memory          bool              `json:",omitempty"` // memory changed: the backup's merged in
	LibraryProblems []library.Problem `json:",omitempty"` // what of it couldn't be given to an agent
	// SignIns is what putting the backup's sign-ins in did; nil when it
	// had none, or they weren't asked for.
	SignIns *provider.SignInsResult `json:",omitempty"`
	// SignInsLocked: the backup has sign-ins sealed with a passphrase
	// other than the one set now, so they were left out.
	SignInsLocked bool `json:",omitempty"`
}

// Restore puts the chosen parts of the bundle in. Providers come first, so
// the agents' models that go through them resolve. Only agents on this
// machine are set.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	// the sign-ins first: the subscriptions the providers name are signed
	// in to then, and aren't listed as not signed in (SignIn)
	if parts.SignIns && b.SignIns != nil {
		res, err := provider.RestoreSignIns(b.SignIns)
		if err != nil {
			return r, fmt.Errorf("putting the sign-ins in: %w", err)
		}
		r.SignIns = &res
	}
	if parts.Providers {
		// the proxies first: a provider's choice of one is checked against them
		if b.Proxies != nil {
			if err := settings.RestoreProxies(*b.Proxies, b.scopedKeys("providers")); err != nil {
				return r, fmt.Errorf("putting the saved proxies in: %w", err)
			}
		}
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
		if err := provider.MirrorOrder(b.Order); err != nil {
			return r, err
		}
		if err := provider.MirrorGroupOrder(b.GroupOrder); err != nil {
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
		if b.Plugins != nil {
			r.Plugins, r.PluginErrors = AddPlugins(*b.Plugins)
		}
		r.SignIn = provider.NotSignedIn(b.Providers)
		stored, err := provider.Stored()
		if err != nil {
			return r, err
		}
		for _, p := range stored {
			// a subscription is signed in to (SignIn), never keyed: its
			// entry has no name, and listed here it read "Needs a key: , "
			if !p.Ready() && !provider.OnlyPicks(p) && slices.ContainsFunc(b.Providers, func(q provider.Provider) bool { return q.ID == p.ID }) {
				r.NeedKey = append(r.NeedKey, p.Name)
			}
		}
	}
	if parts.Settings && (b.Settings != nil || b.GatewayKeys != nil) {
		keys := b.scopedKeys("settings")
		// the window's size, the proxy, the menu bar's usage are this machine's own
		cur := settings.Load()
		s := cur
		if b.Settings != nil {
			s = *b.Settings
		}
		s.KeepOwn(cur)
		if !keys {
			s.LANKey, s.LANKeyID = cur.LANKey, cur.LANKeyID
			s.GitHubToken = cur.GitHubToken
			s.BarkKey = cur.BarkKey
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
		// the groups before the keys that name them
		if keys && b.AccountGroups != nil {
			if err := access.RestoreGroups(*b.AccountGroups); err != nil {
				if rollback := settings.Save(cur); rollback != nil {
					return r, errors.Join(err, fmt.Errorf("could not restore original settings after account-group restore failed: %w", rollback))
				}
				return r, err
			}
		}
		if keys && b.GatewayKeys != nil {
			if err := access.Restore(*b.GatewayKeys); err != nil {
				// Settings must be writable before replacing credentials. If the
				// store refuses the write, restore their original association.
				if rollback := settings.Save(cur); rollback != nil {
					return r, errors.Join(err, fmt.Errorf("could not restore original settings after gateway-key restore failed: %w", rollback))
				}
				return r, err
			}
		}
		if keys && b.GatewayKeys == nil {
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
		// an agent whose picks stay on this computer (AgentsHere) takes
		// none from elsewhere; unread settings tell nothing, so none go in
		cur, err := settings.LoadChecked()
		if err != nil {
			return r, err
		}
		kept := map[string]bool{}
		for _, id := range cur.AgentsHere {
			kept[id] = true
		}
		// one value at a time, so one that fails (a model of a subscription
		// not signed in here) leaves the rest to go in
		for _, k := range profileKeys(b.Agents) {
			id := k[:max(strings.LastIndex(k, "."), 0)] // codex@wsl:Ubuntu-24.04.model
			if kept[id] {
				continue
			}
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
		// Even a keyed uploader can carry a redaction it downloaded and never
		// held a value for. Fill blanks from this computer at apply time: the
		// remote may never have held its private token. Nonempty replacements
		// and removed entries still take effect.
		have, err := library.Collect()
		if err != nil {
			return r, err
		}
		lib := b.Library.WithSecrets(have, Secret)
		res, err := library.Put(lib)
		if err != nil {
			return r, fmt.Errorf("the library: %w", err)
		}
		r.Library, r.LibraryProblems = true, res.Problems
	}
	if parts.Memory && b.Memory != nil {
		changed, err := memory.Put(b.Memory)
		if err != nil {
			return r, fmt.Errorf("memory: %w", err)
		}
		if r.Memory = changed; changed {
			// the agents' instructions given what came, as after a change
			// the user made to memory
			if _, err := library.PublishMemory(false); err != nil {
				log.Printf("backup: memory digest: %v", err)
			}
		}
	}
	return r, nil
}

// addPlugins is provider.AddPlugins: a var so a test installs nothing.
var addPlugins = provider.AddPlugins

// AddPlugins installs the plugins a backup or a sync carries that aren't
// here (provider.AddPlugins), giving each its time: a first one downloads
// Bun. It says why each it couldn't add wasn't, by spec.
func AddPlugins(es []plugin.Entry) ([]string, map[string]string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	added, failed := addPlugins(ctx, es)
	var errs map[string]string
	for spec, err := range failed {
		if errs == nil {
			errs = map[string]string{}
		}
		errs[spec] = err.Error()
	}
	return added, errs
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
