package provider

// ChatGPT (Codex) and Claude accounts brought in from another tool's files
// rather than signed in again: CLIProxyAPI keeps each as
// {type: "codex", id_token, access_token, refresh_token, account_id, email, …}
// or {type: "claude", access_token, refresh_token, email, …}, Codex CLI as
// auth.json ({tokens: {…}}) and Claude Code as .credentials.json
// ({claudeAiOauth: {…}}); a bare refresh token a line is taken too. They
// all sign in with the agents' own OAuth clients, the ones magpie uses.
//
// A ChatGPT one is checked the way a sign-in is finished: its refresh token
// traded for new tokens and the account asked for. That rotates the token,
// so the file it came from stops working: the account is magpie's from then
// on, as a sign-in in magpie would make it. A Claude one is kept as it
// came, magpie asking Anthropic nothing; Claude Code refreshes it the first
// time it runs on it. Tokens are never logged or sent back.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// loginImport is one account read from a file.
type loginImport struct {
	email, idToken, accessToken, refreshToken, accountID string
	scopes                                               []string
	err                                                  string
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
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case []any:
				for _, it := range x {
					walk(it)
				}
			case map[string]any:
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
			if strings.ContainsAny(line, " \t") {
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
	switch sc := src["scopes"].(type) {
	case []any:
		for _, s := range sc {
			if s, ok := s.(string); ok && s != "" {
				e.scopes = append(e.scopes, s)
			}
		}
	case string:
		e.scopes = strings.Fields(sc)
	}
	typ := strings.ToLower(jsonStr(x, "type"))
	switch {
	case typ != "" && typ != agent:
		e.err = fmt.Sprintf("a %s sign-in, not %s's", jsonStr(x, "type"), name)
	case agent == "codex" && x["claudeAiOauth"] != nil, agent == "claude" && x["tokens"] != nil:
		e.err = "not a " + name + " sign-in"
	case e.refreshToken == "" && jsonStr(x, "OPENAI_API_KEY", "api_key", "apiKey") != "":
		e.err = "an API key, not a sign-in; add it as a key instead"
	}
	return e
}

// ImportLogins brings in ChatGPT (agent "codex") or Claude ("claude")
// accounts from other tools' files (see above), each file's text one
// element of files, and says what became of each.
func ImportLogins(ctx context.Context, agent string, files []string) ([]ImportedAccount, error) {
	if agent != "codex" && agent != "claude" {
		return nil, fmt.Errorf("accounts can't be imported for %s", agent)
	}
	var all []loginImport
	for _, f := range files {
		es, err := parseLoginImport(agent, f)
		if err != nil {
			if len(files) == 1 {
				return nil, err
			}
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
	}
	loginsMu.Unlock()

	out := make([]ImportedAccount, len(all))
	seen := map[string]bool{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, e := range all {
		name := e.email
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		switch {
		case e.err != "":
			out[i] = ImportedAccount{User: name, Status: "failed", Error: e.err}
			continue
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
		seen[e.refreshToken] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			out[i] = importLogin(c, agent, e, name, kept)
		}()
	}
	wg.Wait()
	for _, r := range out {
		if r.Status == "added" || r.Status == "updated" {
			_ = ShowAccount(agent)
			break
		}
	}
	return out, nil
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
		if _, err := codexRefresh(ctx, raw); err != nil {
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
		l, err = claudeLogin(c, acct)
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
