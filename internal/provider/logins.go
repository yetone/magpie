package provider

// Several subscriptions per agent. magpie keeps following each agent's own
// sign-in; it also remembers every Codex and Claude Code account it has
// seen signed in, so the user can switch back to one without signing in
// again. A switch moves the saved credentials into the agent's own store
// and saves the ones they replace, so each account's refresh token lives in
// exactly one place: the vendor rotates it on every refresh, and two holders
// of the same one would sign each other out.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// Login is a remembered subscription account, without its secrets.
type Login struct {
	Agent  string    `json:"agent"`
	User   string    `json:"user"`
	Plan   string    `json:"plan,omitempty"`
	Seen   time.Time `json:"seen"`
	Active bool      `json:"active"` // the agent is signed in to this one now
	On     bool      `json:"on"`     // in use: the active one, or next in line
	// Lapsed says the vendor refused to refresh a saved account's sign-in:
	// it has to be signed in again before it can be used.
	Lapsed string `json:"lapsed,omitempty"`
	// Until is when the account's paid plan ends, as its sign-in says (a
	// ChatGPT one's chatgpt_subscription_active_until, codexUntil): nil
	// when it doesn't, or names a time gone by.
	Until *time.Time `json:"until,omitempty"`
	// Own is the agent's own sign-in, which magpie only reads: removed, it
	// is hidden rather than deleted (side_logins.go).
	Own bool `json:"own,omitempty"`
	// Paused is the account the agent is signed in to, passed over by the
	// gateway while another is on (savedLogin.Paused).
	Paused bool `json:"paused,omitempty"`
	// Returns is the account magpie signed the agent out of when it was
	// spent, and signs it back in to once it has room again (#408).
	Returns bool `json:"returns,omitempty"`
	// Token is a Claude account added by a setup-token: magpie's
	// requests use it, Claude Code itself can't be signed in to it.
	Token bool `json:"token,omitempty"`
	// AccessOnly is a ChatGPT account kept access token only
	// (codex_access_only.go): magpie never renews its sign-in, nor signs
	// Codex in to it.
	AccessOnly bool `json:"accessOnly,omitempty"`

	// first is the saved Claude account served in the place of Claude
	// Code's own while it is signed out (claudeStandIn).
	first bool
	// home is the other Codex folder a ChatGPT account is signed in to
	// in, Codex's own there (codexHomeLogins): not one to switch ~/.codex to.
	home string
}

type savedLogin struct {
	// ID is the account's stable id (#905): set once it is written, and
	// kept as it is when the account is renamed or its plan changes — a
	// gateway key held to accounts names it by this, not by a name that
	// moves. Until the logins are next written, one made of the name
	// stands in, so an entry made before stands.
	ID string `json:"id,omitempty"`
	// Order is the user-arranged routing order within this agent. Zero keeps
	// the original alphabetical order for accounts not arranged yet.
	Order     int       `json:"order,omitempty"`
	Agent     string    `json:"agent"`
	User      string    `json:"user"`
	Plan      string    `json:"plan,omitempty"`
	AccessSKU string    `json:"accessSku,omitempty"`
	Seen      time.Time `json:"seen"`
	// On puts the account in use beside the one the agent is signed in to:
	// requests go to it when that one is out of quota (see logins_on.go).
	On bool `json:"on,omitempty"`
	// Auth is the agent's credential blob as the agent stores it: Codex's
	// auth.json, Claude Code's keychain item / .credentials.json.
	Auth json.RawMessage `json:"auth"`
	// Profile is Claude Code's oauthAccount from .claude.json, which says
	// whose credentials those are.
	Profile json.RawMessage `json:"profile,omitempty"`
	// Home is where a Grok account magpie signed in keeps its sign-in; the
	// Grok CLI's own account has none (see grok_accounts.go). First puts a
	// Grok or Copilot account ahead of the agent's own (side_logins.go).
	Home  string `json:"home,omitempty"`
	First bool   `json:"first,omitempty"`
	// Project is the Google Cloud project a Gemini CLI or Antigravity
	// account's requests go to, when the user named one (google.go).
	Project string `json:"project,omitempty"`
	// Renewed is when magpie last refreshed a saved account's sign-in (a
	// Claude one: when Claude Code last refreshed it, or ran on it for the
	// keep-alive), and Lapsed why the vendor last refused to (logins_on.go,
	// keepalive.go).
	Renewed time.Time `json:"renewed,omitzero"`
	Lapsed  string    `json:"lapsed,omitempty"`
	// AccessOnly keeps a ChatGPT account access token only: its sign-in
	// is never refreshed, which would sign out the place it came from
	// (codex_access_only.go).
	AccessOnly bool `json:"accessOnly,omitempty"`
	// Refused is the Claude credential Anthropic refused (its version, in
	// claude_auth.go): Lapsed holds while the account has that one, and
	// says nothing of the one it is refreshed or signed in to next.
	Refused string `json:"refused,omitempty"`
	// Hidden is the agent's own sign-in removed in magpie, with the mark
	// of the sign-in it was (side_logins.go): it is listed and tried no
	// more until the agent signs in anew. The agent's files stay as they are.
	Hidden string `json:"hidden,omitempty"`
	// Paused is set on the account the agent is signed in to when the user
	// paused it in magpie (#263): the gateway passes over it while another
	// of the agent's accounts is on, the agent staying signed in to it.
	Paused bool `json:"paused,omitempty"`
	// Held is the Claude account Claude Code itself was signed in to when
	// magpie last looked: its saved copy is that very sign-in, which
	// Claude Code's /logout revokes (claudeLoggedOut).
	Held bool `json:"held,omitempty"`
	// HeldAs is, on the Held account, the account ~/.claude.json named
	// when magpie last looked (claudeProfileKey): it can be another's, a
	// Claude Code started on that one writing its profile after a switch
	// (claudeSignInOwner). A sign-in renewed under that same profile is
	// the Held account's still (claudeRenewedHeld).
	HeldAs string `json:"heldAs,omitempty"`
	// was is the name a Copilot account read under a new one had in the
	// file (#1220): its per-account settings are still kept by it, and
	// move to the new one before the new name is written (renameSettled).
	was string
}

var (
	// loginsMu is held for every read of logins.json a change is made
	// from, and the write: in this magpie, and across magpies (#1674), so
	// a refresh the gateway writes is never written back over with what a
	// magpie run from a terminal read before it. Taken, it has logins.json
	// read afresh.
	loginsMu = filelock.Mutex{
		Path:   func() string { return loginsPath() + ".lock" },
		Locked: func() { filememo.Look(loginsPath()) },
	}
	// loginsSeenAt is when rememberLogins last looked at the agents'
	// sign-ins (UnixNano), 0 for not since this magpie started or since
	// a change made the next look due at once. It is read without
	// loginsMu, so a look not yet due takes no lock (#1665).
	loginsSeenAt atomic.Int64
)

// loginsLookDue says the 30 seconds since the last look are up.
func loginsLookDue() bool {
	at := loginsSeenAt.Load()
	return at == 0 || time.Since(time.Unix(0, at)) >= 30*time.Second
}

// switchable agents: those whose sign-in magpie can save and put back.
var loginAgents = []string{"claude", "codex"}

// loginID is the account's stable id (#905): the one saved with it, or a
// stand-in made of its name until the logins are next written — set then,
// and kept through renames.
func loginID(l savedLogin) string {
	if l.ID != "" {
		return l.ID
	}
	sum := sha256.Sum256([]byte("magpie login\n" + l.Agent + "\n" + accountKey(l.User)))
	return hex.EncodeToString(sum[:8])
}

// LoginID is the stable id of an agent's account known by its name
// (#905): the login's, kept through renames, or one made of the name
// for an account whose logins haven't been written since — and for an
// agent that keeps its accounts elsewhere, made of the name always.
func LoginID(agent, user string) string {
	for _, l := range readLogins() {
		if l.Agent == agent && accountKey(l.User) == accountKey(user) {
			return loginID(l)
		}
	}
	return loginID(savedLogin{Agent: agent, User: user})
}

func loginsPath() string { return appdir.Join(filepath.Dir(Path()), "logins.json") }

// lastLogins is the accounts last read from logins.json: a read that fails
// (a file half there, one magpie can't open for a moment) is them, not no
// accounts, which the next change of an account would write back over
// every account. loginsKnown is whether there are any such, read or
// written by this magpie.
var (
	lastLoginsMu sync.Mutex
	lastLogins   []savedLogin
	loginsKnown  bool
)

// parseLogins is logins.json's accounts.
func parseLogins(b []byte) ([]savedLogin, error) {
	var out []savedLogin
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	// DimAgent's accounts: magpie no longer signs in to it (DimAgent
	// doesn't allow its subscription used outside its client), so one
	// signed in before is left out, and gone from the file at its next write
	out = slices.DeleteFunc(out, func(l savedLogin) bool { return l.Agent == "dimagent" })
	for i := range out {
		// a Max 20x saved before it was named Ultra
		if out[i].Agent == "claude" && out[i].Plan == "max" {
			if c, ok := parseClaudeCredentials(out[i].Auth); ok {
				out[i].Plan = claudePlan(out[i].Plan, c.OAuth.RateLimitTier)
			}
		}
	}
	return nameAlike(dedupeLogins(out)), nil
}

// validLogins is logins.json's contents read as accounts: valid JSON can
// still have field types they don't take.
func validLogins(b []byte) bool {
	var ls []savedLogin
	return json.Unmarshal(b, &ls) == nil
}

func readLogins() []savedLogin {
	loginsCopied.Add(1)
	return slices.Clone(sharedLogins()) // callers change theirs
}

// loginsCopied counts readLogins' copies, for a test.
var loginsCopied atomic.Int64

// sharedLogins is readLogins without the caller's own copy: the accounts
// as kept, read only. A page's state lists a dozen agents' accounts, and
// a copy of the whole list for each was 61 MB of what twenty window polls
// allocated.
func sharedLogins() []savedLogin {
	if settings.AccountDiscoveryOff() {
		return nil
	}
	// parsed once until the file changes: a state of the page asks for it
	// dozens of times (every agent's drift and models), and with the
	// accounts' credentials in it the file is large — a Save of a profile
	// waited seconds on it
	ls, err := filememo.Read("logins", loginsPath(), parseLogins)
	lastLoginsMu.Lock()
	defer lastLoginsMu.Unlock()
	switch {
	case err == nil:
		lastLogins, loginsKnown = ls, true
	case errors.Is(err, fs.ErrNotExist):
		lastLogins, loginsKnown = nil, true
	case loginsKnown:
		log.Printf("logins.json: %v; the accounts read before are kept", err)
		ls = lastLogins
	default:
		// magpie just started on a file that doesn't read (all zero after
		// a crash, #1505): the accounts are its last good generation's
		ls = nil
		if b, ferr := lastgood.Fallback(loginsPath(), unreadBytes(loginsPath()), validLogins); ferr == nil {
			ls, _ = parseLogins(b)
			lastLogins, loginsKnown = ls, true
		} else {
			log.Printf("logins.json: %v; %v", err, ferr)
		}
	}
	return slices.Clip(ls) // an append makes the caller's own
}

// unreadBytes are a file's bytes, nil when it can't be read: for the log
// to say what was wrong with it.
func unreadBytes(path string) []byte {
	b, _ := os.ReadFile(path)
	return b
}

func writeLogins(ls []savedLogin) error { return writeLoginsTold(ls, true) }

// writeLoginsTold writes the accounts, and with tell has every catalog
// built anew (Changed). Without it the write moved only when accounts
// were last seen, which nothing built from the accounts reads: told, an
// idle magpie built every catalog again each seenEvery, ~42 MB for dsh's
// round alone. A reader still finds the file's new time and size.
func writeLoginsTold(ls []savedLogin, tell bool) error {
	if settings.AccountDiscoveryOff() {
		return errAccountDiscoveryOff
	}
	// every account gets its stable id before it is written (#905): the
	// one it has, or the stand-in made of its name, there to stay
	for i := range ls {
		ls[i].ID = loginID(ls[i])
	}
	sortLogins(ls)
	// a Copilot account renamed on read is written under its new name
	// once its settings have moved to it; until they can, under its old
	disk := renameSettled(ls)
	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	if tell {
		defer Changed() // an account added, switched or gone: All builds anew
	}
	beforeLoginsWrite()
	b = append(b, '\n')
	if fileHolds(loginsPath(), b, 0o600) {
		// the bytes are on the disk already (#6289): no new generation, no
		// new mtime, and what filememo parsed stays
		lastLoginsMu.Lock()
		lastLogins, loginsKnown = slices.Clone(ls), true
		lastLoginsMu.Unlock()
		return nil
	}
	// the file there now is kept first: as its last good generation, or
	// copied aside when it doesn't read, never written over unkept
	if err := lastgood.Keep(loginsPath(), validLogins, 0o600); err != nil {
		return err
	}
	write := writePrivate
	if !tell {
		write = writePrivateQuiet
	}
	if err := write(loginsPath(), b); err != nil {
		return err
	}
	lastLoginsMu.Lock()
	lastLogins, loginsKnown = slices.Clone(ls), true
	lastLoginsMu.Unlock()
	return nil
}

// beforeLoginsWrite is called as logins.json is about to be written: for a
// test to have another magpie write it then.
var beforeLoginsWrite = func() {}

// sortLogins puts accounts in the order logins.json keeps them: by agent,
// then the order the user gave, then by name.
func sortLogins(ls []savedLogin) {
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Agent != ls[j].Agent {
			return ls[i].Agent < ls[j].Agent
		}
		if ls[i].Order != ls[j].Order {
			if ls[i].Order == 0 {
				return false
			}
			if ls[j].Order == 0 {
				return true
			}
			return ls[i].Order < ls[j].Order
		}
		return strings.ToLower(ls[i].User) < strings.ToLower(ls[j].User)
	})
}

// writePrivate replaces a file readable by the user alone, atomically, so
// an agent reading it at that moment sees either version, never half; the
// new one is on the disk before it is renamed over the old (#1505).
func writePrivate(path string, b []byte) error {
	if fileHolds(path, b, 0o600) { // the file has these bytes (#6289): nothing moves
		return nil
	}
	defer filememo.Forget() // read again, where a request holds it
	return writePrivateQuiet(path, b)
}

// writePrivateQuiet is writePrivate with nothing read forgotten: a
// request holding the file reads the old bytes for up to a second.
func writePrivateQuiet(path string, b []byte) error {
	if fileHolds(path, b, 0o600) {
		return nil
	}
	path, err := edit.Target(path) // a symlink stays, its target written
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return steady.WriteFile(path, b, 0o600)
}

// fileHolds reports whether path is a file of exactly b, with the mode
// the write would give it. A read that fails is "no": it is written.
// writeChanged is steady.WriteFile for a file that is left alone, mtime
// and all, when it already holds b (#6289).
func writeChanged(path string, b []byte, perm os.FileMode) error {
	if fileHolds(path, b, perm) {
		return nil
	}
	return steady.WriteFile(path, b, perm)
}

func fileHolds(path string, b []byte, perm os.FileMode) bool {
	path, err := edit.Target(path)
	if err != nil {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != int64(len(b)) {
		return false
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != perm {
		return false
	}
	old, err := os.ReadFile(path)
	return err == nil && bytes.Equal(old, b)
}

func upsertLogin(ls []savedLogin, l savedLogin) []savedLogin {
	l.User = codexName(ls, l)
	if claudeSignInOfAnother(ls, l) {
		return ls
	}
	for i := range ls {
		if sameLogin(ls[i], l) {
			// a refused Claude credential stays refused while it is the
			// one the account has
			if l.Agent == "claude" && l.Lapsed == "" && ls[i].Refused != "" && ls[i].Refused == claudeLoginVersion(l) {
				l.Lapsed, l.Refused = ls[i].Lapsed, ls[i].Refused
			}
			// so does a revoked sign-in's mark (signin_revoked.go), while
			// the credential is the same: a sign-in anew or a refresh
			// clears it, as does a request it serves
			if l.Lapsed == "" && strings.HasPrefix(ls[i].Refused, revokedPrefix) && sameAuth(l.Auth, ls[i].Auth) && l.Home == ls[i].Home {
				l.Lapsed, l.Refused = ls[i].Lapsed, ls[i].Refused
			}
			l.On = l.On || ls[i].On
			l.Paused = l.Paused || ls[i].Paused
			l.Order = ls[i].Order
			ls[i] = l
			return ls
		}
	}
	return append(ls, l)
}

// dedupeLogins keeps one record per Claude or Codex account, which
// sameLogin tells by email and organization or workspace. Other agents'
// accounts are told by name alone: a Grok account magpie signed in and the
// CLI's own sign-in to the same one are two records (side_logins.go), and
// folding them would lose the first's sign-in at the next write.
func dedupeLogins(ls []savedLogin) []savedLogin {
	out := make([]savedLogin, 0, len(ls))
	for _, l := range ls {
		if !slices.Contains(loginAgents, l.Agent) {
			out = append(out, l)
			continue
		}
		found := -1
		for i := range out {
			if sameLogin(out[i], l) {
				found = i
				break
			}
		}
		if found < 0 {
			out = append(out, l)
			continue
		}
		// keep the one seen later, and if either is on or first, keep that too.
		previous := out[found]
		keep := previous
		if l.Seen.After(previous.Seen) {
			keep = l
		}
		keep.On = previous.On || l.On
		keep.Paused = previous.Paused || l.Paused
		keep.First = previous.First || l.First
		keep.Order = previous.Order
		if keep.Order == 0 {
			keep.Order = l.Order
		}
		out[found] = keep
	}
	return out
}

// sameLogin says whether two saved logins are one account. A Claude
// account is its email within an organization, a ChatGPT one its email
// within a workspace: one email can be a personal Plus or Max and a seat on
// a Team, two subscriptions side by side.
func sameLogin(a, b savedLogin) bool {
	if a.Agent != b.Agent {
		return false
	}
	var ea, oa, eb, ob string
	switch a.Agent {
	case "claude":
		ea, oa = claudeWho(a.Profile)
		eb, ob = claudeWho(b.Profile)
	case "codex":
		ea, oa = codexWho(a.Auth)
		eb, ob = codexWho(b.Auth)
	}
	if ea != "" && eb != "" && oa != "" && ob != "" {
		return strings.EqualFold(ea, eb) && oa == ob
	}
	return strings.EqualFold(a.User, b.User)
}

// codexWho reads the email and workspace of a Codex auth.json. What it
// reads is the bytes' alone, so it is kept by them (authMemo): codexName
// asks it of every saved account for each one it compares, and every look
// at the signed-in account (liveLogin, from four loops at rest) asks it
// again, each a JSON parse and a JWT decode of the same few accounts.
func codexWho(auth json.RawMessage) (email, workspace string) {
	w := codexWhos.of(auth, func() [2]string {
		var a codexAuth
		if json.Unmarshal(auth, &a) != nil {
			return [2]string{}
		}
		id := jwtClaims(a.Tokens.IDToken)
		ws := a.Tokens.AccountID
		if ws == "" {
			ws = claimString(id, "https://api.openai.com/auth", "chatgpt_account_id")
		}
		return [2]string{claimString(id, "email"), ws}
	})
	return w[0], w[1]
}

var codexWhos authMemo[[2]string]

// authMemo keeps what is read from a credential's bytes, by those bytes:
// a few accounts' worth, emptied when it reaches authMemoMax, so a token
// renewed every hour leaves nothing behind for long. The lookup by the
// bytes makes no string, so a hit allocates nothing.
type authMemo[T any] struct {
	sync.Mutex
	m map[string]T
}

const authMemoMax = 64

func (c *authMemo[T]) of(b []byte, read func() T) T {
	c.Lock()
	v, ok := c.m[string(b)]
	c.Unlock()
	if ok {
		return v
	}
	v = read()
	c.Lock()
	if c.m == nil || len(c.m) >= authMemoMax {
		c.m = make(map[string]T)
	}
	c.m[string(b)] = v
	c.Unlock()
	return v
}

// codexName is the name a Codex account goes by among the saved ones ls:
// codexUser's, unless another account goes by that already — two seats of
// one email in two Team workspaces read alike — and then the one it was
// saved under, or for a new one the name with its workspace after it.
// Every account is told by its name (switched to, refreshed, removed), and
// two by one name were taken as one: removing the one not in use signed
// Codex out of the other, as the last account, and a refresh of one was
// written over the other's credentials (vincentzhang on Discord).
func codexName(ls []savedLogin, l savedLogin) string {
	if l.Agent != "codex" {
		return l.User
	}
	taken := func(user string) bool {
		return slices.ContainsFunc(ls, func(x savedLogin) bool {
			return x.Agent == "codex" && strings.EqualFold(x.User, user) && !sameLogin(x, l)
		})
	}
	for _, x := range ls {
		// told apart once, it keeps that name; else it takes a new plan's
		if x.Agent == "codex" && sameLogin(x, l) && (taken(l.User) || strings.HasPrefix(strings.ToLower(x.User), strings.ToLower(l.User)+" · ")) {
			return x.User
		}
	}
	if !taken(l.User) {
		return l.User
	}
	_, ws := codexWho(l.Auth)
	if len(ws) > 8 {
		ws = ws[:8]
	}
	name := l.User
	if ws != "" {
		name += " · " + ws
	}
	for n := 2; taken(name); n++ {
		name = fmt.Sprintf("%s · %s (%d)", l.User, ws, n)
	}
	return name
}

// nameAlike gives each Codex account in ls a name of its own (codexName),
// the first by a name keeping it: two saved by one name before are told
// apart from the next write on. A Copilot account on an enterprise's host
// is named with it (copilotSavedName, #1220).
func nameAlike(ls []savedLogin) []savedLogin {
	for i := range ls {
		if ls[i].Agent == "codex" {
			ls[i].User = codexName(ls[:i], ls[i])
		}
		if ls[i].Agent == "copilot" {
			if now := copilotSavedName(ls[i]); now != ls[i].User {
				ls[i].renameTo(now)
			}
		}
	}
	return ls
}

// codexLiveName is the name the ChatGPT account Codex is signed in to
// (auth, as auth.json has it; user, codexUser's) goes by: the one it is
// saved under, which may not be codexUser's (codexName). Two seats of one
// email in two Team workspaces read alike by codexUser's, and everything
// that goes by the account's name — its allowance, its rests, its cap,
// the Routing page — took the one Codex is on for the other one (#1424).
func codexLiveName(user string, auth json.RawMessage) string {
	return codexName(readLogins(), savedLogin{Agent: "codex", User: user, Auth: auth})
}

// codexUser names a ChatGPT account from its ID token's claims: its email,
// and for a seat in a workspace the plan too, so it reads apart from a
// personal plan of the same email.
func codexUser(id map[string]any) string {
	email := claimString(id, "email")
	switch plan := claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"); plan {
	case "team", "business", "enterprise", "edu":
		if email != "" {
			return email + " · " + strings.ToUpper(plan[:1]) + plan[1:]
		}
	}
	return email
}

// claudeWho reads the email and organization of Claude Code's oauthAccount.
func claudeWho(profile json.RawMessage) (email, org string) {
	var acct struct {
		Email string `json:"emailAddress"`
		Org   string `json:"organizationUuid"`
	}
	if json.Unmarshal(profile, &acct) != nil {
		return "", ""
	}
	return acct.Email, acct.Org
}

// claudeUser names a Claude account: its email, and for a seat on a Team
// or Enterprise the organization too, so it reads apart from a personal
// subscription of the same email.
func claudeUser(email, plan string, acct map[string]any) string {
	if email == "" || (plan != "team" && plan != "enterprise") {
		return email
	}
	org, _ := acct["organizationName"].(string)
	if org = strings.TrimSpace(org); org == "" || strings.Contains(org, email) {
		org = strings.ToUpper(plan[:1]) + plan[1:]
	}
	return email + " · " + org
}

// codexAuthPath is Codex's sign-in, under its home (appdir.CodexHome).
func codexAuthPath() string { return filepath.Join(appdir.CodexHome(), "auth.json") }

// CodexAPIKeySignedIn says Codex itself is signed in with an OpenAI
// API key rather than a ChatGPT account (auth.json's auth_mode): its
// sign-in is a key's, and spends no account a gateway key's list
// governs.
func CodexAPIKeySignedIn() bool {
	b, _, err := readCodexLogin(codexHome(), false)
	if err != nil {
		return false
	}
	var a codexAuth
	return json.Unmarshal(b, &a) == nil && a.AuthMode == "apikey"
}

// claudeProfilePath is Claude Code's global state file, which holds the
// signed-in account's identity next to much else.
func claudeProfilePath() string {
	if dir := appdir.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

func readClaudeProfile() (map[string]any, error) {
	b, err := readAccountFile(claudeProfilePath())
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber() // numbers go back exactly as they came
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// claudeProfileAccount is the account ~/.claude.json says Claude Code is
// signed in to, read again only when the file changes: it is large, and a
// page asks for it for every provider it lists. It must not be changed.
func claudeProfileAccount() (map[string]any, bool) {
	if settings.AccountDiscoveryOff() {
		return nil, false
	}
	acct, err := filememo.Read("claude account", claudeProfilePath(), func(b []byte) (map[string]any, error) {
		var m struct {
			OAuthAccount map[string]any `json:"oauthAccount"`
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		err := d.Decode(&m)
		return m.OAuthAccount, err
	})
	return acct, err == nil && acct != nil
}

// ClaudeTrustedProjects are the projects keys of the folders the user
// told Claude Code to trust (hasTrustDialogAccepted), in the state file of
// the account it is signed in to (claudeProfilePath, so CLAUDE_CONFIG_DIR's
// when set), read again only when the file changes. Only read, never
// written: the answers are the user's. A file that isn't there trusts
// nothing; one that can't be read or parsed, which Claude Code may be
// writing, or account discovery turned off, is an error: unknown. What it
// returns is shared and must not be changed.
func ClaudeTrustedProjects() (map[string]bool, error) {
	if settings.AccountDiscoveryOff() {
		return nil, errAccountDiscoveryOff
	}
	trusted, err := filememo.Read("claude trusted projects", claudeProfilePath(), func(b []byte) (map[string]bool, error) {
		var m struct {
			Projects map[string]struct {
				Trusted any `json:"hasTrustDialogAccepted"`
			} `json:"projects"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for k, p := range m.Projects {
			// Claude Code counts only true (=== true, or truthy: a
			// bool either way in what it writes)
			if t, _ := p.Trusted.(bool); t {
				out[k] = true
			}
		}
		return out, nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	return trusted, err
}

// claudeSignedInUser names the account Claude Code is signed in to, by the
// one name both the account (claudeAccount) and its saved login (liveLogin)
// go by: ~/.claude.json's, or status, what `claude auth status` said, when
// that has none. plan is the credentials', or else the CLI's; acct is the
// profile it was named from.
func claudeSignedInUser(plan, statusPlan, status string) (user string, acct map[string]any) {
	if plan == "" {
		plan = statusPlan
	}
	if acct, ok := claudeProfileAccount(); ok {
		if email, _ := acct["emailAddress"].(string); strings.TrimSpace(email) != "" {
			return claudeUser(strings.TrimSpace(email), plan, acct), acct
		}
	}
	return status, nil
}

// savedButSignedOut is, for each agent with accounts saved in magpie that
// isn't signed in where magpie looks, why none of them is offered: they
// are served beside the account the agent is signed in to, and there is
// none.
func savedButSignedOut() []Exclusion {
	saved := map[string]int{}
	users := map[string][]string{}
	for _, l := range readLogins() {
		saved[l.Agent]++
		users[l.Agent] = append(users[l.Agent], l.User)
	}
	var out []Exclusion
	for _, a := range loginAgents {
		// Claude Code signed out, its saved accounts are served all the
		// same (claudeStandIn)
		if saved[a] == 0 || a == "claude" {
			continue
		}
		if _, ok := liveLogin(a); ok {
			continue
		}
		n := "1 account is"
		if saved[a] > 1 {
			n = fmt.Sprintf("%d accounts are", saved[a])
		}
		_, _, err := readCodexLogin(codexHome(), false)
		out = append(out, Exclusion{Agent: a, SignedOut: true, Users: users[a], Retry: codexKeychainUnread(err),
			Why: fmt.Sprintf("%s saved in magpie, and they are only offered beside the account Codex is signed in to. %s", n, codexSignedOutWhy())})
	}
	return out
}

// codexSignedOutWhy says why magpie sees no Codex sign-in: none where
// Codex's config.toml keeps it, or one kept where magpie couldn't read it
// (#2312, #2367, Dony2050), and what to do.
func codexSignedOutWhy() string {
	_, _, err := readCodexLogin(codexHome(), false)
	var se *codexStoreError
	if !errors.As(err, &se) {
		where := codexAuthPath()
		if s, serr := readCodexStoreSetting(codexHome()); serr == nil && s.keychainItem() {
			where = fmt.Sprintf("the keychain item %q for %s", codexKeychainService, codexHome())
			if s.Mode == "auto" {
				where += " nor " + codexAuthPath()
			}
		}
		return fmt.Sprintf("Codex isn't signed in here (nothing in %s). Sign in (codex login) with this HOME.", where)
	}
	switch {
	case se.Setting.Mode == "ephemeral":
		return se.Error() + ". Set cli_auth_credentials_store = \"file\" there and sign in again (codex login)."
	case se.Cause != nil:
		return se.Error() + ". Allow magpie when macOS asks for the keychain (\"Always Allow\" lets /usr/bin/security read that item from then on, so macOS stops asking each time Codex changes it), or set cli_auth_credentials_store = \"file\" there and sign in again (codex login)."
	}
	return se.Error() + fmt.Sprintf(": here it reads only %s. Set cli_auth_credentials_store = \"file\" there and sign in again (codex login).", codexAuthPath())
}

// liveLogin reads the account an agent is signed in to now.
func liveLogin(agent string) (savedLogin, bool) {
	if settings.AccountDiscoveryOff() {
		return savedLogin{}, false
	}
	switch agent {
	case "codex":
		return codexLoginIn(codexHome())
	case "claude":
		c, _, ok := claudeCredential()
		if !ok {
			return savedLogin{}, false
		}
		b, err := c.marshal()
		if err != nil {
			return savedLogin{}, false
		}
		// credentials a logout left behind are not a sign-in: Claude Code
		// says so, and the account is not a provider either (claudeAccount)
		user, plan, signedOut := claudeIdentity()
		if signedOut {
			return savedLogin{}, false
		}
		l := savedLogin{Agent: agent, Plan: c.OAuth.plan(), Auth: b}
		var acct map[string]any
		if l.User, acct = claudeSignedInUser(c.OAuth.SubscriptionType, plan, user); acct != nil {
			l.Profile, _ = json.Marshal(acct)
			l.HeldAs = claudeProfileKey(l.Profile)
		}
		if l.User == "" {
			return savedLogin{}, false
		}
		return claudeSignInOwner(readLogins(), l), true
	}
	return savedLogin{}, false
}

// CodexSignInPlan is the plan of the ChatGPT account this machine's Codex
// is signed in to, as its ID token's chatgpt_plan_type names it ("free",
// "plus", "pro" …), "" when Codex isn't signed in or it can't be read.
// Codex's auto-review follows the session on a Free plan by default
// (codexcat.AutoReviewModel).
func CodexSignInPlan() string {
	l, _ := liveLogin("codex")
	return l.Plan
}

func init() { codexcat.SignInPlan = CodexSignInPlan }

// CodexSignInWho names the ChatGPT account this machine's Codex is signed
// in to now and its plan, "" when it isn't: what the backend's model list
// (CodexLiveList) is the list of. A Pro account lists models a Plus one
// doesn't (chzbz on X: GPT-6.1 Sol).
func CodexSignInWho() string {
	l, ok := liveLogin("codex")
	if !ok {
		return ""
	}
	return l.User + "\x00" + l.Plan
}

// codexLoginIn is the ChatGPT account Codex is signed in to in its folder
// home, as Codex keeps it there (auth.json, or the keychain item named by
// the folder).
func codexLoginIn(home string) (savedLogin, bool) {
	b, _, err := readCodexLogin(home, false)
	if err != nil {
		return savedLogin{}, false
	}
	// read once for the bytes, as codexWho is: liveLogin is asked from
	// four loops at rest, each a parse and a JWT decode of one auth.json
	w := codexLogins.of(b, func() [2]string {
		var a codexAuth
		if json.Unmarshal(b, &a) != nil || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
			return [2]string{}
		}
		id := jwtClaims(a.Tokens.IDToken)
		user := codexUser(id)
		if user == "" {
			user = a.Tokens.AccountID
		}
		return [2]string{user, claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type")}
	})
	user := w[0]
	if user == "" {
		return savedLogin{}, false
	}
	l := savedLogin{Agent: "codex", User: user, Plan: w[1], Auth: json.RawMessage(bytes.TrimSpace(b))}
	l.User = codexLiveName(user, l.Auth)
	return l, true
}

var codexLogins authMemo[[2]string]

// codexOtherHomes are the user's Codex folders but the one magpie and
// Codex run with (appdir.CodexHomes): one ChatGPT account each, Codex run
// in it with CODEX_HOME set.
func codexOtherHomes() []string { return appdir.CodexHomes()[1:] }

// codexHomeLogins are the accounts Codex is signed in to in the user's
// other Codex folders, each with its folder as Home: one Codex keeps
// signed in itself, as it does the one in ~/.codex. The one ~/.codex is
// signed in to is that one's, wherever else it is too, and an account in
// two other folders is the first's. A folder's sign-in the user removed
// from magpie is passed over (codexSetAside); with that record unreadable,
// which ones were removed is unknown, and none is read this time.
func codexHomeLogins(live savedLogin, haveLive bool) []savedLogin {
	aside, err := readCodexAside()
	if err != nil {
		return nil
	}
	var out []savedLogin
	for _, home := range codexOtherHomes() {
		l, ok := codexLoginIn(home)
		if !ok || haveLive && sameLogin(live, l) || slices.ContainsFunc(out, func(o savedLogin) bool { return sameLogin(o, l) }) {
			continue
		}
		l.Home = home
		if codexSetAside(l, aside) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// codexHeldIn is the other Codex folder the saved account l is signed in
// to now, as rememberLogins recorded it in Home: the same login, or the
// same refresh token. "" when it is in none.
func codexHeldIn(l savedLogin) string {
	if l.Agent != "codex" || l.Home == "" {
		return ""
	}
	in, ok := codexLoginIn(l.Home)
	if !ok {
		return ""
	}
	if sameLogin(in, l) || sameRefresh(in.Auth, l.Auth) {
		return l.Home
	}
	return ""
}

// codexWorkspacesApart says two Codex auth.json are for two workspaces,
// each known: one sign-in, its refresh token the same, can be routed to
// either (the Codex app's workspace switch), and is two accounts then.
func codexWorkspacesApart(x, y json.RawMessage) bool {
	_, a := codexWho(x)
	_, b := codexWho(y)
	return a != "" && b != "" && a != b
}

// sameRefresh says two Codex auth.json hold one refresh token.
func sameRefresh(x, y json.RawMessage) bool {
	var a, b codexAuth
	return json.Unmarshal(x, &a) == nil && json.Unmarshal(y, &b) == nil &&
		a.Tokens.RefreshToken != "" && a.Tokens.RefreshToken == b.Tokens.RefreshToken
}

// rememberLogins saves the accounts the agents are signed in to now. The
// copy of an active account is only a bookmark: the agent keeps refreshing
// its own, and a switch saves that fresher one first.
// accountRemoved: the user removed the agent's account from magpie, which
// then neither saves its sign-ins nor keeps them renewed.
func accountRemoved(agent string) bool {
	return slices.ContainsFunc(storedFile().Providers, func(p Provider) bool { return p.ID == agent && p.Hidden })
}

func rememberLogins(force bool) {
	if settings.AccountDiscoveryOff() {
		return
	}
	// the time first, and the lock only for a look that is due: taken
	// first, every ask (each list of accounts, each page's read) made and
	// locked logins.json.lock, ~370 times in a start's first 25s on
	// Windows, where an antivirus' filter driver works on each (#1665)
	if !force && !loginsLookDue() {
		return
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	// another ask may have looked while this one waited for the lock
	if !force && !loginsLookDue() {
		return
	}
	loginsSeenAt.Store(time.Now().UnixNano())
	was := readLogins()
	ls := slices.Clone(was)
	now := time.Now().UTC().Truncate(time.Second)
	changed := false
	for _, agent := range loginAgents {
		if accountRemoved(agent) {
			continue
		}
		l, ok := liveLogin(agent)
		if agent == "codex" {
			// Codex's other folders' accounts (phantom3792 on X: only the
			// default profile's account read a quota)
			for _, h := range codexHomeLogins(l, ok) {
				h.Seen = now
				ls = upsertLogin(ls, h)
				changed = true
			}
		}
		if !ok {
			if agent == "claude" && claudeLoggedOut(ls) {
				changed = true
			}
			continue
		}
		l.Seen = now
		if agent == "claude" {
			for i := range ls {
				ls[i].Held, ls[i].HeldAs = false, ""
			}
			l.Held = true
		}
		ls = upsertLogin(ls, l)
		changed = true
	}
	// every magpie run from a terminal is a new process, which the 30
	// seconds above don't hold for: one that would only move Seen on, and
	// that recently, writes nothing (#1674)
	if changed && !onlySeenMoved(was, ls, now) {
		_ = writeLoginsTold(ls, !sameButSeen(was, ls))
	}
}

// seenEvery is how often an account still signed in has the time it was
// last seen written, when nothing else of it changed.
const seenEvery = 10 * time.Minute

// onlySeenMoved says next is was with nothing changed but the times
// accounts were last seen, each of them less than seenEvery before now.
func onlySeenMoved(was, next []savedLogin, now time.Time) bool {
	return seenApart(was, next, func(a, b time.Time) bool {
		return !a.IsZero() && now.Sub(a) < seenEvery && !b.Before(a)
	})
}

// sameButSeen says that next differs from was in when accounts were last
// seen alone, however long ago.
func sameButSeen(was, next []savedLogin) bool {
	return seenApart(was, next, func(a, b time.Time) bool { return !a.IsZero() })
}

// seenApart says that next is was with Seen moved, each move one that
// let allows.
func seenApart(was, next []savedLogin, let func(was, next time.Time) bool) bool {
	if len(was) != len(next) {
		return false
	}
	norm := func(ls []savedLogin) []savedLogin {
		out := slices.Clone(ls)
		for i := range out {
			out[i].ID = loginID(out[i])
		}
		sortLogins(out)
		return out
	}
	a, b := norm(was), norm(next)
	for i := range b {
		if a[i].ID != b[i].ID {
			return false
		}
		if !b[i].Seen.Equal(a[i].Seen) {
			if !let(a[i].Seen, b[i].Seen) {
				return false
			}
			b[i].Seen = a[i].Seen
		}
	}
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

// SignedOut says that p is a signed-in account whose every account in use
// (on, or the one the agent is signed in to) has had its sign-in refused:
// listed, so it can be signed in again, but not "signed in". It is what
// the app's Providers row says as "{user} is signed out", for magpie
// providers and the TUI to say the same (LeeHom on Discord: Claude Code
// listed on two lapsed accounts).
func (p Provider) SignedOut() bool {
	if p.Account == nil {
		return false
	}
	ls := Logins(p.Account.Agent)
	if _, ok := PluginOf(p.ID); ok && p.IsPlugin() {
		ls = Logins(p.ID)
	}
	n := 0
	for _, l := range ls {
		if !l.On && !l.Active {
			continue
		}
		if l.Lapsed == "" {
			return false
		}
		n++
	}
	return n > 0
}

// Logins lists the remembered accounts of an agent ("" for every one),
// the active one flagged. A request lists an agent's once: /api/providers
// asks it of every provider, twice with its account models, and each
// took logins.json's lock, a file opened across magpies (a seventh of
// the list's CPU). The caller gets its own copy.
func Logins(agent string) []Login {
	return slices.Clone(heldOf("logins\x00"+agent, func() []Login { return loginsNow(agent) }))
}

// loginsRuns counts the lists Logins made (a test reads it).
var loginsRuns atomic.Int64

func loginsNow(agent string) []Login {
	loginsRuns.Add(1)
	if settings.AccountDiscoveryOff() {
		return nil
	}
	if pp, ok := pluginOfAgent(agent); ok {
		return pluginLoginList(pp)
	}
	var side []Login
	switch agent {
	case "copilot":
		return copilotLoginList()
	case ChatGPTAPIID:
		return siwcLoginList()
	case "gemini", "antigravity":
		return googleLoginList(agent)
	case "":
		// a plugin's accounts go by its provider's id (a moved built-in's
		// by the built-in's), the one in use first marked, as its own page
		// lists them
		byPlugin := map[string][]Login{}
		for _, pp := range plugin.Cached() {
			for _, l := range pluginLoginList(pp) {
				l.Agent = PluginID(pp.ID)
				byPlugin[pp.ID] = append(byPlugin[pp.ID], l)
			}
		}
		// a built-in moved onto its plugin lists its accounts there (an
		// agent's own sign-in, which the built-in still finds, too); a
		// retired one (no list) keeps its place, its plugin serving it
		for _, b := range []struct {
			id   string
			list func() []Login
		}{
			{"grok", nil}, {"copilot", copilotLoginList}, {"zcode", nil}, {"kiro", nil},
			{"devin", nil}, {"workbuddy", nil}, {WorkBuddyAIID, nil}, {CommandCodePlanID, nil},
			{"qoder", nil}, {QoderCNID, nil}, {"zed", nil}, {"factory", nil},
			{MiMoID, nil}, {ChatGPTAPIID, siwcLoginList}, {"gemini", func() []Login { return googleLoginList("gemini") }},
			{"antigravity", func() []Login { return googleLoginList("antigravity") }},
		} {
			if !PluginServes(b.id) {
				side = append(side, b.list()...)
				continue
			}
			// a moved built-in's, from its plugin, where the built-in's stood
			side = append(side, byPlugin[b.id]...)
			delete(byPlugin, b.id)
		}
		// the other plugins' after them
		for _, pp := range plugin.Cached() {
			side = append(side, byPlugin[pp.ID]...)
			delete(byPlugin, pp.ID)
		}
	}
	rememberLogins(false)
	loginsMu.Lock()
	defer loginsMu.Unlock()
	active := map[string]string{}
	var liveCodex json.RawMessage
	for _, a := range loginAgents {
		if l, ok := liveLogin(a); ok {
			active[a] = l.User
			if a == "codex" {
				liveCodex = l.Auth
			}
		}
	}
	var out []Login
	ls := readLogins()
	standIn := ""
	if _, ok := active["claude"]; !ok {
		// the one the provider is listed on (claudeStandInAccount), a
		// signed-out one too, which the row then says is signed out
		standIn = claudeListed(ls)
	}
	back := map[string]string{}
	for a, user := range active {
		// only where magpie goes back to it (returnsToFirst): a return
		// kept from before stays on record, for In order or KeepLogin
		if r, ok := loginReturnOf(a, user); ok && returnsToFirst(a) {
			back[a] = r.Back
		}
	}
	for _, l := range ls {
		if (agent != "" && l.Agent != agent) || sideAgent(l.Agent) || strings.HasPrefix(l.Agent, "plugin:") {
			continue
		}
		using := strings.EqualFold(active[l.Agent], l.User)
		first := l.Agent == "claude" && strings.EqualFold(standIn, l.User)
		lg := Login{Agent: l.Agent, User: l.User, Plan: l.Plan, Seen: l.Seen, Active: using, On: using || first || l.On,
			Paused: (using || first) && pausedOwn(ls, l.Agent, l.User), first: first}
		if !using && l.Agent == "codex" {
			lg.home = l.Home
		}
		if l.Agent == "codex" {
			// from the sign-in Codex holds, where it holds this one: it
			// refreshes it there, and logins.json's copy may be older
			auth := l.Auth
			if using {
				auth = liveCodex
			} else if home := codexHeldIn(l); home != "" {
				if in, ok := codexLoginIn(home); ok {
					auth = in.Auth
				}
			}
			lg.Until = codexUntil(auth, time.Now())
		}
		if l.Agent == "claude" {
			lg.Lapsed = claudeSignedOut(l)
			lg.Token = claudeSetupToken(l.Auth) != ""
		} else {
			// the one in use too, its sign-in revoked (#1793)
			lg.Lapsed = revokedLapse(l)
		}
		if !using {
			if l.Agent != "claude" {
				lg.Lapsed = l.Lapsed
			}
			if l.Agent == "codex" && l.AccessOnly {
				lg.AccessOnly = true
				// run out with no request to say so yet: passed over now
				if lg.Lapsed == "" && accessOnlyExpired(l.Auth, time.Now()) {
					lg.Lapsed = accessOnlyLapse(l.User)
				}
			}
			lg.Returns = l.On && strings.EqualFold(back[l.Agent], l.User)
		}
		out = append(out, lg)
	}
	return append(out, side...)
}

// InUseLogin is the account of an agent's the gateway goes to first: the
// one the agent is signed in to, unless it is paused or has used its
// allowance up — then the next one on that still has room — else the first
// other one on; "" when the agent has none.
// Kept signed in to one of the user's choosing, it is the first in the
// order that is in use.
func InUseLogin(agent string) string {
	return inUseLogin(agent, loginRoom(agent))
}

// FirstLogin is the account of an agent's first in the gateway's order,
// as InUseLogin, by the order alone: the one the agent is signed in to
// unless it is paused, else the first other one on; kept signed in to one
// of the user's choosing, the first on in the order (#1957). An account
// spent is still named, so a list that names it isn't rewritten each time
// an allowance is read. "" when the agent has none, or is none whose
// own sign-in is paused for its others (Claude Code's and Codex's).
func FirstLogin(agent string) string {
	if !slices.Contains(loginAgents, agent) {
		return ""
	}
	return inUseLogin(agent, nil)
}

func inUseLogin(agent string, room func(user string) (known, spent bool)) string {
	ls := Logins(agent)
	if keptAs(agent) != "" {
		for _, l := range ls {
			if (l.Active || l.On) && !l.Paused && l.Lapsed == "" {
				return l.User
			}
		}
	}
	return inUseOf(ls, room)
}

func inUseOf(ls []Login, room func(user string) (known, spent bool)) string {
	for _, l := range ls {
		if (l.Active || l.first) && !l.Paused {
			// The account the agent is signed in to leads, but magpie moves
			// the sign-in only every few minutes (SwitchWhenSpent, and for
			// the agents it signs in at all), while the gateway moves its
			// requests as soon as an account is spent. So the one in use can
			// still be spent here; the menu bar watching it would show an
			// allowance already gone. When the account the agent is on has
			// used up, and another is on with room, the gateway goes to that
			// one, so report it. An allowance not known is never taken for
			// spent, so a single account or a read that failed is left as it
			// was. room is nil where the caller has no allowances to weigh.
			if room != nil {
				if known, spent := room(l.User); known && spent {
					if next, ok := nextWithRoom(ls, l.User, room); ok {
						return next
					}
				}
			}
			return l.User
		}
	}
	for _, l := range ls {
		if l.On && !l.Paused {
			return l.User
		}
	}
	return ""
}

// nextWithRoom is the account inUseOf moves to when the one in use is spent:
// the first other one the gateway could take a request to — on, not paused,
// not lapsed — whose allowance is known and not spent, the spares NextLogin
// picks among. ok is false when none has room, and the account in use is
// kept.
func nextWithRoom(ls []Login, spent string, room func(user string) (known, spent bool)) (string, bool) {
	for _, l := range ls {
		if !l.On || l.Paused || l.Lapsed != "" || strings.EqualFold(l.User, spent) {
			continue
		}
		if known, used := room(l.User); known && !used {
			return l.User, true
		}
	}
	return "", false
}

// loginRoom weighs an account against the share the gateway counts it spent
// at (loginSwitching, the same SpentShareOf its routing). It reads the
// allowances the gateway routes by, so the account reported as in use and
// the one the gateway sends to agree on which is out (#209), and the menu
// bar's "account in use" card follows the gateway rather than the sign-in
// that lags behind it.
func loginRoom(agent string) func(user string) (known, spent bool) {
	al := Allowances(agent)
	share, _ := loginSwitching(agent)
	now := time.Now()
	return func(user string) (bool, bool) {
		a, ok := al[user]
		if !ok {
			return false, false // not read yet: unknown, never taken for spent
		}
		// The account-wide windows (For's "" model), as the gateway's
		// usedPast does; a window whose reset passed is empty again there.
		used, _ := a.For("", now)
		return true, used >= share
	}
}

// SwitchLogin signs an agent in to a remembered account. Sessions of the
// agent that are already running keep the account they started with until
// they restart; so does Codex's background app-server, which new Codex
// sessions attach to (CodexDaemonStale says when it is).
func SwitchLogin(agent, user string) error {
	if settings.AccountDiscoveryOff() {
		return errAccountDiscoveryOff
	}
	// the user's own choice: magpie doesn't sign the agent back in to the
	// account it moved it off
	if slices.Contains(loginAgents, agent) {
		setLoginReturn(agent, loginReturn{})
	}
	if err := switchLogin(agent, user); err != nil {
		return err
	}
	servedMoved()
	return nil
}

func switchLogin(agent, user string) error { return switchLoginBy(agent, user, false) }

// switchLoginBy is switchLogin, byMagpie saying magpie made the switch by
// itself (SwitchWhenSpent) rather than the user: the Codex app is then
// restarted to follow it only when the user asked for that (RestartApp).
func switchLoginBy(agent, user string, byMagpie bool) error {
	if pp, ok := pluginOfAgent(agent); ok {
		return switchPluginLogin(pp, user)
	}
	switch agent {
	case "copilot":
		return switchCopilotLogin(user)
	case ChatGPTAPIID:
		return switchSIWCLogin(user)
	case "gemini", "antigravity":
		return switchGoogleLogin(agent, user)
	}
	from, err := switchSavedLogin(agent, user)
	if err == nil && agent == "codex" && from != "" {
		noteCodexSwitchBy(from, user, byMagpie)
	}
	if err == nil && agent == "claude" {
		// the account's own way out goes with it (#1807)
		if p, perr := SyncClaudeProxyEnv(user); perr != nil {
			log.Printf("claude: writing %s's proxy to settings.json: %v", user, perr)
		} else if p != "" {
			log.Printf("claude: Claude Code sessions started from now on go through %s's proxy %s", user, netproxy.Redact(p))
		}
	}
	return err
}

// switchSavedLogin puts a Codex or Claude Code account magpie saved into
// the agent's own store, and answers the account it replaced: "" when there
// was none, or the agent was on that one already.
func switchSavedLogin(agent, user string) (from string, _ error) {
	if agent == "claude" {
		// nor while Claude Code runs in the account's own directory for
		// magpie (inSavedClaudeDir), which may refresh it there
		mu := claudeDirRun(user)
		mu.Lock()
		defer mu.Unlock()
		// nor while another magpie process runs it there (#4532)
		if err := waitClaudeRuns(user); err != nil {
			return "", err
		}
	}
	// not while a saved account is being refreshed: the agent would be
	// given the refresh token that refresh is spending
	savedTokenMu.Lock()
	defer savedTokenMu.Unlock()
	if agent == "codex" {
		// nor while Codex's own sign-in is being refreshed
		// (codexTokenRenewing): the one saved below as the account being
		// replaced would hold the refresh token that refresh is spending,
		// and the refresh would write its account back over the switch
		codexMu.Lock()
		defer codexMu.Unlock()
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	var target *savedLogin
	for i := range ls {
		if ls[i].Agent == agent && strings.EqualFold(ls[i].User, user) {
			target = &ls[i]
		}
	}
	if target == nil {
		return "", fmt.Errorf("no saved %s account %q", agent, user)
	}
	if home := codexHeldIn(*target); home != "" {
		// Codex there keeps it signed in and refreshes it: a copy in
		// ~/.codex would be a second holder of one refresh token, and
		// whichever refreshed first would sign the other out
		return "", fmt.Errorf("%s is signed in to Codex in %s: run Codex there (CODEX_HOME=%s codex) to use it, or turn it on for magpie's requests", target.User, home, home)
	}
	if agent == "claude" && claudeSetupToken(target.Auth) != "" {
		return "", fmt.Errorf(errClaudeTokenSwitch, target.User)
	}
	if agent == "codex" && target.AccessOnly {
		return "", fmt.Errorf(errCodexAccessOnlySwitch, target.User)
	}
	if agent == "claude" {
		// as Claude Code keeps it, if it has run on the account beside the
		// one it is signed in to; never one that can't be used, which
		// Claude Code would only be refused on
		_, changed, err := syncClaudeDir(target)
		if err != nil {
			return "", err
		}
		if why := claudeSignedOut(*target); why != "" {
			if changed {
				_ = writeLogins(ls)
			}
			return "", fmt.Errorf("%s: %s", target.User, why)
		}
	}
	want := *target
	if agent == "claude" {
		// not while Claude Code refreshes its sign-in: the one read below
		// to keep would be the refresh token that refresh is spending
		// (#4532). Held till the new sign-in is written, so the refresh
		// that follows starts from it.
		release, err := claudeRefreshLocks()
		if err != nil {
			return "", err
		}
		defer release()
		// the credentials as Claude Code has them this moment, not as last
		// looked at: one it refreshed since has a new refresh token and the
		// old one is dead (0xAncientTwo on X: signed out again and again)
		forgetClaudeCredential()
	}
	if agent == "codex" {
		// so for Codex's keychain item, which Codex may have refreshed
		forgetCodexKeychain()
	}
	if live, ok := liveLogin(agent); ok {
		if strings.EqualFold(live.User, want.User) {
			return "", nil
		}
		from = live.User
		// the credentials being replaced, as fresh as the agent has them;
		// in use still if the one taking over was: it is next in line now
		live.Seen = time.Now().UTC().Truncate(time.Second)
		ls = upsertLogin(ls, live)
		for i := range ls {
			if ls[i].Agent == agent && strings.EqualFold(ls[i].User, live.User) {
				ls[i].On, ls[i].Paused = want.On, false
			}
		}
		if err := writeLogins(ls); err != nil {
			return "", err
		}
	}
	var err error
	switch agent {
	case "codex":
		// where Codex's config.toml keeps it (#2367); never over a sign-in
		// there that couldn't be read
		err = saveCodexLogin(codexHome(), want.Auth)
	case "claude":
		if err = putClaudeLogin(want); err == nil {
			// Claude Code's own now: its only holder
			forgetClaudeDir(want.User)
			claudeHandedOver(want.User)
		}
	default:
		err = fmt.Errorf("%s accounts can't be switched", agent)
	}
	if err != nil {
		return "", err
	}
	loginsSeenAt.Store(0)
	forgetAccountCaches()
	return from, nil
}

// putClaudeLogin signs Claude Code in to l. The caller holds Claude Code's
// refresh locks (claudeRefreshLocks).
func putClaudeLogin(l savedLogin) error {
	if claudeSetupToken(l.Auth) != "" {
		return fmt.Errorf(errClaudeTokenSwitch, l.User)
	}
	c, ok := parseClaudeCredentials(l.Auth)
	if !ok {
		return errors.New("the saved Claude Code sign-in is unreadable")
	}
	_, loc, found := readClaudeCredential()
	if !found {
		// signed out: put it where Claude Code keeps it on this system
		dir := appdir.ClaudeDir()
		loc = claudeCredentialLocation{path: filepath.Join(dir, ".credentials.json")}
		if claudeKeychain {
			loc = claudeCredentialLocation{keychain: true, account: claudeKeychainAccount()}
		} else if err := os.MkdirAll(dir, 0o700); err != nil {
			// Claude Code never run here yet
			return err
		}
	}
	if err := saveClaudeCredential(loc, c); err != nil {
		return err
	}
	if len(l.Profile) == 0 {
		return nil
	}
	// read and written back under the lock Claude Code saves the file
	// under, or a save of its own between the two is lost
	release, err := claudeConfigLock()
	if err != nil {
		return err
	}
	defer release()
	m, err := readClaudeProfile()
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		m = map[string]any{}
	}
	d := json.NewDecoder(bytes.NewReader(l.Profile))
	d.UseNumber()
	var acct map[string]any
	if err := d.Decode(&acct); err != nil {
		return err
	}
	m["oauthAccount"] = acct
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(claudeProfilePath(), append(b, '\n'))
}

// ForgetLogin drops a remembered account. The one an agent is signed in to
// now would only be remembered again, so Codex and Claude Code are signed
// in to another of their accounts first (nextOnForget), as its Use would
// (StringKe, #4532: Claude's was refused while Codex's switched). Codex's
// last one is signed out instead, as `codex logout` does (mamba on
// Discord: a single Codex account couldn't be removed at all). Claude
// Code's last one that it can be signed in to is kept, and the removal
// refused (SignedInError, Only): magpie never signs Claude Code out.
func ForgetLogin(agent, user string) error {
	if settings.AccountDiscoveryOff() {
		return errAccountDiscoveryOff
	}
	if pp, ok := pluginOfAgent(agent); ok {
		return forgetPluginLogin(pp, user)
	}
	switch agent {
	case "copilot":
		return forgetCopilotLogin(user)
	case ChatGPTAPIID:
		return forgetSIWCLogin(user)
	case "gemini", "antigravity":
		return forgetGoogleLogin(agent, user)
	}
	if agent == "codex" || agent == "claude" {
		// the account the agent is signed in to, with another saved: the
		// agent is signed in to that one first, as its Use would, and this
		// one is forgotten then, rather than Codex left signed out or the
		// removal refused (vincentzhang1_55530 on Discord: a Team account
		// removed beside a Plus one, and Codex was at its sign-in screen).
		// A switch that fails removes nothing: the agent is still on user.
		if next := nextOnForget(agent, user); next != "" {
			if err := SwitchLogin(agent, next); err != nil {
				return fmt.Errorf("%s is signed in to %s now, and signing it in to %s first failed, so %s is kept: %w", loginAgentName(agent), user, next, user, err)
			}
		}
	}
	signedOut, err := forgetLogin(agent, user)
	if signedOut {
		// gone from the agent too: nothing of it is served any more
		ForgetAccounts()
	}
	return err
}

// nextOnForget is the account an agent is signed in to in place of user,
// which is being removed: "" when it isn't signed in to user, or has no
// other saved that it can be signed in to (switchable). The first other in
// the order that is on, else one whose sign-in still holds, else any other.
func nextOnForget(agent, user string) string {
	ls := Logins(agent)
	if !slices.ContainsFunc(ls, func(l Login) bool { return l.Active && strings.EqualFold(l.User, user) }) {
		return ""
	}
	ls = slices.DeleteFunc(ls, func(l Login) bool { return !switchable(l) })
	for _, fit := range []func(Login) bool{
		func(l Login) bool { return l.On && !l.Paused && l.Lapsed == "" },
		func(l Login) bool { return l.Lapsed == "" },
		func(Login) bool { return true },
	} {
		for _, l := range ls {
			if !strings.EqualFold(l.User, user) && fit(l) {
				return l.User
			}
		}
	}
	return ""
}

// switchable is an account an agent can be signed in to: not a Claude
// setup-token one (errClaudeTokenSwitch), nor a ChatGPT account Codex
// keeps in another folder (codexHeldIn).
func switchable(l Login) bool { return !l.Token && !l.AccessOnly && l.home == "" }

func forgetLogin(agent, user string) (signedOut bool, err error) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	if live, ok := liveLogin(agent); ok && strings.EqualFold(live.User, user) {
		other := slices.ContainsFunc(ls, func(l savedLogin) bool { return l.Agent == agent && !strings.EqualFold(l.User, user) })
		if agent == "claude" {
			// Claude Code is never signed out: its sign-in may be in the
			// keychain, and magpie serves its saved accounts while it is
			// signed out (claudeStandIn). Still on user, after a switch
			// to another (ForgetLogin): that one couldn't be signed in to.
			other = slices.ContainsFunc(ls, func(l savedLogin) bool {
				return l.Agent == agent && !strings.EqualFold(l.User, user) && claudeSetupToken(l.Auth) == ""
			})
			return false, &SignedInError{Agent: agent, User: user, Only: !other}
		}
		if agent == "codex" {
			// nor one Codex keeps in another folder, which ~/.codex
			// can't be signed in to (codexHeldIn)
			other = slices.ContainsFunc(ls, func(l savedLogin) bool {
				return l.Agent == agent && !strings.EqualFold(l.User, user) && codexHeldIn(l) == ""
			})
		}
		if agent != "codex" || other {
			return false, &SignedInError{Agent: agent, User: user}
		}
		// as `codex logout` does: the sign-in goes, all of it, from the
		// keychain item and auth.json both (#2367)
		if err := deleteCodexLogin(codexHome()); err != nil {
			return false, err
		}
		signedOut = true
	}
	for _, l := range ls {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			if home := codexHeldIn(l); home != "" {
				// it would be read again from there at the next look: that
				// folder's sign-in is set aside instead, its files left as
				// they are (charliepu's .codex-db-backup-…, which no Codex
				// runs in, refused with "codex logout")
				in, _ := codexLoginIn(home)
				if err := setCodexAside(home, in.Auth); err != nil {
					return false, fmt.Errorf("removing %s, signed in to Codex in %s: %w", l.User, home, err)
				}
			}
		}
	}
	out := ls[:0]
	found := false
	for _, l := range ls {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			found = true
			continue
		}
		out = append(out, l)
	}
	if !found {
		if signedOut {
			return true, nil // signed in, not saved yet
		}
		return false, fmt.Errorf("no saved %s account %q", agent, user)
	}
	if agent == "claude" {
		forgetClaudeDir(user)
	}
	return signedOut, writeLogins(out)
}

// SignedInError is ForgetLogin's refusal of the account an agent is
// signed in to now while it has another: that one is signed in to first
// (its Use), and this one removed then. Only is Claude Code's last account
// it can be signed in to, which magpie keeps rather than sign Claude Code
// out. The GUI says it in the reader's language (codes signed_in and
// signed_in_only).
type SignedInError struct {
	Agent, User string
	Only        bool
}

func (e *SignedInError) Error() string {
	if e.Only {
		return fmt.Sprintf("%s is signed in to %s, the only account in magpie it can be signed in to, and magpie doesn't sign it out: add another account first, or sign out in %s itself, then remove this one", loginAgentName(e.Agent), e.User, loginAgentName(e.Agent))
	}
	return fmt.Sprintf("%s is signed in to %s now: sign it in to another of its accounts first (Use on that account), then remove this one", loginAgentName(e.Agent), e.User)
}

// loginAgentName is a switchable agent's name as its accounts list says it.
func loginAgentName(agent string) string {
	switch agent {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude Code"
	}
	return agent
}

// ForgetAccounts makes the next look at the accounts read them afresh, for
// a caller that changed a sign-in behind magpie's back (a test's home).
func ForgetAccounts() {
	loginsMu.Lock()
	loginsSeenAt.Store(0)
	loginsMu.Unlock()
	// an account asked for its list a moment ago is asked again: the
	// accounts read afresh may be others under the same id
	newFetches.Lock()
	clear(newFetches.m)
	newFetches.Unlock()
	forgetAccountCaches()
	// Codex's keychain item is asked again (#2367)
	forgetCodexKeychain()
	// the revoked sign-ins known are read from logins.json again
	resetRevoked()
}

// forgetAccountCaches makes the next look at the accounts read them afresh.
func forgetAccountCaches() {
	Changed() // the providers a request holds (All)
	forgetClaudeCredential()
	forgetClaudeStatus()
	forgetCursorStatus()
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.at = time.Time{}
	subscriptionUsageCache.data = nil
	subscriptionUsageCache.Unlock()
}

// claudeSignInOfAnother says the Claude sign-in l carries is another saved
// account's, not the one its profile names: Claude Code's credential and
// ~/.claude.json were read from two moments, as when magpie switches
// Claude Code (keeping it signed in to one account) and a Claude Code
// started on the account before writes that one's profile after. Saved,
// it put the one account's sign-in and plan under the other's name, whose
// runs and Usage card then were the first's (netfishx on X). The account
// the profile names holding the sign-in already, it is that one's.
func claudeSignInOfAnother(ls []savedLogin, l savedLogin) bool {
	same, others := claudeHolders(ls, l)
	return !same && len(others) > 0
}

// claudeHolders are the saved Claude accounts holding the sign-in l
// carries: same when l's own record (sameLogin) is one of them, others the
// rest.
func claudeHolders(ls []savedLogin, l savedLogin) (same bool, others []savedLogin) {
	if l.Agent != "claude" {
		return false, nil
	}
	c, ok := parseClaudeCredentials(l.Auth)
	tok := claudeSetupToken(l.Auth)
	if !ok && tok == "" {
		return false, nil
	}
	holds := func(x savedLogin) bool {
		if tok != "" {
			return claudeSetupToken(x.Auth) == tok
		}
		o, ok := parseClaudeCredentials(x.Auth)
		return ok && (c.OAuth.AccessToken != "" && o.OAuth.AccessToken == c.OAuth.AccessToken ||
			c.OAuth.RefreshToken != "" && o.OAuth.RefreshToken == c.OAuth.RefreshToken)
	}
	for _, x := range ls {
		if x.Agent != "claude" || !holds(x) {
			continue
		}
		if sameLogin(x, l) {
			return true, nil
		}
		others = append(others, x)
	}
	return false, others
}

// claudeSignInOwner is the account Claude Code is signed in to by its
// sign-in, l named by its profile: when the profile is another's
// (claudeSignInOfAnother) and one saved account alone holds the sign-in,
// that one. Named by the profile, the account switched to was taken for
// the one before: its /usage went through the other's proxy and was shown
// on the other's card (#1938).
//
// Held by none, the sign-in is new: Claude Code signed in anew, or renewed
// the one it had, whose tokens are new too. Under a profile that names
// another account than the one Claude Code held at the last look, and the
// very profile it named then (HeldAs), it is the held account's renewed:
// a /login writes its own account's profile, while a Claude Code started
// on the other account before a switch kept writing that one's. Named by
// the profile, the account the user made first was saved with the other's
// name and shown as that one, and the other's record took its tokens
// (motui9535: made first in Providers, then a refresh of Usage, whose
// /usage renewed the sign-in, turned the first back).
func claudeSignInOwner(ls []savedLogin, l savedLogin) savedLogin {
	same, others := claudeHolders(ls, l)
	switch {
	case !same && len(others) == 1:
		l.User, l.Profile = others[0].User, others[0].Profile
	case !same && len(others) == 0:
		if h, ok := claudeRenewedHeld(ls, l); ok {
			l.User, l.Profile = h.User, h.Profile
		}
	}
	return l
}

// claudeRenewedHeld is the Held account whose sign-in Claude Code renewed,
// when l, held by no saved account, is that (claudeSignInOwner): its
// profile is the one read at the last look, and names another account
// than the held one; the plan, when both say one, the held one's.
func claudeRenewedHeld(ls []savedLogin, l savedLogin) (savedLogin, bool) {
	key := claudeProfileKey(l.Profile)
	if l.Agent != "claude" || key == "" {
		return savedLogin{}, false
	}
	c, ok := parseClaudeCredentials(l.Auth)
	if !ok {
		return savedLogin{}, false
	}
	for _, h := range ls {
		if h.Agent != "claude" || !h.Held || h.HeldAs != key || sameLogin(h, l) || claudeSetupToken(h.Auth) != "" {
			continue
		}
		if plan := c.OAuth.plan(); plan != "" && h.Plan != "" && !strings.EqualFold(plan, h.Plan) {
			return savedLogin{}, false
		}
		return h, true
	}
	return savedLogin{}, false
}

// claudeProfileKey is the account a ~/.claude.json profile names, by
// email and organization: "" for none.
func claudeProfileKey(profile json.RawMessage) string {
	email, org := claudeWho(profile)
	if strings.TrimSpace(email) == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email)) + " " + org
}
