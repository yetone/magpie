// Package davsync keeps magpie's setup the same on every computer through
// a WebDAV folder, an S3-compatible bucket or a GitHub repository. What goes there is a backup (see internal/backup),
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
	"bytes"
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

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// Every is how often the gateway's magpie syncs.
const Every = 3 * time.Minute

// Parts, in the order they are told.
var Parts = []string{"providers", "settings", "profiles", "agents", "library"}

// Config is the sync's setup, kept in sync.json beside magpie's other
// files, readable by the user alone, as provider keys are. An s3://bucket/prefix
// address is an S3-compatible bucket, User its access key ID and Password
// the secret, kept as a WebDAV password is. For github://owner/repo/prefix,
// Password is the repository's Contents token, independent of GitHubToken
// in settings (the library's).
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
	// GitHub alone: an empty branch uses the repository's default branch.
	Branch string `json:"branch,omitempty"`
	Keys   bool   `json:"keys"`   // providers carry their API keys
	Agents bool   `json:"agents"` // the agents' models go too
	// Library is whether the library goes too; nil, as in a setup made
	// before it could, is yes
	Library *bool `json:"library,omitempty"`
	// Usage is whether this computer shares its usage with the others
	// syncing to the server, and sees theirs (#542)
	Usage bool `json:"usage,omitempty"`
	// Other is the most recently left server, retained for older clients.
	// Servers holds every inactive kind. Nothing syncs to them.
	Other *Server `json:"other,omitempty"`
	// Servers keeps every inactive kind, including Other for older clients.
	Servers map[string]Server `json:"servers,omitempty"`
	// AutoEvery is how many minutes apart the gateway's magpie syncs by
	// itself: 0 is Every's 3, Manual never — then only Sync now syncs
	// (#847). Set by SetAuto alone; Configure keeps it.
	AutoEvery int `json:"autoEvery,omitempty"`
}

// Manual is AutoEvery's never: sync runs only when asked.
const Manual = -1

// AutoChoices are the minutes SetAuto takes, Manual first.
var AutoChoices = []int{Manual, 3, 15, 30, 60}

// auto is how often c syncs by itself; 0 when it doesn't.
func (c Config) auto() time.Duration {
	switch {
	case c.AutoEvery == Manual:
		return 0
	case c.AutoEvery > 0:
		return time.Duration(c.AutoEvery) * time.Minute
	}
	return Every
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
	Branch    string `json:"branch,omitempty"`
}

func (c Config) where() Server {
	return Server{URL: c.URL, User: c.User, Password: c.Password, Endpoint: c.Endpoint, Region: c.Region, PathStyle: c.PathStyle, Branch: c.Branch}
}

func (s Server) config() Config {
	return Config{URL: s.URL, User: s.User, Password: s.Password, Endpoint: s.Endpoint, Region: s.Region, PathStyle: s.PathStyle, Branch: s.Branch}
}

// Kept is an inactive server, including setups saved before Servers existed.
func (c Config) Kept(kind string) (Server, bool) {
	kind = strings.ToLower(kind)
	if s, ok := c.Servers[kind]; ok {
		return s, true
	}
	if c.Other != nil && strings.EqualFold(c.Other.config().Kind(), kind) {
		return *c.Other, true
	}
	return Server{}, false
}

// saved is the setup whose password c may keep: old, or, for c of the
// other kind, the server of c's kind kept beside it, when there is one.
func (old Config) saved(c Config) Config {
	if old.Kind() != c.Kind() {
		if s, ok := old.Kept(c.Kind()); ok {
			return s.config()
		}
		return Config{}
	}
	return old
}

func (c Config) library() bool { return c.Library == nil || *c.Library }

// S3 is whether c keeps the file in an S3 bucket rather than a WebDAV folder.
func (c Config) S3() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.URL)), "s3://")
}

// GitHub is whether c keeps the file in a GitHub repository.
func (c Config) GitHub() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.URL)), "github://")
}

// Kind is the server's kind, as the user reads it.
func (c Config) Kind() string {
	if c.GitHub() {
		return "GitHub"
	}
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
	// Restored is a Restore's notice: Here are the parts it brought in,
	// what was here before kept in Saved, which Undo brings back
	Restored bool `json:"restored,omitempty"`
}

// state is what the last sync saw: the parts here and on the server, by
// hash, and the server's file.
type state struct {
	Key    string    `json:"key"` // the address, user and passphrase it was for
	Last   time.Time `json:"last,omitzero"`
	Error  string    `json:"error,omitempty"`
	Notice *Notice   `json:"notice,omitempty"`
	Sum    string    `json:"sum,omitempty"` // the server's file, hashed
	// Server is that file's version, as the server tells it: the next sync
	// asks for the file only if it isn't still this one
	Server version           `json:"server,omitzero"`
	Local  map[string]string `json:"local,omitempty"`
	Remote map[string]string `json:"remote,omitempty"`
	Usage  *usageState       `json:"usage,omitempty"`
	// Undo is the file the last Restore kept this computer's setup in, from
	// just before it: Undo brings it back
	Undo string `json:"undo,omitempty"`
	// File is what the server held where the backup should be, when the
	// last sync couldn't read it as one
	File *ServerFile `json:"file,omitempty"`
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
	c.Servers = nil
	c.URL, c.User = strings.TrimSpace(c.URL), strings.TrimSpace(c.User)
	c.Endpoint, c.Region = strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.Region)
	c.Branch = strings.TrimSpace(c.Branch)
	if !c.S3() { // WebDAV's own: nothing of an S3 setup left behind
		c.Endpoint, c.Region, c.PathStyle = "", "", false
	}
	if !c.GitHub() {
		c.Branch = ""
	} else {
		c.User = ""
		c.Password = strings.TrimSpace(c.Password)
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
				if c.GitHub() {
					return errors.New("type a GitHub token for this repository: the saved token is only used with the repository it was given for")
				}
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
			c.Servers = maps.Clone(old.Servers)
			if c.Servers == nil {
				c.Servers = map[string]Server{}
			}
			if old.Other != nil {
				c.Servers[strings.ToLower(old.Other.config().Kind())] = *old.Other
			}
			c.AutoEvery = old.AutoEvery // SetAuto's, not the form's
			if old.Kind() != c.Kind() {
				o := old.where()
				c.Other = &o
				c.Servers[strings.ToLower(old.Kind())] = o
			}
			delete(c.Servers, strings.ToLower(c.Kind()))
		}
		if c.GitHub() && c.Password == "" {
			return errors.New("GitHub sync needs a token with Contents read and write permission for the repository")
		}
		if c.S3() && c.Password == "" {
			return fmt.Errorf("type the secret for the access key %s", c.User)
		}
		if c.Passphrase == "" {
			return errors.New("sync needs a passphrase: the file is sealed with it before it leaves this computer")
		}
		if c.Password != "" && c.Passphrase == c.Password {
			if c.GitHub() {
				return errors.New("the passphrase is the GitHub token: GitHub receives it, and could open the file with it. Pick a passphrase of its own")
			}
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
	if c.GitHub() || o.GitHub() {
		a, ea := newGitHub(c)
		b, eb := newGitHub(o)
		return c.GitHub() && o.GitHub() && ea == nil && eb == nil && strings.EqualFold(a.repo, b.repo)
	}
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

// Off turns sync off. The file on the server stays, and so does what this
// computer last synced with it: turned on again to the same server, it is
// not a new computer, so what was changed here while it was off goes up
// rather than the server's older setup coming down over it (#939: every
// agent back to how it was when sync was turned off). How the last sync
// went, its notice and a restore's undo go.
func Off() error {
	return locked(func() error {
		st := loadState()
		if st.Local == nil {
			os.Remove(path("sync-state.json"))
			os.Remove(path(cacheName))
		} else {
			st.Error, st.Notice, st.Undo, st.Usage = "", nil, "", nil
			saveState(st)
		}
		usage.DropAllShared() // the others' usage came by sync: it goes with it (#542)
		if err := os.Remove(path("sync.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}

// View is sync as the Settings page shows it: never the secrets.
type View struct {
	On            bool   `json:"on"`
	URL           string `json:"url,omitempty"`
	User          string `json:"user,omitempty"`
	PasswordSet   bool   `json:"passwordSet,omitempty"`
	PassphraseSet bool   `json:"passphraseSet,omitempty"`
	Keys          bool   `json:"keys"`
	Agents        bool   `json:"agents"`
	Kind          string `json:"kind,omitempty"` // "webdav", "s3" or "github", when on
	Endpoint      string `json:"endpoint,omitempty"`
	Region        string `json:"region,omitempty"`
	PathStyle     bool   `json:"pathStyle,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Library       bool   `json:"library"`
	Usage         bool   `json:"usage,omitempty"`
	// UsageError is why usage couldn't be shared the last time it was tried
	UsageError string    `json:"usageError,omitempty"`
	Last       time.Time `json:"last,omitzero"`
	Error      string    `json:"error,omitempty"`
	Notice     *Notice   `json:"notice,omitempty"`
	// Other is the other kind's server, kept for moving back to: not
	// synced to
	Other   *OtherView           `json:"other,omitempty"`
	Servers map[string]OtherView `json:"servers,omitempty"`
	// Auto is how many minutes apart it syncs by itself; 0 never: only when
	// asked
	Auto int `json:"auto"`
	// Undo is whether the last Restore can be undone
	Undo bool `json:"undo,omitempty"`
	// File is what the server holds instead of a backup, when Error is
	// that it isn't one; File.Replace offers Upload
	File *ServerFile `json:"serverFile,omitempty"`
}

// OtherView is the other kind's server as the Settings page shows it.
type OtherView struct {
	Kind        string `json:"kind"` // "webdav", "s3" or "github"
	URL         string `json:"url"`
	User        string `json:"user,omitempty"`
	PasswordSet bool   `json:"passwordSet,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	PathStyle   bool   `json:"pathStyle,omitempty"`
	Branch      string `json:"branch,omitempty"`
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
		Kind: strings.ToLower(c.Kind()), Endpoint: c.Endpoint, Region: c.Region, PathStyle: c.PathStyle, Branch: c.Branch,
		Auto: int(c.auto() / time.Minute)}
	if st.Key == stateKey(c) {
		v.Last = st.Last
		v.Undo = st.Undo != "" && isFile(st.Undo)
		if st.Error != "" {
			v.File = st.File
		}
		if c.Usage && st.Usage != nil {
			v.UsageError = st.Usage.Error
		}
	}
	v.Usage = c.Usage
	if o := c.Other; o != nil {
		v.Other = &OtherView{Kind: strings.ToLower(o.config().Kind()), URL: o.URL, User: o.User, PasswordSet: o.Password != "",
			Endpoint: o.Endpoint, Region: o.Region, PathStyle: o.PathStyle, Branch: o.Branch}
	}
	for kind, s := range c.Servers {
		if v.Servers == nil {
			v.Servers = map[string]OtherView{}
		}
		v.Servers[kind] = OtherView{Kind: kind, URL: s.URL, User: s.User, PasswordSet: s.Password != "",
			Endpoint: s.Endpoint, Region: s.Region, PathStyle: s.PathStyle, Branch: s.Branch}
	}
	return v
}

// Dismiss clears the notice, and with a restore's, its undo.
func Dismiss() error {
	return locked(func() error {
		st := loadState()
		st.Notice, st.Undo = nil, "" // a restore's undo goes with its notice
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
	if c.GitHub() {
		k += "\x00" + c.Branch
	}
	return sum([]byte(k + "\x00" + c.User + "\x00" + c.Passphrase))
}

// cacheName is the copy of the server's file as last read or written, kept
// readable by the user alone: a sync that finds it unchanged on the server
// but something changed here merges with it, rather than reading it again.
const cacheName = "sync-server" + backup.Ext

// remember keeps data as the server's file last seen.
func remember(data []byte) {
	if b, err := os.ReadFile(path(cacheName)); err == nil && sum(b) == sum(data) {
		return
	}
	os.MkdirAll(settings.Dir(), 0o755)
	if edit.WriteAtomic(path(cacheName), data) == nil {
		os.Chmod(path(cacheName), 0o600)
	}
}

// cached is the server's file as last seen, when it is the one hashed to
// want; nil when it isn't kept.
func cached(want string) []byte {
	b, err := os.ReadFile(path(cacheName))
	if err != nil || want == "" || sum(b) != want {
		return nil
	}
	return b
}

// keepDamaged writes a server file that couldn't be read aside under the
// sync folder, so the damage is inspectable rather than silently replaced.
// It keeps the newest few: a sync that rebuilds on every tick (a server
// that always answers the same unreadable way) would otherwise pile up a
// copy each time. The name is sorted by time, so the oldest are the first
// to go.
func keepDamaged(data []byte) { keepAside(data, "server-damaged", 3) }

// keepAside writes data under the sync folder as <time>-<tag>, keeping the
// newest n so tagged.
func keepAside(data []byte, tag string, n int) error {
	dir := path("sync")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := filepath.Join(dir, time.Now().Format("2006-01-02-150405")+"-"+tag+backup.Ext)
	if err := edit.WriteAtomic(name, data); err != nil {
		return err
	}
	os.Chmod(name, 0o600)
	old, _ := filepath.Glob(filepath.Join(dir, "*-"+tag+backup.Ext))
	slices.Sort(old) // names begin with the time, so oldest first
	for len(old) > n {
		os.Remove(old[0])
		old = old[1:]
	}
	return nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

var mu sync.Mutex

// Now syncs once; nothing when sync is off. Usage is shared at most every
// usageEvery.
func Now(ctx context.Context) error { return syncNow(ctx, false) }

// SyncNow is Now asked for: usage is shared whatever the time.
func SyncNow(ctx context.Context) error { return syncNow(ctx, true) }

func syncNow(ctx context.Context, force bool) error {
	if _, ok := Load(); !ok { // off: no lock taken, so none made
		return nil
	}
	// a sync has no time to end in (see stallAfter), a wait for another's
	// does
	lctx, lcancel := context.WithTimeout(ctx, wait)
	unlock, err := lock(lctx)
	lcancel()
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
	st.Error, st.File = "", nil
	var nb *notBackup
	if errors.As(err, &nb) {
		st.File = &nb.f
	}
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Last = time.Now()
		shareUsage(ctx, c, &st, force)
	}
	saveState(st)
	return err
}

// maxWait is the longest Run waits for a server that is limiting requests
// and didn't say for how long; one that says is waited for as long as it
// asks, up to limitWait.
const (
	maxWait   = 30 * time.Minute
	limitWait = 6 * time.Hour
)

// backoff is how long Run waits after a sync that ended with err, the last
// wait having been prev: Every, but longer for a server limiting requests
// (429, 503) — as long as its Retry-After says, or else twice the last
// wait, up to maxWait — so that a limit is not run into again and again.
func backoff(err error, prev time.Duration) time.Duration { return backoffFrom(err, prev, Every) }

// backoffFrom is backoff for a sync every base.
func backoffFrom(err error, prev, base time.Duration) time.Duration {
	var rl *rateLimited
	if !errors.As(err, &rl) {
		return base
	}
	if rl.after > 0 {
		return min(max(rl.after, base), max(limitWait, base))
	}
	return min(max(2*prev, 2*base), max(maxWait, base))
}

// poll is how often Run looks at the setup: whether sync is on, by
// itself, and how often (#847). Looking costs no request. A var for tests.
var poll = time.Minute

// Run syncs a little after it starts and every AutoEvery after that, until
// ctx ends — less often while the server is limiting requests, and never
// while sync is set to run only when asked. A failure is logged once, not
// on every try.
func Run(ctx context.Context) { run(ctx, 20*time.Second) }

func run(ctx context.Context, first time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	last := ""
	var tried time.Time // the last sync Run made
	var gap time.Duration
	limited := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(poll)
		cfg, ok := Load()
		every := cfg.auto()
		if !ok || every == 0 {
			continue
		}
		// a server limiting requests is waited for as backoff says; else the
		// time set now, which may have changed since the last sync
		w := every
		if limited {
			w = max(gap, every)
		}
		if !tried.IsZero() && time.Since(tried) < w {
			continue
		}
		// no request is let stand still for long (stallAfter), but a large
		// backup on a slow line takes what it takes: only an hour ends it
		c, cancel := context.WithTimeout(ctx, time.Hour)
		err := Now(c)
		cancel()
		tried = time.Now()
		if msg := fmt.Sprint(err); err != nil && msg != last {
			log.Printf("syncing through %s: %s", cfg.Kind(), msg)
			last = msg
		} else if err == nil {
			last = ""
		}
		gap = backoffFrom(err, w, every)
		var rl *rateLimited
		limited = errors.As(err, &rl)
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
	settingsHash := h(s)
	if b.GatewayKeys != nil { // a missing store keeps older bundles' hash
		settingsHash = h([]any{s, *b.GatewayKeys})
	}
	providers := []any{b.Providers, b.Icons, b.Groups}
	if b.Searches != nil && len(*b.Searches) > 0 { // as before them, without one
		providers = append(providers, *b.Searches)
	}
	if len(b.Order) > 0 { // as before it, when never arranged
		providers = append(providers, map[string][]string{"order": b.Order})
	}
	if len(b.GroupOrder) > 0 { // as before it, when never arranged
		providers = append(providers, map[string][]string{"groupOrder": b.GroupOrder})
	}
	return map[string]string{
		"providers": h(providers),
		"settings":  settingsHash,
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

// take puts from's part in to. The merged bundle is a sync one (BundleVersion):
// its per-part markers, not the whole-bundle Keys bit, say which part
// carries credentials, so a keyed providers upload no longer tells a reader
// the settings and the library came with keys too. If an older writer drops
// those markers, readers fall back to Keys even if the version stays 2.
func take(to *backup.Bundle, from backup.Bundle, part string) {
	// Preserve every legacy part's scope before a providers upload changes
	// Keys for older readers. An untouched nil marker would otherwise inherit
	// that new value and turn a redaction into a credential-bearing update.
	if to.ProvidersKeys == nil {
		to.ProvidersKeys = backup.Flag(to.Keys)
	}
	if to.SettingsKeys == nil {
		to.SettingsKeys = backup.Flag(to.Keys)
	}
	if to.LibraryKeys == nil {
		to.LibraryKeys = backup.Flag(to.Keys)
	}
	switch part {
	case "providers":
		keys := from.Keys || (from.ProvidersKeys != nil && *from.ProvidersKeys)
		ps := from.Providers
		if !keys && (to.Keys || (to.ProvidersKeys != nil && *to.ProvidersKeys)) { // sent without keys: keep the ones the server has
			keys := map[string]provider.Provider{}
			for _, p := range to.Providers {
				keys[p.ID] = p
			}
			ps = slices.Clone(ps)
			for i, p := range ps {
				if k, ok := keys[p.ID]; ok && p.Key == "" && len(p.Keys) == 0 {
					ps[i].Key, ps[i].KeyName, ps[i].Keys, ps[i].KeyProtocol, ps[i].KeyWeight = k.Key, k.KeyName, k.Keys, k.KeyProtocol, k.KeyWeight
					if p.BalanceToken == "" {
						ps[i].BalanceToken = k.BalanceToken
					}
					if p.AccessKeyID == "" && p.SecretAccessKey == "" {
						ps[i].AccessKeyID, ps[i].SecretAccessKey = k.AccessKeyID, k.SecretAccessKey
					}
				}
			}
		}
		searches := from.Searches
		if searches != nil && !keys && (to.Keys || (to.ProvidersKeys != nil && *to.ProvidersKeys)) && to.Searches != nil { // the same for the search APIs
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
		to.Providers, to.Icons, to.Groups, to.Searches, to.Order, to.GroupOrder = ps, from.Icons, from.Groups, searches, from.Order, from.GroupOrder
		to.ProvidersKeys = backup.Flag(keys || (to.ProvidersKeys != nil && *to.ProvidersKeys))
		to.Keys = to.Keys || keys // the whole-bundle bit follows it, for a magpie that reads no per-part ones: it keeps the server's keys on its own upload then
	case "settings":
		keys := from.Keys || (from.SettingsKeys != nil && *from.SettingsKeys)
		s := from.Settings
		if s != nil && !keys && (to.Keys || (to.SettingsKeys != nil && *to.SettingsKeys)) && to.Settings != nil {
			// Sent without keys: keep the ones the server has, as for providers.
			copy := *s
			copy.LANKey, copy.LANKeyID = to.Settings.LANKey, to.Settings.LANKeyID
			copy.GitHubToken = to.Settings.GitHubToken
			copy.OTel.Headers = nil
			if strings.TrimRight(strings.TrimSpace(copy.OTel.Endpoint), "/") == to.Settings.OTel.Endpoint {
				copy.OTel.Headers = to.Settings.OTel.Headers
			}
			s = &copy
		}
		if s != nil {
			to.Settings = s
		}
		if keys && from.GatewayKeys != nil {
			to.GatewayKeys = from.GatewayKeys // an explicit empty store clears it
		}
		to.SettingsKeys = backup.Flag(keys || (to.SettingsKeys != nil && *to.SettingsKeys))
	case "profiles":
		to.Profiles = from.Profiles
	case "agents":
		to.Agents = from.Agents
	case "library":
		keys := from.Keys || (from.LibraryKeys != nil && *from.LibraryKeys)
		lib := from.Library
		// An empty secret is not a clear: a keyed computer's library holds
		// "" for a server it synced and never held a token for, and writes
		// that over the token the keyless computer that set it up has. The
		// server's value is kept for such an entry whatever the policy; a
		// server taken out here is gone, as a part's own clearing works.
		if lib != nil && (to.Keys || (to.LibraryKeys != nil && *to.LibraryKeys)) {
			lib = lib.WithSecrets(to.Library, backup.Secret)
		}
		to.Library = lib
		to.LibraryKeys = backup.Flag(keys || (to.LibraryKeys != nil && *to.LibraryKeys))
	}
	to.Version = backup.BundleVersion // the merged bundle is a sync one, whatever the server's file was
}

// changed is when a part was last changed here, as its files say.
func changed(part string, keys bool) time.Time {
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
		m := mtime(settings.Path())
		if k := mtime(access.Path()); keys && k.After(m) {
			m = k
		}
		return m
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
		if err := provider.MirrorOrder(b.Order); err != nil {
			return err
		}
		if err := provider.MirrorGroupOrder(b.GroupOrder); err != nil {
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
	// the file only if it changed since the last sync: one unchanged costs
	// a request, not a download
	data, ver, err := d.get(ctx, st.Server)
	unchanged := errors.Is(err, errNotModified)
	if err != nil && !unchanged {
		return err
	}
	local, err := collect(c)
	if err != nil {
		return err
	}
	L := hashes(local)
	if unchanged {
		if maps.Equal(L, st.Local) {
			return nil // nothing changed on either side
		}
		// changed here: merged with the server's file as last seen, or,
		// when that copy is gone, with the file read again
		if data = cached(st.Sum); data == nil {
			if data, ver, err = d.get(ctx, version{}); err != nil {
				return err
			}
		}
	}
	etag := ver.ETag
	rebuilt := false // a damaged server file replaced by this computer's merge
	push := func(b backup.Bundle, etag string) error {
		b.Created, b.App = time.Now().UTC(), "magpie"
		sealed, err := backup.Seal(b, c.Passphrase)
		if err != nil {
			return err
		}
		v, err := d.put(ctx, sealed, etag)
		if err != nil {
			return err
		}
		st.Sum, st.Server, st.Remote = sum(sealed), v, hashes(b)
		remember(sealed)
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
		st.Server = ver
		remember(data)
		return nil // nothing changed on either side
	}
	remote, err := backup.Open(data, c.Passphrase)
	if errors.Is(err, backup.ErrPassphrase) {
		return errors.New("the passphrase doesn't open the file on the server: it was sealed with another one; use the passphrase set on your other computers")
	}
	if err != nil {
		// The server's file can't be read as a backup: a write a relay or
		// tunnel cut short, or one damaged since. Failing here would stop
		// every sync after, because this computer's own setup is fine and
		// could never be pushed over the unreadable file. But rebuilding
		// must not lose another computer's newer setup that the damaged
		// write swallowed, so this is not a place to push this computer's
		// bundle as it is. It is a place to fall back to the last good copy
		// of the server's file — the one this computer read whole before it
		// broke — and merge over it as a normal sync would. That needs three
		// things to hold, and any one missing means the error stands:
		//   - this computer has synced before (st.Local): a fresh one has no
		//     last-good copy and no right to rebuild what it never saw;
		//   - the cached copy is the file this sync hashed (sum == st.Sum),
		//     so it is the version the damage replaced, not some older one;
		//   - the body really reads as a damaged backup, not as some other
		//     answer a server gave (a captive portal's page): a body that
		//     isn't backup-shaped at all is its own error, never rebuilt.
		// The merge then runs with the cached copy as the remote and the
		// damaged file's ETag as the version to replace, so the push is
		// still checked against the server and another computer's parts
		// survive the way they would on any sync.
		if !errors.Is(err, backup.ErrCorrupt) || st.Local == nil {
			return described(c, data, err)
		}
		good := cached(st.Sum) // the version this computer last read whole
		// The damaged body must be that version cut short — a prefix of it,
		// which is what a relay that stops a long write leaves. But Seal
		// writes with json.MarshalIndent, so every version starts with the
		// same constant head up to the salt; a body cut inside that head is a
		// prefix of every version, including an out-of-date computer's older
		// cached copy. So the prefix check alone lets that computer rebuild
		// and drop a newer one's data. Only a body reaching past the random
		// fields — to the "data" key that follows them — is tied to the one
		// version it was cut from. Requiring it keeps an out-of-date computer
		// from rebuilding (its copy isn't the version that broke, so the body
		// isn't its prefix and the error stands), and a body cut before the
		// random fields stays an error for every computer, even the one that
		// saw the good version: better to fail loud than rebuild blind. A
		// sealed file holds a whole setup — hundreds of bytes at its very
		// smallest — and a relay cuts a real write deep into the data, well
		// past "data", so legit rebuilds still go.
		if good == nil || !bytes.HasPrefix(good, data) || len(data) <= bytes.Index(good, []byte(`"data"`)) {
			return described(c, data, err)
		}
		remote, err = backup.Open(good, c.Passphrase)
		if err != nil {
			return err
		}
		keepDamaged(data) // the unreadable body, kept aside before it is replaced
		data = good
		rebuilt = true // push the merged result even if it matches the cached copy
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
			if changed(p, c.Keys).After(remote.Created) {
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
	st.Local, st.Sum, st.Server, st.Remote = pending, sum(data), ver, R
	remember(data)
	if len(here)+len(there) > 0 {
		st.Notice = &Notice{At: time.Now(), Here: here, There: there, Saved: saved}
		st.Undo = "" // a restore's undo goes with its notice
	}
	// A rebuild always writes back: the server's file is the damaged one,
	// so even a merge that matches the cached copy must replace it.
	if rebuilt || !maps.Equal(M, R) {
		if err := push(merged, etag); err != nil {
			return err
		}
	}
	st.Local = now
	return nil
}
