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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/plugin"
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
	// Own is the agent's own sign-in, which magpie only reads: removed, it
	// is hidden rather than deleted (side_logins.go).
	Own bool `json:"own,omitempty"`
	// Paused is the account the agent is signed in to, passed over by the
	// gateway while another is on (savedLogin.Paused).
	Paused bool `json:"paused,omitempty"`
}

type savedLogin struct {
	Agent string    `json:"agent"`
	User  string    `json:"user"`
	Plan  string    `json:"plan,omitempty"`
	Seen  time.Time `json:"seen"`
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
	// Renewed is when magpie last refreshed a saved account's sign-in, and
	// Lapsed why the vendor last refused to (logins_on.go, keepalive.go).
	Renewed time.Time `json:"renewed,omitzero"`
	Lapsed  string    `json:"lapsed,omitempty"`
	// Hidden is the agent's own sign-in removed in magpie, with the mark
	// of the sign-in it was (side_logins.go): it is listed and tried no
	// more until the agent signs in anew. The agent's files stay as they are.
	Hidden string `json:"hidden,omitempty"`
	// Paused is set on the account the agent is signed in to when the user
	// paused it in magpie (#263): the gateway passes over it while another
	// of the agent's accounts is on, the agent staying signed in to it.
	Paused bool `json:"paused,omitempty"`
}

var (
	loginsMu     sync.Mutex
	loginsSeenAt time.Time
)

// switchable agents: those whose sign-in magpie can save and put back.
var loginAgents = []string{"claude", "codex"}

func loginsPath() string { return filepath.Join(filepath.Dir(Path()), "logins.json") }

func readLogins() []savedLogin {
	// parsed once until the file changes: a state of the page asks for it
	// dozens of times (every agent's drift and models), and with the
	// accounts' credentials in it the file is large — a Save of a profile
	// waited seconds on it
	ls, _ := filememo.Read("logins", loginsPath(), func(b []byte) ([]savedLogin, error) {
		var out []savedLogin
		_ = json.Unmarshal(b, &out)
		// DimAgent's accounts: magpie no longer signs in to it (DimAgent
		// doesn't allow its subscription used outside its client), so one
		// signed in before is left out, and gone from the file at its next write
		out = slices.DeleteFunc(out, func(l savedLogin) bool { return l.Agent == "dimagent" })
		return dedupeLogins(out), nil
	})
	return slices.Clone(ls) // callers change theirs
}

func writeLogins(ls []savedLogin) error {
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Agent != ls[j].Agent {
			return ls[i].Agent < ls[j].Agent
		}
		return strings.ToLower(ls[i].User) < strings.ToLower(ls[j].User)
	})
	b, err := json.MarshalIndent(ls, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(loginsPath(), append(b, '\n'))
}

// writePrivate replaces a file readable by the user alone, atomically, so
// an agent reading it at that moment sees either version, never half.
func writePrivate(path string, b []byte) error {
	path, err := edit.Target(path) // a symlink stays, its target written
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func upsertLogin(ls []savedLogin, l savedLogin) []savedLogin {
	for i := range ls {
		if sameLogin(ls[i], l) {
			l.On = l.On || ls[i].On
			l.Paused = l.Paused || ls[i].Paused
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

// codexWho reads the email and workspace of a Codex auth.json.
func codexWho(auth json.RawMessage) (email, workspace string) {
	var a codexAuth
	if json.Unmarshal(auth, &a) != nil {
		return "", ""
	}
	id := jwtClaims(a.Tokens.IDToken)
	workspace = a.Tokens.AccountID
	if workspace == "" {
		workspace = claimString(id, "https://api.openai.com/auth", "chatgpt_account_id")
	}
	return claimString(id, "email"), workspace
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

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// claudeProfilePath is Claude Code's global state file, which holds the
// signed-in account's identity next to much else.
func claudeProfilePath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

func readClaudeProfile() (map[string]any, error) {
	b, err := os.ReadFile(claudeProfilePath())
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
		if saved[a] == 0 {
			continue
		}
		if _, ok := liveLogin(a); ok {
			continue
		}
		why, signIn := "nothing at "+codexAuthPath(), "codex login"
		if a == "claude" {
			why, signIn = claudeNotSignedInWhy(), "claude, then /login"
		}
		n := "1 account is"
		if saved[a] > 1 {
			n = fmt.Sprintf("%d accounts are", saved[a])
		}
		out = append(out, Exclusion{Agent: a, SignedOut: true, Users: users[a],
			Why: fmt.Sprintf("%s saved in magpie, but it isn't signed in here (%s), and they are only offered beside the account it is signed in to. Sign in (%s) with this HOME.", n, why, signIn)})
	}
	return out
}

// claudeNotSignedInWhy says which of liveLogin's checks found no Claude Code
// sign-in: its credentials (on a Mac, the keychain first), what
// `claude auth status` says, or the account's name.
func claudeNotSignedInWhy() string {
	c, _, ok := claudeCredential()
	if !ok {
		if claudeKeychain {
			return `no "Claude Code-credentials" in the keychain magpie could read, and nothing at ` + claudeCredentialsPath()
		}
		return "nothing at " + claudeCredentialsPath()
	}
	user, plan, signedOut := claudeIdentity()
	if signedOut {
		return "its credentials are there, but claude auth status says no one is signed in"
	}
	if u, _ := claudeSignedInUser(c.OAuth.SubscriptionType, plan, user); u == "" {
		return "its credentials are there, but neither " + claudeProfilePath() + " nor claude auth status names the account"
	}
	return "its sign-in could not be read"
}

// liveLogin reads the account an agent is signed in to now.
func liveLogin(agent string) (savedLogin, bool) {
	switch agent {
	case "codex":
		b, err := os.ReadFile(codexAuthPath())
		if err != nil {
			return savedLogin{}, false
		}
		var a codexAuth
		if json.Unmarshal(b, &a) != nil || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
			return savedLogin{}, false
		}
		id := jwtClaims(a.Tokens.IDToken)
		user := codexUser(id)
		if user == "" {
			user = a.Tokens.AccountID
		}
		if user == "" {
			return savedLogin{}, false
		}
		return savedLogin{Agent: agent, User: user, Plan: claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"),
			Auth: json.RawMessage(bytes.TrimSpace(b))}, true
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
		l := savedLogin{Agent: agent, Plan: c.OAuth.SubscriptionType, Auth: b}
		var acct map[string]any
		if l.User, acct = claudeSignedInUser(l.Plan, plan, user); acct != nil {
			l.Profile, _ = json.Marshal(acct)
		}
		if l.User == "" {
			return savedLogin{}, false
		}
		return l, true
	}
	return savedLogin{}, false
}

// rememberLogins saves the accounts the agents are signed in to now. The
// copy of an active account is only a bookmark: the agent keeps refreshing
// its own, and a switch saves that fresher one first.
// accountRemoved: the user removed the agent's account from magpie, which
// then neither saves its sign-ins nor keeps them renewed.
func accountRemoved(agent string) bool {
	return slices.ContainsFunc(load().Providers, func(p Provider) bool { return p.ID == agent && p.Hidden })
}

func rememberLogins(force bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if !force && time.Since(loginsSeenAt) < 30*time.Second {
		return
	}
	loginsSeenAt = time.Now()
	ls := readLogins()
	changed := false
	for _, agent := range loginAgents {
		if accountRemoved(agent) {
			continue
		}
		l, ok := liveLogin(agent)
		if !ok {
			continue
		}
		l.Seen = time.Now().UTC().Truncate(time.Second)
		ls = upsertLogin(ls, l)
		changed = true
	}
	if changed {
		_ = writeLogins(ls)
	}
}

// Logins lists the remembered accounts of an agent ("" for every one),
// the active one flagged.
func Logins(agent string) []Login {
	if pp, ok := pluginOfAgent(agent); ok {
		return pluginLoginList(pp)
	}
	var side []Login
	switch agent {
	case "grok":
		return grokLoginList()
	case "copilot":
		return copilotLoginList()
	case "zcode":
		return zcodeLoginList()
	case "kiro":
		return kiroLoginList()
	case "devin":
		return devinLoginList()
	case "workbuddy", WorkBuddyAIID:
		return wbLoginList(wbSiteOf(agent))
	case CommandCodePlanID:
		return cmdLoginList()
	case "qoder", QoderCNID:
		return loginsOf(qoderLoginsOf(agent))
	case "zed":
		return zedLoginList()
	case "factory":
		return factoryLoginList()
	case MiMoID:
		return mimoLoginList()
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
		// agent's own sign-in, which the built-in still finds, too)
		for _, b := range []struct {
			id   string
			list func() []Login
		}{
			{"grok", grokLoginList}, {"copilot", copilotLoginList}, {"zcode", zcodeLoginList}, {"kiro", kiroLoginList},
			{"devin", devinLoginList}, {"workbuddy", func() []Login { return wbLoginList(wbCN) }},
			{WorkBuddyAIID, func() []Login { return wbLoginList(wbAI) }}, {CommandCodePlanID, cmdLoginList},
			{"qoder", func() []Login { return loginsOf(qoderLogins()) }},
			{QoderCNID, func() []Login { return loginsOf(qoderLoginsOf(QoderCNID)) }}, {"zed", zedLoginList}, {"factory", factoryLoginList},
			{MiMoID, mimoLoginList}, {"gemini", func() []Login { return googleLoginList("gemini") }},
			{"antigravity", func() []Login { return googleLoginList("antigravity") }},
		} {
			if !Moved(b.id) {
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
	for _, a := range loginAgents {
		if l, ok := liveLogin(a); ok {
			active[a] = l.User
		}
	}
	var out []Login
	ls := readLogins()
	for _, l := range ls {
		if (agent != "" && l.Agent != agent) || sideAgent(l.Agent) || strings.HasPrefix(l.Agent, "plugin:") {
			continue
		}
		using := strings.EqualFold(active[l.Agent], l.User)
		lg := Login{Agent: l.Agent, User: l.User, Plan: l.Plan, Seen: l.Seen, Active: using, On: using || l.On,
			Paused: using && pausedOwn(ls, l.Agent, l.User)}
		if !using {
			lg.Lapsed = l.Lapsed
		}
		out = append(out, lg)
	}
	return append(out, side...)
}

// InUseLogin is the account of an agent's the gateway goes to first: the
// one the agent is signed in to, unless it is paused, else the first other
// one on; "" when the agent has none.
func InUseLogin(agent string) string {
	return inUseOf(Logins(agent))
}

func inUseOf(ls []Login) string {
	for _, l := range ls {
		if l.Active && !l.Paused {
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

// SwitchLogin signs an agent in to a remembered account. Sessions of the
// agent that are already running keep the account they started with until
// they restart; so does Codex's background app-server, which new Codex
// sessions attach to (CodexDaemonStale says when it is).
func SwitchLogin(agent, user string) error {
	if pp, ok := pluginOfAgent(agent); ok {
		return switchPluginLogin(pp, user)
	}
	switch agent {
	case "grok":
		return switchGrokLogin(user)
	case "copilot":
		return switchCopilotLogin(user)
	case "zcode":
		return switchZCodeLogin(user)
	case "kiro":
		return switchKiroLogin(user)
	case "devin":
		return switchDevinLogin(user)
	case "workbuddy", WorkBuddyAIID:
		return switchWorkBuddyLogin(wbSiteOf(agent), user)
	case CommandCodePlanID:
		return switchCommandCodeLogin(user)
	case "qoder", QoderCNID:
		return switchSideLogin(agent, user, qoderLoginsOf(agent))
	case "zed":
		return switchZedLogin(user)
	case "factory":
		return switchFactoryLogin(user)
	case MiMoID:
		return switchMiMoLogin(user)
	case "gemini", "antigravity":
		return switchGoogleLogin(agent, user)
	}
	from, err := switchSavedLogin(agent, user)
	if err == nil && agent == "codex" && from != "" {
		noteCodexSwitch(from, user)
	}
	return err
}

// switchSavedLogin puts a Codex or Claude Code account magpie saved into
// the agent's own store, and answers the account it replaced: "" when there
// was none, or the agent was on that one already.
func switchSavedLogin(agent, user string) (from string, _ error) {
	// not while a saved account is being refreshed: the agent would be
	// given the refresh token that refresh is spending
	savedTokenMu.Lock()
	defer savedTokenMu.Unlock()
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
	want := *target
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
		err = writePrivate(codexAuthPath(), append(bytes.TrimSpace(want.Auth), '\n'))
	case "claude":
		err = putClaudeLogin(want)
	default:
		err = fmt.Errorf("%s accounts can't be switched", agent)
	}
	if err != nil {
		return "", err
	}
	loginsSeenAt = time.Time{}
	forgetAccountCaches()
	return from, nil
}

func putClaudeLogin(l savedLogin) error {
	c, ok := parseClaudeCredentials(l.Auth)
	if !ok {
		return errors.New("the saved Claude Code sign-in is unreadable")
	}
	_, loc, found := readClaudeCredential()
	if !found {
		// signed out: put it where Claude Code keeps it on this system
		dir := os.Getenv("CLAUDE_CONFIG_DIR")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".claude")
		}
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
// now can't be forgotten; it would only be remembered again.
func ForgetLogin(agent, user string) error {
	if pp, ok := pluginOfAgent(agent); ok {
		return forgetPluginLogin(pp, user)
	}
	switch agent {
	case "grok":
		return forgetGrokLogin(user)
	case "copilot":
		return forgetCopilotLogin(user)
	case "zcode":
		return forgetZCodeLogin(user)
	case "kiro":
		return forgetKiroLogin(user)
	case "devin":
		return forgetDevinLogin(user)
	case "workbuddy", WorkBuddyAIID:
		return forgetWorkBuddyLogin(wbSiteOf(agent), user)
	case CommandCodePlanID:
		return forgetCommandCodeLogin(user)
	case "qoder", QoderCNID:
		return forgetQoderLogin(agent, user)
	case "zed":
		return forgetZedLogin(user)
	case "factory":
		return forgetFactoryLogin(user)
	case MiMoID:
		return forgetMiMoLogin(user)
	case "gemini", "antigravity":
		return forgetGoogleLogin(agent, user)
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if live, ok := liveLogin(agent); ok && strings.EqualFold(live.User, user) {
		return fmt.Errorf("%s is signed in to %s now; switch to another account first", agent, user)
	}
	ls := readLogins()
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
		return fmt.Errorf("no saved %s account %q", agent, user)
	}
	return writeLogins(out)
}

// ForgetAccounts makes the next look at the accounts read them afresh, for
// a caller that changed a sign-in behind magpie's back (a test's home).
func ForgetAccounts() {
	loginsMu.Lock()
	loginsSeenAt = time.Time{}
	loginsMu.Unlock()
	forgetAccountCaches()
}

// forgetAccountCaches makes the next look at the accounts read them afresh.
func forgetAccountCaches() {
	forgetClaudeCredential()
	forgetClaudeStatus()
	forgetCursorStatus()
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.at = time.Time{}
	subscriptionUsageCache.data = nil
	subscriptionUsageCache.Unlock()
}
