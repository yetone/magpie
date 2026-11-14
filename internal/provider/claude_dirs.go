package provider

// A saved Claude account in use beside the one Claude Code is signed in to
// runs Claude Code in a config directory of its own (CLAUDE_CONFIG_DIR): its
// sign-in is put there once, from logins.json, and from then on Claude Code
// keeps it, refreshing it as it does its own. magpie never refreshes a
// Claude sign-in nor asks Anthropic anything with one; it only reads back
// what Claude Code left, so logins.json follows.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
	"golang.org/x/text/unicode/norm"
)

// claudeDirsRoot holds the saved Claude accounts' config directories, and
// the sign-ins being made.
func claudeDirsRoot() string { return filepath.Join(filepath.Dir(Path()), "claude-accounts") }

// claudeAccountDir is the config directory a saved Claude account runs
// Claude Code in.
func claudeAccountDir(user string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(user))))
	return filepath.Join(claudeDirsRoot(), hex.EncodeToString(sum[:])[:16])
}

// claudeDirService is the keychain item Claude Code keeps its sign-in in
// when CLAUDE_CONFIG_DIR is dir: its own name, told apart by the directory.
func claudeDirService(dir string) string {
	sum := sha256.Sum256([]byte(norm.NFC.String(dir)))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// readClaudeDir is the sign-in Claude Code keeps in config directory dir,
// where it looks first: the keychain on a Mac, then the file.
func readClaudeDir(dir string) (claudeCredentials, bool) {
	if settings.AccountDiscoveryOff() {
		return claudeCredentials{}, false
	}
	if claudeKeychain {
		out, err := proc.Command("security", "find-generic-password", "-s", claudeDirService(dir), "-a", claudeKeychainAccount(), "-w").Output()
		if err == nil {
			b, _ := keychainText(bytes.TrimSpace(out))
			if c, ok := parseClaudeCredentials(b); c.raw != nil {
				// the item there is the sign-in, emptied or not: Claude
				// Code reads no file beside it
				return c, ok
			}
		}
	}
	b, err := readAccountFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return claudeCredentials{}, false
	}
	return parseClaudeCredentials(b)
}

// clearClaudeDir takes the sign-in out of config directory dir, so the one
// it was moved to is its only holder.
func clearClaudeDir(dir string) {
	if discoveryOff() {
		return
	}
	if claudeKeychain {
		_ = proc.Command("security", "delete-generic-password", "-s", claudeDirService(dir), "-a", claudeKeychainAccount()).Run()
	}
	_ = os.Remove(filepath.Join(dir, ".credentials.json"))
}

// forgetClaudeDir removes a saved account's config directory altogether.
// ClaudeHandedOver is told of a saved Claude account switched in to Claude
// Code itself, whose directory forgetClaudeDir has just removed: a Claude
// Code still running there (the gateway's, waiting for its conversation's
// next turn) holds the refresh token that is Claude Code's own now, and a
// refresh of it there would leave Claude Code's copy refused, the account
// signed out (0xAncientTwo on X). The gateway sets it.
var ClaudeHandedOver func(user string)

func claudeHandedOver(user string) {
	if f := ClaudeHandedOver; f != nil {
		f(user)
	}
}

func forgetClaudeDir(user string) {
	dir := claudeAccountDir(user)
	clearClaudeDir(dir)
	_ = os.RemoveAll(dir)
}

// claudeDirRuns are the runs magpie waits on in a saved Claude account's
// config directory (inSavedClaudeDir), by account: its /usage, a warm-up.
// Claude Code may refresh the sign-in it keeps there as it runs, spending
// the refresh token, so the account isn't handed over to Claude Code
// itself (switchSavedLogin) until they end: Claude Code's own copy would
// be the spent one. Two Claude Codes in one directory don't both refresh
// it: Claude Code takes a lock in its config directory to refresh, and
// reads the sign-in again once it has it.
var claudeDirRuns struct {
	sync.Mutex
	m map[string]*sync.RWMutex
}

func claudeDirRun(user string) *sync.RWMutex {
	key := strings.ToLower(strings.TrimSpace(user))
	claudeDirRuns.Lock()
	defer claudeDirRuns.Unlock()
	if claudeDirRuns.m == nil {
		claudeDirRuns.m = map[string]*sync.RWMutex{}
	}
	mu := claudeDirRuns.m[key]
	if mu == nil {
		mu = &sync.RWMutex{}
		claudeDirRuns.m[key] = mu
	}
	return mu
}

// claudeDirRun is this magpie's own runs only: a switch from another
// magpie process (`magpie accounts switch` while the app reads a card)
// didn't wait for them, and handed Claude Code the refresh token a run of
// the app's could still spend (#4532). So each run also holds a lock file
// of its own beside the account's directory, for as long as it runs, and
// a switch waits on every one of them (waitClaudeRuns). A file whose
// holder ended without removing it is free, and taken out.

// claudeRunWait is how long a switch waits for runs in the account's
// directory: a /usage or a warm-up takes seconds.
var claudeRunWait = 90 * time.Second

var claudeRunSeq atomic.Int64

// claudeRunMarks matches the lock files of the runs in user's directory.
func claudeRunMarks(user string) string { return claudeAccountDir(user) + ".run-*.lock" }

// markClaudeRun holds a lock file of this run's own, released as it ends.
func markClaudeRun(user string) (release func()) {
	path := fmt.Sprintf("%s.run-%d-%d.lock", claudeAccountDir(user), os.Getpid(), claudeRunSeq.Add(1))
	unlock, held := filelock.Lock(path, 0)
	if !held {
		_ = os.Remove(path)
		return func() {}
	}
	return func() {
		unlock()
		_ = os.Remove(path)
	}
}

// errClaudeRunning is a run in the account's directory past claudeRunWait.
var errClaudeRunning = errors.New("Claude Code is still running on this account for magpie (its usage or a warm-up): try again in a moment")

// waitClaudeRuns waits until no process runs Claude Code in user's
// directory for magpie.
func waitClaudeRuns(user string) error {
	marks, _ := filepath.Glob(claudeRunMarks(user))
	deadline := time.Now().Add(claudeRunWait)
	for _, m := range marks {
		start := time.Now()
		wait := max(time.Until(deadline), 0)
		unlock, held := filelock.Lock(m, wait)
		if !held {
			if time.Since(start) >= wait {
				return errClaudeRunning
			}
			continue // a file system without locks: nothing to wait on
		}
		unlock()
		_ = os.Remove(m) // its run has ended
	}
	return nil
}

// inSavedClaudeDir runs f with the config directory of the saved Claude
// account user (claudeSavedDir), and keeps what Claude Code made of the
// sign-in there in logins.json once it is done: a refreshed one is the
// account's now, the one it replaced spent.
func inSavedClaudeDir(user string, f func(dir string) error) error {
	mu := claudeDirRun(user)
	mu.RLock()
	defer mu.RUnlock()
	defer markClaudeRun(user)()
	dir, err := claudeSavedDir(user)
	if err != nil {
		return err
	}
	defer syncSavedClaude(user)
	return f(dir)
}

// syncSavedClaude brings the saved Claude account user in logins.json up
// to what Claude Code keeps in its config directory, making nothing there.
func syncSavedClaude(user string) {
	savedTokenMu.Lock()
	defer savedTokenMu.Unlock()
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == "claude" && strings.EqualFold(ls[i].User, user) {
			if claudeSetupToken(ls[i].Auth) != "" {
				return // Claude Code keeps no sign-in of its own there
			}
			if _, changed, err := syncClaudeDir(&ls[i]); err == nil && changed {
				_ = writeLogins(ls)
			}
			return
		}
	}
}

// readSavedClaudeUsage is the allowance of the saved Claude account user:
// Claude Code's /usage run in the account's own config directory, through
// the account's own proxy. Claude Code's own sign-in isn't touched.
func readSavedClaudeUsage(ctx context.Context, user string) ([]QuotaWindow, error) {
	ws := []QuotaWindow{}
	if claudeTokenAccount(user) {
		// its /usage shows the API cost view, which would read as no
		// allowance at all: unknown, not run
		return ws, errClaudeSetupToken
	}
	err := inSavedClaudeDir(user, func(dir string) error {
		var err error
		ws, err = readClaudeUsage(ViaLogin(ctx, "claude", user), dir)
		return err
	})
	return ws, err
}

// claudeSavedDir is the config directory to run Claude Code in for the
// saved account user: its sign-in is put there from logins.json when there
// is none yet, and what Claude Code has made of it since is read back. An
// account that can't be used (claudeSignedOut) gets none.
func claudeSavedDir(user string) (string, error) {
	savedTokenMu.Lock()
	defer savedTokenMu.Unlock()
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	i := -1
	for j := range ls {
		if ls[j].Agent == "claude" && strings.EqualFold(ls[j].User, user) {
			i = j
		}
	}
	if i < 0 {
		return "", fmt.Errorf("no saved claude account %q", user)
	}
	if claudeSetupToken(ls[i].Auth) != "" {
		// no sign-in to put there: the token goes in Claude Code's
		// environment (ClaudeDirToken)
		if why := claudeSignedOut(ls[i]); why != "" {
			return "", errors.New(why)
		}
		return claudeTokenDir(user)
	}
	dir := claudeAccountDir(user)
	has, changed, err := syncClaudeDir(&ls[i])
	if err != nil {
		return "", err
	}
	if changed {
		if err := writeLogins(ls); err != nil {
			return "", err
		}
	}
	if why := claudeSignedOut(ls[i]); why != "" {
		return "", errors.New(why)
	}
	if has {
		claudeOnboarded(dir, nil)
		return dir, nil
	}
	c, _ := parseClaudeCredentials(ls[i].Auth)
	b, err := c.marshal()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// the sign-in that was there is out first, so the one the saved login
	// has is the directory's only holder: an older one left in the
	// directory's keychain item would shadow the fresh file on a Mac, and
	// be read back over it the next look. Nothing there, this removes
	// nothing.
	clearClaudeDir(dir)
	if err := writePrivate(filepath.Join(dir, ".credentials.json"), append(b, '\n')); err != nil {
		return "", err
	}
	// who it is, as Claude Code keeps it beside a sign-in it made
	var acct map[string]any
	if len(ls[i].Profile) > 0 && json.Unmarshal(ls[i].Profile, &acct) != nil {
		acct = nil
	}
	claudeOnboarded(dir, acct)
	return dir, nil
}

// claudeOnboarded marks Claude Code's onboarding done in config directory
// dir's .claude.json, as Claude Code does once a person has gone through
// it. Without the mark its interactive mode starts on its first-run
// screens, and from 2.1.316 on one that asks for a login method beside a
// sign-in that works (Jaxxxxnn on X), which a run can only end on. A
// directory -p alone ran in, or one made before magpie wrote the mark,
// has a .claude.json without it, so the mark is added to what is there,
// every other key kept. A new file gets the account's profile (acct) too.
// A file that can't be read or parsed, which Claude Code may be writing,
// is left as it is.
func claudeOnboarded(dir string, acct map[string]any) {
	profile := filepath.Join(dir, ".claude.json")
	b, err := os.ReadFile(profile)
	if os.IsNotExist(err) {
		m := map[string]any{"hasCompletedOnboarding": true}
		if acct != nil {
			m["oauthAccount"] = acct
		}
		pb, _ := json.MarshalIndent(m, "", "  ")
		_ = writePrivate(profile, append(pb, '\n'))
		return
	}
	if err != nil {
		return
	}
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber() // Claude Code's numbers go back as they were written
	if d.Decode(&m) != nil || m == nil {
		return
	}
	if done, _ := m["hasCompletedOnboarding"].(bool); done {
		return
	}
	m["hasCompletedOnboarding"] = true
	pb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = writePrivate(profile, append(pb, '\n'))
}

// claudeTrustMu keeps two runs' ClaudeTrustFolder from writing one
// .claude.json at once.
var claudeTrustMu sync.Mutex

// ClaudeTrustFolder records, in saved account config directory dir, that
// Claude Code may work in magpie's own folder, which keys names as Claude
// Code keys its projects (2.1.316: the folder's path, on Windows with
// forward slashes, "C:/Users/<me>/AppData/Local/Temp/magpie-claude-work"),
// as Claude Code itself records it once the trust question is answered
// yes. On mydosu's Windows (#4830) a saved account's directory had no
// such entry, and every /usage read of that account waited out its time
// on the question.
// Only a directory of magpie's saved accounts is written, never the
// user's own ~/.claude.json; every other key is kept, and a file that
// can't be read or parsed, which Claude Code may be writing, is left as it
// is.
func ClaudeTrustFolder(dir string, keys ...string) error {
	root := filepath.Clean(claudeDirsRoot())
	dir = filepath.Clean(dir)
	if rel, err := filepath.Rel(root, dir); err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsAny(rel, `/\`) {
		return fmt.Errorf("%s is not a saved Claude account's folder", dir)
	}
	claudeTrustMu.Lock()
	defer claudeTrustMu.Unlock()
	profile := filepath.Join(dir, ".claude.json")
	m := map[string]any{}
	b, err := os.ReadFile(profile)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	default:
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber() // Claude Code's numbers go back as they were written
		if err := d.Decode(&m); err != nil || m == nil {
			return fmt.Errorf("%s can't be read: %v", profile, err)
		}
	}
	projects, ok := m["projects"].(map[string]any)
	if !ok {
		if _, there := m["projects"]; there {
			return fmt.Errorf("%s has projects that aren't an object", profile)
		}
		projects = map[string]any{}
	}
	changed := false
	for _, k := range keys {
		if k == "" {
			continue
		}
		p, ok := projects[k].(map[string]any)
		if !ok {
			p = map[string]any{}
		}
		if t, _ := p["hasTrustDialogAccepted"].(bool); t {
			continue
		}
		p["hasTrustDialogAccepted"] = true
		projects[k] = p
		changed = true
	}
	if !changed {
		return nil
	}
	m["projects"] = projects
	pb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(profile, append(pb, '\n'))
}

// syncClaudeDir brings the saved Claude login l up to what Claude Code
// keeps in the account's config directory: the sign-in it refreshed to
// there, or, when it emptied that sign-in, Anthropic's refusal of the one
// l has. has says the directory holds a sign-in to run on; changed, that l
// changed.
func syncClaudeDir(l *savedLogin) (has, changed bool, err error) {
	c, ok := readClaudeDir(claudeAccountDir(l.User))
	switch {
	case ok:
		// a directory sign-in older than the saved one (#6391): the
		// directory was made again behind a switch (#4750) and the holder
		// the agent itself runs on refreshed meanwhile, so the directory's
		// is spent. Taking it would replace the fresh sign-in with the
		// spent one, and the next refresh in the directory would empty it,
		// the account signed out. has=false has claudeSavedDir make the
		// directory from the saved one instead, as when it holds none.
		if old, ook := parseClaudeCredentials(l.Auth); ook && old.OAuth.ExpiresAt > 0 &&
			c.OAuth.ExpiresAt > 0 && c.OAuth.ExpiresAt < old.OAuth.ExpiresAt {
			return false, false, nil
		}
		changed, err = takeClaudeDir(l, c)
		return true, changed, err
	case c.cleared():
		return false, refuseClaudeLogin(l, claudeLoginVersion(*l)), nil
	}
	return false, false, nil
}

// takeClaudeDir puts the sign-in Claude Code keeps in an account's config
// directory into its saved login, and says whether that changed it.
func takeClaudeDir(l *savedLogin, c claudeCredentials) (bool, error) {
	if old, ok := parseClaudeCredentials(l.Auth); ok && old.OAuth.AccessToken == c.OAuth.AccessToken &&
		old.OAuth.RefreshToken == c.OAuth.RefreshToken {
		return false, nil
	}
	b, err := c.marshal()
	if err != nil {
		return false, err
	}
	l.Auth = b
	if c.OAuth.SubscriptionType != "" {
		l.Plan = c.OAuth.plan()
	}
	l.Renewed = time.Now().UTC().Truncate(time.Second)
	l.Lapsed, l.Refused = "", ""
	return true, nil
}

// claudeStandIn is the saved account served in the place of Claude Code's
// own while Claude Code is signed out — the user logged out of it, as it
// tells them to when magpie's token is set beside a claude.ai sign-in.
// magpie keeps the other accounts' sign-ins itself, each its own, so a
// logout signs none of them out of magpie: the stand-in runs Claude Code
// in a config directory of its own, as the others on do. Never the one
// Claude Code was signed in to, whose sign-in went with it
// (claudeLoggedOut, or still Held before rememberLogins next looks: it is
// asked only while Claude Code is signed out); of the others, one that can
// be used, on before off, then the one seen last; one that can't — its
// sign-in gone, or refused — only when no other can; "" when there is none.
func claudeStandIn(ls []savedLogin) string {
	user, best := "", -1
	var seen time.Time
	for _, l := range ls {
		if l.Agent != "claude" || l.Held || l.Lapsed != "" && l.Refused == "" {
			continue
		}
		rank := 0
		if claudeSignedOut(l) == "" {
			rank = 2
			if l.On {
				rank++
			}
		}
		if rank > best || rank == best && l.Seen.After(seen) {
			user, seen, best = l.User, l.Seen, rank
		}
	}
	return user
}

// claudeListed is the saved account the Claude Code provider is listed on
// while Claude Code is signed out: the stand-in, else, with every account's
// sign-in gone, the one seen last, signed out. Listed without one, Claude
// Code left the Providers list with every account on it, which read as a
// ban (LeeHom on Discord: two Claude Pro accounts vanished); listed on a
// signed-out one, it says to sign in again, and its token says why it
// can't be used (claudeSavedDir). Never one still Held: Claude Code's own
// sign-in, which it may have left on disk after a logout, and which would
// then be served as if signed in (TestClaudeSignedOut). "" with no such
// account saved.
func claudeListed(ls []savedLogin) string {
	if u := claudeStandIn(ls); u != "" {
		return u
	}
	user := ""
	var seen time.Time
	for _, l := range ls {
		if l.Agent == "claude" && !l.Held && claudeSignedOut(l) != "" && (user == "" || l.Seen.After(seen)) {
			user, seen = l.User, l.Seen
		}
	}
	return user
}

// claudeLogoutLapse is why the account Claude Code was signed in to can't
// be used once Claude Code let go of it: logged out, which revokes the
// sign-in it held, or emptied that sign-in once Anthropic refused it.
const claudeLogoutLapse = "Claude Code is no longer signed in; sign in again in magpie"

// legacyClaudeLogoutLapse is claudeLogoutLapse as magpie wrote it before,
// which took every such lapse for a /logout.
const legacyClaudeLogoutLapse = "Claude Code's /logout signed it out (it revokes the sign-in it holds); sign in again"

// claudeGoneLapse is why a saved Claude account with no sign-in kept can't
// be used.
const claudeGoneLapse = "its sign-in is gone; sign in again"

// claudeLoggedOut marks the account Claude Code held, now that it holds no
// sign-in, as lapsed: magpie's copy of that account is the same sign-in,
// gone too — revoked by a /logout (POST <token URL>/revoke, Claude Code
// 2.1.x's performLogout), or refused by Anthropic, as the sign-in Claude
// Code emptied was. Which it was can't be told from here. Signed in to it
// again, Claude Code holds it once more (rememberLogins). The accounts
// magpie keeps in config directories of their own are sign-ins of their own
// and stay. It says whether ls changed.
func claudeLoggedOut(ls []savedLogin) bool {
	changed := false
	for i := range ls {
		if ls[i].Agent != "claude" || !ls[i].Held {
			continue
		}
		ls[i].Held, changed = false, true
		if claudeSignedOut(ls[i]) == "" {
			ls[i].Lapsed, ls[i].Refused = claudeLogoutLapse, ""
		}
	}
	return changed
}

// claudeSignedOut is why a saved Claude account can't be used, "" when it
// can: its saved sign-in is gone, Claude Code let go of it
// (claudeLoggedOut), or Anthropic refused the credential it has now
// (claude_auth.go) — a refusal of an earlier one says nothing of it.
func claudeSignedOut(l savedLogin) string {
	if l.Lapsed != "" && (l.Refused == "" || l.Refused == claudeLoginVersion(l)) {
		if l.Lapsed == legacyClaudeLogoutLapse {
			return claudeLogoutLapse
		}
		return l.Lapsed
	}
	if claudeAuthOf(l.Auth).ok || claudeSetupToken(l.Auth) != "" {
		return ""
	}
	return claudeGoneLapse
}

// claudeStandInAccount is the Claude Code provider while Claude Code is
// signed out, on claudeListed's account; false with no account saved.
func claudeStandInAccount() (Provider, bool) {
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	user := claudeListed(ls)
	if user == "" {
		return Provider{}, false
	}
	acct := &Account{Agent: "claude", User: user, standIn: true}
	for _, l := range ls {
		if l.Agent == "claude" && strings.EqualFold(l.User, user) {
			acct.Plan = l.Plan
		}
	}
	acct.token = func(context.Context) (string, error) { return claudeSavedDir(user) }
	return claudeProvider(acct), true
}

// AgentsOwn says the account is the one the agent itself is signed in to,
// run in the agent's own home with what it keeps there, not with a sign-in
// magpie hands it. Which account that is moves with a switch (SwitchLogin):
// a Claude Code started on it before goes on as whatever Claude Code is
// signed in to by then, as it reads its keychain again every half minute.
func (a *Account) AgentsOwn() bool { return a != nil && a.token == nil }

// ClaudeCodeMovedOff says Claude Code itself is signed in to another
// account than user now: what a Claude Code run in its own home says of
// the account it is on is no longer user's. false when it can't be told.
// Named from its profile, the account is told by its whole name: a Team
// seat and a personal plan of one email ("me@x.com · Acme", "me@x.com")
// are two accounts, and one's usage isn't the other's (netfishx on X).
func ClaudeCodeMovedOff(user string) bool {
	on, named := claudeCodeOn()
	if on == "" {
		return false
	}
	if named {
		return !strings.EqualFold(strings.TrimSpace(on), strings.TrimSpace(user))
	}
	return !sameClaudeUser(on, user)
}

// claudeCodeOn is the account Claude Code itself is signed in to now, as
// magpie names it; "" when signed out or unknown. named: the name is from
// its profile (~/.claude.json), whole, not the email alone `claude auth
// status` gives.
func claudeCodeOn() (user string, named bool) {
	l, ok := liveLogin("claude")
	if !ok {
		return "", false
	}
	return l.User, len(l.Profile) > 0
}

// sameClaudeUser: a and b name one account, the one as magpie names a Team
// or Enterprise seat ("me@x.com · Org") and the other as its email alone,
// as `claude auth status` gives it.
func sameClaudeUser(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == b {
		return true
	}
	ea, _, _ := strings.Cut(a, " · ")
	eb, _, _ := strings.Cut(b, " · ")
	return (ea == b || eb == a) && ea != ""
}
