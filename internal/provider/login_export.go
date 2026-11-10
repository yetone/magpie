package provider

// ChatGPT (Codex) and Claude accounts taken out of magpie as a file the
// tools ImportLogins reads can take back: Sub2API's data export
// ({type: "sub2api-data", accounts: […]}) in one file, or CLIProxyAPI's
// auth files ({type: "codex"|"claude", …}) one file a account, as it keeps
// them in its auth folder. A file holds live tokens: whoever has it is
// signed in as the account. The tokens are never logged; the caller writes
// the files 0600.
//
// A ChatGPT refresh token is spent by its first use, so an exported account
// can't be signed in in two places: the first to renew it signs the other
// out. Exporting changes nothing here; the account is the other tool's from
// the moment it is used there.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

// Export formats.
const (
	ExportSub2API   = "sub2api"
	ExportCLIProxy  = "cliproxyapi"
	sub2apiDataType = "sub2api-data"
)

// ExportFile is one file of an export.
type ExportFile struct {
	Name string
	Data []byte
}

// ExportResult is what an export made: its files, the accounts in them and
// the accounts left out, with why.
type ExportResult struct {
	Files    []ExportFile
	Accounts []string
	Skipped  []ImportedAccount // Status "skipped", Error says why
}

// exportAccount is one account's sign-in, read.
type exportAccount struct {
	user, plan                                     string
	email, idToken, accessToken, refreshToken, aid string
	scopes                                         []string
	expires                                        time.Time
	claudeAccount, claudeOrg                       string
}

// ExportLogins writes the accounts of agent (codex, claude) named in users
// (every one when empty) in format (ExportSub2API or ExportCLIProxy).
func ExportLogins(agent, format string, users []string) (ExportResult, error) {
	if discoveryOff() {
		return ExportResult{}, errAccountDiscoveryOff
	}
	if agent != "codex" && agent != "claude" {
		return ExportResult{}, fmt.Errorf("accounts can't be exported for %s", agent)
	}
	if format != ExportSub2API && format != ExportCLIProxy {
		return ExportResult{}, fmt.Errorf("format %q: sub2api or cliproxyapi", format)
	}
	loginsMu.Lock()
	ls := slices.DeleteFunc(readLogins(), func(l savedLogin) bool { return l.Agent != agent })
	// the account the agent is signed in to is read from its own file: that
	// is fresher than magpie's copy
	if live, ok := liveLogin(agent); ok {
		at := slices.IndexFunc(ls, func(l savedLogin) bool { return sameLogin(l, live) })
		if at < 0 {
			ls = append(ls, live)
		} else {
			ls[at].Auth = live.Auth
		}
	}
	for i, l := range ls {
		if agent == "codex" {
			if home := codexHeldIn(l); home != "" {
				if b, _, err := readCodexLogin(home, false); err == nil {
					ls[i].Auth = b
				}
			}
		}
	}
	loginsMu.Unlock()

	var res ExportResult
	var accts []exportAccount
	want := map[string]bool{}
	for _, u := range users {
		want[strings.ToLower(strings.TrimSpace(u))] = true
	}
	found := map[string]bool{}
	for _, l := range ls {
		if len(want) > 0 && !want[strings.ToLower(l.User)] {
			continue
		}
		found[strings.ToLower(l.User)] = true
		a, why := exportRead(agent, l)
		if why != "" {
			res.Skipped = append(res.Skipped, ImportedAccount{User: l.User, Status: "skipped", Error: why})
			continue
		}
		accts = append(accts, a)
	}
	for _, u := range users {
		if !found[strings.ToLower(strings.TrimSpace(u))] {
			return ExportResult{}, fmt.Errorf("no %s account %q", agent, u)
		}
	}
	if len(accts) == 0 {
		if len(res.Skipped) > 0 {
			return res, errors.New("none of the accounts can be exported")
		}
		return res, fmt.Errorf("no %s accounts", agent)
	}
	slices.SortFunc(accts, func(a, b exportAccount) int { return strings.Compare(strings.ToLower(a.user), strings.ToLower(b.user)) })
	for _, a := range accts {
		res.Accounts = append(res.Accounts, a.user)
	}
	switch format {
	case ExportSub2API:
		accounts := make([]any, len(accts))
		for i, a := range accts {
			accounts[i] = sub2apiAccount(agent, a)
		}
		b, err := json.MarshalIndent(map[string]any{"type": sub2apiDataType, "version": 1,
			"exported_at": time.Now().UTC().Format(time.RFC3339), "proxies": []any{}, "accounts": accounts}, "", "  ")
		if err != nil {
			return ExportResult{}, err
		}
		res.Files = []ExportFile{{Name: "magpie-sub2api-" + agent + ".json", Data: append(b, '\n')}}
	default:
		used := map[string]bool{}
		for _, a := range accts {
			b, err := json.MarshalIndent(cliproxyAuth(agent, a), "", "  ")
			if err != nil {
				return ExportResult{}, err
			}
			name := exportName(agent, a)
			for i := 2; used[name]; i++ {
				name = fmt.Sprintf("%s-%d", strings.TrimSuffix(exportName(agent, a), ".json"), i) + ".json"
			}
			used[name] = true
			res.Files = append(res.Files, ExportFile{Name: name, Data: append(b, '\n')})
		}
	}
	return res, nil
}

var exportUnsafe = lazyre.New(`[^A-Za-z0-9@._+-]+`)

// exportName is CLIProxyAPI's name for an auth file: <type>-<email>.json,
// a ChatGPT one's plan after the email.
func exportName(agent string, a exportAccount) string {
	n := agent + "-" + cmpOr(a.email, a.user)
	if agent == "codex" && a.plan != "" {
		n += "-" + a.plan
	}
	return strings.Trim(exportUnsafe.ReplaceAllString(n, "_"), "_.") + ".json"
}

// exportRead reads one saved account's tokens; why says it can't be exported.
func exportRead(agent string, l savedLogin) (exportAccount, string) {
	a := exportAccount{user: l.User, plan: l.Plan}
	switch agent {
	case "codex":
		var c codexAuth
		if json.Unmarshal(l.Auth, &c) != nil || c.Tokens.AccessToken == "" && c.Tokens.RefreshToken == "" {
			return a, "its sign-in can't be read"
		}
		a.idToken, a.accessToken, a.refreshToken, a.aid = c.Tokens.IDToken, c.Tokens.AccessToken, c.Tokens.RefreshToken, c.Tokens.AccountID
		id := jwtClaims(c.Tokens.IDToken)
		a.email = cmpOr(claimString(id, "email"), l.User)
		if p := claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"); p != "" {
			a.plan = p
		}
		if a.aid == "" {
			a.aid = claimString(id, "https://api.openai.com/auth", "chatgpt_account_id")
		}
		if exp, ok := jwtClaims(a.accessToken)["exp"].(float64); ok {
			a.expires = time.Unix(int64(exp), 0)
		}
	case "claude":
		if claudeSetupToken(l.Auth) != "" {
			return a, "a setup-token account has no refresh token to give"
		}
		c, ok := parseClaudeCredentials(l.Auth)
		if !ok {
			return a, "its sign-in can't be read"
		}
		a.accessToken, a.refreshToken, a.scopes = c.OAuth.AccessToken, c.OAuth.RefreshToken, c.OAuth.Scopes
		if c.OAuth.ExpiresAt > 1 {
			a.expires = time.UnixMilli(c.OAuth.ExpiresAt)
		}
		var p map[string]any
		_ = json.Unmarshal(l.Profile, &p)
		a.email = jsonStr(p, "emailAddress")
		a.claudeAccount, a.claudeOrg = jsonStr(p, "accountUuid"), jsonStr(p, "organizationUuid")
		if a.email == "" && strings.Contains(l.User, "@") {
			a.email = l.User
		}
	}
	if a.refreshToken == "" {
		return a, "it has no refresh token, only an access token"
	}
	if bytes.ContainsAny([]byte(a.refreshToken), " \n") {
		return a, "its refresh token isn't one"
	}
	return a, ""
}

// cliproxyAuth is CLIProxyAPI's auth file for the account.
func cliproxyAuth(agent string, a exportAccount) map[string]any {
	m := map[string]any{"type": agent, "email": a.email, "access_token": a.accessToken, "refresh_token": a.refreshToken,
		"last_refresh": time.Now().UTC().Format(time.RFC3339)}
	if !a.expires.IsZero() {
		m["expired"] = a.expires.UTC().Format(time.RFC3339)
	}
	if agent == "codex" {
		m["id_token"], m["account_id"] = a.idToken, a.aid
	}
	// CLIProxyAPI's own claude file names no organization, so two seats of
	// one email would come back as one account; the uuids are extra keys it
	// leaves alone and magpie reads (loginEntry)
	if a.claudeAccount != "" {
		m["account_uuid"] = a.claudeAccount
	}
	if a.claudeOrg != "" {
		m["org_uuid"] = a.claudeOrg
	}
	return m
}

// sub2apiAccount is one entry of Sub2API's data export.
func sub2apiAccount(agent string, a exportAccount) map[string]any {
	c := map[string]any{"access_token": a.accessToken, "refresh_token": a.refreshToken}
	m := map[string]any{"name": cmpOr(a.email, a.user), "type": "oauth", "concurrency": 10, "priority": 1,
		"rate_multiplier": 1, "auto_pause_on_expired": true, "credentials": c}
	switch agent {
	case "codex":
		m["platform"] = "openai"
		c["id_token"], c["chatgpt_account_id"], c["email"], c["client_id"] = a.idToken, a.aid, a.email, codexClientID
		if a.plan != "" {
			c["plan_type"] = a.plan
		}
		if !a.expires.IsZero() {
			c["expires_at"] = a.expires.UTC().Format(time.RFC3339)
		}
	case "claude":
		m["platform"] = "anthropic"
		extra := map[string]any{}
		if a.email != "" {
			c["email_address"], extra["email_address"] = a.email, a.email
		}
		if a.claudeAccount != "" {
			c["account_uuid"], extra["account_uuid"] = a.claudeAccount, a.claudeAccount
		}
		if a.claudeOrg != "" {
			c["org_uuid"], extra["org_uuid"] = a.claudeOrg, a.claudeOrg
		}
		if len(a.scopes) > 0 {
			c["scope"] = strings.Join(a.scopes, " ")
		}
		if !a.expires.IsZero() {
			c["expires_at"] = a.expires.Unix()
		}
		if len(extra) > 0 {
			m["extra"] = extra
		}
	}
	return m
}

// ExportRisk is said wherever an export is offered.
const ExportRisk = "The file holds each account's live sign-in: whoever has it is signed in as that account. Keep it private and delete it when it has been imported. A ChatGPT sign-in can be renewed in one place only, so the first tool to renew it signs the other out."

// WriteExport writes the files of res into dir (made 0700 when it is not
// there), each 0600 and never over a file that is there: a name taken gets
// -2, -3 … before its extension. It answers the paths written.
func WriteExport(res ExportResult, dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range res.Files {
		stem, ext := strings.TrimSuffix(f.Name, ".json"), ".json"
		var err error
		for i := 1; ; i++ {
			name := filepath.Join(dir, stem+ext)
			if i > 1 {
				name = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
			}
			var fh *os.File
			fh, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(err, os.ErrExist) && i < 1000 {
				continue
			}
			if err == nil {
				_, err = fh.Write(f.Data)
				if cerr := fh.Close(); err == nil {
					err = cerr
				}
				if err != nil {
					os.Remove(name)
				} else {
					paths = append(paths, name)
				}
			}
			break
		}
		if err != nil {
			return paths, err
		}
	}
	return paths, nil
}
