// Package davsync keeps magpie's setup the same on every computer through
// a WebDAV folder or an S3-compatible bucket. What goes there is a backup (see internal/backup),
// sealed with a passphrase before it leaves the computer, so the server
// only ever holds a file it can't read.
//
// The magpie serving the gateway syncs every few minutes. The setup is
// taken in five parts: providers (with their pictures and groups),
// settings, profiles, the agents' models and the library (instructions,
// MCP servers and skills). A part changed only here is
// pushed; one changed only on the server is brought in; one changed on
// both since the last sync keeps the newer, and the one it replaced is
// saved in the sync folder beside magpie's files and named in a notice.
// What the server holds is mirrored: a provider removed on one computer
// goes from the others.
package davsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Every is how often the gateway's magpie syncs.
const Every = 3 * time.Minute

// Parts, in the order they are told.
var Parts = []string{"providers", "settings", "profiles", "agents", "library"}

// Config is the sync's setup, kept in sync.json beside magpie's other
// files, readable by the user alone, as provider keys are. An s3://bucket/prefix
// address is an S3-compatible bucket, User its access key ID and Password
// the secret, kept as a WebDAV password is.
type Config struct {
	URL        string `json:"url"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	Passphrase string `json:"passphrase"`
	// S3 alone: the server (none is AWS), its region (none is us-east-1,
	// auto for R2), and the bucket in the path rather than the host name
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	PathStyle bool   `json:"pathStyle,omitempty"`
	Keys      bool   `json:"keys"`   // providers carry their API keys
	Agents    bool   `json:"agents"` // the agents' models go too
	// Library is whether the library goes too; nil, as in a setup made
	// before it could, is yes
	Library *bool `json:"library,omitempty"`
	// Other is the other kind's server, kept when sync moved from it (a
	// WebDAV folder's while an S3 bucket is synced to, or the other way):
	// moving back finds it as it was, its password too. Nothing syncs to it.
	Other *Server `json:"other,omitempty"`
}

// Server is where a Config syncs to, without what it syncs or the
// passphrase: the other kind's, kept beside the one synced to.
type Server struct {
	URL       string `json:"url"`
	User      string `json:"user,omitempty"`
	Password  string `json:"password,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	PathStyle bool   `json:"pathStyle,omitempty"`
}

func (c Config) where() Server {
	return Server{URL: c.URL, User: c.User, Password: c.Password, Endpoint: c.Endpoint, Region: c.Region, PathStyle: c.PathStyle}
}

func (s Server) config() Config {
	return Config{URL: s.URL, User: s.User, Password: s.Password, Endpoint: s.Endpoint, Region: s.Region, PathStyle: s.PathStyle}
}

// saved is the setup whose password c may keep: old, or, for c of the
// other kind, the server of c's kind kept beside it, when there is one.
func (old Config) saved(c Config) Config {
	if o := old.Other; o != nil && old.S3() != c.S3() && o.config().S3() == c.S3() {
		return o.config()
	}
	return old
}

func (c Config) library() bool { return c.Library == nil || *c.Library }

// S3 is whether c keeps the file in an S3 bucket rather than a WebDAV folder.
func (c Config) S3() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.URL)), "s3://")
}

// Kind is the server's kind, as the user reads it: "WebDAV" or "S3".
func (c Config) Kind() string {
	if c.S3() {
		return "S3"
	}
	return "WebDAV"
}

// Notice says what a sync replaced when a part had changed on both sides,
// or when this computer first joined a folder with a setup in it.
type Notice struct {
	At    time.Time `json:"at"`
	Here  []string  `json:"here,omitempty"`  // this computer's parts, replaced by the server's
	There []string  `json:"there,omitempty"` // the server's parts, replaced by this computer's
	Saved string    `json:"saved,omitempty"` // the folder the replaced copies are kept in
}

// state is what the last sync saw: the parts here and on the server, by
// hash, and the server's file.
type state struct {
	Key    string            `json:"key"` // the address, user and passphrase it was for
	Last   time.Time         `json:"last,omitzero"`
	Error  string            `json:"error,omitempty"`
	Notice *Notice           `json:"notice,omitempty"`
	Sum    string            `json:"sum,omitempty"` // the server's file, hashed
	Local  map[string]string `json:"local,omitempty"`
	Remote map[string]string `json:"remote,omitempty"`
}

func path(name string) string { return filepath.Join(settings.Dir(), name) }

// Load reads the setup; false when sync is off.
func Load() (Config, bool) {
	var c Config
	b, err := os.ReadFile(path("sync.json"))
	if err != nil || json.Unmarshal(b, &c) != nil || c.URL == "" {
		return Config{}, false
	}
	return c, true
}

// Configure turns sync on, or changes it. A password or passphrase left
// empty keeps the one set before — the password only for the same server
// and user: it is never sent to another, and is asked for again there.
func Configure(c Config) error {
	c.Other = nil // kept here, never given
	c.URL, c.User = strings.TrimSpace(c.URL), strings.TrimSpace(c.User)
	c.Endpoint, c.Region = strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.Region)
	if !c.S3() { // WebDAV's own: nothing of an S3 setup left behind
		c.Endpoint, c.Region, c.PathStyle = "", "", false
	}
	if err := Check(c); err != nil {
		return err
	}
	if c.S3() && c.User == "" {
		return errors.New("S3 sync needs an access key ID, and its secret")
	}
	// after a sync in progress, which would save its state for the setup it
	// began with
	return locked(func() error {
		if old, ok := Load(); ok {
			kept, needed := password(old.saved(c), c)
			if kept {
				c.Password = old.saved(c).Password
			}
			if needed {
				if c.S3() {
					return fmt.Errorf("type the secret for the access key %s on %s: the one saved is only used with the server and key it was given for", c.User, c.server())
				}
				who := c.server()
				if c.User != "" {
					who = c.User + " on " + who
				}
				return fmt.Errorf("type the password for %s: the one saved is only sent to the server and user it was given for", who)
			}
			if c.Passphrase == "" {
				c.Passphrase = old.Passphrase
			}
			// the other kind's server is kept: the one moved from, or the
			// one kept before
			c.Other = old.Other
			if old.S3() != c.S3() {
				o := old.where()
				c.Other = &o
			}
		}
		if c.S3() && c.Password == "" {
			return fmt.Errorf("type the secret for the access key %s", c.User)
		}
		if c.Passphrase == "" {
			return errors.New("sync needs a passphrase: the file is sealed with it before it leaves this computer")
		}
		if c.Password != "" && c.Passphrase == c.Password {
			// the server is sent the password (an S3 server holds the
			// secret): with it, it could open the file
			if c.S3() {
				return errors.New("the passphrase is the access key's secret: the S3 server holds it, and could open the file with it. Pick a passphrase of its own")
			}
			return errors.New("the passphrase is the server's password: the server is sent the password, and could open the file with it. Pick a passphrase of its own")
		}
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		os.MkdirAll(settings.Dir(), 0o755)
		if err := edit.WriteAtomic(path("sync.json"), b); err != nil {
			return err
		}
		return os.Chmod(path("sync.json"), 0o600)
	})
}

// Check is Configure's look at the address (and an S3 endpoint), for a
// caller to make before asking for the password and passphrase.
func Check(c Config) error {
	_, err := newRemote(c)
	return err
}

// server is the host c's password or secret goes with: the WebDAV
// address's, or the S3 endpoint's (AWS's when none is given).
func (c Config) server() string {
	a := strings.TrimSpace(c.URL)
	if c.S3() {
		if strings.TrimSpace(c.Endpoint) == "" {
			return "AWS"
		}
		a = s3Endpoint(c.Endpoint, "")
	}
	if u, err := url.Parse(a); err == nil && u.Host != "" {
		return u.Host
	}
	return a
}

// password is what becomes of the saved password when old is changed to
// c, with c's own left empty: kept, for the same server and user; or
// needed, a new one typed, for another — unless the user name was just
// taken away, for a server that asks for no sign-in.
func password(old, c Config) (kept, needed bool) {
	if c.Password != "" || old.Password == "" {
		return false, false
	}
	if c.sameAccount(old) {
		return true, false
	}
	cleared := strings.TrimSpace(c.User) == "" && strings.TrimSpace(old.User) != ""
	return false, !cleared
}

// SavedPassword is Configure's look at the saved password for c, with c's
// own left empty, for a caller to make before asking for one: whether it
// is kept, or a new one is needed.
func SavedPassword(c Config) (kept, needed bool) {
	old, ok := Load()
	if !ok {
		return false, false
	}
	c.Password = ""
	return password(old.saved(c), c)
}

// sameAccount is whether c and o are one user on one server: the password
// given for one is only ever sent to the other when they are.
func (c Config) sameAccount(o Config) bool {
	if c.S3() || o.S3() { // one access key at one endpoint, whatever the bucket
		return c.S3() && o.S3() && strings.TrimSpace(c.User) == strings.TrimSpace(o.User) &&
			strings.EqualFold(s3Endpoint(c.Endpoint, ""), s3Endpoint(o.Endpoint, ""))
	}
	a, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil {
		return false
	}
	b, err := url.Parse(strings.TrimSpace(o.URL))
	if err != nil {
		return false
	}
	return a.Host != "" && a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) &&
		strings.TrimSpace(c.User) == strings.TrimSpace(o.User)
}

// Off turns sync off. The file on the server stays.
func Off() error {
	return locked(func() error {
		os.Remove(path("sync-state.json"))
		if err := os.Remove(path("sync.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}

// View is sync as the Settings page shows it: never the secrets.
type View struct {
	On            bool      `json:"on"`
	URL           string    `json:"url,omitempty"`
	User          string    `json:"user,omitempty"`
	PasswordSet   bool      `json:"passwordSet,omitempty"`
	PassphraseSet bool      `json:"passphraseSet,omitempty"`
	Keys          bool      `json:"keys"`
	Agents        bool      `json:"agents"`
	Kind          string    `json:"kind,omitempty"` // "webdav" or "s3", when on
	Endpoint      string    `json:"endpoint,omitempty"`
	Region        string    `json:"region,omitempty"`
	PathStyle     bool      `json:"pathStyle,omitempty"`
	Library       bool      `json:"library"`
	Last          time.Time `json:"last,omitzero"`
	Error         string    `json:"error,omitempty"`
	Notice        *Notice   `json:"notice,omitempty"`
	// Other is the other kind's server, kept for moving back to: not
	// synced to
	Other *OtherView `json:"other,omitempty"`
}

// OtherView is the other kind's server as the Settings page shows it.
type OtherView struct {
	Kind        string `json:"kind"` // "webdav" or "s3"
	URL         string `json:"url"`
	User        string `json:"user,omitempty"`
	PasswordSet bool   `json:"passwordSet,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	PathStyle   bool   `json:"pathStyle,omitempty"`
}

// Status is sync's setup and how the last sync went.
func Status() View {
	c, ok := Load()
	if !ok {
		return View{Keys: true, Agents: true, Library: true}
	}
	st := loadState()
	v := View{On: true, URL: c.URL, User: c.User, PasswordSet: c.Password != "", PassphraseSet: c.Passphrase != "",
		Keys: c.Keys, Agents: c.Agents, Library: c.library(), Error: st.Error, Notice: st.Notice,
		Kind: strings.ToLower(c.Kind()), Endpoint: c.Endpoint, Region: c.Region, PathStyle: c.PathStyle}
	if st.Key == stateKey(c) {
		v.Last = st.Last
	}
	if o := c.Other; o != nil {
		v.Other = &OtherView{Kind: strings.ToLower(o.config().Kind()), URL: o.URL, User: o.User, PasswordSet: o.Password != "",
			Endpoint: o.Endpoint, Region: o.Region, PathStyle: o.PathStyle}
	}
	return v
}

// Dismiss clears the notice.
func Dismiss() error {
	return locked(func() error {
		st := loadState()
		st.Notice = nil
		saveState(st)
		return nil
	})
}

func loadState() state {
	var st state
	if b, err := os.ReadFile(path("sync-state.json")); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

func saveState(st state) {
	b, _ := json.MarshalIndent(st, "", "  ")
	edit.WriteAtomic(path("sync-state.json"), b)
}

// stateKey is the setup the state is for: an S3 endpoint too, when there is one
func stateKey(c Config) string {
	k := c.URL
	if c.Endpoint != "" {
		k += "\x00" + c.Endpoint
	}
	return sum([]byte(k + "\x00" + c.User + "\x00" + c.Passphrase))
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

var mu sync.Mutex

// Now syncs once; nothing when sync is off.
func Now(ctx context.Context) error {
	if _, ok := Load(); !ok { // off: no lock taken, so none made
		return nil
	}
	unlock, err := lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	mu.Lock()
	defer mu.Unlock()
	// read again under the lock: another magpie may have turned sync off,
	// or changed it, while this one waited
	c, ok := Load()
	if !ok {
		return nil
	}
	st := loadState()
	if st.Key != stateKey(c) { // another folder or passphrase: start afresh
		st = state{Key: stateKey(c)}
	}
	err = syncOnce(ctx, c, &st)
	if errors.Is(err, errChanged) { // another computer got in between: again, over its version
		err = syncOnce(ctx, c, &st)
	}
	st.Error = ""
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Last = time.Now()
	}
	saveState(st)
	return err
}

// Run syncs a little after it starts and every Every after that, until
// ctx ends. A failure is logged once, not on every try.
func Run(ctx context.Context) {
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := Now(c)
		cancel()
		if msg := fmt.Sprint(err); err != nil && msg != last {
			kind := "WebDAV"
			if c, ok := Load(); ok {
				kind = c.Kind()
			}
			log.Printf("syncing through %s: %s", kind, msg)
			last = msg
		} else if err == nil {
			last = ""
		}
		t.Reset(Every)
	}
}

func collect(c Config) (backup.Bundle, error) {
	b, err := backup.Collect(c.Keys, "magpie")
	if b.Settings == nil {
		s := settings.Load()
		b.Settings = &s
	}
	if !c.Agents {
		b.Agents = nil
	}
	if !c.library() {
		b.Library = nil
	}
	return b, err
}

// hashes is each part of b, hashed: what is compared to tell a change.
func hashes(b backup.Bundle) map[string]string {
	h := func(v any) string { j, _ := json.Marshal(v); return sum(j) }
	var s settings.Settings
	if b.Settings != nil {
		s = *b.Settings
	}
	s.KeepOwn(settings.Settings{}) // this computer's own: never synced
	providers := []any{b.Providers, b.Icons, b.Groups}
	if b.Searches != nil && len(*b.Searches) > 0 { // as before them, without one
		providers = append(providers, *b.Searches)
	}
	return map[string]string{
		"providers": h(providers),
		"settings":  h(s),
		"profiles":  h(orEmpty(b.Profiles)),
		"agents":    h(orEmpty(b.Agents)),
		"library":   h(b.Library),
	}
}

func orEmpty[V any](m map[string]V) map[string]V {
	if m == nil {
		return map[string]V{}
	}
	return m
}

// take puts from's part in to.
func take(to *backup.Bundle, from backup.Bundle, part string) {
	switch part {
	case "providers":
		ps := from.Providers
		if !from.Keys && to.Keys { // sent without keys: keep the ones the server has
			keys := map[string]provider.Provider{}
			for _, p := range to.Providers {
				keys[p.ID] = p
			}
			ps = slices.Clone(ps)
			for i, p := range ps {
				if k, ok := keys[p.ID]; ok && p.Key == "" && len(p.Keys) == 0 {
					ps[i].Key, ps[i].KeyName, ps[i].Keys, ps[i].KeyProtocol = k.Key, k.KeyName, k.Keys, k.KeyProtocol
					if p.BalanceToken == "" {
						ps[i].BalanceToken = k.BalanceToken
					}
				}
			}
		}
		searches := from.Searches
		if searches != nil && !from.Keys && to.Keys && to.Searches != nil { // the same for the search APIs
			keys := map[string]string{}
			for _, a := range *to.Searches {
				keys[a.Vendor] = a.Key
			}
			ss := slices.Clone(*searches)
			for i, a := range ss {
				if a.Key == "" {
					ss[i].Key = keys[a.Vendor]
				}
			}
			searches = &ss
		}
		to.Providers, to.Icons, to.Groups, to.Searches = ps, from.Icons, from.Groups, searches
		to.Keys = to.Keys || from.Keys
	case "settings":
		to.Settings = from.Settings
	case "profiles":
		to.Profiles = from.Profiles
	case "agents":
		to.Agents = from.Agents
	case "library":
		lib := from.Library
		if lib != nil && !from.Keys && to.Keys { // sent without keys: keep the ones the server has
			lib = lib.WithSecrets(to.Library, backup.Secret)
		}
		to.Library = lib
	}
}

// changed is when a part was last changed here, as its files say.
func changed(part string) time.Time {
	mtime := func(p string) time.Time {
		if fi, err := os.Stat(p); err == nil {
			return fi.ModTime()
		}
		return time.Time{}
	}
	switch part {
	case "providers":
		return mtime(provider.Path())
	case "settings":
		return mtime(settings.Path())
	case "profiles":
		return mtime(profile.Path())
	case "library":
		return library.Changed()
	}
	var t time.Time
	for _, a := range agent.Detected() {
		if m := mtime(a.Path); m.After(t) {
			t = m
		}
	}
	return t
}

// bring puts the server's part in here: providers and profiles mirrored,
// so what went elsewhere goes here too.
func bring(b backup.Bundle, part string) error {
	switch part {
	case "providers":
		for name, data := range b.Icons {
			if f := provider.IconFile(name); f != "" && len(data) <= provider.MaxIcon {
				if _, err := os.Stat(f); err != nil {
					os.MkdirAll(filepath.Dir(f), 0o755)
					edit.WriteAtomic(f, data)
				}
			}
		}
		if err := provider.Mirror(b.Providers, b.Groups); err != nil {
			return err
		}
		if b.Searches == nil { // from a magpie before them: the ones here stay
			return nil
		}
		return provider.MirrorSearchAPIs(*b.Searches)
	case "settings":
		_, err := backup.Restore(b, backup.Parts{Settings: true})
		return err
	case "profiles":
		here, err := profile.Load()
		if err != nil {
			return err
		}
		for name := range here {
			if _, ok := b.Profiles[name]; !ok {
				if err := profile.Delete(name); err != nil {
					return err
				}
			}
		}
		_, err = backup.Restore(b, backup.Parts{Profiles: true})
		return err
	case "agents":
		_, err := backup.Restore(b, backup.Parts{Agents: true})
		return err
	case "library":
		_, err := backup.Restore(b, backup.Parts{Library: true})
		return err
	}
	return nil
}

func syncOnce(ctx context.Context, c Config, st *state) error {
	d, err := newRemote(c)
	if err != nil {
		return err
	}
	data, etag, err := d.get(ctx)
	if err != nil {
		return err
	}
	local, err := collect(c)
	if err != nil {
		return err
	}
	L := hashes(local)
	push := func(b backup.Bundle, etag string) error {
		b.Created, b.App = time.Now().UTC(), "magpie"
		sealed, err := backup.Seal(b, c.Passphrase)
		if err != nil {
			return err
		}
		if err := d.put(ctx, sealed, etag); err != nil {
			return err
		}
		st.Sum, st.Remote = sum(sealed), hashes(b)
		return nil
	}
	if data == nil { // nothing there yet: this computer's setup is the first
		if err := push(local, ""); err != nil {
			return err
		}
		st.Local = L
		return nil
	}
	if sum(data) == st.Sum && maps.Equal(L, st.Local) {
		return nil // nothing changed on either side
	}
	remote, err := backup.Open(data, c.Passphrase)
	if errors.Is(err, backup.ErrPassphrase) {
		return errors.New("the passphrase doesn't open the file on the server: it was sealed with another one; use the passphrase set on your other computers")
	}
	if err != nil {
		return err
	}
	R := hashes(remote)
	first := st.Local == nil
	merged := remote
	var bringIn, here, there []string
	for _, p := range Parts {
		if L[p] == R[p] || (p == "agents" && !c.Agents) || (p == "library" && !c.library()) {
			continue
		}
		if p == "library" && remote.Library == nil { // from a magpie before the library went: this computer's goes up
			take(&merged, local, p)
			continue
		}
		lc := !first && L[p] != st.Local[p]
		rc := first || R[p] != st.Remote[p]
		switch {
		case lc && !rc:
			take(&merged, local, p)
		case rc && !lc:
			bringIn = append(bringIn, p)
			if first {
				here = append(here, p)
			}
		case lc && rc: // both: the newer stays
			if changed(p).After(remote.Created) {
				take(&merged, local, p)
				there = append(there, p)
			} else {
				bringIn = append(bringIn, p)
				here = append(here, p)
			}
		}
	}
	var saved string
	if len(here)+len(there) > 0 {
		dir := path("sync")
		os.MkdirAll(dir, 0o700)
		stamp := time.Now().Format("2006-01-02-150405")
		if len(here) > 0 {
			if b, err := backup.Seal(local, c.Passphrase); err == nil {
				edit.WriteAtomic(filepath.Join(dir, stamp+"-this-computer"+backup.Ext), b)
			}
		}
		if len(there) > 0 {
			edit.WriteAtomic(filepath.Join(dir, stamp+"-server"+backup.Ext), data)
		}
		saved = dir
	}
	for _, p := range bringIn {
		if err := bring(remote, p); err != nil {
			return fmt.Errorf("bringing in the %s: %w", p, err)
		}
	}
	// the server's file is in; what is pushed next counts as changed here
	// until the push is done
	now := L
	if len(bringIn) > 0 {
		if local, err = collect(c); err != nil {
			return err
		}
		now = hashes(local)
	}
	M, pending := hashes(merged), map[string]string{}
	for _, p := range Parts {
		pending[p] = now[p]
		if M[p] != R[p] && st.Local != nil {
			pending[p] = st.Local[p]
		}
	}
	st.Local, st.Sum, st.Remote = pending, sum(data), R
	if len(here)+len(there) > 0 {
		st.Notice = &Notice{At: time.Now(), Here: here, There: there, Saved: saved}
	}
	if !maps.Equal(M, R) {
		if err := push(merged, etag); err != nil {
			return err
		}
	}
	st.Local = now
	return nil
}
