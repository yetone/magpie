package provider

// PLUGIN-SERVED (see AGENTS.md): WorkBuddy ("workbuddy") and WorkBuddy AI
// ("workbuddy-ai") are retired built-in subscriptions served by their
// plugin, @magpie-community/opencode-workbuddy-auth, moved or not. Their
// built-in sign-in, models, requests and usage are gone; what is left here
// reads the built-in accounts for the move (migrate_workbuddy.go), checks
// the plugin's accounts in (workbuddy_checkin.go), and tells a restricted
// account's refusal (WBRestricted). A fix belongs in the plugin
// (github.com/magpie-community/plugins, packages/workbuddy), with the
// movers' min raised in internal/provider/migrate_workbuddy.go.

// A WorkBuddy subscription is Tencent's CodeBuddy plan, which WorkBuddy
// (its desktop app, packaged from CodeBuddy Code) signs in to. WorkBuddy's
// own account is read, never changed, from its auth store,
// <shared data>/auth/workbuddy-desktop.info (plain JSON while credential
// protection is off, which it is by default); accounts magpie signed in
// keep their tokens in logins.json.
//
// WorkBuddy AI, the international build (www.workbuddy.ai, the CodeBuddy
// plan sold at www.codebuddy.ai), is its own subscription, workbuddy-ai: the
// same API at its own endpoint, and its own account file
// (workbuddy-desktop-ai.info) beside the other. A wbSite is which of the two
// an account belongs to.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
)

// Where WorkBuddy's plan is; vars so tests can point them elsewhere.
var (
	wbEndpoint = "https://copilot.tencent.com"
	// wbAIEndpoint is WorkBuddy AI's, the international build's.
	wbAIEndpoint = "https://www.workbuddy.ai"
	// wbUAVersion is the WorkBuddy desktop version the User-Agent carries.
	// copilot.tencent.com's gateway rejects a request whose User-Agent it
	// doesn't recognise with code 10085 ("请求不合法"), so every call to it —
	// chat and billing alike — must go out as WorkBuddy/<version>.
	wbUAVersion = "5.5.6"
)

// wbSite is one WorkBuddy build: the Chinese one or WorkBuddy AI.
type wbSite struct {
	id, name, website string
	authID            string  // its account file, <authID>.info
	platform          string  // what its sign-in says it is
	endpoint          *string // its API root
}

var (
	wbCN = &wbSite{id: "workbuddy", name: "WorkBuddy", website: "https://www.codebuddy.cn",
		authID: "workbuddy-desktop", platform: "workbuddy", endpoint: &wbEndpoint}
	wbAI = &wbSite{id: WorkBuddyAIID, name: "WorkBuddy AI", website: "https://www.workbuddy.ai",
		authID: "workbuddy-desktop-ai", platform: "workbuddy-ai", endpoint: &wbAIEndpoint}
)

// WorkBuddyAIID is WorkBuddy AI's subscription, the international build's.
const WorkBuddyAIID = "workbuddy-ai"

// wbSiteOf is the site of a subscription id, nil for neither.
func wbSiteOf(id string) *wbSite {
	switch id {
	case wbCN.id:
		return wbCN
	case wbAI.id:
		return wbAI
	}
	return nil
}

// api is the site's API root: auth and billing sit under it.
func (w *wbSite) api() string { return strings.TrimRight(*w.endpoint, "/") }

// wbCreds is a WorkBuddy account's tokens and where they are served, as the
// auth store and the sign-in name them.
type wbCreds struct {
	UID              string `json:"uid"`
	Access           string `json:"accessToken"`
	Refresh          string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"`        // unix ms, when the access token lapses
	RefreshExpiresAt int64  `json:"refreshExpiresAt"` // unix ms, when the refresh token lapses
	Domain           string `json:"domain"`
	TokenType        string `json:"tokenType,omitempty"`
}

// wbAccount is a signed-in WorkBuddy account: which login it is (who, its
// plan, and whether it is the one in use), its tokens, and whether it is
// WorkBuddy's own sign-in (read-only) or one magpie added (in logins.json).
type wbAccount struct {
	Login
	site  *wbSite
	creds wbCreds
	own   bool
	// via, for an account on the plugin, sends a request as the plugin
	// does, signed with its sign-in, which magpie never renews itself
	via func(*http.Request) (*http.Response, error)
	// card is the provider id its Usage card has, when not the site's:
	// the plugin's signed in under its own id, "workbuddy-plugin"
	card string
}

// ---- WorkBuddy's own account --------------------------------------------------

// wbAuthPath is where WorkBuddy keeps the signed-in session, per platform,
// as its file-authentication-storage does: <shared data>/auth/<id>.info.
// Both builds share the folder, each with its own file.
func wbAuthPath(w *wbSite) string {
	home, _ := os.UserHomeDir()
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support", "CodeBuddyExtension")
	case "windows":
		base = filepath.Join(home, "AppData", "Local", "CodeBuddyExtension")
	default:
		base = filepath.Join(home, ".local", "share", "CodeBuddyExtension")
	}
	return filepath.Join(base, "Data", "Public", "auth", w.authID+".info")
}

// wbStoredSession is the shape of the auth store's session: the account and
// its tokens. The tokens are plain strings while credential protection is
// off (the default); when it is on they are objects magpie can't read, and
// the account is then taken as not readable.
type wbStoredSession struct {
	Account struct {
		UID         string          `json:"uid"`
		Nickname    json.RawMessage `json:"nickname"`
		PhoneNumber json.RawMessage `json:"phoneNumber"`
	} `json:"account"`
	Auth struct {
		AccessToken      json.RawMessage `json:"accessToken"`
		RefreshToken     json.RawMessage `json:"refreshToken"`
		ExpiresAt        int64           `json:"expiresAt"`
		ExpiresIn        int64           `json:"expiresIn"`
		RefreshExpiresAt int64           `json:"refreshExpiresAt"`
		RefreshExpiresIn int64           `json:"refreshExpiresIn"`
		Domain           string          `json:"domain"`
		TokenType        string          `json:"tokenType"`
	} `json:"auth"`
}

// wbString reads a JSON value that is a plain string, "" for anything else
// (an encrypted-field object magpie doesn't decrypt).
func wbString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// wbOwn is the account WorkBuddy is signed in to and its tokens; ok is
// false when it has none, or they are encrypted at rest.
func wbOwn(w *wbSite) (who string, c wbCreds, ok bool) {
	var s wbStoredSession
	if !readJSON(wbAuthPath(w), &s) {
		return "", wbCreds{}, false
	}
	access, refresh := wbString(s.Auth.AccessToken), wbString(s.Auth.RefreshToken)
	if s.Account.UID == "" || access == "" {
		return "", wbCreds{}, false
	}
	c = wbCreds{
		UID:              s.Account.UID,
		Access:           access,
		Refresh:          refresh,
		ExpiresAt:        s.Auth.ExpiresAt,
		RefreshExpiresAt: s.Auth.RefreshExpiresAt,
		Domain:           s.Auth.Domain,
		TokenType:        s.Auth.TokenType,
	}
	now := time.Now().UnixMilli()
	if c.ExpiresAt == 0 && s.Auth.ExpiresIn > 0 {
		c.ExpiresAt = now + s.Auth.ExpiresIn*1000
	}
	if c.RefreshExpiresAt == 0 && s.Auth.RefreshExpiresIn > 0 {
		c.RefreshExpiresAt = now + s.Auth.RefreshExpiresIn*1000
	}
	return wbWho(wbString(s.Account.Nickname), wbString(s.Account.PhoneNumber), s.Account.UID), c, true
}

// wbWho names a WorkBuddy account: its nickname, its phone number, or its id.
func wbWho(nickname, phone, id string) string {
	return firstNonEmpty(strings.TrimSpace(nickname), strings.TrimSpace(phone), id, "WorkBuddy")
}

// ---- the accounts -------------------------------------------------------------

func wbSavedCreds(l savedLogin) (wbCreds, bool) {
	var c wbCreds
	if json.Unmarshal(l.Auth, &c) != nil || c.Access == "" || c.UID == "" {
		return wbCreds{}, false
	}
	return c, true
}

// wbLogins is every account of w signed in, the first in use first.
func wbLogins(w *wbSite) []wbAccount {
	ownUser, own, hasOwn := wbOwn(w)
	if !hasOwn {
		ownUser = ""
	}
	var out []wbAccount
	for _, l := range sideLogins(w.id, ownUser, func(l savedLogin) bool {
		_, ok := wbSavedCreds(l)
		return ok
	}) {
		a := wbAccount{Login: l.Login, site: w, own: l.saved.own()}
		if a.own {
			a.creds = own
		} else {
			a.creds, _ = wbSavedCreds(l.saved)
		}
		out = append(out, a)
	}
	// one magpie signed in under the desktop's name stands for the
	// desktop's only when it is the same uid: a nickname is no one's own, so
	// another account of that name leaves the desktop's its own place
	// (wakkana on Discord), as the plugin keeps both
	// (one removed in magpie stays out)
	if hasOwn && !slices.ContainsFunc(out, func(a wbAccount) bool { return a.own || a.creds.UID == own.UID }) &&
		slices.ContainsFunc(out, func(a wbAccount) bool { return strings.EqualFold(a.User, ownUser) }) &&
		!slices.ContainsFunc(readLogins(), func(l savedLogin) bool { return l.Agent == w.id && l.own() && l.Hidden != "" }) {
		out = append(out, wbAccount{Login: Login{Agent: w.id, User: ownUser, On: true, Own: true}, site: w, own: true, creds: own})
	}
	return out
}

// WBRestrictedHint is what magpie adds to WorkBuddy's 403 code 11140
// "request illegal" (#1849), whose displayMsg says "Your account has been
// restricted due to unusual activity": the account, not the request, is
// out until WorkBuddy lifts the restriction, which its appeal asks for.
// The gateway reads it as the account restricted (failRestricted), the
// plugin adds the same words, and the Routing page says it apart.
const WBRestrictedHint = "WorkBuddy restricted this account for unusual activity; it stays out until WorkBuddy lifts that, which an appeal (Submit feedback in WorkBuddy) asks for"

// ZCodeRestrictedHint is what the ZCode plugin (0.1.27 on) adds to the
// Start Plan's 405 code 3012, "request has been blocked due to unusual
// activity": ZCode's risk control blocked the account, ZCode's own app
// included (plugins#348), until ZCode support lifts it. The gateway reads
// it as the account restricted (failRestricted), and the Routing page says
// it apart.
const ZCodeRestrictedHint = "ZCode's risk control blocked this account for unusual activity (code 3012); it rests, and the group's other providers are used. If the ZCode app fails on this account too, only ZCode support can lift the block"

// wbRestrictedWords are the restriction as WorkBuddy's 11140 words it in
// its displayMsg and displayTips: English, Simplified and Traditional
// Chinese.
var wbRestrictedWords = lazyre.New(`(?i)account has been restricted due to unusual activity|账号存在异常行为|帳號存在異常行為`)

// wbRestrictedCode is WorkBuddy's code for an account it restricted.
const wbRestrictedCode = 11140

// WBRestricted says body is WorkBuddy's refusal of a restricted account:
// its code 11140 (top level, or under data or error, as a number or a
// string), or its displayMsg's words. Only for a WorkBuddy answer: another
// vendor's 11140 is its own.
func WBRestricted(status int, body []byte) bool {
	if status < 400 {
		return false
	}
	var v map[string]any
	if json.Unmarshal(body, &v) == nil {
		codes := []any{v["code"]}
		for _, k := range []string{"data", "error"} {
			if in, ok := v[k].(map[string]any); ok {
				codes = append(codes, in["code"])
			}
		}
		for _, c := range codes {
			switch c := c.(type) {
			case float64:
				if c == wbRestrictedCode {
					return true
				}
			case string:
				if c == strconv.Itoa(wbRestrictedCode) {
					return true
				}
			}
		}
	}
	return wbRestrictedWords.Match(body)
}

// ---- tokens -------------------------------------------------------------------

// wbTokens caches each account's freshest tokens (by site and uid), so a token
// refreshed for one request is used by the next; WorkBuddy's own file is
// never written, and a magpie-added account's refresh goes to logins.json.
var wbTokens = struct {
	sync.Mutex
	m map[string]wbCreds
}{m: map[string]wbCreds{}}

// wbNum is a capacity the billing API sends as a JSON string ("3300",
// "438.88000002"), though it occasionally comes as a bare number; it parses
// either, and an empty or unparseable value reads as 0.
type wbNum float64

func (n *wbNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*n = wbNum(f)
	return nil
}

// ---- WorkBuddy's API ----------------------------------------------------------

// wbError is a WorkBuddy business error: its code lets a poll loop tell a
// "come back" answer (retry) from a real failure.
type wbError struct {
	code int
	msg  string
}

func (e *wbError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("error %d", e.code)
}

func wbCallVia(ctx context.Context, do func(*http.Request) (*http.Response, error), method, u string, headers map[string]string, body, dst any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WorkBuddy/"+wbUAVersion)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var env struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(b, &env)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if code, _ := env.Code.Int64(); code != 0 {
			return &wbError{code: int(code), msg: env.Msg}
		}
		if env.Msg != "" {
			return &wbError{code: res.StatusCode, msg: env.Msg}
		}
		return &accountStatusError{status: res.StatusCode}
	}
	if code, _ := env.Code.Int64(); code != 0 {
		return &wbError{code: int(code), msg: env.Msg}
	}
	if dst == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, dst)
}

// textOnly marks the models of ms whose images w drops as taking none,
// whatever WorkBuddy's config says: WorkBuddy AI's GLM models. On
// 2026-10-13, with a real WorkBuddy AI account (Yirasumi on Discord),
// www.workbuddy.ai's /v3/config marked every model supportsImages: true,
// but a chat to glm-5.3-flash, glm-5.3 or glm-5.2 came back saying
// "[System reminder: the current model does not support images. Content
// filtered. ...]": the server had taken the image out. Its other models
// (deepseek-v4.1-flash, kimi, gemini, grok, gpt) read them, and WorkBuddy
// (China)'s glm-5.3-flash does too, so this is WorkBuddy AI's GLM family
// alone. Said text-only, magpie's image-describing model reads the image
// for them; a user's own answer (settings' ModelImages) still wins over
// this, in entryFor. ms is not changed; the result is a copy.
func (w *wbSite) textOnly(ms []catalog.Model) []catalog.Model {
	if w.id != WorkBuddyAIID {
		return ms
	}
	out := slices.Clone(ms)
	for i := range out {
		if strings.HasPrefix(out[i].ID, "glm-") {
			no := false
			out[i].Images, out[i].ImageInput = false, &no
		}
	}
	return out
}
