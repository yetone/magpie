package provider

// Google accounts brought in from another tool's export rather than signed
// in again: Antigravity Cockpit (jlcodes99/vscode-antigravity-cockpit) and
// Antigravity Manager export [{email, refresh_token}], Antigravity Manager
// keeps each account as {email, token: {refresh_token, project_id, …}},
// CLIProxyAPI as {type: "antigravity", email, refresh_token, project_id, …},
// and Cockpit also takes bare refresh tokens, one a line. They all sign in
// with Antigravity's own OAuth client, the one magpie uses, so a refresh
// token from any of them works here. Each is checked the way a sign-in is
// finished: the token refreshed with Google, whose account it is asked,
// the Code Assist project found; only then is it kept, as a signed-in
// account is. Tokens are never logged or sent back.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ImportedAccount is what became of one account in an import.
type ImportedAccount struct {
	// User is the account's email, or "#n" (its place in the file) when
	// the file doesn't say and Google wasn't asked or didn't answer.
	User string `json:"user"`
	// Status: "added", "updated" (in magpie already, now with this
	// sign-in), "exists" (in magpie already with this very sign-in),
	// "failed".
	Status string `json:"status"`
	Plan   string `json:"plan,omitempty"`
	Error  string `json:"error,omitempty"`
}

// googleImport is one account read from an export.
type googleImport struct {
	n                            int // its place in the file(s), from 1
	email, refreshToken, project string
	err                          string // why it can't be imported, found reading it
}

// maxGoogleImport caps the accounts one import takes.
const maxGoogleImport = 200

// parseGoogleImport reads the accounts in an export: a JSON array or
// object in any of the shapes above, or refresh tokens one a line. agent
// is the app they are for; an entry another app's sign-in is refused.
func parseGoogleImport(agent, data string) ([]googleImport, error) {
	s := strings.TrimSpace(strings.TrimPrefix(data, "\xef\xbb\xbf")) // a BOM
	if s == "" {
		return nil, errors.New("nothing to import")
	}
	var out []googleImport
	add := func(e googleImport) {
		e.n = len(out) + 1
		out = append(out, e)
	}
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
				// Antigravity Manager's export response: {accounts: [...]}
				if as, ok := x["accounts"].([]any); ok && jsonStr(x, "refresh_token", "refreshToken") == "" {
					walk(as)
					return
				}
				add(googleEntry(agent, x))
			case string:
				add(googleImport{refreshToken: strings.TrimSpace(x)})
			default:
				add(googleImport{err: "not an account"})
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
				add(googleImport{err: "not a refresh token"})
				continue
			}
			add(googleImport{refreshToken: line})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no accounts in it")
	}
	if len(out) > maxGoogleImport {
		return nil, fmt.Errorf("%d accounts in it; at most %d at a time", len(out), maxGoogleImport)
	}
	// the same account twice is imported once
	seenTok, seenUser := map[string]bool{}, map[string]bool{}
	for i := range out {
		e := &out[i]
		if e.err != "" {
			continue
		}
		if e.refreshToken == "" {
			e.err = "no refresh_token in it"
			continue
		}
		u := strings.ToLower(e.email)
		if seenTok[e.refreshToken] || (u != "" && seenUser[u]) {
			e.err = "in the file twice"
			continue
		}
		seenTok[e.refreshToken] = true
		if u != "" {
			seenUser[u] = true
		}
	}
	return out, nil
}

// googleEntry reads one account object.
func googleEntry(agent string, x map[string]any) googleImport {
	e := googleImport{
		email:        jsonStr(x, "email"),
		refreshToken: jsonStr(x, "refresh_token", "refreshToken"),
		project:      jsonStr(x, "project_id", "projectId", "project"),
	}
	// Antigravity Manager's account file keeps the sign-in under token
	if tok, ok := x["token"].(map[string]any); ok {
		if e.refreshToken == "" {
			e.refreshToken = jsonStr(tok, "refresh_token", "refreshToken")
		}
		if e.email == "" {
			e.email = jsonStr(tok, "email")
		}
		if e.project == "" {
			e.project = jsonStr(tok, "project_id", "projectId")
		}
	}
	// CLIProxyAPI names the app its auth file is for
	if typ := strings.ToLower(jsonStr(x, "type")); typ != "" && typ != agent {
		app, _ := googleAppOf(agent)
		e.err = fmt.Sprintf("a %s sign-in, not %s's", jsonStr(x, "type"), app.name)
	}
	return e
}

// jsonStr is the first of keys that x has as a non-empty string.
func jsonStr(x map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := x[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// ImportGoogleAccounts brings in the accounts in exports from other tools
// (see above), each file's text one element of files, and says what
// became of each.
func ImportGoogleAccounts(ctx context.Context, agent string, files []string) ([]ImportedAccount, error) {
	app, ok := googleAppOf(agent)
	if !ok || agent != "antigravity" {
		return nil, fmt.Errorf("accounts can't be imported for %s", agent)
	}
	var all []googleImport
	for _, f := range files {
		es, err := parseGoogleImport(agent, f)
		if err != nil {
			if len(files) == 1 {
				return nil, err
			}
			continue // one file of several that holds none
		}
		all = append(all, es...)
	}
	if len(all) == 0 {
		return nil, errors.New("no accounts in these files")
	}
	if len(all) > maxGoogleImport {
		return nil, fmt.Errorf("%d accounts; at most %d at a time", len(all), maxGoogleImport)
	}
	// what magpie has already: refresh token and user of each account
	haveTok, haveUser := map[string]string{}, map[string]string{}
	loginsMu.Lock()
	for _, l := range readLogins() {
		if l.Agent != agent {
			continue
		}
		var a googleAuth
		_ = json.Unmarshal(l.Auth, &a)
		haveUser[strings.ToLower(l.User)] = a.RefreshToken
		if a.RefreshToken != "" {
			haveTok[a.RefreshToken] = l.User
		}
	}
	loginsMu.Unlock()

	out := make([]ImportedAccount, len(all))
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
		case haveTok[e.refreshToken] != "":
			out[i] = ImportedAccount{User: haveTok[e.refreshToken], Status: "exists"}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			out[i] = importGoogleAccount(c, app, e, name, haveUser)
		}()
	}
	wg.Wait()
	for _, r := range out {
		if r.Status == "added" || r.Status == "updated" {
			// an import brings back an account removed from magpie, as
			// signing in does
			_ = ShowAccount(agent)
			break
		}
	}
	return out, nil
}

// importGoogleAccount checks one account as a sign-in is finished and
// keeps it.
func importGoogleAccount(ctx context.Context, app googleApp, e googleImport, name string, haveUser map[string]string) ImportedAccount {
	fail := func(msg string) ImportedAccount {
		return ImportedAccount{User: name, Status: "failed", Error: msg}
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {e.refreshToken},
		"client_id": {app.clientID}, "client_secret": {app.clientSecret}}.Encode()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := postToken(ctx, googleTokenURL, "application/x-www-form-urlencoded", []byte(form), &tok); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "(401)") || strings.Contains(strings.ToLower(msg), "unauthorized") {
			msg += " — this sign-in was made for another app, not " + app.name
		} else if strings.Contains(msg, "(400)") && !strings.Contains(msg, "revoked") {
			// Google says only "Bad Request" for a token it doesn't know
			msg += " — it has expired or been revoked, or isn't a refresh token"
		}
		return fail("Google didn't take the refresh token: " + msg)
	}
	if tok.AccessToken == "" {
		return fail("Google sent back no token")
	}
	g := googleAccount{app: app, auth: googleAuth{AccessToken: tok.AccessToken, RefreshToken: e.refreshToken,
		Expiry: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli()}}
	who, err := googleWho(ctx, tok.AccessToken)
	switch {
	case err == nil:
		g.user = who
	case e.email != "":
		g.user = e.email // the file says, and the token works
	default:
		return fail(err.Error())
	}
	status := "added"
	if old, ok := haveUser[strings.ToLower(g.user)]; ok {
		if old == e.refreshToken {
			return ImportedAccount{User: g.user, Status: "exists"}
		}
		status = "updated"
	}
	// its project and plan found as a sign-in finds them; the file's
	// project is kept when Code Assist won't say
	plan := ""
	if p, err := g.project(ctx); err == nil {
		g.auth.Project, plan = p.id, p.plan
	} else if e.project != "" {
		g.auth.Project = e.project
	}
	if err := addGoogleLogin(app.agent, g.user, plan, g.auth); err != nil {
		return fail(err.Error())
	}
	googleState.Lock()
	googleState.tokens[e.refreshToken] = g.auth
	googleState.Unlock()
	return ImportedAccount{User: g.user, Status: status, Plan: plan}
}
