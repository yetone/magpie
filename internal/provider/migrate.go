package provider

// Retiring a built-in subscription. Each subscription with a mover below
// has a community plugin that does what the built-in does
// (github.com/magpie-community/plugins). Moving one puts its accounts onto
// the plugin under the same id, so agents on zed/<model>, the model picks,
// routing and fallbacks carry on as they were:
//
//  1. the plugin is installed, if it isn't;
//  2. each account's sign-in is kept as the plugin keeps one, in the order
//     and on or off as the user had it;
//  3. each is tried as a request would try it (the plugin's loader and its
//     model list for that account), and every model the built-in has in use
//     must be one the plugin serves;
//  4. only then does the id change hands: the built-in's saved accounts go
//     out of logins.json into migrations.json, kept for going back.
//
// Anything short of that puts everything back as it was — the plugin's
// newer tokens included, since a refresh token used once is spent — and
// the built-in carries on. MoveBack undoes a move the same way.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/update"
)

// A migration's states.
const (
	MoveMoving = "moving" // under way, or cut short (put back at the next try)
	MovePlugin = "plugin" // the plugin has the id
	MovedBack  = "back"   // the user moved it back: it stays built-in
	MoveFailed = "failed" // the last try failed: the built-in carries on
)

// Retiring are the built-ins magpie moves onto their plugins by itself,
// at start-up; the rest move only when the user asks.
var Retiring = []string{}

// moveRetry is how long a failed move waits before it is tried again.
const moveRetry = 6 * time.Hour

// Migration is where one built-in's move stands.
type Migration struct {
	State   string    `json:"state"`
	Package string    `json:"package,omitempty"`
	At      time.Time `json:"at"`
	Err     string    `json:"error,omitempty"`
	// Accounts are the plugin's accounts the move made or took over.
	Accounts []movedAccount `json:"accounts,omitempty"`
	// Backup is the built-in's accounts as logins.json kept them.
	Backup []savedLogin `json:"backup,omitempty"`
	// Kept is what else of the built-in's the move set aside (Kiro's key).
	Kept json.RawMessage `json:"kept,omitempty"`
	// Installed is whether the move installed the plugin: going back takes
	// it away again when it serves nothing else.
	Installed bool `json:"installed,omitempty"`
	// Host is the built-in's API host, which the plugin's provider shows.
	Host string `json:"host,omitempty"`
	// Why is a failed move's reason, for the page to say in the user's
	// language.
	Why *MoveWhy `json:"why,omitempty"`
}

// MoveWhy is why a move didn't go through: Code names the case, Args fill
// in its sentence.
//
//	offline   magpie couldn't reach npm (or Bun's download) to install the plugin
//	install   npm couldn't install it: line
//	lapsed    every account of name needs signing in again
//	unserved  the plugin doesn't serve models (their names) for user (with
//	          its plan), though the built-in does
//	account   user doesn't work through the plugin: error
type MoveWhy struct {
	Code string            `json:"code"`
	Args map[string]string `json:"args,omitempty"`
}

// moveError is a failed move's error in words a user reads, with its why.
type moveError struct {
	why MoveWhy
	msg string
	err error // what lay under it, for the log
}

func (e *moveError) Error() string { return e.msg }
func (e *moveError) Unwrap() error { return e.err }

// WhyOf is the reason a move failed, nil for one it has no words for.
func WhyOf(err error) *MoveWhy {
	var me *moveError
	if errors.As(err, &me) {
		w := me.why
		return &w
	}
	return nil
}

// unreachable is an install that never got through to npm (or to Bun's
// download): Bun's words for it, and Go's.
var unreachable = regexp.MustCompile(`(?i)ConnectionRefused|ConnectionClosed|FailedToOpenSocket|ENOTFOUND|ETIMEDOUT|ECONNRESET|ECONNREFUSED|EAI_AGAIN|Unable to connect|network|no such host|dial tcp|i/o timeout|TLS handshake`)

// installFailed says why the plugin couldn't be installed.
func installFailed(pkg string, err error) error {
	if unreachable.MatchString(err.Error()) {
		return &moveError{MoveWhy{Code: "offline"}, "magpie couldn't reach npm to install the plugin. Check the network or proxy, then try again.", err}
	}
	line := strings.TrimSpace(err.Error())
	if ls := strings.Split(line, "\n"); len(ls) > 1 {
		line = strings.TrimSpace(ls[len(ls)-1])
	}
	return &moveError{MoveWhy{Code: "install", Args: map[string]string{"line": line}}, fmt.Sprintf("npm couldn't install %s: %s", pkg, line), err}
}

// nameOf is the built-in id's name, as the providers list shows it.
func nameOf(id string) string {
	for _, p := range All() {
		if p.ID == id && !p.IsPlugin() && p.Name != "" {
			return p.Name
		}
	}
	return id
}

type movedAccount struct {
	Key  string `json:"key"`
	User string `json:"user"`
	Own  bool   `json:"own,omitempty"`
	// Was is what the plugin kept at Key before, when the user had signed
	// in to the same account through the plugin already.
	Was map[string]any `json:"was,omitempty"`
	// Sent is (a hash of) the sign-in given to the plugin: a plugin still
	// holding it renewed nothing, so the built-in's own is as new.
	Sent string `json:"sent,omitempty"`
}

// signinHash tells two sign-ins apart without keeping either.
func signinHash(a map[string]any) string {
	h := sha256.Sum256([]byte(jsonText(a)))
	return hex.EncodeToString(h[:8])
}

// renewedSince are the built-in's accounts whose sign-in isn't the one
// sent: the built-in renewed them while the move went on (a request, a
// refresh) and, a refresh token being spent once used, the plugin's copy
// may be dead.
func renewedSince(mv *mover, moved []movedAccount) []string {
	now, err := mv.out()
	if err != nil {
		return nil
	}
	var out []string
	for _, ma := range moved {
		if ma.Own {
			continue
		}
		for _, a := range now {
			if strings.EqualFold(a.User, ma.User) && signinHash(a.Auth) != ma.Sent {
				out = append(out, ma.User)
			}
		}
	}
	return out
}

// Moving is one of a built-in's accounts on its way to the plugin.
type Moving struct {
	User      string
	First, On bool
	// Lapsed is an account the vendor already refused: it goes along, but
	// isn't tried.
	Lapsed bool
	// Plan is the plan the account showed, kept beside it until the
	// plugin tells it anew.
	Plan string
	// Own is the agent's own sign-in (its CLI's, its app's), which the
	// plugin reads where the agent keeps it, as the built-in did.
	Own  bool
	Auth map[string]any
}

// A mover carries one built-in's accounts to its plugin and back.
type mover struct {
	pkg string
	// min is the oldest version of pkg that runs the accounts as moved
	min string
	// agents are the logins.json agents its accounts are saved under.
	agents []string
	// out is the built-in's accounts as the plugin's sign-ins, the one in
	// use first.
	out func() ([]Moving, error)
	// back puts one of the plugin's sign-ins back into the built-in's
	// saved accounts: into the one it came from (user), else a new one.
	// It gives the accounts and the one it wrote to.
	back func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error)
	// take and give set aside what else of the built-in's the plugin now
	// has, and put it back.
	take func() (json.RawMessage, error)
	give func(json.RawMessage) error
	// settle finishes the sign-ins handed back once logins.json is let go:
	// what else of the built-in's they carry (a key onto its provider),
	// whose saving syncs the agents, which read the accounts.
	settle func(auths map[string]map[string]any) error
	// served is whether the plugin, listing listed, still serves model, one
	// of the built-in's picks it doesn't list; nil for none.
	served func(model string, listed []string) bool
	// builtin is whether the built-in served model on account a, by the
	// plan a is on; nil for every model on every account. One it didn't
	// (a ZCode Start Plan account's GLM-5.3) is no loss when the plugin
	// doesn't list it either.
	builtin func(ctx context.Context, a Moving, model string) bool
}

// movers are the deprecated built-ins and their plugins, by id. Once one
// is moved, its plugin serves it and the built-in's code no longer does:
// each built-in's files say so (PLUGIN-SERVED, AGENTS.md), and
// TestMovedBuiltinsSayTheirPlugin fails for a mover added without that.
var movers = map[string]*mover{}

// errStays is a back's answer for a sign-in the built-in has nowhere to
// keep (one made in the plugin's own way): it stays with the plugin, which
// lists it under its own id once the built-in has the id again.
var errStays = errors.New("stays with the plugin")

// Movable is whether the built-in id has a plugin it can move to.
func Movable(id string) bool { return movers[id] != nil }

// MovableIDs are the built-ins that have a plugin to move to, by id: the
// page marks them deprecated, moved or not.
func MovableIDs() []string {
	out := make([]string, 0, len(movers))
	for id := range movers {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// MoveCandidate is a built-in with accounts its plugin could run instead.
type MoveCandidate struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Package  string `json:"package"`
	Accounts int    `json:"accounts"`
}

// MoveCandidates are the built-ins not on their plugins that have accounts
// to move, by name.
func MoveCandidates() []MoveCandidate {
	out := []MoveCandidate{}
	for id, mv := range movers {
		if Moved(id) {
			continue
		}
		if accts, err := mv.out(); err == nil && len(accts) > 0 {
			out = append(out, MoveCandidate{ID: id, Name: nameOf(id), Package: mv.pkg, Accounts: len(accts)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MovePackage is the plugin the built-in id moves to.
func MovePackage(id string) string {
	if m := movers[id]; m != nil {
		return m.pkg
	}
	return ""
}

func migrationsPath() string { return filepath.Join(filepath.Dir(Path()), "migrations.json") }

var migrationsMu sync.Mutex

// readMigrations is held while a request reads: the GUI's state asks each
// provider's move several times over, thousands of looks at the file.
func readMigrations() map[string]Migration { return heldOf("migrations", readMigrationsFile) }

func readMigrationsFile() map[string]Migration {
	m, _ := filememo.Read("migrations", migrationsPath(), func(b []byte) (map[string]Migration, error) {
		var m map[string]Migration
		_ = json.Unmarshal(b, &m)
		return m, nil
	})
	return m
}

// MigrationOf is where the built-in id's move stands.
func MigrationOf(id string) (Migration, bool) {
	m, ok := readMigrations()[id]
	return m, ok
}

// Moved is whether the built-in id's accounts are its plugin's now.
func Moved(id string) bool {
	m, ok := MigrationOf(id)
	return ok && m.State == MovePlugin
}

// movedAgent is whether the logins.json agent's accounts are a moved
// built-in's, its plugin's now.
func movedAgent(agent string) bool {
	for id, mv := range movers {
		if slices.Contains(mv.agents, agent) && Moved(id) {
			return true
		}
	}
	return false
}

// OnPlugins are the built-ins moved onto their plugins.
func OnPlugins() []string {
	var out []string
	for id, m := range readMigrations() {
		if m.State == MovePlugin && movers[id] != nil {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func movingNow(id string) bool {
	m, ok := MigrationOf(id)
	return ok && m.State == MoveMoving
}

func setMigration(id string, f func(m *Migration)) error {
	migrationsMu.Lock()
	defer migrationsMu.Unlock()
	all := map[string]Migration{}
	for k, v := range readMigrationsFile() {
		all[k] = v
	}
	m := all[id]
	f(&m)
	all[id] = m
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	defer Changed()
	return writePrivate(migrationsPath(), append(b, '\n'))
}

// lockMoves keeps two magpies (the app and a CLI, a dev build beside a
// release) from moving at once.
func lockMoves() (func(), error) {
	p := migrationsPath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	for range 2 {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprint(f, os.Getpid())
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		// one left by a magpie that died moving (killed, or stopped
		// mid-move) holds nothing
		if b, rerr := os.ReadFile(p); rerr == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && pid != os.Getpid() && !update.Alive(pid) {
				os.Remove(p)
				continue
			}
		}
		if fi, serr := os.Stat(p); serr == nil && time.Since(fi.ModTime()) > 15*time.Minute {
			os.Remove(p)
			continue
		}
		return nil, errors.New("another magpie is moving subscriptions to plugins")
	}
	return nil, errors.New("another magpie is moving subscriptions to plugins")
}

// Hooks the tests stand in for.
var (
	installPlugin = func(ctx context.Context, pkg, min string) error {
		older := false
		for _, e := range plugin.Load().Plugins {
			if plugin.PackageName(e.Spec) != pkg {
				continue
			}
			if e.Off {
				return fmt.Errorf("%s is turned off in Plugins", pkg)
			}
			v := plugin.Version(e.Spec)
			if min == "" || !update.Newer(min, v) {
				return nil
			}
			if plugin.IsPath(e.Spec) {
				return fmt.Errorf("%s at %s is %s; moving needs %s or newer", pkg, e.Spec, v, min)
			}
			older = !plugin.IsGit(e.Spec)
			break
		}
		var err error
		if older {
			// npm's newest, which bun says why it won't install (a
			// minimumReleaseAge), where bun's own latest is an older
			// version without a word (sweanng424 on Discord)
			err = plugin.Upgrade(ctx, pkg)
		} else {
			_, err = plugin.Add(ctx, pkg)
		}
		if err == nil && min != "" {
			if v := plugin.Version(pkg); update.Newer(min, v) {
				return fmt.Errorf("%s %s is installed; moving needs %s or newer", pkg, v, min)
			}
		}
		return err
	}
	pluginProviders = plugin.Providers
	removePlugin    = plugin.Remove
)

// modelsInUse are the models the built-in id serves now: the ones agents,
// picks and routing can name.
var modelsInUse = func(id string) []string {
	for _, p := range All() {
		if p.ID == id && !p.IsPlugin() {
			var out []string
			for _, m := range p.Exposed() {
				out = append(out, m.ID)
			}
			return out
		}
	}
	return nil
}

// Move puts the built-in id's accounts onto its plugin (see above). An
// error leaves the built-in as it was.
func Move(ctx context.Context, id string) error {
	mv := movers[id]
	if mv == nil {
		return fmt.Errorf("%s has no plugin to move to", id)
	}
	unlock, err := lockMoves()
	if err != nil {
		return err
	}
	defer unlock()
	if Moved(id) {
		return nil
	}
	if movingNow(id) {
		putBack(ctx, id, mv, "")
	}
	err = move(ctx, id, mv)
	if err != nil {
		_ = setMigration(id, func(m *Migration) {
			*m = Migration{State: MoveFailed, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second), Err: err.Error(), Why: WhyOf(err)}
		})
		if me := (*moveError)(nil); errors.As(err, &me) && me.err != nil {
			log.Printf("moving %s to its plugin: %s (%s)", id, me.msg, me.err)
		}
	}
	return err
}

// Adopt puts the built-in id onto its plugin before any account of it is
// signed in: the plugin installed, and the subscription signs in through
// it from now on, as a moved one does. One with accounts is moved instead.
func Adopt(ctx context.Context, id string) error {
	mv := movers[id]
	if mv == nil {
		return fmt.Errorf("%s has no plugin to move to", id)
	}
	if accts, err := mv.out(); err != nil {
		return err
	} else if len(accts) > 0 {
		return Move(ctx, id)
	}
	unlock, err := lockMoves()
	if err != nil {
		return err
	}
	defer unlock()
	if Moved(id) {
		return nil
	}
	had := pluginListed(mv.pkg)
	if err := installPlugin(ctx, mv.pkg, mv.min); err != nil {
		return installFailed(mv.pkg, err)
	}
	pps, err := pluginProviders(ctx)
	if err == nil && !slices.ContainsFunc(pps, func(p plugin.Provider) bool { return p.ID == id && plugin.PackageName(p.Spec) == mv.pkg }) {
		err = fmt.Errorf("%s doesn't serve %s", mv.pkg, id)
	}
	if err == nil {
		err = setMigration(id, func(m *Migration) {
			*m = Migration{State: MovePlugin, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second), Installed: !had}
		})
	}
	if err != nil && !had {
		if rerr := removePlugin(context.WithoutCancel(ctx), mv.pkg); rerr != nil {
			log.Printf("removing %s after %s failed to go onto it: %s", mv.pkg, id, rerr)
		}
	}
	return err
}

func move(ctx context.Context, id string, mv *mover) (err error) {
	inUse := modelsInUse(id)
	var host string
	if p, err := Find(id); err == nil && !p.IsPlugin() {
		host = p.Host()
	}
	accts, err := mv.out()
	if err != nil {
		return err
	}
	if len(accts) == 0 {
		return fmt.Errorf("no %s account to move", id)
	}
	// every account lapsed: nothing to try the plugin with, so nothing is
	// installed for it
	if !slices.ContainsFunc(accts, func(a Moving) bool { return !a.Lapsed }) {
		return lapsedAll(id)
	}
	had := pluginListed(mv.pkg)
	if err := installPlugin(ctx, mv.pkg, mv.min); err != nil {
		return installFailed(mv.pkg, err)
	}
	installed := !had
	// a plugin the move installed goes with a move that failed, which
	// leaves nothing of it behind: no provider saying it's signed out
	defer func() {
		if err != nil && installed {
			if rerr := removePlugin(context.WithoutCancel(ctx), mv.pkg); rerr != nil {
				log.Printf("removing %s after a failed move: %s", mv.pkg, rerr)
			}
		}
	}()
	pps, err := pluginProviders(ctx)
	if err != nil {
		return err
	}
	var pp plugin.Provider
	for _, p := range pps {
		if p.ID == id {
			if plugin.PackageName(p.Spec) != mv.pkg {
				return fmt.Errorf("%s is served by another plugin, %s", id, p.Spec)
			}
			pp = p
		}
	}
	if pp.ID == "" {
		return fmt.Errorf("%s doesn't serve %s", mv.pkg, id)
	}
	before := plugin.Auths(id)
	if err := setMigration(id, func(m *Migration) {
		*m = Migration{State: MoveMoving, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second)}
	}); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			putBack(ctx, id, mv, err.Error())
		}
	}()
	var moved []movedAccount
	for _, a := range accts {
		key, err := plugin.Import(ctx, id, a.Auth)
		if err != nil {
			return fmt.Errorf("%s: %w", a.User, err)
		}
		ma := movedAccount{Key: key, User: a.User, Own: a.Own, Was: before[key], Sent: signinHash(a.Auth)}
		moved = append(moved, ma)
		if err := setMigration(id, func(m *Migration) { m.Accounts = moved }); err != nil {
			return err
		}
	}
	if err := keepOrder(pp, accts, moved); err != nil {
		return err
	}
	tried := false
	for i, a := range accts {
		if a.Lapsed {
			continue
		}
		// through the account's proxy, as its requests go
		c, err := plugin.Check(ViaLogin(ctx, id, a.User), id, moved[i].Key)
		if err == nil && (c.Refused != "" || refused(c.Usage)) {
			// the vendor turned the sign-in away: marked, as the built-in
			// marked it, and along untried, as a lapsed one goes
			notePluginLapse(pp, moved[i].Key, http.StatusUnauthorized)
			continue
		}
		if err != nil && signInGone.MatchString(err.Error()) {
			// the plugin says in words the sign-in is gone (Grok's past
			// its time): it moves along untried, as a lapsed one does, and
			// unmarked, as the plugin's own reads leave its accounts
			continue
		}
		if err != nil {
			return &moveError{MoveWhy{Code: "account", Args: map[string]string{"user": a.User, "error": err.Error()}}, fmt.Sprintf("%s doesn't work through the plugin: %s", a.User, err), err}
		}
		if !tried {
			if missing := slices.DeleteFunc(slices.Clone(inUse), func(m string) bool {
				return slices.Contains(c.Models, m) || mv.served != nil && mv.served(m, c.Models) ||
					mv.builtin != nil && !mv.builtin(ctx, a, m)
			}); len(missing) > 0 {
				names := strings.Join(modelNames(id, missing), ", ")
				who := a.User
				if a.Plan != "" {
					who += " (" + a.Plan + ")"
				}
				return &moveError{MoveWhy{Code: "unserved", Args: map[string]string{"models": names, "user": who}}, fmt.Sprintf("the plugin doesn't serve %s for %s, though the built-in does. Untick them under Models, or keep the built-in.", names, who), nil}
			}
			tried = true
		}
	}
	if !tried {
		return lapsedAll(id)
	}
	return commitMove(id, mv, moved, installed, host)
}

func lapsedAll(id string) error {
	name := nameOf(id)
	return &moveError{MoveWhy{Code: "lapsed", Args: map[string]string{"name": name}}, fmt.Sprintf("every %s account needs signing in again. Sign one in, then move.", name), nil}
}

// refused is a check's usage read saying the vendor turned the account's
// sign-in away, read as the usage page reads it: a models hook may fall
// back to a list it keeps, but the read asks the vendor of the account.
func refused(u *plugin.Usage) bool {
	return u != nil && (u.SignIn == "expired" || u.SignIn == "" && signInGone.MatchString(u.Error))
}

// modelNames are the ids' names, as the built-in id lists them.
func modelNames(id string, ids []string) []string {
	names := map[string]string{}
	for _, p := range All() {
		if p.ID == id && !p.IsPlugin() {
			for _, m := range p.Available() {
				names[m.ID] = m.Name
			}
		}
	}
	var out []string
	for _, m := range ids {
		if names[m] != "" {
			m = names[m]
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// pluginListed is whether the package is in the plugin list already.
func pluginListed(pkg string) bool {
	return slices.ContainsFunc(plugin.Load().Plugins, func(e plugin.Entry) bool { return plugin.PackageName(e.Spec) == pkg })
}

// keepOrder lists the accounts moved as the built-in had them: the one in
// use first, the rest on or off. What the user had signed in to through
// the plugin already keeps its place.
func keepOrder(pp plugin.Provider, accts []Moving, moved []movedAccount) error {
	agent := pluginAgent(pp)
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	first := ""
	for i, a := range accts {
		if a.First && moved[i].Was == nil {
			first = moved[i].Key
		}
	}
	for i, a := range accts {
		ma := moved[i]
		if ma.Was != nil {
			continue
		}
		ls = slices.DeleteFunc(ls, func(l savedLogin) bool { return l.Agent == agent && l.Home == ma.Key })
		l := savedLogin{Agent: agent, User: a.User, Plan: a.Plan, Home: ma.Key, On: a.On || a.First, First: ma.Key == first, Seen: time.Now().UTC().Truncate(time.Second)}
		if a.Lapsed {
			l.Lapsed = lapsedText(pp, a.User)
		}
		ls = append(ls, l)
	}
	if first != "" {
		for i := range ls {
			if ls[i].Agent == agent && ls[i].Home != first {
				ls[i].First = false
			}
		}
	}
	return writeLogins(ls)
}

// commitMove hands the id over: the built-in's accounts go aside.
func commitMove(id string, mv *mover, moved []movedAccount, installed bool, host string) error {
	if r := renewedSince(mv, moved); len(r) > 0 {
		return fmt.Errorf("%s renewed while moving; tried again later", strings.Join(r, ", "))
	}
	var kept json.RawMessage
	if mv.take != nil {
		k, err := mv.take()
		if err != nil {
			return err
		}
		kept = k
	}
	loginsMu.Lock()
	var backup []savedLogin
	for _, l := range readLogins() {
		if slices.Contains(mv.agents, l.Agent) {
			backup = append(backup, l)
		}
	}
	loginsMu.Unlock()
	if err := setMigration(id, func(m *Migration) {
		*m = Migration{State: MovePlugin, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second), Accounts: moved, Backup: backup, Kept: kept, Installed: installed, Host: host}
	}); err != nil {
		if mv.give != nil {
			_ = mv.give(kept)
		}
		return err
	}
	tidyMoved(id)
	return nil
}

// tidyMoved takes a moved built-in's accounts out of logins.json (into its
// backup, if one was left behind when a magpie stopped between the two).
func tidyMoved(id string) {
	mv := movers[id]
	if mv == nil || !Moved(id) {
		return
	}
	loginsMu.Lock()
	ls := readLogins()
	var left []savedLogin
	ls = slices.DeleteFunc(ls, func(l savedLogin) bool {
		if slices.Contains(mv.agents, l.Agent) {
			left = append(left, l)
			return true
		}
		return false
	})
	if len(left) > 0 {
		_ = writeLogins(ls)
	}
	loginsMu.Unlock()
	if len(left) == 0 {
		return
	}
	_ = setMigration(id, func(m *Migration) {
		for _, l := range left {
			if !slices.ContainsFunc(m.Backup, func(b savedLogin) bool { return sameMoved(b, l) }) {
				m.Backup = append(m.Backup, l)
			}
		}
	})
}

func sameMoved(a, b savedLogin) bool {
	return a.Agent == b.Agent && strings.EqualFold(a.User, b.User) && a.Home == b.Home
}

// putBack undoes a move cut short: the plugin's sign-ins go back into the
// built-in's accounts (its newer tokens with them), then out of the
// plugin — but for one the user had there already, which gets back what
// it had.
func putBack(ctx context.Context, id string, mv *mover, why string) {
	m, _ := MigrationOf(id)
	var keys []string
	for _, ma := range m.Accounts {
		keys = append(keys, ma.Key)
	}
	renewed := renewedSince(mv, m.Accounts)
	skip := func(ma movedAccount, a map[string]any) bool {
		return signinHash(a) == ma.Sent || slices.ContainsFunc(renewed, func(u string) bool { return strings.EqualFold(u, ma.User) })
	}
	_, _, _ = handBack(ctx, id, mv, m.Accounts, keys, skip, nil, func(ls []savedLogin, _ map[string]string) []savedLogin { return ls })
	for _, ma := range m.Accounts {
		if ma.Was != nil {
			_ = plugin.Restore(ctx, id, ma.Key, ma.Was)
		}
	}
	// the plugin's rows keepOrder wrote go with its accounts, but for one
	// the user had there before
	loginsMu.Lock()
	ls := readLogins()
	if n := len(ls); n > 0 {
		ls = slices.DeleteFunc(ls, func(l savedLogin) bool {
			return l.Agent == "plugin:"+id && slices.ContainsFunc(m.Accounts, func(ma movedAccount) bool { return ma.Key == l.Home && ma.Was == nil })
		})
		if len(ls) != n {
			_ = writeLogins(ls)
		}
	}
	loginsMu.Unlock()
	_ = setMigration(id, func(m *Migration) {
		*m = Migration{State: MoveFailed, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second), Err: why}
	})
}

// handBack writes the plugin's accounts keys back into the built-in's
// saved accounts, then takes them out of the plugin. They are written
// before they are taken, so a magpie stopped between the two loses no
// sign-in; one the plugin renewed in between is written again. arrange
// finishes the accounts written (given each key's user). An account that
// can't go back stops it all before anything is written.
func handBack(ctx context.Context, id string, mv *mover, accts []movedAccount, keys []string, skip func(movedAccount, map[string]any) bool, prep, arrange func([]savedLogin, map[string]string) []savedLogin) (map[string]string, []error, error) {
	var stays []string
	byKey := map[string]movedAccount{}
	for _, ma := range accts {
		byKey[ma.Key] = ma
	}
	write := func(auths map[string]map[string]any) (map[string]string, []error, error) {
		loginsMu.Lock()
		defer loginsMu.Unlock()
		ls := readLogins()
		if prep != nil {
			ls = prep(ls, nil)
		}
		users := map[string]string{}
		var errs []error
		stays = nil
		for _, k := range keys {
			ma := byKey[k]
			if ma.Own {
				users[k] = ma.User
				continue
			}
			a := auths[k]
			if a == nil {
				continue
			}
			if skip != nil && skip(ma, a) {
				users[k] = ma.User
				continue
			}
			next, u, err := mv.back(ls, ma.User, a)
			if errors.Is(err, errStays) {
				stays = append(stays, k)
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			ls, users[k] = next, u
		}
		if len(errs) > 0 {
			return users, errs, nil
		}
		return users, nil, writeLogins(arrange(ls, users))
	}
	settle := func(auths map[string]map[string]any) error {
		if mv.settle == nil {
			return nil
		}
		mine := map[string]map[string]any{}
		for _, k := range keys {
			if a := auths[k]; a != nil && !byKey[k].Own {
				mine[k] = a
			}
		}
		return mv.settle(mine)
	}
	seen := plugin.Auths(id)
	users, errs, err := write(seen)
	if err == nil && len(errs) == 0 {
		err = settle(seen)
	}
	if err != nil || len(errs) > 0 {
		return users, errs, err
	}
	keys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return slices.Contains(stays, k) })
	if len(keys) == 0 {
		return users, nil, nil
	}
	taken, err := plugin.Take(ctx, id, keys)
	if err != nil {
		return users, nil, err
	}
	for _, k := range keys {
		if t := taken[k]; t != nil && jsonText(t) != jsonText(seen[k]) {
			users, errs, err := write(taken)
			if err == nil && len(errs) == 0 {
				err = settle(taken)
			}
			return users, errs, err
		}
	}
	return users, nil, nil
}

// MoveBack gives the id back to the built-in: its accounts as they were
// set aside, with what the plugin has of them now (newer tokens, accounts
// signed in to since), in the plugin's order; then the plugin signs out.
func MoveBack(ctx context.Context, id string) error {
	return moveBack(ctx, id, true)
}

// moveBack is MoveBack; tidy takes away a plugin the move installed that
// serves nothing else now, which releasePlugin, about to remove or turn
// off the plugin itself, leaves to its caller.
func moveBack(ctx context.Context, id string, tidy bool) error {
	mv := movers[id]
	if mv == nil {
		return fmt.Errorf("%s isn't a built-in subscription", id)
	}
	unlock, err := lockMoves()
	if err != nil {
		return err
	}
	defer unlock()
	m, ok := MigrationOf(id)
	if !ok || m.State != MovePlugin {
		return nil
	}
	var order []plugin.Account
	if pp, ok := pluginOfAgent("plugin:" + id); ok {
		for _, l := range pluginLogins(pp) {
			order = append(order, l.acct)
		}
	}
	keys := slices.Collect(maps.Keys(plugin.Auths(id)))
	sort.SliceStable(keys, func(i, j int) bool {
		if a, b := indexOfKey(order, keys[i]), indexOfKey(order, keys[j]); a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	onOf := pluginOn(id)
	// the backup first, for back to write into the accounts it came from
	prep := func(ls []savedLogin, _ map[string]string) []savedLogin {
		for _, b := range m.Backup {
			// the agent's own sign-in is one row: one written while moved
			// (the agent signed in to another account meanwhile) gives
			// way to the one set aside, which takes the agent's user anew
			if b.own() {
				ls = slices.DeleteFunc(ls, func(l savedLogin) bool { return l.Agent == b.Agent && l.own() && !sameMoved(l, b) })
			}
			if !slices.ContainsFunc(ls, func(l savedLogin) bool { return sameMoved(l, b) }) {
				ls = append(ls, b)
			}
		}
		return ls
	}
	arrange := func(ls []savedLogin, users map[string]string) []savedLogin {
		// an account the vendor refused through the plugin goes back
		// marked, as the built-in had marked it: back writes the plugin's
		// sign-in as one just made, but the plugin's mark is the newer word
		lapsed := map[string]string{}
		for _, l := range ls {
			if u := users[l.Home]; l.Agent == "plugin:"+id && u != "" && l.Lapsed != "" {
				lapsed[strings.ToLower(u)] = l.Lapsed
			}
		}
		// the plugin's rows of the accounts going back go with them, or
		// the accounts page lists each twice, the plugin's copy empty
		ls = slices.DeleteFunc(ls, func(l savedLogin) bool {
			_, back := users[l.Home]
			return l.Agent == "plugin:"+id && (back || len(keys) == 0)
		})
		firstUser := ""
		for i, k := range keys {
			user := users[k]
			if user == "" {
				continue
			}
			if firstUser == "" && i == 0 {
				firstUser = user
			}
			for j := range ls {
				if slices.Contains(mv.agents, ls[j].Agent) && strings.EqualFold(ls[j].User, user) {
					if on, ok := onOf[k]; ok {
						ls[j].On = on
					}
				}
			}
		}
		for j := range ls {
			if t := lapsed[strings.ToLower(ls[j].User)]; t != "" && slices.Contains(mv.agents, ls[j].Agent) && !ls[j].own() {
				ls[j].Lapsed = t
			}
		}
		if firstUser != "" {
			for j := range ls {
				if slices.Contains(mv.agents, ls[j].Agent) {
					ls[j].First = strings.EqualFold(ls[j].User, firstUser) && !ls[j].own()
				}
			}
		}
		return ls
	}
	// the backup goes back even with no account left in the plugin
	if len(keys) == 0 {
		loginsMu.Lock()
		err := writeLogins(arrange(prep(readLogins(), nil), nil))
		loginsMu.Unlock()
		if err != nil {
			return err
		}
	} else {
		_, errs, err := handBack(ctx, id, mv, m.Accounts, keys, nil, prep, arrange)
		if err != nil {
			tidyMoved(id)
			return err
		}
		if len(errs) > 0 {
			// the plugin keeps the id and every account
			return errors.Join(errs...)
		}
	}
	if mv.give != nil && len(m.Kept) > 0 {
		if err := mv.give(m.Kept); err != nil {
			return err
		}
	}
	if err := setMigration(id, func(m *Migration) {
		*m = Migration{State: MovedBack, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second)}
	}); err != nil {
		return err
	}
	if tidy && m.Installed && !servesAnything(mv.pkg) {
		if err := removePlugin(context.WithoutCancel(ctx), mv.pkg); err != nil {
			log.Printf("removing %s, installed by %s's move: %s", mv.pkg, id, err)
		}
	}
	return nil
}

// servesAnything is whether the plugin package has an account signed in
// to any of its providers, or a built-in moved onto it.
func servesAnything(pkg string) bool {
	for _, p := range plugin.Cached() {
		if plugin.PackageName(p.Spec) == pkg && len(plugin.Auths(p.ID)) > 0 {
			return true
		}
	}
	for _, id := range OnPlugins() {
		if m, _ := MigrationOf(id); m.Package == pkg {
			return true
		}
	}
	return false
}

func indexOfKey(order []plugin.Account, key string) int {
	if i := slices.IndexFunc(order, func(a plugin.Account) bool { return a.Key == key }); i >= 0 {
		return i
	}
	return len(order)
}

// pluginOn is which of the plugin provider id's accounts are on.
func pluginOn(id string) map[string]bool {
	out := map[string]bool{}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "plugin:"+id {
			out[l.Home] = l.On || l.First
		}
	}
	return out
}

// MoveRetiring moves the built-ins being retired that have accounts, at
// start-up: once, or again moveRetry after one failed. One the user moved
// back stays.
func MoveRetiring(ctx context.Context) map[string]error {
	out := map[string]error{}
	for _, id := range Retiring {
		m, ok := MigrationOf(id)
		switch {
		case ok && m.State == MovePlugin:
			tidyMoved(id)
			continue
		case ok && m.State == MovedBack:
			continue
		case ok && m.State == MoveFailed && time.Since(m.At) < moveRetry:
			continue
		}
		if accts, err := movers[id].out(); err != nil || len(accts) == 0 {
			continue
		}
		out[id] = Move(ctx, id)
	}
	return out
}

// HandOver gives each deprecated built-in whose plugin the user has
// installed, at the version its move needs, and which that plugin serves,
// its id, so one subscription is never listed twice: the built-in, and the
// plugin's under "<id>-plugin" (ARNO on Discord: Qoder CN twice once the
// Qoder plugin's 0.2.0 served it, as nothing gave it the id). One with no
// accounts is the plugin's at once, as Adopt makes it; with accounts too,
// one that has some is moved (Move: each tried through the plugin, all put
// back on any failure, the built-in carrying on). One the plugin has
// accounts of its own for already is left as it is, since its models are
// in use as <id>-plugin's and the id changing would leave agents asking
// for a provider gone; the add sheet lists it once regardless. One the
// user moved back stays built-in, and a failed move waits moveRetry. No
// plugin is installed for it: the user's own install is what says to.
func HandOver(ctx context.Context, accounts bool) map[string]error {
	out := map[string]error{}
	var pps []plugin.Provider
	asked := false
	for _, id := range MovableIDs() {
		mv := movers[id]
		if m, ok := MigrationOf(id); ok && (m.State != MoveFailed || time.Since(m.At) < moveRetry) {
			continue
		}
		if !pluginReady(mv) {
			continue
		}
		if !asked {
			var err error
			if pps, err = pluginProviders(ctx); err != nil {
				return out
			}
			asked = true
		}
		i := slices.IndexFunc(pps, func(p plugin.Provider) bool { return p.ID == id && plugin.PackageName(p.Spec) == mv.pkg })
		if i < 0 || pps[i].SignedIn || len(pps[i].Accounts) > 0 {
			continue
		}
		accts, err := mv.out()
		switch {
		case err != nil:
		case len(accts) == 0:
			out[id] = Adopt(ctx, id)
		case accounts:
			out[id] = Move(ctx, id)
		}
	}
	return out
}

// pluginReady is whether the mover's plugin is installed, switched on and
// at the version its move needs.
func pluginReady(mv *mover) bool {
	for _, e := range plugin.Load().Plugins {
		if plugin.PackageName(e.Spec) == mv.pkg {
			return !e.Off && (mv.min == "" || !update.Newer(mv.min, plugin.Version(e.Spec)))
		}
	}
	return false
}

// keepMovedCurrent updates the plugin of each built-in moved onto one to
// the version its move needs, when older: a built-in came up to date with
// magpie, and its plugin does too. One turned off or run from a folder is
// left as it is.
func keepMovedCurrent(ctx context.Context) {
	for _, id := range OnPlugins() {
		mv := movers[id]
		if mv.min == "" {
			continue
		}
		if err := installPlugin(ctx, mv.pkg, mv.min); err != nil {
			log.Printf("updating %s's plugin: %s", id, err)
		}
	}
}

// KeepRetiringMoved moves the built-ins being retired, and hands over
// those whose plugin the user installed (HandOver), run by the magpie
// serving the gateway (one magpie, never two at once): a little after it
// starts, then every hour, so a failed move is tried again once moveRetry
// has gone. It also keeps each moved built-in's plugin up to date.
func KeepRetiringMoved(ctx context.Context) {
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		keepMovedCurrent(ctx)
		moves := MoveRetiring(ctx)
		maps.Copy(moves, HandOver(ctx, true))
		for id, err := range moves {
			if err != nil {
				log.Printf("moving %s to its plugin: %s (it stays built-in)", id, err)
			} else {
				log.Printf("moved %s to its plugin, %s", id, MovePackage(id))
			}
		}
		t.Reset(time.Hour)
	}
}

// MovedOnto are the built-ins moved onto the plugin named (its spec or
// package name).
func MovedOnto(name string) []string {
	var out []string
	for _, e := range plugin.Load().Plugins {
		if plugin.Name(e.Spec) != name && e.Spec != name {
			continue
		}
		pkg := plugin.PackageName(e.Spec)
		for _, id := range OnPlugins() {
			if m, _ := MigrationOf(id); m.Package == pkg && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// releasePlugin moves the built-ins on the plugin named back first, so
// taking the plugin away leaves none of them without its accounts.
func releasePlugin(ctx context.Context, name string) error {
	for _, id := range MovedOnto(name) {
		if err := moveBack(ctx, id, false); err != nil {
			return fmt.Errorf("moving %s back to the built-in: %w", id, err)
		}
	}
	return nil
}

// RemovePlugin removes the plugin named, the built-ins moved onto it moved
// back to themselves first.
func RemovePlugin(ctx context.Context, name string) error {
	if err := releasePlugin(ctx, name); err != nil {
		return err
	}
	return plugin.Remove(ctx, name)
}

// SetPluginOff turns the plugin named off, the built-ins moved onto it
// moved back to themselves first, or back on.
func SetPluginOff(ctx context.Context, name string, off bool) error {
	if off {
		if err := releasePlugin(ctx, name); err != nil {
			return err
		}
	}
	return plugin.SetOff(name, off)
}
