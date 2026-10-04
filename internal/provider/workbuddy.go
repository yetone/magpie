package provider

// PLUGIN-SERVED (see AGENTS.md): WorkBuddy ("workbuddy" and "workbuddy-ai")
// is a deprecated built-in subscription served by its plugin,
// @magpie-community/opencode-workbuddy-auth, once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's sign-ins,
// models, requests and usage are all the plugin's, never this code's (only
// the move, in migrate*.go, still reads its accounts). A fix here alone
// doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/workbuddy) and raise the
// mover's min in internal/provider/migrate_workbuddy.go.

// A WorkBuddy subscription is Tencent's CodeBuddy plan, which WorkBuddy
// (its desktop app, packaged from CodeBuddy Code) signs in to. The plan is
// served on an OpenAI-compatible endpoint under the account's own access
// token — a JWT the account refreshes — with the account's id and domain in
// headers, exactly as WorkBuddy sends them.
//
// WorkBuddy's own account is read, never changed, from its auth store,
// <shared data>/auth/workbuddy-desktop.info (plain JSON while credential
// protection is off, which it is by default); its access and refresh tokens
// and the account are taken as they sit there. Further accounts are signed
// in by magpie with WorkBuddy's own external-link flow (auth/state, then
// auth/token polled until ready, then login/account), and their tokens are
// kept in logins.json. A near-expired token is refreshed as WorkBuddy does,
// and a magpie-signed-in one's refresh is written back beside it.
//
// The plan's models are WorkBuddy's CLI agent's, from its product config
// (workbuddy_models.go); wbModels are them before that is read.
//
// WorkBuddy AI, the international build (www.workbuddy.ai, the CodeBuddy
// plan sold at www.codebuddy.ai), is its own subscription, workbuddy-ai: the
// same API and sign-in at its own endpoint, its own models, and its own
// account file (workbuddy-desktop-ai.info) beside the other. A wbSite is
// which of the two an account belongs to.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Where WorkBuddy's plan and its sign-in are; vars so tests can point them
// elsewhere.
var (
	wbEndpoint = "https://copilot.tencent.com"
	// wbAIEndpoint is WorkBuddy AI's, the international build's.
	wbAIEndpoint = "https://www.workbuddy.ai"
	// wbAppVersion is the plugin version the sign-in page is told.
	wbAppVersion = "2.0.0"
	// wbUAVersion is the WorkBuddy desktop version the User-Agent carries.
	// copilot.tencent.com's gateway rejects a request whose User-Agent it
	// doesn't recognise with code 10085 ("请求不合法"), so every call to it —
	// chat and billing alike — must go out as WorkBuddy/<version>.
	wbUAVersion = "5.5.6"
	// wbPollInterval is how often the sign-in asks whether it is ready.
	wbPollInterval = time.Second
)

// wbModels are the plan's models, WorkBuddy's CLI agent's coding picks, with
// the names and context windows its config gives them.
var wbModels = []catalog.Model{
	{ID: "auto", Name: "Auto", Context: 168_000},
	{ID: "hy4-preview-f", Name: "Hy4 preview", Context: 1_000_000, Efforts: []string{"high"}},
	{ID: "hy3", Name: "Hy3", Context: 192_000, Efforts: []string{"low", "high"}},
	{ID: "hy3-x", Name: "Hy3-X", Context: 192_000, Efforts: []string{"low", "high"}},
	{ID: "deepseek-v4.1-flash", Name: "Deepseek-V4.1-Flash", Context: 1_000_000},
	{ID: "glm-5.3", Name: "GLM-5.3", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "glm-5.3-flash", Name: "GLM-5.3-Flash", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "glm-5.2", Name: "GLM-5.2", Context: 1_000_000, Efforts: []string{"high", "xhigh"}},
	{ID: "glm-5.1", Name: "GLM-5.1", Context: 200_000},
	{ID: "glm-5v-turbo", Name: "GLM-5v-Turbo", Context: 200_000},
	{ID: "minimax-m3", Name: "MiniMax-M3", Context: 512_000},
	{ID: "kimi-k3-1", Name: "Kimi-K3", Context: 1_000_000, Efforts: []string{"low", "high", "xhigh"}},
	{ID: "kimi-k2.7", Name: "Kimi-K2.7-Code", Context: 256_000},
	{ID: "kimi-k2.6", Name: "Kimi-K2.6", Context: 256_000},
	{ID: "deepseek-v4-pro", Name: "Deepseek-V4-Pro", Context: 1_000_000, Efforts: []string{"none", "high", "xhigh"}},
}

// wbAIModels are WorkBuddy AI's, from its product config: its tiers and
// the models of its picker.
var wbAIModels = []catalog.Model{
	{ID: "default-model", Name: "Default", Context: 176_000},
	{ID: "fast-model", Name: "Fast", Context: 200_000},
	{ID: "balanced-model", Name: "Balanced", Context: 256_000},
	{ID: "primary-model", Name: "Primary", Context: 272_000},
	{ID: "deep-model", Name: "Deep", Context: 176_000},
	{ID: "gpt-5.5", Name: "GPT-5.5", Context: 1_000_000},
	{ID: "gpt-5.4", Name: "GPT-5.4", Context: 272_000},
	{ID: "gpt-5.3-codex", Name: "GPT-5.3-Codex", Context: 272_000},
	{ID: "gemini-3.1-pro", Name: "Gemini-3.1-Pro", Context: 400_000},
	{ID: "gemini-3.5-flash", Name: "Gemini-3.5-Flash", Context: 1_000_000},
	{ID: "glm-5.3", Name: "GLM-5.3", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "glm-5.2", Name: "GLM-5.2", Context: 1_000_000, Efforts: []string{"high", "xhigh"}},
	{ID: "hy3", Name: "Hy3", Context: 192_000, Efforts: []string{"low", "high"}},
	{ID: "kimi-k3", Name: "Kimi-K3", Context: 1_000_000},
	{ID: "kimi-k2.6", Name: "Kimi-K2.6", Context: 256_000},
	{ID: "minimax-m3", Name: "MiniMax-M3", Context: 512_000},
}

// wbSite is one WorkBuddy build: the Chinese one or WorkBuddy AI.
type wbSite struct {
	id, name, website string
	authID            string  // its account file, <authID>.info
	platform          string  // what its sign-in says it is
	endpoint          *string // its API root
	models            []catalog.Model
}

var (
	wbCN = &wbSite{id: "workbuddy", name: "WorkBuddy", website: "https://www.codebuddy.cn",
		authID: "workbuddy-desktop", platform: "workbuddy", endpoint: &wbEndpoint, models: wbModels}
	wbAI = &wbSite{id: WorkBuddyAIID, name: "WorkBuddy AI", website: "https://www.workbuddy.ai",
		authID: "workbuddy-desktop-ai", platform: "workbuddy-ai", endpoint: &wbAIEndpoint, models: wbAIModels}
)

// WorkBuddyBaseForTest points WorkBuddy's API at cn, and WorkBuddy AI's at
// ai, until the returned function runs. A provider built after the call
// uses them. Tests outside this package use it.
func WorkBuddyBaseForTest(cn, ai string) func() {
	oldC, oldA := wbEndpoint, wbAIEndpoint
	wbEndpoint, wbAIEndpoint = cn, ai
	return func() {
		wbEndpoint, wbAIEndpoint = oldC, oldA
	}
}

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
	return out
}

func wbSide(w *wbSite) []sideLogin {
	var out []sideLogin
	for _, a := range wbLogins(w) {
		out = append(out, sideLogin{Login: a.Login})
	}
	return out
}

func wbLoginList(w *wbSite) []Login { return loginsOf(wbSide(w)) }

func switchWorkBuddyLogin(w *wbSite, user string) error {
	return switchSideLogin(w.id, user, wbSide(w))
}

func setWorkBuddyLoginOn(w *wbSite, user string, on bool) error {
	return setSideLoginOn(w.id, user, on, wbSide(w))
}

func forgetWorkBuddyLogin(w *wbSite, user string) error {
	return forgetSideLogin(w.id, user, wbSide(w), nil)
}

func workBuddyAccount(w *wbSite) (Provider, bool) {
	ls := wbLogins(w)
	if len(ls) == 0 {
		return Provider{}, false
	}
	return wbProvider(ls[0]), true
}

// workBuddyAlsoOn is w's accounts in use behind the first.
func workBuddyAlsoOn(w *wbSite) []Provider {
	var out []Provider
	for _, a := range wbLogins(w) {
		if !a.Active && a.On {
			out = append(out, wbProvider(a))
		}
	}
	return out
}

func wbProvider(a wbAccount) Provider {
	w := a.site
	// WorkBuddy refuses a request that doesn't stream: "Non-stream chat
	// request is currently not supported" (#124)
	acct := &Account{Agent: w.id, User: a.User, Plan: a.Plan, Stream: true}
	acct.body = wbBody
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		c, err := wbFresh(ctx, a)
		if err != nil {
			return err
		}
		req.Header.Del("Authorization")
		req.Header.Set("Authorization", "Bearer "+c.Access)
		req.Header.Set("X-User-Id", c.UID)
		req.Header.Set("X-Domain", wbDomain(w, c))
		req.Header.Set("X-Product", "SaaS")
		req.Header.Set("X-IDE-Type", "WorkBuddy")
		req.Header.Set("User-Agent", "WorkBuddy/"+wbUAVersion)
		if w.id == WorkBuddyAIID {
			wbClientHeaders(req)
		}
		return nil
	}
	acct.explain = wbExplain
	acct.models = func() []catalog.Model { return w.models }
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := wbFetchModels(ctx, w, acct.sign)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive(w.id, w.api()+"/v2", ms)
	}
	return Provider{ID: w.id, Name: w.name, Icon: "workbuddy-color", Chat: w.api() + "/v2", Website: w.website, Account: acct}
}

// wbSystem is the system message a chat that has none is sent with.
const wbSystem = "You are a helpful assistant."

// wbBody starts a chat with a system message when it has none: WorkBuddy
// refuses one whose first message isn't the system prompt ("first message
// is not system prompt"), and scripts and plain chat clients often send
// none.
func wbBody(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"messages"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	msgs, ok := m["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body
	}
	if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
		return body
	}
	m["messages"] = append([]any{map[string]any{"role": "system", "content": wbSystem}}, msgs...)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(m) != nil {
		return body
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// wbClientHeaders are the rest of what WorkBuddy's own chats carry to its
// /v2/chat/completions, as its desktop app lists them for that gateway
// (x-requested-with is "the gateway's admission convention"): the request
// marked as an XHR, the agent's intent, the client's name and version, and
// the conversation and request ids. They were added for WorkBuddy AI's
// "illegal API invocation from an unapproved channel", but that answer
// turned out to come from what the prompt says, not the headers: both
// builds give it to a chat whose system prompt is Claude Code's or Codex's
// own (#182). Only WorkBuddy AI gets them, as its own client sends them.
func wbClientHeaders(req *http.Request) {
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Type", "main")
	req.Header.Set("X-IDE-Name", "WorkBuddy")
	req.Header.Set("X-IDE-Version", wbUAVersion)
	id := wbRequestID()
	for _, h := range []string{"X-Conversation-ID", "X-Conversation-Request-ID"} {
		if req.Header.Get(h) == "" {
			req.Header.Set(h, id)
		}
	}
	id = wbRequestID()
	req.Header.Set("X-Conversation-Message-ID", id)
	req.Header.Set("X-Request-ID", id)
}

// WBRefusedHint is what the user can do about WorkBuddy's "Illegal API
// invocation from an unapproved channel": both builds (the CodeBuddy plan)
// answer it to a chat whose system prompt is Codex's or Claude Code's own
// (#182), whatever the headers. magpie never rewrites that prompt; the
// agent is told to use WorkBuddy from another agent instead.
const WBRefusedHint = "WorkBuddy refuses chats from Codex and Claude Code (their system prompt); use it from Hermes, OpenCode or Pi, or add another provider to this group"

// wbRefused matches that refusal in what WorkBuddy answered.
var wbRefused = regexp.MustCompile(`(?i)unapproved channel|illegal api invocation`)

// wbExplain adds WBRefusedHint to WorkBuddy's refusal of the client.
func wbExplain(status int, body []byte) string {
	if status >= 400 && wbRefused.Match(body) {
		return WBRefusedHint
	}
	return ""
}

// wbRequestID is a new id as WorkBuddy makes them: 32 hex digits.
func wbRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// wbDomain is the X-Domain a request carries: the account's own domain, or
// the endpoint's authority.
func wbDomain(w *wbSite, c wbCreds) string {
	if c.Domain != "" {
		return c.Domain
	}
	if u, err := url.Parse(w.api()); err == nil && u.Host != "" {
		return u.Host
	}
	return ""
}

// ---- tokens -------------------------------------------------------------------

// wbTokens caches each account's freshest tokens (by site and uid), so a token
// refreshed for one request is used by the next; WorkBuddy's own file is
// never written, and a magpie-added account's refresh goes to logins.json.
var wbTokens = struct {
	sync.Mutex
	m map[string]wbCreds
}{m: map[string]wbCreds{}}

// wbFresh is a's tokens, refreshed when the access token is about to lapse.
func wbFresh(ctx context.Context, a wbAccount) (wbCreds, error) {
	wbTokens.Lock()
	c := a.creds
	if cached, ok := wbTokens.m[a.site.id+"|"+a.creds.UID]; ok && cached.ExpiresAt >= c.ExpiresAt {
		c = cached
	}
	wbTokens.Unlock()

	if c.Access != "" && !wbNearExpiry(c.ExpiresAt) {
		return c, nil
	}
	if c.Refresh == "" || (c.RefreshExpiresAt > 0 && time.Now().UnixMilli() >= c.RefreshExpiresAt) {
		if c.Access != "" {
			return c, nil // no way to refresh; let the request try what there is
		}
		return wbCreds{}, errors.New("this WorkBuddy account is signed out; sign in again")
	}
	refreshed, err := wbRefresh(ctx, a.site, c)
	if err != nil {
		if c.Access != "" {
			return c, nil // a refresh hiccup: the current token may still work
		}
		return wbCreds{}, err
	}
	wbTokens.Lock()
	wbTokens.m[a.site.id+"|"+refreshed.UID] = refreshed
	wbTokens.Unlock()
	if !a.own {
		wbSaveCreds(a.site, a.User, refreshed)
	}
	return refreshed, nil
}

// wbNearExpiry is true within a minute of a token's end (or when unknown).
func wbNearExpiry(expiresAt int64) bool {
	if expiresAt == 0 {
		return false // the store didn't say; trust it until refused
	}
	return time.Now().UnixMilli() >= expiresAt-60_000
}

// wbRefresh trades a refresh token for a fresh access token, as WorkBuddy's
// auth provider does: POST /v2/plugin/auth/token/refresh with the refresh
// token in a header.
func wbRefresh(ctx context.Context, w *wbSite, c wbCreds) (wbCreds, error) {
	var got wbRefreshed
	err := wbCall(ctx, http.MethodPost, w.api()+"/v2/plugin/auth/token/refresh", map[string]string{
		"X-Refresh-Token":       c.Refresh,
		"X-Auth-Refresh-Source": "plugin",
		"X-Domain":              wbDomain(w, c),
	}, map[string]any{}, &got)
	if err != nil {
		return wbCreds{}, fmt.Errorf("WorkBuddy token refresh: %w", err)
	}
	if got.AccessToken == "" {
		return wbCreds{}, errors.New("WorkBuddy gave no refreshed token")
	}
	return wbMergeRefreshed(c, got), nil
}

type wbRefreshed struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"`
	ExpiresIn        int64  `json:"expiresIn"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn"`
	Domain           string `json:"domain"`
	TokenType        string `json:"tokenType"`
}

func wbMergeRefreshed(c wbCreds, got wbRefreshed) wbCreds {
	now := time.Now().UnixMilli()
	out := c
	out.Access = got.AccessToken
	if got.RefreshToken != "" {
		out.Refresh = got.RefreshToken
	}
	if got.Domain != "" {
		out.Domain = got.Domain
	}
	if got.TokenType != "" {
		out.TokenType = got.TokenType
	}
	switch {
	case got.ExpiresAt > 0:
		out.ExpiresAt = got.ExpiresAt
	case got.ExpiresIn > 0:
		out.ExpiresAt = now + got.ExpiresIn*1000
	}
	switch {
	case got.RefreshExpiresAt > 0:
		out.RefreshExpiresAt = got.RefreshExpiresAt
	case got.RefreshExpiresIn > 0:
		out.RefreshExpiresAt = now + got.RefreshExpiresIn*1000
	}
	return out
}

// wbSaveCreds writes a magpie-added account's refreshed tokens back to
// logins.json, so the next run starts from them.
func wbSaveCreds(w *wbSite, user string, c wbCreds) {
	_ = editSideLogin(w.id, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		if ls[i].own() {
			return ls, nil // never write WorkBuddy's own file
		}
		auth, err := json.Marshal(c)
		if err != nil {
			return ls, err
		}
		ls[i].Auth = auth
		ls[i].Renewed = time.Now().UTC().Truncate(time.Second)
		return ls, nil
	})
}

// ---- allowance ----------------------------------------------------------------

// wbQuota is a WorkBuddy account's credit allowance, from its resource
// summary: the plan's credits used against what the cycle grants.
func wbQuota(ctx context.Context, a wbAccount) SubscriptionQuota {
	q := SubscriptionQuota{Provider: a.site.id, Name: a.site.name, Icon: "workbuddy-color", Plan: a.Plan, User: a.User, Windows: []QuotaWindow{}}
	c, err := wbFresh(ctx, a)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	// The resource-summary meter (unlike the older /v2/billing meters) has
	// no /v2 gateway prefix — WorkBuddy asks it at /billing/meter/... on
	// both desktop and web.
	var sum wbResourceSummary
	if err := wbCall(ctx, http.MethodPost, a.site.api()+"/billing/meter/get-user-resource-summary", wbAuthHeaders(a.site, c), map[string]any{}, &sum); err != nil {
		q.Error = err.Error()
		return q
	}
	if sum.IsPaidUser {
		q.Plan = firstNonEmpty(a.Plan, "Pro")
	} else {
		q.Plan = firstNonEmpty(a.Plan, "Free")
	}
	var total, used float64
	for _, p := range sum.Packages {
		total += float64(p.CycleTotalCapacity)
		used += float64(p.CycleUsedCapacity)
	}
	if total > 0 {
		w := QuotaWindow{Name: "Credits", Used: 100 * used / total, Display: fmt.Sprintf("%s / %s", compactNumber(used), compactNumber(total)),
			Amount: used, Limit: total, Unit: "credits"}
		q.Windows = append(q.Windows, w)
	}
	return q
}

type wbResourceSummary struct {
	Packages []struct {
		PackageCode         string `json:"PackageCode"`
		CycleTotalCapacity  wbNum  `json:"CycleTotalCapacity"`
		CycleRemainCapacity wbNum  `json:"CycleRemainCapacity"`
		CycleUsedCapacity   wbNum  `json:"CycleUsedCapacity"`
	} `json:"Packages"`
	SubscriptionPackageCode string `json:"SubscriptionPackageCode"`
	IsPaidUser              bool   `json:"IsPaidUser"`
}

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

func wbAuthHeaders(w *wbSite, c wbCreds) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + c.Access,
		"X-User-Id":     c.UID,
		"X-Domain":      wbDomain(w, c),
		"X-Product":     "SaaS",
		"X-IDE-Type":    "WorkBuddy",
	}
}

func wbLoginQuota(ctx context.Context, w *wbSite, l Login) SubscriptionQuota {
	for _, a := range wbLogins(w) {
		if strings.EqualFold(a.User, l.User) {
			return wbQuota(ctx, a)
		}
	}
	return SubscriptionQuota{Provider: w.id, Plan: l.Plan, Windows: []QuotaWindow{}, Error: "not signed in"}
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

// WorkBuddy's retry codes: the token or the account isn't ready yet.
const (
	wbRetryToken   = 11217
	wbRetryAccount = 12151
)

// wbCall asks one of WorkBuddy's JSON endpoints, which wrap what they say in
// {code, msg, data}: code 0 is a success. On a non-zero code it returns a
// *wbError carrying it.
func wbCall(ctx context.Context, method, u string, headers map[string]string, body, dst any) error {
	return wbCallVia(ctx, http.DefaultClient.Do, method, u, headers, body, dst)
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

// ---- signing in ---------------------------------------------------------------

// startWorkBuddySignIn is WorkBuddy's own external-link sign-in, at w: the
// app asks for a state and a page, opens the page for the user to sign in,
// then polls for the token and the account.
func startWorkBuddySignIn(s *signInFlow, w *wbSite) error {
	ctx, cancel := context.WithCancel(context.Background())
	var state struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	stateHeaders := map[string]string{
		"X-No-Authorization":   "true",
		"X-No-User-Id":         "true",
		"X-No-Enterprise-Id":   "true",
		"X-No-Department-Info": "true",
	}
	if err := wbCall(ctx, http.MethodPost, w.api()+"/v2/plugin/auth/state?platform="+url.QueryEscape(w.platform), stateHeaders, map[string]any{}, &state); err != nil {
		cancel()
		return fmt.Errorf("WorkBuddy sign-in: %w", err)
	}
	u, err := url.Parse(state.AuthURL)
	if state.State == "" || err != nil || u.Scheme != "https" {
		cancel()
		return errors.New("WorkBuddy gave no sign-in page")
	}
	sid := make([]byte, 16)
	_, _ = rand.Read(sid)
	q := u.Query()
	q.Set("version", wbAppVersion)
	q.Set("loginSessionId", hex.EncodeToString(sid))
	u.RawQuery = q.Encode()
	s.mu.Lock()
	s.st.URL = u.String()
	s.stop = cancel
	s.mu.Unlock()

	go func() {
		defer cancel()
		fail := func(msg string) { s.finish(SignInState{State: "failed", Error: msg}) }
		deadline := time.Now().Add(5 * time.Minute)
		token, ok := wbPoll(ctx, w, deadline, "/v2/plugin/auth/token?state="+url.QueryEscape(state.State), nil, wbRetryToken, fail)
		if !ok {
			return
		}
		var tok wbRefreshed
		if json.Unmarshal(token, &tok) != nil || tok.AccessToken == "" {
			fail("WorkBuddy gave no token")
			return
		}
		c := wbMergeRefreshed(wbCreds{}, tok)
		acctHeaders := map[string]string{
			"Authorization":      "Bearer " + c.Access,
			"X-Domain":           wbDomain(w, c),
			"X-No-User-Id":       "true",
			"X-No-Enterprise-Id": "true",
		}
		account, ok := wbPoll(ctx, w, deadline, "/v2/plugin/login/account?state="+url.QueryEscape(state.State), acctHeaders, wbRetryAccount, fail)
		if !ok {
			return
		}
		var acc struct {
			UID         string `json:"uid"`
			Nickname    string `json:"nickname"`
			PhoneNumber string `json:"phoneNumber"`
		}
		if json.Unmarshal(account, &acc) != nil || acc.UID == "" {
			fail("WorkBuddy gave no account")
			return
		}
		c.UID = acc.UID
		who, again, using, err := wbKeepSignIn(w, c, wbWho(acc.Nickname, acc.PhoneNumber, acc.UID))
		if err != nil {
			fail(err.Error())
			return
		}
		wbTokens.Lock()
		wbTokens.m[w.id+"|"+c.UID] = c
		wbTokens.Unlock()
		s.finish(SignInState{State: "done", User: who, Using: using, Again: again})
	}()
	return nil
}

// wbKeepSignIn keeps an account magpie just signed in, told from the
// others by its WorkBuddy uid, never by its name: two accounts can share a
// nickname, and the second took the first one's place (#413). The same
// account again renews its sign-in where it is listed (again). The one
// WorkBuddy itself is signed in to becomes magpie's own sign-in of it, so
// it stays listed once WorkBuddy signs in to another: WorkBuddy's page
// offers the account the app is signed in to, and accounts added one by
// one that way each pushed the last off the list (#413). using says
// WorkBuddy is signed in to it too.
func wbKeepSignIn(w *wbSite, c wbCreds, name string) (who string, again, using bool, err error) {
	ownUser, own, hasOwn := wbOwn(w)
	auth, err := json.Marshal(c)
	if err != nil {
		return "", false, false, err
	}
	using = hasOwn && own.UID == c.UID
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	// uidOf is a saved account's uid, "" where it isn't known: WorkBuddy's
	// own while WorkBuddy can't be read, a sign-in that can't be read
	uidOf := func(l savedLogin) string {
		if l.own() {
			if hasOwn && strings.EqualFold(l.User, ownUser) {
				return own.UID
			}
			return ""
		}
		sc, _ := wbSavedCreds(l)
		return sc.UID
	}
	same := slices.IndexFunc(ls, func(l savedLogin) bool { return l.Agent == w.id && uidOf(l) == c.UID })
	if same < 0 {
		// one of the same name whose uid isn't known is taken to be it, as
		// before (#155); one whose uid is another's is another account
		same = slices.IndexFunc(ls, func(l savedLogin) bool {
			return l.Agent == w.id && strings.EqualFold(l.User, name) && uidOf(l) == ""
		})
	}
	now := time.Now().UTC().Truncate(time.Second)
	if same >= 0 {
		l := &ls[same]
		again = l.Hidden == ""
		if l.own() {
			// WorkBuddy's own is first unless another was put first: it
			// stays first as magpie's
			if !slices.ContainsFunc(ls, func(m savedLogin) bool { return m.Agent == w.id && m.First }) {
				l.First = true
			}
			l.On = true
		}
		l.Auth, l.Seen, l.Lapsed, l.Hidden = auth, now, "", ""
		return l.User, again, using, writeLogins(ls)
	}
	// another account of the same name is told apart by the end of its uid
	who = name + " (" + c.UID + ")"
	tail := c.UID[max(0, len(c.UID)-4):]
	for _, n := range []string{name, name + " (" + tail + ")"} {
		if !slices.ContainsFunc(ls, func(l savedLogin) bool { return l.Agent == w.id && strings.EqualFold(l.User, n) }) {
			who = n
			break
		}
	}
	return who, false, using, writeLogins(append(ls, savedLogin{Agent: w.id, User: who, Auth: auth, Seen: now, On: true}))
}

// wbPoll asks path every second until it answers with data, giving up at
// the deadline. A retry code (the answer isn't ready) waits and asks again;
// any other error ends the sign-in through fail.
func wbPoll(ctx context.Context, w *wbSite, deadline time.Time, path string, headers map[string]string, retryCode int, fail func(string)) (json.RawMessage, bool) {
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(wbPollInterval):
		}
		if time.Now().After(deadline) {
			fail("the sign-in expired; start again")
			return nil, false
		}
		var raw json.RawMessage
		err := wbCall(ctx, http.MethodGet, w.api()+path, headers, nil, &raw)
		switch {
		case ctx.Err() != nil:
			return nil, false
		case err == nil && len(raw) > 0 && string(raw) != "null":
			return raw, true
		case err == nil:
			continue // ready, but empty: ask again
		}
		var we *wbError
		if errors.As(err, &we) && we.code == retryCode {
			continue // not ready yet
		}
		var st *accountStatusError
		if errors.As(err, &st) && (st.status == 408 || st.status == 429) {
			continue // a hiccup: ask again
		}
		fail("WorkBuddy sign-in: " + err.Error())
		return nil, false
	}
}
