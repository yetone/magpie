package provider

// A Factory subscription is the account Factory's Droid CLI signs in to
// (factory.ai: Pro, Plus, Max). Its models are served under Factory's own
// API, each on the wire it speaks natively: Claude on Anthropic's Messages
// at /api/llm/a, GPT and Grok on OpenAI's Responses at /api/llm/o/v1, the
// open models Factory hosts on chat completions beside it. The model field
// is Factory's own model id; the server picks the vendor behind it.
//
// The sign-in is droid's own: WorkOS's device flow under droid's client, run
// by magpie and kept in logins.json. droid's own login is encrypted with a
// key in the keychain and its refresh token rotates, so magpie never reads
// or shares it; each account magpie signs in is its own.
//
// Read from droid 0.229.0 (the npm package @factory/cli-darwin-arm64).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Where Factory and its WorkOS sign-in are; vars so tests can point them
// elsewhere.
var (
	factoryWorkOS = "https://api.workos.com/user_management"
	factoryAPI    = "https://api.factory.ai"
	factoryAPIEU  = "https://api.eu.factory.ai"
)

const (
	// factoryClientID is droid's WorkOS client, production.
	factoryClientID = "client_01HNM792M5G5G1A2THWPXKFMXB"
	// factoryVersion is the droid release magpie's requests say they are.
	factoryVersion = "0.229.0"
	// factoryRefreshLead is how long before an access token lapses it is
	// renewed; droid renews a minute ahead, magpie a little more.
	factoryRefreshLead = 2 * time.Minute
)

var (
	factoryClient = &http.Client{Timeout: 30 * time.Second}
	// factoryMu serializes checking, rotating and saving tokens: WorkOS
	// rotates the refresh token, so two refreshes would spend it twice.
	factoryMu sync.Mutex
	// factorySession is the session id this magpie's requests carry.
	factorySession = randomUUID()
)

// factoryCreds is what magpie keeps of a Factory account.
type factoryCreds struct {
	Access    string `json:"accessToken"`
	Refresh   string `json:"refreshToken"`
	ExpiresAt int64  `json:"expiresAt"` // unix ms, from the token's exp
	// Org is the WorkOS organization (org_…) the token was put in, kept
	// for the record: it is never sent to Factory's API.
	Org string `json:"orgId,omitempty"`
	// Active is droid's active_organization_id: Factory's own id for the
	// org, as /api/cli/whoami answers it, sent as X-Factory-Org-Id. droid
	// asks whoami as soon as it holds a token and keeps its orgId, so its
	// requests carry it; an account with none asks before it sends.
	Active string `json:"activeOrganizationId,omitempty"`
	Email  string `json:"email,omitempty"`
	UserID string `json:"userId,omitempty"`
	Region string `json:"region,omitempty"` // "eu" for an org served from Factory's EU region
	// Prem is whoami's premBaseHostV2: an org Factory serves from a host
	// of its own, where droid sends its model requests instead.
	Prem string `json:"premBaseHost,omitempty"`
}

// base is the Factory API the account's org is served from.
func (c factoryCreds) base() string {
	if c.Region == "eu" {
		return factoryAPIEU
	}
	return factoryAPI
}

// llmBase is where the account's model requests go: the org's own host
// when whoami named one, else its region's API.
func (c factoryCreds) llmBase() string {
	if c.Prem != "" {
		p := strings.TrimRight(c.Prem, "/")
		if !strings.Contains(p, "://") {
			p = "https://" + p
		}
		return p
	}
	return c.base()
}

type factoryLogin struct {
	Login
	creds factoryCreds
}

func factorySaved(l savedLogin) (factoryCreds, bool) {
	var c factoryCreds
	if json.Unmarshal(l.Auth, &c) != nil || c.Access == "" {
		return factoryCreds{}, false
	}
	return c, true
}

// factoryLogins is every Factory account signed in, the first in use first.
func factoryLogins() []factoryLogin {
	var out []factoryLogin
	for _, l := range sideLogins("factory", "", func(l savedLogin) bool {
		_, ok := factorySaved(l)
		return ok
	}) {
		c, _ := factorySaved(l.saved)
		a := factoryLogin{Login: l.Login, creds: c}
		a.Lapsed = l.saved.Lapsed
		out = append(out, a)
	}
	return out
}

func factorySide() []sideLogin {
	var out []sideLogin
	for _, l := range factoryLogins() {
		out = append(out, sideLogin{Login: l.Login})
	}
	return out
}

func factoryLoginList() []Login { return loginsOf(factorySide()) }

func switchFactoryLogin(user string) error {
	return switchSideLogin("factory", user, factorySide())
}

func setFactoryLoginOn(user string, on bool) error {
	return setSideLoginOn("factory", user, on, factorySide())
}

func forgetFactoryLogin(user string) error {
	return forgetSideLogin("factory", user, factorySide(), nil)
}

// ---- keeping the sign-in --------------------------------------------------

// factoryStatus is an answer Factory or WorkOS gave with an error status.
type factoryStatus struct {
	Code int
	Msg  string
}

func (e *factoryStatus) Error() string { return e.Msg }

// factoryRefused is WorkOS turning the refresh token away for good: any
// 4xx but a rate limit, as droid reads it.
func factoryRefused(err error) bool {
	var st *factoryStatus
	return errors.As(err, &st) && st.Code >= 400 && st.Code < 500 && st.Code != http.StatusTooManyRequests
}

// factoryTokens is WorkOS's answer to /authenticate.
type factoryTokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Org     string `json:"organization_id"`
	User    struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
	Error  string `json:"error"`
	Desc   string `json:"error_description"`
	status int
}

// factoryPost posts a form to WorkOS: the answer and its status.
func factoryPost(ctx context.Context, path string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, factoryWorkOS+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := factoryClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return b, res.StatusCode, nil
}

// factoryAuthenticate asks WorkOS for tokens. A 400 carrying an OAuth error
// is an answer, not a failure: the device flow's polls are told to wait so.
func factoryAuthenticate(ctx context.Context, form url.Values) (factoryTokens, error) {
	var t factoryTokens
	b, code, err := factoryPost(ctx, "/authenticate", form)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal(b, &t)
	t.status = code
	if code != http.StatusOK && t.Error == "" {
		return t, &factoryStatus{code, "Factory sign-in: " + APIError(b, http.StatusText(code))}
	}
	return t, nil
}

// factoryRenew trades a refresh token for a new pair, in the WorkOS org
// when one is named (droid's way of putting a token that names no org in
// one); droid's routine refresh names none, and WorkOS keeps the org.
func factoryRenew(ctx context.Context, refresh, org string) (factoryTokens, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {factoryClientID}}
	if org != "" {
		form.Set("organization_id", org)
	}
	t, err := factoryAuthenticate(ctx, form)
	if err == nil && t.Error != "" {
		err = &factoryStatus{t.status, "Factory: " + strings.TrimSpace(t.Error+" "+t.Desc)}
	}
	if err == nil && t.Access == "" {
		err = errors.New("Factory: the refresh gave no access token")
	}
	return t, err
}

// factoryExpiry is when an access token lapses, from its exp; zero when it
// doesn't say.
func factoryExpiry(access string) int64 {
	if exp, _ := jwtClaims(access)["exp"].(float64); exp > 0 {
		return int64(exp) * 1000
	}
	return 0
}

// factoryEdit saves c over the account named user.
func factoryEdit(user string, c factoryCreds, renewed bool) error {
	return editSideLogin("factory", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		b, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		ls[i].Auth, ls[i].Seen = b, time.Now().UTC()
		if renewed {
			ls[i].Renewed, ls[i].Lapsed = time.Now().UTC(), ""
		}
		return ls, nil
	})
}

// factoryLapse records that WorkOS refused an account's refresh token.
func factoryLapse(user string, err error) error {
	if !factoryRefused(err) {
		return err
	}
	msg := user + "'s Factory sign-in has expired — sign in again"
	_ = editSideLogin("factory", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = msg
		return ls, nil
	})
	return fmt.Errorf("%s (%w)", msg, err)
}

func factoryLookup(user string) (savedLogin, bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "factory" && strings.EqualFold(l.User, user) {
			return l, true
		}
	}
	return savedLogin{}, false
}

// factoryFresh answers with a live token for one account, renewing it near
// its end. The renewal runs on a context of its own: once WorkOS has rotated
// the pair, its reply must be kept even if the request that asked is gone.
func factoryFresh(ctx context.Context, user string) (factoryCreds, error) {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	l, ok := factoryLookup(user)
	if !ok {
		return factoryCreds{}, fmt.Errorf("no Factory account %q", user)
	}
	c, valid := factorySaved(l)
	if !valid {
		return factoryCreds{}, errors.New("Factory: unreadable sign-in")
	}
	if c.ExpiresAt > 0 && time.Now().UnixMilli() < c.ExpiresAt-factoryRefreshLead.Milliseconds() {
		return factoryOrgOf(ctx, l.User, c), nil
	}
	if c.Refresh == "" {
		return factoryOrgOf(ctx, l.User, c), nil // nothing to renew it with; let the request try what there is
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	t, err := factoryRenew(rctx, c.Refresh, "")
	cancel()
	if err != nil {
		// a hiccup while the token still runs: go on with it
		if !factoryRefused(err) && c.ExpiresAt > 0 && time.Now().UnixMilli() < c.ExpiresAt {
			return factoryOrgOf(ctx, l.User, c), nil
		}
		return factoryCreds{}, factoryLapse(l.User, err)
	}
	c.Access, c.ExpiresAt = t.Access, factoryExpiry(t.Access)
	if t.Refresh != "" {
		c.Refresh = t.Refresh
	}
	// droid asks whoami again for each new token, keeping the org it names
	factoryReconcile(ctx, &c)
	factoryAsked.Store(strings.ToLower(l.User), time.Now())
	return c, factoryEdit(l.User, c, true)
}

// factoryAsked is when each account last asked whoami for its org, so an
// account whoami can't answer doesn't ask before every request.
var factoryAsked sync.Map

// factoryAskAgain is how long an account whose whoami failed waits before
// asking again.
const factoryAskAgain = 10 * time.Minute

// factoryOrgOf fills in an account's active org when it has none: droid
// asks whoami as soon as it holds a token (its auth's Do → Ar) and sends the
// orgId it answers as X-Factory-Org-Id on every request after; Factory
// answered one with none "Forbidden" (#242). Logins kept before magpie
// asked have none. Called under factoryMu.
func factoryOrgOf(ctx context.Context, user string, c factoryCreds) factoryCreds {
	if c.Active != "" {
		return c
	}
	key := strings.ToLower(user)
	if at, ok := factoryAsked.Load(key); ok && time.Since(at.(time.Time)) < factoryAskAgain {
		return c
	}
	factoryAsked.Store(key, time.Now())
	if factoryReconcile(ctx, &c) {
		_ = factoryEdit(user, c, false)
	}
	return c
}

// factoryWho is what /api/cli/whoami tells of an account.
type factoryWho struct {
	UserID string `json:"userId"`
	OrgID  string `json:"orgId"`
	Email  string `json:"email"`
	Region string `json:"region"`
	Prem   string `json:"premBaseHostV2"`
}

// factoryWhoami asks Factory whose the token is with the headers droid's
// whoami (ZA) sends: the token, X-Factory-Whoami-Extended, and the active
// org when there is one — nothing else.
func factoryWhoami(ctx context.Context, c factoryCreds) (factoryWho, error) {
	var who factoryWho
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/api/cli/whoami", nil)
	if err != nil {
		return who, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Access)
	req.Header.Set("X-Factory-Whoami-Extended", "true")
	if c.Active != "" {
		req.Header.Set("X-Factory-Org-Id", c.Active)
	}
	res, err := factoryClient.Do(req)
	if err != nil {
		return who, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return who, &factoryStatus{res.StatusCode, "Factory: " + APIError(b, res.Status)}
	}
	return who, json.Unmarshal(b, &who)
}

// factoryReconcile asks whoami, as droid does for each token it holds, and
// keeps the org, region and host it names: true when any changed. An active
// org whoami refuses is left off and whoami asked again without it.
func factoryReconcile(ctx context.Context, c *factoryCreds) bool {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	who, err := factoryWhoami(wctx, *c)
	var st *factoryStatus
	if c.Active != "" && errors.As(err, &st) && st.Code == http.StatusForbidden {
		without := *c
		without.Active = ""
		who, err = factoryWhoami(wctx, without)
	}
	if err != nil || who.OrgID == "" {
		return false
	}
	before := *c
	c.Active, c.Region, c.Prem = who.OrgID, who.Region, who.Prem
	return *c != before
}

// ---- Factory's API ----------------------------------------------------------

// factoryHeaders are what droid sends on every call to Factory's API.
func factoryHeaders(h http.Header, c factoryCreds) {
	h.Set("Authorization", "Bearer "+c.Access)
	h.Set("X-Factory-Client", "cli")
	h.Set("X-Client-Version", factoryVersion)
	h.Set("User-Agent", "factory-cli/"+factoryVersion)
	if c.Active != "" {
		h.Set("X-Factory-Org-Id", c.Active)
	}
}

// factoryOrgRefused is Factory's 403 for an X-Factory-Org-Id the user can't
// reach: "Requested active organization is not accessible by this user".
func factoryOrgRefused(status int, body []byte) bool {
	return status == http.StatusForbidden && strings.Contains(strings.ToLower(string(body)), "active organization is not accessible")
}

// factoryMendOrg answers Factory refusing an account's request. An active
// org it can't reach is left off and whoami asked again without it (droid's
// org picker retries "without the active-org header" so), keeping the org it
// then names or none; with no header sent, the token is put in the first org
// /api/cli/org lists, as droid does for a token with none. Any other 403 to
// an account that sent no org asks whoami for one, as droid would have
// before its first request. True when there was something to change and the
// request is worth resending.
func factoryMendOrg(ctx context.Context, user string, status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	refused := factoryOrgRefused(status, body)
	factoryMu.Lock()
	defer factoryMu.Unlock()
	l, ok := factoryLookup(user)
	if !ok {
		return false
	}
	c, valid := factorySaved(l)
	if !valid {
		return false
	}
	if c.Active != "" {
		if !refused {
			return false // the org was sent: the refusal is about something else
		}
		was := c.Active
		c.Active = ""
		if factoryReconcile(ctx, &c) && c.Active == was {
			c.Active = "" // whoami names the org refused: send none
		}
		factoryAsked.Store(strings.ToLower(l.User), time.Now())
		return factoryEdit(l.User, c, false) == nil
	}
	if !refused {
		// no org was sent: ask whoami for the one droid would have sent
		factoryAsked.Store(strings.ToLower(l.User), time.Now())
		return factoryReconcile(ctx, &c) && c.Active != "" && factoryEdit(l.User, c, false) == nil
	}
	if c.Refresh == "" {
		return false
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	org, err := factoryFirstOrg(rctx, c)
	if err != nil || org == "" {
		return false
	}
	t, err := factoryRenew(rctx, c.Refresh, org)
	if err != nil {
		return false
	}
	c.Access, c.ExpiresAt, c.Org = t.Access, factoryExpiry(t.Access), org
	if t.Refresh != "" {
		c.Refresh = t.Refresh
	}
	return factoryEdit(l.User, c, true) == nil
}

// factoryExplain is what the user can do about a 403 Factory still answers
// once magpie has sent what droid sends. Factory takes a subscription's
// model requests only from Droid: in #242 the same account's Claude, GPT and
// GLM models answered droid through magpie every time, with magpie's
// headers, and refused Grok Build's and Claude Code's every time, on the
// same models and efforts, a request's body (droid's system prompt opens
// with its own "You are Droid…") the only difference. So the first thing to
// say is to use the models from Droid; an org's model policy or the plan is
// what is left when Droid is refused too.
func factoryExplain(status int, body []byte) string {
	if status != http.StatusForbidden {
		return ""
	}
	if factoryOrgRefused(status, body) {
		return "the Factory account's organization changed; remove the account in magpie and sign in to it again"
	}
	return "Factory takes a Factory subscription's requests only from Droid itself: other agents' (Claude Code, Grok Build…) are refused even on models Droid runs through magpie, so use the Factory models from Droid; if Droid is refused too, the organization's model policy or the plan doesn't allow this model"
}

// factoryFirstOrg is the first WorkOS org /api/cli/org says the account is
// in, "" for none. droid asks it with the bearer token alone.
func factoryFirstOrg(ctx context.Context, c factoryCreds) (string, error) {
	var orgs struct {
		IDs []string `json:"workosOrgIds"`
	}
	c.Active = ""
	if err := factoryGet(ctx, c, "/api/cli/org", nil, &orgs); err != nil {
		return "", err
	}
	if len(orgs.IDs) == 0 {
		return "", nil
	}
	return orgs.IDs[0], nil
}

// factoryGet reads a JSON answer from Factory's API.
func factoryGet(ctx context.Context, c factoryCreds, path string, extra map[string]string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return err
	}
	factoryHeaders(req.Header, c)
	for k, x := range extra {
		req.Header.Set(k, x)
	}
	req.Header.Set("Accept", "application/json")
	res, err := factoryClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return &factoryStatus{res.StatusCode, "Factory: " + APIError(b, res.Status)}
	}
	return json.Unmarshal(b, v)
}

// ---- models ---------------------------------------------------------------

// factoryModel is one of the models Factory serves a subscription, as
// droid's registry lists it.
type factoryModel struct {
	id, name string
	api      Protocol // the wire droid sends it on
	upstream string   // the vendor droid names in x-api-provider
	context  int
	output   int
	efforts  []string
	images   bool
}

// factoryModels are the ones droid's /model picker offers, less Gemini's
// (sent on a route of Factory's own) and auto (droid picks it client side).
var factoryModels = []factoryModel{
	{"claude-fable-5.1", "Fable 5.1", Anthropic, "anthropic", 867000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, true},
	{"claude-fable-5", "Fable 5", Anthropic, "anthropic", 867000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, true},
	{"claude-opus-5-5", "Opus 5.5", Anthropic, "anthropic", 872000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, true},
	{"claude-opus-5", "Opus 5", Anthropic, "anthropic", 867000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, true},
	{"claude-opus-4-8", "Opus 4.8", Anthropic, "anthropic", 867000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, true},
	{"claude-sonnet-5-5", "Sonnet 5.5", Anthropic, "anthropic", 872000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, true},
	{"claude-sonnet-5", "Sonnet 5", Anthropic, "anthropic", 872000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, true},
	{"claude-sonnet-4-6", "Sonnet 4.6", Anthropic, "anthropic", 931000, 64000, []string{"low", "medium", "high", "max"}, true},
	{"claude-haiku-4-5-20251001", "Haiku 4.5", Anthropic, "anthropic", 0, 0, []string{"low", "medium", "high"}, true},
	{"gpt-6-sol", "GPT-6 Sol", Responses, "openai", 1050000, 128000, []string{"none", "low", "medium", "high", "xhigh", "max"}, true},
	{"gpt-6-astra", "GPT-6 Astra", Responses, "openai", 1050000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, true},
	{"gpt-6-luna", "GPT-6 Luna", Responses, "openai", 1050000, 128000, []string{"none", "low", "medium", "high", "xhigh", "max"}, true},
	{"gpt-5.6-sol", "GPT-5.6 Sol", Responses, "openai", 1050000, 128000, []string{"none", "low", "medium", "high", "xhigh", "max"}, true},
	{"gpt-5.6-terra", "GPT-5.6 Terra", Responses, "openai", 1050000, 128000, []string{"none", "low", "medium", "high", "xhigh", "max"}, true},
	{"gpt-5.6-luna", "GPT-5.6 Luna", Responses, "openai", 1050000, 128000, []string{"none", "low", "medium", "high", "xhigh", "max"}, true},
	{"gpt-5.5", "GPT-5.5", Responses, "openai", 1050000, 128000, []string{"low", "medium", "high", "xhigh"}, true},
	{"gpt-5.4", "GPT-5.4", Responses, "openai", 1050000, 128000, []string{"low", "medium", "high", "xhigh"}, true},
	{"gpt-5.3-codex", "GPT-5.3-Codex", Responses, "openai", 400000, 128000, []string{"low", "medium", "high", "xhigh"}, true},
	{"grok-4.7", "Grok 4.7", Responses, "xai", 500000, 63356, []string{"low", "medium", "high", "xhigh"}, true},
	{"grok-4.6", "Grok 4.6", Responses, "xai", 200000, 63356, []string{"low", "medium", "high", "xhigh"}, true},
	{"glm-5.3", "GLM-5.3", Chat, "fireworks", 1040000, 131072, []string{"low", "high", "max"}, false},
	{"glm-5.3-flash", "GLM-5.3-Flash", Chat, "fireworks", 1048576, 131072, []string{"low", "high", "max"}, false},
	{"glm-5.2", "GLM-5.2", Chat, "baseten", 1040000, 131072, []string{"high", "max"}, false},
	{"kimi-k3", "Kimi K3", Chat, "fireworks", 262144, 65536, []string{"low", "high", "max"}, true},
	{"deepseek-v4.1-flash", "DeepSeek V4.1 Flash", Chat, "fireworks", 1040000, 131072, []string{"low", "high", "max"}, true},
	{"qwen3.8-max", "Qwen3.8 Max", Chat, "fireworks", 262144, 131072, []string{"low", "medium", "xhigh"}, false},
	{"minimax-m3", "MiniMax M3", Chat, "fireworks", 512000, 64000, []string{"high"}, true},
	{"minimax-m2.7", "MiniMax M2.7", Anthropic, "fireworks", 196600, 64000, []string{"high"}, false},
	{"mistral-medium-3.5", "Mistral Medium 3.5", Chat, "mistral", 256000, 64000, []string{"high"}, true},
	{"nemotron-3-ultra", "Nemotron 3 Ultra", Chat, "baseten", 202000, 65536, []string{"high"}, false},
}

// factoryCore is whether a model is one of the open ones Factory hosts,
// billed to Droid Core; droid's registry has every one of them as provider
// "factory", served by Fireworks, Baseten or Mistral.
func factoryCore(id string) bool {
	m, ok := factoryModelOf(id)
	return ok && m.upstream != "anthropic" && m.upstream != "openai" && m.upstream != "xai"
}

func factoryModelOf(id string) (factoryModel, bool) {
	for _, m := range factoryModels {
		if m.id == id {
			return m, true
		}
	}
	return factoryModel{}, false
}

// factoryCatalog is the models as the picker lists them, each with the one
// API Factory serves it on.
func factoryCatalog() []catalog.Model {
	out := make([]catalog.Model, 0, len(factoryModels))
	for _, m := range factoryModels {
		out = append(out, catalog.Model{ID: m.id, Name: m.name, Provider: "factory", Efforts: m.efforts,
			APIs: []string{string(m.api)}, Images: m.images, Context: m.context, Output: m.output})
	}
	return out
}

// factoryAPIs is the API a model is served on; nil for one droid didn't
// list, which may be tried on any.
func factoryAPIs(model string) []Protocol {
	if m, ok := factoryModelOf(model); ok {
		return []Protocol{m.api}
	}
	return nil
}

// ---- the provider ---------------------------------------------------------

func factoryProvider(a factoryLogin) Provider {
	user := a.User
	acct := &Account{Agent: "factory", User: user, Plan: a.Plan}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		c, err := factoryFresh(ctx, user)
		if err != nil {
			return err
		}
		// an EU org is served from Factory's EU region, an on-prem one from
		// its own host (whoami's premBaseHostV2): the request goes there
		if base := c.llmBase(); base != factoryAPI && strings.HasPrefix(req.URL.String(), factoryAPI) {
			if u, err := url.Parse(base + strings.TrimPrefix(req.URL.String(), factoryAPI)); err == nil {
				req.URL, req.Host = u, u.Host
			}
		}
		factoryHeaders(req.Header, c)
		model := bodyModel(body)
		upstream := "anthropic"
		if m, ok := factoryModelOf(model); ok {
			upstream = m.upstream
		} else if strings.Contains(req.URL.Path, "/llm/o/") {
			upstream = "openai"
		}
		req.Header.Set("x-api-provider", upstream)
		req.Header.Set("x-session-id", factorySession)
		req.Header.Set("x-assistant-message-id", randomUUID())
		req.Header.Set("x-provider-routing-source", "registry_default")
		if upstream == "openai" {
			req.Header.Set("OpenAI-Platform", "org-bHuLtG1fGmYk5YaOihAAXFBw")
		}
		if strings.Contains(req.URL.Path, "/llm/a/") {
			// droid's Anthropic client is made with the key "placeholder",
			// which Anthropic's SDK sends beside the bearer token
			req.Header.Set("X-Api-Key", "placeholder")
		}
		return nil
	}
	acct.retry = func(ctx context.Context, _ string, status int, body []byte) bool {
		return factoryMendOrg(ctx, user, status, body)
	}
	acct.explain = factoryExplain
	acct.models = factoryCatalog
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms := factoryCatalog()
		return ms, catalog.SaveLive("factory", factoryAPI+"/api/llm/o/v1", ms)
	}
	return Provider{ID: "factory", Name: "Factory", Icon: "factory",
		Anthropic: factoryAPI + "/api/llm/a", Responses: factoryAPI + "/api/llm/o/v1", Chat: factoryAPI + "/api/llm/o/v1",
		Website: "https://factory.ai", Account: acct}
}

func factoryAccount() (Provider, bool) {
	ls := factoryLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return factoryProvider(ls[0]), true
}

// factoryAlsoOn is the Factory accounts in use behind the first.
func factoryAlsoOn() []Provider {
	var out []Provider
	for _, l := range factoryLogins() {
		if !l.Active && l.On {
			out = append(out, factoryProvider(l))
		}
	}
	return out
}
