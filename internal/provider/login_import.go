package provider

// ChatGPT (Codex) and Claude accounts brought in from another tool's files
// rather than signed in again: CLIProxyAPI keeps each as
// {type: "codex", id_token, access_token, refresh_token, account_id, email, …}
// or {type: "claude", access_token, refresh_token, email, …}, Codex CLI as
// auth.json ({tokens: {…}}), Claude Code as .credentials.json
// ({claudeAiOauth: {…}}), and Sub2API as {accounts: [{platform, type:
// "oauth", credentials: {…}}]}. Cockpit Tools (jlcodes99/cockpit-tools)
// exports its Codex accounts in these shapes: its own (CLIProxyAPI's),
// Codex's auth.json, CPA and Sub2API. codexbar (lizhelang/codexbar), a
// menu-bar switcher, keeps its ChatGPT accounts in ~/.codexbar/config.json
// (see codexbarEntries); its own export is Sub2API's. claude-swap (cswap,
// realiti4/claude-swap) exports its Claude accounts as {version: 1,
// swapVersion, encrypted, accounts: [{email, uuid, organizationUuid,
// organizationName, credentials: {claudeAiOauth: {…}}, config:
// {oauthAccount: {…}}, alias, kind}]} (see cswapEntry). A bare refresh token
// a line is taken too. They all sign in with the agents' own OAuth clients, the ones
// magpie uses.
//
// A ChatGPT one is checked the way a sign-in is finished: its refresh token
// traded for new tokens and the account asked for. That rotates the token,
// so the file it came from stops working: the account is magpie's from then
// on, as a sign-in in magpie would make it. A Claude one is kept as it
// came, magpie asking Anthropic nothing; Claude Code refreshes it the first
// time it runs on it. Tokens are never logged or sent back.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// loginImport is one account read from a file.
type loginImport struct {
	email, idToken, accessToken, refreshToken, accountID string
	scopes                                               []string
	err                                                  string
	file                                                 int // a file that held no account (ImportedAccount.File)
	// claudeAccount and claudeOrg: the Claude account's and organization's
	// uuids, Claude Code's oauthAccount accountUuid and organizationUuid,
	// when the file has them
	claudeAccount, claudeOrg string
	// claudeOrgName is the organization's name, as oauthAccount has it;
	// alias the name the tool it came from gave the account (cswap's)
	claudeOrgName, alias string
	// expiresAt is when the file's Claude access token runs out, in
	// milliseconds, 0 when it doesn't say: a refresh gives a later one,
	// so it tells which of two sign-ins of one account is the newer
	expiresAt int64
}

// jsonMillis is the number under key, 0 when there is none.
func jsonMillis(x map[string]any, key string) int64 {
	switch n := x[key].(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// claudeImportScopes are what Claude Code's sign-in asks for, taken when a
// file doesn't say.
var claudeImportScopes = []string{"user:inference", "user:profile", "user:sessions:claude_code", "user:mcp_servers"}

// parseLoginImport reads the accounts in a file for agent (codex, claude).
func parseLoginImport(agent, data string) ([]loginImport, error) {
	s := strings.TrimSpace(strings.TrimPrefix(data, "\xef\xbb\xbf"))
	if s == "" {
		return nil, errors.New("nothing to import")
	}
	var out []loginImport
	if s[0] == '[' || s[0] == '{' {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, fmt.Errorf("not valid JSON: %v", err)
		}
		if x, ok := v.(map[string]any); ok && x["swapVersion"] != nil {
			// as cswap's own import refuses them (transfer.py)
			if x["encrypted"] == true {
				return nil, errors.New("an encrypted claude-swap export: decrypt it first (gpg -d), then import what that gives")
			}
			if n, ok := x["version"].(float64); !ok || n != 1 {
				return nil, fmt.Errorf("a claude-swap export of format %v: magpie reads format 1 (claude-swap 0.29)", x["version"])
			}
		}
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case []any:
				for _, it := range x {
					walk(it)
				}
			case map[string]any:
				// a Sub2API account: its vendor in platform, its sign-in
				// in credentials
				if c, ok := x["credentials"].(map[string]any); ok && jsonStr(x, "platform") != "" {
					out = append(out, sub2apiEntry(agent, x, c))
					return
				}
				// a cswap account: credentials and config, no platform
				if _, ok := x["config"].(map[string]any); ok && x["credentials"] != nil && jsonStr(x, "email") != "" {
					out = append(out, cswapEntry(agent, x))
					return
				}
				if es, ok := codexbarEntries(agent, x); ok {
					out = append(out, es...)
					return
				}
				if as, ok := x["accounts"].([]any); ok && jsonStr(x, "refresh_token", "refreshToken") == "" {
					walk(as)
					return
				}
				out = append(out, loginEntry(agent, x))
			case string:
				out = append(out, loginImport{refreshToken: strings.TrimSpace(x)})
			default:
				out = append(out, loginImport{err: "not an account"})
			}
		}
		walk(v)
	} else {
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// an address (a sign-in's callback among them) is never sent to
			// the vendor as a token
			if strings.ContainsAny(line, " \t") || looksLikeAddress(line) {
				out = append(out, loginImport{err: "not a refresh token"})
				continue
			}
			out = append(out, loginImport{refreshToken: line})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no accounts in it")
	}
	return out, nil
}

// loginEntry reads one account object.
func loginEntry(agent string, x map[string]any) loginImport {
	name := map[string]string{"codex": "Codex", "claude": "Claude"}[agent]
	e := loginImport{email: jsonStr(x, "email")}
	// Codex CLI's auth.json keeps the sign-in under tokens, Claude Code's
	// credentials under claudeAiOauth
	src := x
	if t, ok := x["tokens"].(map[string]any); ok && agent == "codex" {
		src = t
	}
	if t, ok := x["claudeAiOauth"].(map[string]any); ok && agent == "claude" {
		src = t
	}
	e.idToken = jsonStr(src, "id_token", "idToken")
	e.accessToken = jsonStr(src, "access_token", "accessToken")
	e.refreshToken = jsonStr(src, "refresh_token", "refreshToken")
	e.accountID = jsonStr(src, "account_id", "accountId")
	e.scopes = importScopes(src["scopes"])
	e.claudeAccount, e.claudeOrg = jsonStr(x, "account_uuid"), jsonStr(x, "org_uuid")
	if agent == "claude" {
		e.expiresAt = jsonMillis(src, "expiresAt")
	}
	typ := strings.ToLower(jsonStr(x, "type"))
	switch {
	case typ != "" && typ != agent:
		e.err = fmt.Sprintf("a %s sign-in, not %s's", jsonStr(x, "type"), name)
	case agent == "codex" && x["claudeAiOauth"] != nil, agent == "claude" && x["tokens"] != nil:
		e.err = "not a " + name + " sign-in"
	case e.refreshToken == "" && jsonStr(x, "OPENAI_API_KEY", "api_key", "apiKey") != "":
		e.err = "an API key, not a sign-in; add it as a key instead"
	case e.refreshToken == "" && (e.accessToken != "" || jsonStr(x, "personal_access_token") != ""):
		e.err = accessOnlyErr
	}
	return e
}

// cswapEntry reads one account of a claude-swap export (cswap --export,
// transfer.py): {number, email, uuid, organizationUuid, organizationName,
// added, credentials, config, alias, kind}. credentials is Claude Code's
// own ({claudeAiOauth: {…}}), or with --full whatever else it kept beside
// it; config is {oauthAccount: {…}}, or with --full all of ~/.claude.json.
// An API-key account's credentials are the sk-ant-… key, its kind
// "api_key".
func cswapEntry(agent string, x map[string]any) loginImport {
	cfg, _ := x["config"].(map[string]any)
	acct, _ := cfg["oauthAccount"].(map[string]any)
	e := loginImport{email: cmp.Or(jsonStr(acct, "emailAddress"), jsonStr(x, "email")),
		claudeAccount: cmp.Or(jsonStr(acct, "accountUuid"), jsonStr(x, "uuid")),
		claudeOrg:     cmp.Or(jsonStr(acct, "organizationUuid"), jsonStr(x, "organizationUuid")),
		claudeOrgName: cmp.Or(jsonStr(acct, "organizationName"), jsonStr(x, "organizationName")),
		alias:         jsonStr(x, "alias")}
	creds, ok := x["credentials"].(map[string]any)
	switch {
	case agent != "claude":
		e.err = "a Claude account (claude-swap's), not Codex's"
		return e
	case jsonStr(x, "kind") == "api_key" || !ok:
		e.err = "an API key, not a sign-in; add it as a key instead"
		return e
	}
	src := creds
	if t, ok := creds["claudeAiOauth"].(map[string]any); ok {
		src = t
	}
	e.accessToken = jsonStr(src, "accessToken", "access_token")
	e.refreshToken = jsonStr(src, "refreshToken", "refresh_token")
	e.scopes = importScopes(src["scopes"])
	e.expiresAt = jsonMillis(src, "expiresAt")
	if e.refreshToken == "" && e.accessToken != "" {
		e.err = accessOnlyErr
	}
	return e
}

// codexbarEntries reads codexbar's config.json, as its CodexBarConfigStore
// writes it: {version, active, openAI: {remoteConnectionAccounts: […]},
// providers: [{id, kind, accounts: […]}]}. The ChatGPT sign-ins are the
// accounts of the provider of kind "openai_oauth" and the remote
// connection's, each {id, kind: "oauth_tokens" or "api_key", email,
// openAIAccountId, accessToken, refreshToken, idToken, …}: the account's
// workspace is openAIAccountId, its id is codexbar's own. The other
// providers are other vendors' API endpoints, not accounts, and are left
// alone. A sign-in in both places is read once.
func codexbarEntries(agent string, x map[string]any) ([]loginImport, bool) {
	ps, ok := x["providers"].([]any)
	if !ok {
		return nil, false
	}
	var accts []any
	for _, p := range ps {
		if p, ok := p.(map[string]any); ok && jsonStr(p, "kind") == "openai_oauth" {
			as, _ := p["accounts"].([]any)
			accts = append(accts, as...)
		}
	}
	if o, ok := x["openAI"].(map[string]any); ok {
		as, _ := o["remoteConnectionAccounts"].([]any)
		accts = append(accts, as...)
	}
	var out []loginImport
	seen := map[string]bool{}
	for _, a := range accts {
		a, ok := a.(map[string]any)
		if !ok {
			continue
		}
		e := loginImport{email: jsonStr(a, "email"), idToken: jsonStr(a, "idToken"), accessToken: jsonStr(a, "accessToken"),
			refreshToken: jsonStr(a, "refreshToken"), accountID: jsonStr(a, "openAIAccountId")}
		if e.refreshToken != "" {
			if seen[e.refreshToken] {
				continue
			}
			seen[e.refreshToken] = true
		}
		switch {
		case agent != "codex":
			e.err = "a ChatGPT account, not Claude's"
		case jsonStr(a, "kind") == "api_key":
			e.err = "an API key, not a sign-in; add it as a key instead"
		case e.refreshToken == "" && e.accessToken != "":
			e.err = accessOnlyErr
		}
		out = append(out, e)
	}
	return out, true
}

// accessOnlyErr: an account with an access token and no refresh token (a
// ChatGPT web session, an access-token export) works for days at most and
// can't be renewed, so it isn't kept as an account that would just stop,
// unless it is imported access token only (ImportCodexAccessOnly).
const accessOnlyErr = "only an access token, no refresh_token: it would stop working within days and can't be renewed; sign in to this account instead, or import it as access token only"

// importScopes reads a sign-in's scopes, a list or one string of them
// apart by spaces, as OAuth's scope is.
func importScopes(v any) []string {
	var out []string
	switch sc := v.(type) {
	case []any:
		for _, s := range sc {
			if s, ok := s.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	case string:
		out = strings.Fields(sc)
	}
	return out
}

// sub2apiEntry reads one account of a Sub2API export. A ChatGPT one keeps
// email, id_token and chatgpt_account_id in credentials. A Claude one's
// credentials are the token Sub2API was given, with Anthropic's own names
// (email_address, scope, account_uuid, org_uuid), and extra holds the
// account's email_address and uuids again (useAccountOAuth's
// buildExtraInfo): email_address alone was "the file doesn't say which
// Claude account this is" (#1975).
func sub2apiEntry(agent string, x, c map[string]any) loginImport {
	name := map[string]string{"codex": "Codex", "claude": "Claude"}[agent]
	platform := jsonStr(x, "platform")
	ours := map[string]string{"openai": "codex", "anthropic": "claude", "claude": "claude"}[strings.ToLower(platform)] == agent
	extra, _ := x["extra"].(map[string]any)
	e := loginImport{email: cmp.Or(jsonStr(c, "email", "email_address"), jsonStr(extra, "email_address", "email")),
		idToken: jsonStr(c, "id_token"), accessToken: jsonStr(c, "access_token"), refreshToken: jsonStr(c, "refresh_token"),
		accountID: jsonStr(c, "chatgpt_account_id", "account_id"), scopes: importScopes(c["scope"]),
		claudeAccount: cmp.Or(jsonStr(c, "account_uuid"), jsonStr(extra, "account_uuid")),
		claudeOrg:     cmp.Or(jsonStr(c, "org_uuid"), jsonStr(extra, "org_uuid"))}
	if n := jsonStr(x, "name"); e.email == "" && strings.Contains(n, "@") {
		e.email = n // Cockpit Tools names each account by its email
	}
	switch typ := strings.ToLower(jsonStr(x, "type")); {
	case !ours:
		e.err = fmt.Sprintf("a %s account, not %s's", platform, name)
	case typ == "apikey":
		e.err = "an API key, not a sign-in; add it as a key instead"
	case typ != "oauth":
		e.err = "not a " + name + " sign-in"
	case e.refreshToken == "" && e.accessToken != "":
		e.err = accessOnlyErr
	}
	return e
}

// ImportLogins brings in ChatGPT (agent "codex") or Claude ("claude")
// accounts from other tools' files (see above), each file's text one
// element of files, and says what became of each.
func ImportLogins(ctx context.Context, agent string, files []string) ([]ImportedAccount, error) {
	return importLogins(ctx, agent, files, false)
}

// ImportCodexAccessOnly is ImportLogins for ChatGPT accounts kept access
// token only (codex_access_only.go): each is kept with the access token
// the file has, never refreshed, so the sign-in it came from stays signed
// in. One whose access token has run out isn't taken.
func ImportCodexAccessOnly(ctx context.Context, files []string) ([]ImportedAccount, error) {
	return importLogins(ctx, "codex", files, true)
}

func importLogins(ctx context.Context, agent string, files []string, accessOnly bool) ([]ImportedAccount, error) {
	if accessOnly && agent != "codex" {
		return nil, fmt.Errorf("only ChatGPT accounts can be kept access token only")
	}
	if settings.AccountDiscoveryOff() {
		return nil, errAccountDiscoveryOff
	}
	if agent != "codex" && agent != "claude" {
		return nil, fmt.Errorf("accounts can't be imported for %s", agent)
	}
	var all []loginImport
	for i, f := range files {
		es, err := parseLoginImport(agent, f)
		if accessOnly {
			for j := range es {
				// an access token alone is what is wanted here
				if es[j].err == accessOnlyErr && es[j].accessToken != "" {
					es[j].err = ""
				}
			}
		}
		if err != nil {
			if len(files) == 1 {
				return nil, err
			}
			// one file of several that holds none: said, not passed over
			all = append(all, loginImport{file: i + 1, err: err.Error()})
			continue
		}
		all = append(all, es...)
	}
	if len(all) == 0 {
		return nil, errors.New("no accounts in these files")
	}
	if len(all) > maxGoogleImport {
		return nil, fmt.Errorf("%d accounts; at most %d at a time", len(all), maxGoogleImport)
	}
	// the refresh tokens magpie holds already, the agent's own included
	have := map[string]string{}
	// kept access token only, an account is told by its access token: one
	// imported again with a newer one (the other computer's Codex renewed
	// it) updates it
	haveAccess := map[string]string{}
	loginsMu.Lock()
	ls := readLogins()
	if l, ok := liveLogin(agent); ok {
		ls = append(ls, l)
	}
	var kept []savedLogin
	for _, l := range ls {
		if l.Agent != agent {
			continue
		}
		kept = append(kept, l)
		if t := loginRefreshToken(l); t != "" {
			have[t] = l.User
		}
		if t := loginAccessToken(l); t != "" {
			haveAccess[t] = l.User
		}
	}
	loginsMu.Unlock()

	out := make([]ImportedAccount, len(all))
	seen := map[string]bool{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, e := range all {
		name := e.email
		if name == "" && e.file == 0 {
			name = fmt.Sprintf("#%d", i+1)
		}
		key := e.refreshToken
		if accessOnly {
			key = e.accessToken
		}
		switch {
		case e.err != "":
			out[i] = ImportedAccount{User: name, File: e.file, Status: "failed", Error: e.err}
			continue
		case accessOnly && e.accessToken == "":
			out[i] = ImportedAccount{User: name, Status: "failed", Error: "no access_token in it"}
			continue
		case accessOnly && haveAccess[e.accessToken] != "":
			out[i] = ImportedAccount{User: haveAccess[e.accessToken], Status: "exists"}
			continue
		case accessOnly && seen[key]:
			out[i] = ImportedAccount{User: name, Status: "failed", Error: "in the files twice"}
			continue
		case accessOnly:
		case e.refreshToken == "":
			out[i] = ImportedAccount{User: name, Status: "failed", Error: "no refresh_token in it"}
			continue
		case have[e.refreshToken] != "":
			out[i] = ImportedAccount{User: have[e.refreshToken], Status: "exists"}
			continue
		case seen[e.refreshToken]:
			out[i] = ImportedAccount{User: name, Status: "failed", Error: "in the files twice"}
			continue
		}
		seen[key] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			if accessOnly {
				out[i] = importAccessOnly(e, name, kept)
				return
			}
			out[i] = importLogin(c, agent, e, name, kept)
		}()
	}
	wg.Wait()
	importNames(agent, all, out)
	for _, r := range out {
		if r.Status == "added" || r.Status == "updated" {
			_ = ShowAccount(agent)
			break
		}
	}
	return out, nil
}

// importNames gives each account brought in the name the tool it came from
// gave it (cswap's alias), while it has none of the user's own and the
// name picks no other account: not another's name, email or email part
// (AccountByName). A name that can't be given is left off, the account
// still added.
func importNames(agent string, es []loginImport, out []ImportedAccount) {
	for i, e := range es {
		r := out[i]
		if e.alias == "" || (r.Status != "added" && r.Status != "updated") || AccountNameOf(agent, r.User) != "" {
			continue
		}
		var users []string
		for _, l := range Logins(agent) {
			users = append(users, l.User)
		}
		taken := false
		for _, u := range users {
			email, _, _ := strings.Cut(u, " · ")
			if !strings.EqualFold(u, r.User) && (strings.EqualFold(AccountNameOf(agent, u), e.alias) ||
				strings.EqualFold(u, e.alias) || strings.EqualFold(strings.TrimSpace(email), e.alias)) {
				taken = true
			}
		}
		if !taken {
			_ = SetAccountName(agent, r.User, e.alias)
		}
	}
}

// loginRefreshToken is the refresh token a saved sign-in holds.
func loginRefreshToken(l savedLogin) string {
	switch l.Agent {
	case "codex":
		var a codexAuth
		if json.Unmarshal(l.Auth, &a) == nil {
			return a.Tokens.RefreshToken
		}
	case "claude":
		if c, ok := parseClaudeCredentials(l.Auth); ok {
			return c.OAuth.RefreshToken
		}
	}
	return ""
}

// loginAccessToken is the access token a saved ChatGPT sign-in holds.
func loginAccessToken(l savedLogin) string {
	var a codexAuth
	if l.Agent == "codex" && json.Unmarshal(l.Auth, &a) == nil {
		return a.Tokens.AccessToken
	}
	return ""
}

// importAccessOnly keeps one ChatGPT account access token only, as the
// file has it: nothing is asked of ChatGPT, whose refresh would rotate
// the file's sign-in. Its id_token says whose account it is.
func importAccessOnly(e loginImport, name string, kept []savedLogin) ImportedAccount {
	fail := func(msg string) ImportedAccount {
		return ImportedAccount{User: name, Status: "failed", Error: msg}
	}
	if accessTokenRunOut(e.accessToken, time.Now()) {
		return fail("its access token has expired; export it again from where it is signed in")
	}
	if e.idToken == "" {
		return fail("no id_token in it, which says whose account it is; import Codex's auth.json, or an export that keeps it")
	}
	l, err := codexLogin(e.idToken, e.accessToken, e.refreshToken, e.accountID)
	if err != nil {
		return fail(err.Error())
	}
	l.AccessOnly = true
	status := "added"
	for _, k := range kept {
		if sameLogin(k, l) {
			status = "updated"
			break
		}
	}
	if _, err := addLogin(l); err != nil {
		return fail(err.Error())
	}
	return ImportedAccount{User: l.User, Status: status, Plan: l.Plan}
}

// claudeImportOlder is why a Claude sign-in in a file wasn't taken.
const claudeImportOlder = "magpie has a newer sign-in for this account (Claude Code's own, when it is signed in to it): kept, not replaced by this file's"

// claudeNewerKept is the account in kept that l, a Claude sign-in read
// from a file, is the same account as, when what magpie has for it is no
// older than the file's and can still be used. Its refresh token differs
// (ImportLogins answers "exists" for the same one), so one of the two is
// spent or about to be: Claude Code refreshes its own and the tool the
// file came from (claude-swap) its copy. An export made before the last
// refresh written over Claude Code's live sign-in, or a saved one, signed
// the account out (#4532). The newer is the one with the later expiresAt,
// as each refresh gives a later one; a file that doesn't say is never
// taken over a sign-in that can be used. A saved account's own config
// directory, where Claude Code may have refreshed it since, counts too.
func claudeNewerKept(e loginImport, kept []savedLogin, l savedLogin) (savedLogin, bool) {
	for _, k := range kept {
		if k.Agent != "claude" || !sameLogin(k, l) || claudeSignedOut(k) != "" {
			continue
		}
		var at int64
		if c, ok := parseClaudeCredentials(k.Auth); ok {
			at = c.OAuth.ExpiresAt
		}
		if c, ok := readClaudeDir(claudeAccountDir(k.User)); ok && c.OAuth.ExpiresAt > at {
			at = c.OAuth.ExpiresAt
		}
		if e.expiresAt <= at {
			return k, true
		}
	}
	return savedLogin{}, false
}

// importLogin checks one account as a sign-in is finished and keeps it.
func importLogin(ctx context.Context, agent string, e loginImport, name string, kept []savedLogin) ImportedAccount {
	fail := func(msg string) ImportedAccount {
		return ImportedAccount{User: name, Status: "failed", Error: msg}
	}
	refused := func(vendor string, err error) ImportedAccount {
		var r refreshRefused
		if errors.As(err, &r) {
			return fail(vendor + " didn't take the refresh token — it has been used since (by the tool it came from, which refreshes it), expired or signed out")
		}
		return fail(err.Error())
	}
	var l savedLogin
	var err error
	switch agent {
	case "codex":
		raw := map[string]any{"tokens": map[string]any{"refresh_token": e.refreshToken}}
		if _, err := codexRefresh(ctx, raw, codexRefreshForced); err != nil {
			return refused("ChatGPT", err)
		}
		t := raw["tokens"].(map[string]any)
		str := func(k string) string { s, _ := t[k].(string); return s }
		id := str("id_token")
		if id == "" {
			id = e.idToken
		}
		l, err = codexLogin(id, str("access_token"), str("refresh_token"), e.accountID)
	case "claude":
		scopes := e.scopes
		if len(scopes) == 0 {
			scopes = claudeImportScopes
		}
		// kept as it came, not tried: trying it would ask Anthropic, which
		// magpie never does with a Claude sign-in. Claude Code refreshes it
		// the first time it runs on the account, its access token taken
		// for spent.
		c := claudeCredentials{raw: map[string]any{}, OAuth: claudeAuth{AccessToken: e.accessToken, RefreshToken: e.refreshToken,
			ExpiresAt: 1, Scopes: scopes}}
		if e.email == "" {
			return fail("the file doesn't say which Claude account this is; sign in to it from magpie instead")
		}
		acct := map[string]any{"emailAddress": e.email}
		if e.claudeAccount != "" {
			acct["accountUuid"] = e.claudeAccount
		}
		if e.claudeOrg != "" {
			acct["organizationUuid"] = e.claudeOrg
		}
		if e.claudeOrgName != "" {
			acct["organizationName"] = e.claudeOrgName
		}
		l, err = claudeLogin(c, acct)
		if err == nil {
			if k, ok := claudeNewerKept(e, kept, l); ok {
				return ImportedAccount{User: k.User, Status: "exists", Plan: k.Plan, Error: claudeImportOlder}
			}
		}
	}
	if err != nil {
		return fail(err.Error())
	}
	status := "added"
	for _, k := range kept {
		if sameLogin(k, l) {
			status = "updated"
			break
		}
	}
	if _, err := addLogin(l); err != nil {
		return fail(err.Error())
	}
	return ImportedAccount{User: l.User, Status: status, Plan: l.Plan}
}
