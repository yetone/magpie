package provider

// Accounts: agents the user has signed in to, offered as providers.
//
// Claude Code, Codex CLI (ChatGPT), and Copilot logins are subscriptions with
// models behind them. magpie reads the credentials the agent itself keeps on
// disk or in the macOS Keychain, so every other agent can use those models
// through the gateway. Nothing is stored twice: sign out of the agent and the
// provider is gone.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// Account is the signed-in agent behind a provider.
type Account struct {
	Agent string `json:"agent"`          // the agent's id: codex, copilot
	User  string `json:"user"`           // who is signed in: an email, a GitHub login
	Plan  string `json:"plan,omitempty"` // the subscription, when the agent says

	// Stream is set when the backend only streams; magpie then translates
	// a non-streaming request instead of relaying it.
	Stream bool `json:"-"`

	// Home is where a Grok, Kiro or Devin account keeps its sign-in: the
	// agent's own (the CLI's home for Grok, "" for Kiro and Devin), or one
	// of magpie's for a further account (grok_accounts.go, kiro_accounts.go,
	// devin_accounts.go).
	Home string `json:"-"`

	// token is set on a saved sign-in in use beside the agent's own (see
	// logins_on.go): the access token to run the agent's binary with.
	token func(ctx context.Context) (string, error)

	// standIn is a saved Claude account served in the place of Claude
	// Code's own while Claude Code is signed out (claude_dirs.go).
	standIn bool

	// unnamed is Claude Code's own sign-in while it is known by no name:
	// User is a name made from its plan ("Claude Max") until Claude Code
	// puts the account's profile in its config, as its first run does.
	unnamed bool

	// codeAssist is where a Gemini CLI or Antigravity account's requests
	// go (google.go).
	codeAssist string

	sign func(ctx context.Context, req *http.Request, body []byte) error
	// renew is set on a ChatGPT account: its sign-in refreshed in place of
	// stale, the access token the backend just called expired while it
	// had time left (codexTokenRenewing, renewSavedLogin).
	renew  func(ctx context.Context, stale string) (tok, accountID string, err error)
	body   func(body []byte) []byte // request tweaks the backend insists on
	models func() []catalog.Model
	fetch  func(ctx context.Context) ([]catalog.Model, error)
	// magpieList is set on an account whose fetch asks no vendor and
	// keeps a copy of models, magpie's own list (Claude's, from the
	// models.dev catalog; Factory's, compiled in): the list served is
	// always models as it is now, never the copy, which held a model
	// listed after it out until the next Refresh (wakaka on Discord:
	// Claude Haiku 5.5, in Claude Code on the same account, missing from
	// magpie).
	magpieList bool

	// auto is set on a Copilot account: the session of Copilot's Auto,
	// the model it picks for the account (copilot_auto.go).
	auto func(ctx context.Context) (copilotAutoSession, error)

	// retry is asked about a refusal the backend answered to a request for
	// model: true when the account has put right what it names and the
	// request is worth sending once more (a Factory org the server can't
	// reach, factory.go; another model for Copilot's Auto, copilot_refused.go).
	retry func(ctx context.Context, model string, status int, body []byte) bool
	// unusable is set on a Copilot account: whether a model its list offers
	// is one the account was refused (copilot_refused.go); and on a ZCode
	// account on the Start Plan: whether it is one only the Coding Plan has.
	unusable func(model string) bool
	// listedOnly is set on a ChatGPT account: the backend serves none of
	// the models its list leaves out ("The 'gpt-5.5' model is not
	// supported when using Codex with a ChatGPT account", #4772), so a
	// pick the fetched list doesn't have isn't offered.
	listedOnly bool
	// explain adds what the user can do about a refusal the account's
	// backend answered, "" when there is nothing to add (factory.go).
	explain func(status int, body []byte) string

	// plugin is set on a plugin's provider (plugins.go), and transport
	// carries its requests: the plugin's fetch.
	plugin    *plugin.Provider
	pluginKey string // the account's key in plugin-auth.json
	// wasHost is the built-in's API host, for a moved one's to show as it
	// did; moved says there is one to show ("" too: Zed's had none).
	wasHost   string
	moved     bool
	transport func(req *http.Request) (*http.Response, error)
	// clientFor is the client a request of the account's goes through in
	// place of the one it was given, nil for that one (zcode_start.go).
	clientFor func(req *http.Request) *http.Client
}

// APIs lists the APIs model is served on, as the provider's last model
// list said: Copilot serves its GPT models on Responses alone and its
// Claude models on Chat and Anthropic's. nil is not known, and every API
// the provider speaks may be tried. One the user set for the model
// (SetModelAPI) is the only one.
func (p Provider) APIs(model string) []Protocol { return p.apisAmong(p.Speaks(), model) }

// apisAmong is APIs with the protocols p speaks found already.
func (p Provider) apisAmong(speaks []Protocol, model string) []Protocol {
	// the one the user said it is asked on, whatever the list says
	if proto, ok := p.apiSet(model); ok {
		if proto, ok := p.modelAPIAmong(len(speaks), proto); ok {
			return []Protocol{proto}
		}
	}
	return p.ListedAPIs(model)
}

// ListedAPIs are the APIs model is served on as its vendor says, whatever
// the user set: what APIs gives with nothing set (the editor's Auto).
func (p Provider) ListedAPIs(model string) []Protocol {
	if p.IsPlugin() {
		return p.pluginAPIs(model)
	}
	// asked for every model of every agent: looked up, not the list
	// copied and scanned for each (2,500 models' /v1/models took 3s)
	for _, m := range p.liveByID(model) {
		if len(m.APIs) > 0 {
			out := make([]Protocol, len(m.APIs))
			for i, a := range m.APIs {
				out[i] = Protocol(a)
			}
			return out
		}
	}
	// the model Copilot's Auto picked may be one it lists for no picker
	if p.ID == "copilot" {
		if apis := copilotSeenAPIs(model); len(apis) > 0 {
			return apis
		}
	}
	// Vertex AI serves Gemini on generateContent, its partner models
	// (publisher/model) on chat completions alone, and Claude on
	// Anthropic's Messages API alone
	if p.IsVertex() {
		if VertexPartner(model) {
			return []Protocol{Chat}
		}
		if VertexClaude(model) {
			return []Protocol{Anthropic}
		}
		return []Protocol{Gemini}
	}
	// Factory serves each model on the one API droid sends it on
	if p.ID == "factory" && p.Account != nil {
		return factoryAPIs(model)
	}
	// Bedrock has no list to say it: Claude is served on Anthropic's
	// messages alone, OpenAI's GPT models on Responses and chat
	// completions, every other model (gpt-oss too) on chat completions
	// alone
	if p.IsBedrock() {
		if bedrockClaude(model) {
			return []Protocol{Anthropic}
		}
		if bedrockGPT(model) && p.Responses != "" {
			return []Protocol{Responses, Chat}
		}
		return []Protocol{Chat}
	}
	// OpenCode serves some models on OpenAI's Responses API only (Grok,
	// GPT) or Anthropic's (Claude, MiniMax), and turns the others away:
	// models.dev says which, in the list of the gateway its URL is at,
	// whatever catalog the provider was saved with (#1215)
	if p.IsOpenCode() {
		for _, c := range p.openCodeCatalogs() {
			if a := catalog.APIOf(c, model); a != "" {
				return []Protocol{Protocol(a)}
			}
		}
	}
	return nil
}

// Sign authenticates a request to the provider, refreshing what needs it.
// Plain providers get their key; accounts get the agent's tokens.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account != nil && settings.AccountDiscoveryOff() && !p.publicAccount() {
		return errAccountDiscoveryOff
	}
	ctx = p.Via(ctx) // a sign-in refreshed on the way goes through its proxy
	// the agent's own headers the provider lists (PassHeaders), before an
	// account's signing, which has the last word on its own
	passed := p.passClientHeaders(ctx, req.Header)
	if p.Account != nil && p.Account.sign != nil {
		return p.Account.sign(ctx, req, body)
	}
	// OpenCode's gateway routes nothing that names no session; the user's
	// own header, below, still has the last word
	OpenCodeSession(req.Header, req.URL.String())
	if p.IsVertex() {
		// a Google token, which goes nowhere but the project's own address (a key,
		// nowhere but where the provider is asked)
		if base := p.vertexBase(); base == "" || !strings.HasPrefix(req.URL.String(), base+"/") {
			return fmt.Errorf("%s is asked only at Vertex AI, at its project's address", p.Name)
		}
		if p.Key != "" {
			// an API key (express mode), in the header Google's docs name
			req.Header.Set("x-goog-api-key", p.Key)
		} else {
			tok, err := p.vertexAccessToken(ctx)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	for k, v := range AuthHeaders(p, proto) {
		req.Header.Set(k, v)
	}
	// The user's own headers ride on plain key+URL providers, after auth so
	// they can override a default when a gateway insists on a private scheme.
	// Written with the name as the user typed it — some gateways match
	// header names case-sensitively — in place of the same header in any
	// other case (PutUserHeader), so it is sent once, theirs.
	// A list the request has its own of (anthropic-beta) is added to, not
	// replaced: the user's betas go after those the agent asked.
	// ${sessionId} and ${threadId} in a value are the conversation's
	// (conversation.go).
	for k, v := range p.SentHeaders(ctx) {
		if slices.ContainsFunc(passed, func(n string) bool { return strings.EqualFold(n, k) }) {
			continue // the agent sent its own, which stands over the user's fixed one
		}
		if ListHeader(k) {
			MergeList(req.Header, k, v)
			continue
		}
		catalog.PutUserHeader(req.Header, k, v)
	}
	// a remote magpie is always sent its key as x-api-key, which no header
	// of the user's replaces: their Authorization (a proxy's) goes on
	// without taking the key away (remoteKey)
	if p.IsRemoteMagpie() && p.Key != "" {
		remoteKey(req.Header, p.Key)
	}
	return nil
}

// ListHeader is a header whose value is a comma-separated list the request
// has its own of, which a provider's header of the same name adds to:
// anthropic-beta, the betas an agent asks on each request.
func ListHeader(name string) bool { return strings.EqualFold(name, "anthropic-beta") }

// MergeList writes name in h as the items h has under it, in any case,
// followed by those of add: comma-separated, each once, empty ones left
// out, under name as written.
func MergeList(h http.Header, name, add string) {
	var vals []string
	for _, k := range slices.Sorted(maps.Keys(h)) {
		if strings.EqualFold(k, name) {
			vals = append(vals, h[k]...)
			delete(h, k)
		}
	}
	var out []string
	for _, v := range append(vals, add) {
		for _, it := range strings.Split(v, ",") {
			if it = strings.TrimSpace(it); it != "" && !slices.Contains(out, it) {
				out = append(out, it)
			}
		}
	}
	if len(out) > 0 {
		h[name] = []string{strings.Join(out, ",")}
	}
}

// Retries is whether the account can mend a refusal (Retry), so the
// refusal's body is worth reading before it is passed on.
func (p Provider) Retries() bool { return p.Account != nil && p.Account.retry != nil }

// Retry is whether a request (sent) the backend refused with status and
// body is worth sending once more, the account having mended what it named.
func (p Provider) Retry(ctx context.Context, sent []byte, status int, body []byte) bool {
	if p.Account == nil || p.Account.retry == nil {
		return false
	}
	return p.Account.retry(p.Via(ctx), bodyModel(sent), status, body)
}

// Explain is the error a refusal is passed on as: msg, with what the
// user can do about it when the account knows.
func (p Provider) Explain(msg string, status int, body []byte) string {
	if p.Account == nil || p.Account.explain == nil {
		return msg
	}
	if more := p.Account.explain(status, body); more != "" {
		return msg + " — " + more
	}
	return msg
}

// Prepare adjusts a request body the way the backend wants it.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.body != nil {
		return p.Account.body(body)
	}
	return body
}

// Exclusion is a sign-in magpie found but will not offer as a provider.
type Exclusion struct {
	Agent    string `json:"agent"`
	Provider string `json:"provider,omitempty"` // set when the user removed it; saving it brings it back
	// Name is the removed provider's, for a sign-in no agent names: a
	// plugin's, whose Agent is "plugin" (#694)
	Name string `json:"name,omitempty"`
	Why  string `json:"why"`
	// SignedOut: the agent has accounts saved in magpie but isn't signed
	// in where magpie looks, and so none of them is offered.
	SignedOut bool `json:"signedOut,omitempty"`
	// Users names those saved accounts (no secrets), so they can be
	// removed from magpie while none of them is offered.
	Users []string `json:"users,omitempty"`
	// Quiet: the user asked not to be reminded of it; only the Add sheet
	// offers it back.
	Quiet bool `json:"quiet,omitempty"`
	// Tucked: hidden from the Add sheet too, but for its "Show N hidden"
	// and a search by name (#116).
	Tucked bool `json:"tucked,omitempty"`
	// Retry: the keychain didn't give Codex's sign-in (refused, no
	// answer, or its prompt still up); Try again asks it once more
	// (RetryCodexKeychain, #2367).
	Retry bool `json:"retry,omitempty"`
}

// Excluded lists sign-ins magpie detects but leaves out: the accounts the
// user removed from magpie, and the saved accounts of an agent that isn't
// signed in here (a magpie serve under another HOME, say).
func Excluded() []Exclusion {
	var out []Exclusion
	forgot := map[string]bool{}
	for _, p := range storedFile().Providers {
		forgot[p.ID] = p.Forgot
	}
	for _, a := range Hidden() {
		if forgot[a.ID] {
			continue
		}
		x := Exclusion{Agent: a.Account.Agent, Provider: a.ID, Why: "You removed it from magpie.", Quiet: a.Quiet, Tucked: a.Tucked}
		if a.IsPlugin() {
			x.Name = a.Name
		}
		out = append(out, x)
	}
	return append(out, savedButSignedOut()...)
}

// claudeBase is Anthropic's API root, which Claude Code asks; magpie never
// does with a Claude sign-in.
var claudeBase = "https://api.anthropic.com"

// claudeKeychain reads credentials from the macOS Keychain; a var so tests
// never touch the machine's own login.
var claudeKeychain = runtime.GOOS == "darwin"

var (
	claudeStatusMu   sync.Mutex
	claudeStatusAt   time.Time
	claudeStatusUser string
	claudeStatusPlan string
	claudeStatusOut  bool // Claude Code says nobody is signed in
	claudeStatusGen  int  // bumped when forgotten: an answer asked before is dropped
	claudeStatusBusy bool // asked again behind the last answer

	claudeCacheMu  sync.Mutex
	claudeCacheAt  time.Time
	claudeCacheC   claudeCredentials
	claudeCacheLoc claudeCredentialLocation
	claudeCacheOK  bool

	claudeAskMu sync.Mutex
	claudeAskAt time.Time // a keychain read that may have shown a dialog
)

// claudeCacheTTL keeps All() from spawning `security` on every gateway
// request while still noticing a fresh login quickly.
const claudeCacheTTL = 10 * time.Second

// keychainDeniedTTL is how long a keychain read that may have shown the
// user a dialog is not asked again, as copilotCLISecret doesn't re-ask a
// denied one at once (#723, #2408): with the item's trust list holding no
// `security`, every ask can show the dialog again.
const keychainDeniedTTL = 10 * time.Minute

type claudeAuth struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"` // milliseconds since the Unix epoch
	RefreshExpiresAt int64    `json:"refreshTokenExpiresAt"`
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

// plan names the Claude subscription as Anthropic sells it now:
// Max 20x is Claude Ultra, while Claude Code still says "max" for it
// and tells the two apart only by the rate-limit tier.
func (a claudeAuth) plan() string { return claudePlan(a.SubscriptionType, a.RateLimitTier) }

func claudePlan(sub, tier string) string {
	if sub == "max" && strings.Contains(tier, "max_20x") {
		return "ultra"
	}
	return sub
}

// claudeCredentials keeps the whole credential blob in raw, so refreshing a
// token writes back everything else — MCP OAuth state included — untouched.
type claudeCredentials struct {
	raw   map[string]any
	OAuth claudeAuth
}

func parseClaudeCredentials(b []byte) (claudeCredentials, bool) {
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return claudeCredentials{}, false
	}
	c := claudeCredentials{raw: raw}
	if o, ok := raw["claudeAiOauth"].(map[string]any); ok {
		ob, _ := json.Marshal(o)
		json.Unmarshal(ob, &c.OAuth)
	}
	return c, c.OAuth.AccessToken != ""
}

// marshal writes into copies: c.raw is shared with every copy of c, the
// cached one included, which other goroutines marshal at the same time.
func (c claudeCredentials) marshal() ([]byte, error) {
	raw := maps.Clone(c.raw)
	if raw == nil {
		raw = map[string]any{}
	}
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	oauth = maps.Clone(oauth)
	if oauth == nil {
		oauth = map[string]any{}
	}
	oauth["accessToken"] = c.OAuth.AccessToken
	oauth["refreshToken"] = c.OAuth.RefreshToken
	oauth["expiresAt"] = c.OAuth.ExpiresAt
	if c.OAuth.RefreshExpiresAt != 0 {
		oauth["refreshTokenExpiresAt"] = c.OAuth.RefreshExpiresAt
	}
	if len(c.OAuth.Scopes) > 0 {
		oauth["scopes"] = c.OAuth.Scopes
	}
	if c.OAuth.SubscriptionType != "" {
		oauth["subscriptionType"] = c.OAuth.SubscriptionType
	}
	if c.OAuth.RateLimitTier != "" {
		oauth["rateLimitTier"] = c.OAuth.RateLimitTier
	}
	raw["claudeAiOauth"] = oauth
	return json.MarshalIndent(raw, "", "  ")
}

type claudeCredentialLocation struct {
	path     string
	account  string
	keychain bool
}

// claudeCredentialsPath is Claude Code's credentials file, where it keeps
// its sign-in off the Mac's keychain.
func claudeCredentialsPath() string {
	return filepath.Join(appdir.ClaudeDir(), ".credentials.json")
}

func readClaudeCredential() (claudeCredentials, claudeCredentialLocation, bool) {
	if settings.AccountDiscoveryOff() {
		return claudeCredentials{}, claudeCredentialLocation{}, false
	}
	var mine claudeKeychainItem
	if claudeKeychain {
		// the item under the account ($USER) is the sign-in Claude Code goes
		// by, emptied or not: it reads no file beside it, so an emptied item
		// is a sign-out even where an older file still parses, and a sign-in
		// made in magpie is put in the item, not beside it (#1742)
		mine = readClaudeKeychainItem(claudeKeychainAccount())
		if mine.found && !mine.hex {
			return mine.c, mine.loc, mine.ok
		}
	}
	path := claudeCredentialsPath()
	if b, err := readAccountFile(path); err == nil {
		if c, ok := parseClaudeCredentials(b); ok {
			return c, claudeCredentialLocation{path: path}, true
		}
	}
	if !claudeKeychain {
		return claudeCredentials{}, claudeCredentialLocation{}, false
	}
	// neither the account's item, as Claude Code reads it, nor the file
	// holds a sign-in. Left: the account's item as magpie wrote it before,
	// over lines that `security -w` prints as hex (#70) — Claude Code reads
	// that as no item, hence the file first — which is written again on one
	// line, as Claude Code writes it; else by service alone another item,
	// left from an earlier sign-in, that Claude Code never reads (58b67666)
	i := mine
	if !i.ok {
		i = readClaudeKeychainItem("")
	}
	if !i.ok {
		return claudeCredentials{}, claudeCredentialLocation{}, false
	}
	if i.hex {
		// not through saveClaudeCredential: claudeCredential reads under
		// claudeCacheMu, which caching would take again, and caches what
		// this returns itself
		writeClaudeCredential(i.loc, i.c)
	}
	return i.c, i.loc, true
}

// claudeKeychainItem is what `security` reads of the "Claude Code-credentials"
// item. found says an item is there that reads as a JSON object — the sign-in
// Claude Code goes by, emptied or not (an item that reads as none it takes
// for an absent one, and so does readClaudeDir of the item in a config
// directory); ok, that it holds tokens to run on; hex, that magpie wrote it
// over more than one line, which `security -w` prints as hex and Claude Code
// reads as none (#70).
type claudeKeychainItem struct {
	c         claudeCredentials
	loc       claudeCredentialLocation
	ok, found bool
	hex       bool
}

// keychainDenied says whether a failed `security find-generic-password`
// could have been a dialog the user answered no to: 44 is the item not
// being there, which asks nobody.
func keychainDenied(err error) bool {
	var xe *exec.ExitError
	return errors.As(err, &xe) && xe.ExitCode() != 44
}

// readClaudeKeychainItem reads the item for a keychain account, or by
// service alone when the account is "". Either way it is the one under
// $USER that a sign-in is written back to.
func readClaudeKeychainItem(account string) claudeKeychainItem {
	args := []string{"find-generic-password", "-s", claudeHomeService(), "-w"}
	if account != "" {
		args = append(args, "-a", account)
	}
	out, err := proc.Command("security", args...).Output()
	if err != nil {
		if keychainDenied(err) {
			claudeAskMu.Lock()
			claudeAskAt = time.Now()
			claudeAskMu.Unlock()
		}
		return claudeKeychainItem{}
	}
	b, wasHex := keychainText(bytes.TrimSpace(out))
	c, ok := parseClaudeCredentials(b)
	if c.raw == nil {
		return claudeKeychainItem{}
	}
	return claudeKeychainItem{
		c: c, ok: ok, found: true, hex: wasHex,
		loc: claudeCredentialLocation{keychain: true, account: claudeKeychainAccount()},
	}
}

// claudeHomeService is the keychain item Claude Code keeps the sign-in
// magpie reads as its own in: "Claude Code-credentials", or with
// CLAUDE_CONFIG_DIR set, that directory's own (claudeDirService), as
// Claude Code names it, beside the .credentials.json there.
func claudeHomeService() string {
	if d := appdir.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return claudeDirService(d)
	}
	return "Claude Code-credentials"
}

// claudeKeychainAccount is the account Claude Code keeps its sign-in
// under: $USER, else the login name, and claude-code-user for a name it
// won't use.
func claudeKeychainAccount() string {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	if !keychainAccountRe.MatchString(name) {
		return "claude-code-user"
	}
	return name
}

var keychainAccountRe = lazyre.New(`^[a-zA-Z0-9._-]+$`)

// keychainText undoes `security find-generic-password -w` printing a
// password with a character it can't print — a newline — as hex.
func keychainText(out []byte) ([]byte, bool) {
	if len(out) == 0 || len(out)%2 != 0 || out[0] == '{' {
		return out, false
	}
	b, err := hex.DecodeString(string(out))
	if err != nil || !json.Valid(b) {
		return out, false
	}
	return b, true
}

func claudeCredential() (claudeCredentials, claudeCredentialLocation, bool) {
	if settings.AccountDiscoveryOff() {
		return claudeCredentials{}, claudeCredentialLocation{}, false
	}
	claudeCacheMu.Lock()
	defer claudeCacheMu.Unlock()
	ttl := claudeCacheTTL
	if !claudeCacheOK {
		claudeAskMu.Lock()
		ask := claudeAskAt
		claudeAskMu.Unlock()
		if time.Since(ask) < keychainDeniedTTL {
			ttl = keychainDeniedTTL
		}
	}
	if time.Since(claudeCacheAt) < ttl {
		return claudeCacheC, claudeCacheLoc, claudeCacheOK
	}
	c, loc, ok := readClaudeCredential()
	claudeCacheC, claudeCacheLoc, claudeCacheOK, claudeCacheAt = c, loc, ok, time.Now()
	return c, loc, ok
}

func cacheClaudeCredential(c claudeCredentials, loc claudeCredentialLocation) {
	claudeCacheMu.Lock()
	claudeCacheC, claudeCacheLoc, claudeCacheOK, claudeCacheAt = c, loc, true, time.Now()
	claudeCacheMu.Unlock()
}

// forgetClaudeCredential drops the cache; tests use it between homes.
func forgetClaudeCredential() {
	claudeCacheMu.Lock()
	claudeCacheAt = time.Time{}
	claudeCacheMu.Unlock()
	claudeAskMu.Lock()
	claudeAskAt = time.Time{}
	claudeAskMu.Unlock()
}

func saveClaudeCredential(loc claudeCredentialLocation, c claudeCredentials) error {
	if err := writeClaudeCredential(loc, c); err != nil {
		return err
	}
	cacheClaudeCredential(c, loc)
	return nil
}

// writeClaudeCredential writes c where loc says, leaving the cache alone.
func writeClaudeCredential(loc claudeCredentialLocation, c claudeCredentials) error {
	if discoveryOff() {
		return errAccountDiscoveryOff
	}
	b, err := c.marshal()
	if err != nil {
		return err
	}
	if loc.keychain {
		// on one line, as Claude Code writes it: a password with a newline
		// comes back from `security -w` as hex, which Claude Code takes for
		// no sign-in at all (#70)
		var one bytes.Buffer
		if err := json.Compact(&one, b); err != nil {
			return err
		}
		b = one.Bytes()
	}
	if !loc.keychain {
		// Claude Code's own sign-in: never truncated in place, so a machine
		// going down mid-write or a file held for a moment on Windows
		// leaves the old one whole, not an empty file (#2239, #1505)
		if err := writePrivate(loc.path, append(b, '\n')); err != nil {
			return err
		}
	} else {
		args := []string{"add-generic-password", "-U", "-s", claudeHomeService()}
		if loc.account != "" {
			args = append(args, "-a", loc.account)
		}
		args = append(args, "-w", string(b))
		if out, err := proc.Command("security", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("save Claude Code credentials: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// claudeExecutable finds the claude CLI (proc.FindTool: claude.exe in
// ~/.local/bin on Windows too, #839), else the one magpie downloaded
// (claudecode.Find); a var so tests can fake it.
var claudeExecutable = claudecode.Find

// claudeIdentity asks Claude Code itself which account is active. Its credential
// blob intentionally contains tokens and plan metadata but no display identity;
// `claude auth status --json` is the authoritative, non-secret view shown by the
// CLI. Cache it briefly because the providers screen refreshes often.
//
// It also says when Claude Code is signed out even though credentials are
// still lying around (a keychain item logout left behind), so a signed-out
// account stops showing up as a provider.
func claudeIdentity() (user, plan string, signedOut bool) {
	// switched off or removed, Claude Code isn't asked: what it said last
	// is served (leftAlone)
	quiet := leftAlone("claude")
	claudeStatusMu.Lock()
	defer claudeStatusMu.Unlock()
	switch {
	case quiet:
	case claudeStatusAt.IsZero():
		claudeStatusAt = time.Now()
		if u, p, out, ok := askClaudeStatus(); ok {
			claudeStatusUser, claudeStatusPlan, claudeStatusOut = u, p, out
		}
	case time.Since(claudeStatusAt) >= 30*time.Second && !claudeStatusBusy:
		// the CLI takes up to seconds and the accounts are read by every page
		// and request: the last answer is served while it is asked again (#123)
		claudeStatusBusy = true
		gen := claudeStatusGen
		go func() {
			u, p, out, ok := askClaudeStatus()
			claudeStatusMu.Lock()
			defer claudeStatusMu.Unlock()
			if gen != claudeStatusGen {
				return
			}
			claudeStatusAt, claudeStatusBusy = time.Now(), false
			if ok {
				claudeStatusUser, claudeStatusPlan, claudeStatusOut = u, p, out
			}
		}()
	}
	return claudeStatusUser, claudeStatusPlan, claudeStatusOut
}

// askClaudeStatus runs `claude auth status`; ok is false when it gave no answer.
func askClaudeStatus() (user, plan string, signedOut, ok bool) {
	if settings.AccountDiscoveryOff() {
		return "", "", false, false
	}
	path := claudeExecutable()
	if path == "" {
		return "", "", false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// signed out, `claude auth status` exits 1 but still prints the JSON.
	// Not with a token or endpoint from magpie's own environment: the CLI
	// would tell of that, not of its sign-in. magpie's wiring in
	// settings.json it applies itself (auth status takes no
	// --setting-sources), and then answers with no email; claudeSignedInUser
	// names the account from ~/.claude.json instead (#177).
	cmd := proc.ProbeContext(ctx, path, "auth", "status", "--json")
	cmd.Env = withoutClaudeWiring(os.Environ())
	out, _ := cmd.Output()
	var status struct {
		LoggedIn         *bool  `json:"loggedIn"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal(out, &status) != nil || status.LoggedIn == nil {
		return "", "", false, false
	}
	if !*status.LoggedIn {
		return "", "", true, true
	}
	return strings.TrimSpace(status.Email), strings.TrimSpace(status.SubscriptionType), false, true
}

// withoutClaudeWiring is env less what points Claude Code at another
// endpoint or token than its own sign-in.
func withoutClaudeWiring(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func claudeAccount() (Provider, bool) {
	c, _, ok := claudeCredential()
	if !ok {
		return claudeStandInAccount()
	}
	user, statusPlan, signedOut := claudeIdentity()
	if signedOut {
		return claudeStandInAccount()
	}
	plan := c.OAuth.plan()
	if statusPlan != "" {
		plan = claudePlan(statusPlan, c.OAuth.RateLimitTier)
	}
	// named as its saved login is (liveLogin): it is then the account Logins
	// flags Active, not served a second time beside itself, and its
	// allowances and limits are found under its name (#177)
	user, acct := claudeSignedInUser(c.OAuth.SubscriptionType, statusPlan, user)
	if b, err := c.marshal(); err == nil && user != "" {
		// by its sign-in when its profile is another's (claudeSignInOwner)
		l := savedLogin{Agent: "claude", User: user, Auth: b}
		if acct != nil {
			l.Profile, _ = json.Marshal(acct)
		}
		user = claudeSignInOwner(readLogins(), l).User
	}
	unnamed := user == ""
	if unnamed {
		user = "Claude account"
		if plan != "" {
			user = "Claude " + strings.ToUpper(plan[:1]) + plan[1:]
		}
	}
	return claudeProvider(&Account{Agent: "claude", User: user, Plan: plan, unnamed: unnamed}), true
}

// Unnamed says the account is Claude Code's own sign-in known by no name
// yet: its User is made from its plan, and it is named by its email once
// Claude Code's first run has put its profile in Claude Code's config.
func (a *Account) Unnamed() bool { return a != nil && a.unnamed }

// ClaudeOwnName is the name Claude Code's own sign-in is served by now,
// and false while it has none (Unnamed) or Claude Code is signed out.
func ClaudeOwnName() (string, bool) {
	p, ok := claudeAccount()
	if !ok || p.Account == nil || !p.Account.AgentsOwn() || p.Account.standIn || p.Account.unnamed {
		return "", false
	}
	return p.Account.User, true
}

// StandIn says the account is a saved one served in the place of the
// agent's own sign-in, which is signed out.
func (a *Account) StandIn() bool { return a != nil && a.standIn }

// claudeProvider is the Claude Code provider of acct.
func claudeProvider(acct *Account) Provider {
	// nothing is sent to Anthropic in Claude Code's name: a request on the
	// account runs Claude Code itself (the gateway's bridge, a test), so
	// one that would go straight to the API with its sign-in is refused
	acct.sign = func(context.Context, *http.Request, []byte) error { return errClaudeViaCLI }
	acct.models = claudeModels
	acct.magpieList = true
	// Claude's models are the ones magpie knows and the ones Claude Code
	// was told an account has (claude_models.go): listing them would ask
	// Anthropic with the account's sign-in, which magpie never does
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		// models.dev's list, fetched again first when it is older than
		// a few minutes: a model launched since the day's sync is listed
		catalog.Asked(ctx)
		ms := claudeModels()
		return ms, catalog.SaveLive("claude", claudeBase, ms)
	}
	return Provider{ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Anthropic: claudeBase,
		Catalog: "anthropic", Website: "https://claude.ai", Account: acct}
}

// refreshRefused is a refresh the vendor answered and turned down: the
// sign-in is gone, where a refresh that got no answer may yet go through.
type refreshRefused string

func (e refreshRefused) Error() string { return string(e) }

// refreshFailed is a refresh that didn't go through: refused when the vendor
// turned the token down (400 invalid_grant, 401), a hiccup otherwise — a 403
// is as likely a proxy or bot check in the way as the vendor's answer.
func refreshFailed(status int, agent, msg string) error {
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		return refreshRefused(msg)
	}
	return fmt.Errorf("%s token refresh failed (HTTP %d)", agent, status)
}

// Accounts lists the signed-in agents as providers.
func Accounts() []Provider {
	if settings.AccountDiscoveryOff() {
		return pluginAccounts() // the public plugins' alone (publicPlugin)
	}
	rememberLogins(false)
	home, _ := os.UserHomeDir()
	cfg := appdir.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	var out []Provider
	if p, ok := claudeAccount(); ok {
		out = append(out, p)
	}
	if p, ok := codexAccount(home); ok {
		out = append(out, p)
	}
	if p, ok := copilotAccount(cfg); ok {
		out = append(out, p)
	}
	if p, ok := siwcAccount(); ok {
		out = append(out, p)
	}
	for _, agent := range []string{"gemini", "antigravity"} {
		if p, ok := googleAccountOf(agent); ok {
			out = append(out, p)
		}
	}
	// a built-in moved onto its plugin is the plugin's now (migrate.go)
	out = slices.DeleteFunc(out, func(p Provider) bool { return PluginServes(p.ID) })
	return placeMoved(out, pluginAccounts())
}

// builtinOrder is the built-ins' ids in the order Accounts lists them.
var builtinOrder = slices.Concat([]string{"claude", "codex", "copilot", "cursor", "grok", "devin", "kiro", "zcode",
	"workbuddy", WorkBuddyAIID, CommandCodePlanID}, qoderAgents, []string{"zed", "factory", MiMoID, ChatGPTAPIID, "gemini", "antigravity"})

// placeMoved adds the plugins' accounts to the built-ins': one a built-in
// was moved onto stands where the built-in stood, the others go last.
func placeMoved(out, plugins []Provider) []Provider {
	at := func(id string) int {
		if i := slices.Index(builtinOrder, id); i >= 0 {
			return i
		}
		return len(builtinOrder)
	}
	var rest []Provider
	for _, p := range plugins {
		if !PluginServes(p.ID) || !slices.Contains(builtinOrder, p.ID) {
			rest = append(rest, p)
			continue
		}
		// moved ones placed already count too: two moved in the plugins'
		// order (WorkBuddy AI before WorkBuddy) keep the built-ins'
		i := slices.IndexFunc(out, func(q Provider) bool { return at(q.ID) > at(p.ID) })
		if i < 0 {
			i = len(out)
		}
		out = slices.Insert(out, i, p)
	}
	return append(out, rest...)
}

func readJSON(path string, v any) bool {
	b, err := readAccountFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// jwtClaims decodes the payload of a JWT without checking it; the
// tokens are the user's own, only their expiry and subject matter here.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func claimString(m map[string]any, keys ...string) string {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = mm[k]
	}
	s, _ := v.(string)
	return s
}

// ---- Codex CLI: a ChatGPT account ----------------------------------------------

const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann" // Codex CLI's own OAuth client

// codexTokenURL is where ChatGPT's tokens are issued and refreshed; a var
// so tests can point it elsewhere.
var codexTokenURL = "https://auth.openai.com/oauth/token"

// CodexTokenURLForTest points ChatGPT's token endpoint at a test server,
// for the CLI's tests; the func it returns puts it back.
func CodexTokenURLForTest(u string) func() {
	old := codexTokenURL
	codexTokenURL = u
	return func() { codexTokenURL = old }
}

// CodexBase is where a ChatGPT account's Codex requests go; a var so tests
// can point it elsewhere.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// codexMu keeps two refreshes of a Codex sign-in from running at once,
// in this magpie and across magpies: the refresh token rotates, and the
// second would send the one the first spent (refresh_token_reused). Two
// magpies run at once while a restart hands the gateway over (xuanwo on
// X), and beside a magpie run from a terminal. The sign-in is read after
// it is taken, so one refreshed meanwhile is used as it is.
var codexMu = filelock.Mutex{Path: func() string { return loginsPath() + ".codex.lock" }}

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

func codexAccount(home string) (Provider, bool) {
	if settings.AccountDiscoveryOff() {
		return Provider{}, false
	}
	dir := appdir.CodexHomeIn(home)
	// where Codex's config.toml keeps it: auth.json, or the macOS
	// keychain item (#2367)
	b, _, err := readCodexLogin(dir, false)
	var a codexAuth
	if err != nil || json.Unmarshal(b, &a) != nil || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
		return Provider{}, false
	}
	id := jwtClaims(a.Tokens.IDToken)
	acct := &Account{Agent: "codex", Stream: true,
		User: codexUser(id), Plan: claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type")}
	if acct.User == "" {
		acct.User = "ChatGPT"
	} else {
		// as it is saved, as liveLogin names it: a second Team seat of
		// the email is "email · Team · <workspace>" (#1424)
		acct.User = codexLiveName(acct.User, bytes.TrimSpace(b))
	}
	acct.sign = codexSign(func(ctx context.Context) (string, string, error) { return codexToken(ctx, dir) })
	acct.renew = func(ctx context.Context, stale string) (string, string, error) {
		return codexTokenRenewing(ctx, dir, stale)
	}
	acct.body = codexBody
	acct.models = catalog.Codex
	acct.listedOnly = true
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, ver, err := codexModelsAsked(ctx, acct.sign)
		if err != nil {
			return nil, err
		}
		catalog.SaveLiveAsked(accountModels("codex", acct.User), CodexBase, ver, ms)
		codexFetchSaved(ctx)
		ms = codexPoolModels(ms)
		return ms, catalog.SaveLive("codex", CodexBase, ms)
	}
	return Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Responses: CodexBase, Website: "https://chatgpt.com/codex", Account: acct}, true
}

// codexToken returns a usable access token for the Codex home dir,
// refreshing it through OpenAI when it is about to expire. A refresh
// rotates the tokens, so the new ones go back where Codex keeps its
// sign-in (auth.json or the keychain item, codexStore.write) for Codex to
// find.
func codexToken(ctx context.Context, dir string) (tok, accountID string, err error) {
	return codexTokenRenewing(ctx, dir, "")
}

// codexTokenAsKept is the access token Codex keeps in dir, as it is: an
// expired one is an error, not refreshed, for a sign-in magpie leaves to
// Codex (leftAlone), which renews it itself.
func codexTokenAsKept(dir string) (tok, accountID string, err error) {
	b, _, err := readCodexLogin(dir, false)
	if err != nil {
		return "", "", err
	}
	var a codexAuth
	if json.Unmarshal(b, &a) != nil || a.Tokens.AccessToken == "" {
		return "", "", errors.New("Codex is signed out; run codex login")
	}
	if exp, _ := jwtClaims(a.Tokens.AccessToken)["exp"].(float64); exp != 0 && !time.Now().Before(time.Unix(int64(exp), 0)) {
		return "", "", errors.New("Codex's sign-in has expired; Codex renews it as it runs")
	}
	return a.Tokens.AccessToken, codexAccountID(a), nil
}

// CodexSignInAsKept is the access token and account id of the ChatGPT
// sign-in Codex keeps, as it is (codexTokenAsKept): an expired one is an
// error, left to Codex to renew.
func CodexSignInAsKept() (tok, accountID string, err error) {
	return codexTokenAsKept(codexHome())
}

// codexTokenRenewing is codexToken, and with stale, the access token the
// backend just answered 401 "token expired" for while its exp was still
// ahead (willz on Discord, #2973: a Team account's), refreshes the
// sign-in all the same. Unless the store holds another token by now,
// refreshed meanwhile by Codex or another request, which is returned
// instead: its refresh token is the one spent already.
func codexTokenRenewing(ctx context.Context, dir, stale string) (tok, accountID string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	// a refresh asked for: the store's own, not a kept read
	b, st, err := readCodexLogin(dir, stale != "")
	var a codexAuth
	if err == nil && json.Unmarshal(b, &a) == nil && a.Tokens.AccessToken != "" {
		if stale != "" {
			if a.Tokens.AccessToken != stale {
				return a.Tokens.AccessToken, codexAccountID(a), nil
			}
		} else if exp, _ := jwtClaims(a.Tokens.AccessToken)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
			return a.Tokens.AccessToken, codexAccountID(a), nil
		} else if st.keychain {
			// about to be refreshed: the store's own, not a kept read,
			// which Codex may have rotated since (its refresh token spent)
			b, st, err = readCodexLogin(dir, true)
			a = codexAuth{}
			if err == nil && json.Unmarshal(b, &a) == nil && a.Tokens.AccessToken != "" {
				if exp, _ := jwtClaims(a.Tokens.AccessToken)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
					return a.Tokens.AccessToken, codexAccountID(a), nil
				}
			}
		}
	}
	var se *codexStoreError
	if errors.As(err, &se) {
		return "", "", se
	}
	var raw map[string]any
	if err != nil || a.Tokens.AccessToken == "" || json.Unmarshal(b, &raw) != nil {
		return "", "", errors.New("Codex is signed out; run codex login")
	}
	accountID = codexAccountID(a)
	reason := codexRefreshExpiry
	if stale != "" {
		reason = codexRefreshRecovery
	}
	tok, err = codexRefresh(ctx, raw, reason)
	if err != nil {
		// OpenAI refused this sign-in for good: Codex is signed out to
		// magpie's wiring until it is signed in again (#4308)
		if errors.As(err, new(codexRefreshGoneError)) {
			noteCodexSignInGone(dir, a.Tokens.RefreshToken)
		}
		return "", "", err
	}
	forgetCodexSignInGone(dir)
	// keep every other field as Codex wrote it. auth.json is the user's
	// own sign-in, which Codex CLI and the Codex app read: written beside
	// it, flushed and renamed in, never truncated in place, so a machine
	// going down mid-write leaves the old one whole and a file held for a
	// moment on Windows is waited out (#2239, #1505). A write that still
	// fails loses the rotation, said in the log; the fresh access token in
	// hand still serves this request.
	//
	// Only over the sign-in this refresh started from, though. The refresh
	// is a network round trip, and the store may hold another sign-in by
	// now: Codex signed in to another account (the Codex app's own
	// sign-in, or a magpie outside codexMu), or signed out. Writing the
	// refreshed one over it switched Codex back to the account it had
	// been switched off (darwish4021 on Discord: "switched my account").
	// The fresh access token still serves the request in hand.
	if now, _, err := readCodexLogin(dir, true); err != nil || !sameRefreshToken(now, a.Tokens.RefreshToken) {
		log.Printf("codex: the sign-in in %s changed while %s was being refreshed; the refresh is not written over it", dir, accountID)
		return tok, accountID, nil
	}
	if out, err := json.MarshalIndent(raw, "", "  "); err == nil {
		if werr := st.write(out); werr != nil {
			where := st.file()
			if st.keychain {
				where = "the keychain item " + codexKeychainService
			}
			log.Printf("codex: the refreshed sign-in didn't reach %s: %v", where, werr)
		}
	}
	return tok, accountID, nil
}

// codexAccountID is the ChatGPT account a sign-in's requests name.
func codexAccountID(a codexAuth) string {
	if a.Tokens.AccountID != "" {
		return a.Tokens.AccountID
	}
	return claimString(jwtClaims(a.Tokens.IDToken), "https://api.openai.com/auth", "chatgpt_account_id")
}

// Why a ChatGPT sign-in is refreshed, in the words codex-rs sends with
// its own refresh (refresh_reason, login/src/oauth/client.rs).
const (
	codexRefreshExpiry    = "proactive_expiry"      // its access token is about to run out
	codexRefreshScheduled = "scheduled"             // not refreshed for keepAliveEvery
	codexRefreshRecovery  = "unauthorized_recovery" // the backend called its token expired
	codexRefreshForced    = "forced"                // asked for: an import, a check
)

type refreshReasonKey struct{}

// refreshingFor has a forced refresh made under ctx say why it is made,
// when it is more than "forced" (the keep-alive's "scheduled").
func refreshingFor(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, refreshReasonKey{}, reason)
}

// forcedReason is why ctx forces a refresh: what refreshingFor said, else
// "forced".
func forcedReason(ctx context.Context) string {
	if r, _ := ctx.Value(refreshReasonKey{}).(string); r != "" {
		return r
	}
	return codexRefreshForced
}

// codexRefresh renews the tokens of an auth.json-shaped sign-in in place,
// every other field left as it was, and returns the new access token.
// It is asked as codex-rs asks it (request_chatgpt_token_refresh): a JSON
// body of client_id, grant_type, refresh_token and refresh_reason, with
// no scope (a refresh keeps the scopes granted), and Codex's originator
// and User-Agent.
// sameRefreshToken says the sign-in b still holds refresh, the refresh
// token a refresh started from.
func sameRefreshToken(b []byte, refresh string) bool {
	var a codexAuth
	return json.Unmarshal(b, &a) == nil && refresh != "" && a.Tokens.RefreshToken == refresh
}

func codexRefresh(ctx context.Context, raw map[string]any, reason string) (string, error) {
	toks, _ := raw["tokens"].(map[string]any)
	if toks == nil {
		toks = map[string]any{}
	}
	refresh, _ := toks["refresh_token"].(string)
	// Once sent, a refresh is waited for even when whoever asked gives up
	// (a request the agent cancelled, a usage read's or the keep-alive's
	// deadline): OpenAI rotates the refresh token as it answers, and an
	// answer dropped leaves magpie holding the spent one, which the next
	// refresh is refused for (refresh_token_reused). The account then
	// read as signed out while its access token still served (willz on
	// Discord). The ChatGPT API's refresh waits so already (siwcFresh).
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": refresh, "refresh_reason": reason})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	codexClientHeaders(req.Header, codexVersion())
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return "", errors.New("Codex token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		err := refreshFailed(res.StatusCode, "Codex", "Codex is signed out (token refresh failed); sign in to this account again")
		if res.StatusCode != 200 && codexRefreshGone(res.StatusCode, b) {
			// refused for good, by Codex's own rule (#4308)
			err = codexRefreshGoneError{err}
		}
		return "", err
	}
	toks["access_token"] = fresh.AccessToken
	if fresh.IDToken != "" {
		toks["id_token"] = fresh.IDToken
	}
	if fresh.RefreshToken != "" {
		toks["refresh_token"] = fresh.RefreshToken
	}
	raw["tokens"] = toks
	raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	return fresh.AccessToken, nil
}

// ---- Copilot: a GitHub account -------------------------------------------------

// CopilotTokenURL trades the GitHub OAuth token for a short-lived Copilot
// session token; a var so tests can point it elsewhere.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

const copilotBase = "https://api.githubcopilot.com"

var copilotHeaders = map[string]string{
	"Editor-Version":         "vscode/1.140.0",
	"Editor-Plugin-Version":  "copilot-chat/0.68.0",
	"Copilot-Integration-Id": "vscode-chat",
	"User-Agent":             "GitHubCopilotChat/0.68.0",
}

// copilotAPIHeaders are what the editors' sign-in sends Copilot's API
// (api.githubcopilot.com, not GitHub's): Copilot Chat's API version, which
// it sends on every chat request (networking.ts). Without one Copilot
// leaves Auto's Copilot-Session-Token unread and takes the request as a
// model picked by hand, which a Student plan is refused for Auto's pick
// (#256: gpt-5.6-luna, "The requested model is not supported.", served to
// VS Code on the same account); with it the session is held to its pick.
var copilotAPIHeaders = func() map[string]string {
	h := maps.Clone(copilotHeaders)
	h["X-GitHub-Api-Version"] = "2026-01-09"
	return h
}()

type copilotSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
	direct bool // the Copilot CLI's token, sent as is
}

// headers are what a request with this session carries.
func (s copilotSession) headers() map[string]string {
	if s.direct {
		return copilotCLIHeaders
	}
	return copilotAPIHeaders
}

var (
	copilotMu       sync.Mutex
	copilotSessions = map[string]copilotSession{} // by GitHub token
)

type copilotApp struct {
	User  string `json:"user"`
	Token string `json:"oauth_token"`
	// Host is an enterprise's <name>.ghe.com the account is on, "" for
	// github.com (copilot_ghe.go)
	Host string `json:"host,omitempty"`
	cli  bool   // the standalone Copilot CLI's sign-in
	src  string // the editors' file it was read from, "" for any other
}

// copilotLogin finds the GitHub token Copilot's editors and CLI keep: an
// editor's github.com sign-in first, then the Copilot CLI's, then an
// editor's on an enterprise's GHE.com. An editor's token GitHub has
// refused (copilotRefusedToken) gives way to the CLI's sign-in of the same
// account, never to another account's (#1238).
func copilotLogin(cfg string) (copilotApp, bool) {
	gh, ghe := copilotEditorLogins(cfg)
	if gh != nil && !copilotRefusedToken(gh.Token) {
		return *gh, true
	}
	if gh != nil {
		if cli, ok := copilotCLILogin(); ok && copilotSameAccount(cli, *gh) {
			return cli, true
		}
		return *gh, true
	}
	if app, ok := copilotCLILogin(); ok || ghe == nil {
		return app, ok
	}
	return *ghe, true
}

// copilotEditorLogins are the sign-ins Copilot's editors keep: the first on
// github.com and the first on an enterprise's <name>.ghe.com, each with the
// file it was read from.
func copilotEditorLogins(cfg string) (gh, ghe *copilotApp) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		var apps map[string]copilotApp
		src := filepath.Join(cfg, "github-copilot", name)
		if !readJSON(src, &apps) {
			continue
		}
		var keys []string
		for k := range apps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, "github.com") && apps[k].Token != "" {
				app := apps[k]
				app.Host, app.src = "", src
				return &app, ghe
			}
			// "acme.ghe.com:Iv1…", as copilot.lua keeps one (#723)
			host, _, _ := strings.Cut(k, ":")
			if h, err := CopilotHost(host); ghe == nil && err == nil && h != "" && apps[k].Token != "" {
				app := apps[k]
				app.Host, app.src = h, src
				ghe = &app
			}
		}
	}
	return nil, ghe
}

// copilotSameAccount says two sign-ins are one GitHub account: the same
// login on the same host. An editor's sign-in that names no user is no
// one's to stand in for.
func copilotSameAccount(a, b copilotApp) bool {
	return a.User != "" && strings.EqualFold(a.User, b.User) && a.Host == b.Host
}

// copilotRefusals is when GitHub last turned away the trade of an editor's
// GitHub token for a session token (401 Bad credentials, or 403), by
// token: a stale sign-in left in apps.json, or a CLI token put there
// (#1238). It is tried again after an hour, or at once when traded.
var copilotRefusals = struct {
	sync.Mutex
	at     map[string]time.Time
	status map[string]int
}{at: map[string]time.Time{}, status: map[string]int{}}

func copilotRefusedToken(token string) bool {
	copilotRefusals.Lock()
	defer copilotRefusals.Unlock()
	at, ok := copilotRefusals.at[token]
	return ok && time.Since(at) < time.Hour
}

// copilotStaleToken says GitHub answered the token's trade 401: the token
// itself is no good. A 403 can be an account without Copilot as well, so
// it isn't called stale.
func copilotStaleToken(token string) bool {
	copilotRefusals.Lock()
	defer copilotRefusals.Unlock()
	return copilotRefusals.status[token] == http.StatusUnauthorized
}

func copilotRefuse(token string, status int) {
	copilotRefusals.Lock()
	defer copilotRefusals.Unlock()
	copilotRefusals.at[token] = time.Now()
	copilotRefusals.status[token] = status
}

// copilotStandIn is the Copilot CLI's sign-in of the same account as a,
// an editor's sign-in GitHub refused (#1238).
func copilotStandIn(a copilotApp) (copilotApp, bool) {
	if a.cli || a.src == "" {
		return copilotApp{}, false
	}
	cli, ok := copilotCLILogin()
	return cli, ok && copilotSameAccount(cli, a)
}

// session is what this sign-in's requests carry, and what GitHub said of
// it is kept on its account: refused, it is signed out; served, it isn't
// (copilotSessionDone).
func (a copilotApp) session(ctx context.Context) (copilotSession, error) {
	s, err := a.trade(ctx)
	copilotSessionDone(a, err)
	return s, err
}

// trade gets this sign-in's session. An editor's token GitHub refuses is
// replaced by the Copilot CLI's sign-in of the same account, sent as the
// CLI sends it; with none, the error names the file the refused token is
// in, since it is read before the CLI's (#1238).
func (a copilotApp) trade(ctx context.Context) (copilotSession, error) {
	if a.cli {
		return copilotDirect(ctx, a)
	}
	// refused before: not asked again at every request
	if copilotRefusedToken(a.Token) {
		if cli, ok := copilotStandIn(a); ok {
			return copilotDirect(ctx, cli)
		}
	}
	s, err := copilotToken(ctx, a)
	if err == nil || a.src == "" || !copilotRefusedToken(a.Token) {
		return s, err
	}
	cli, ok := copilotCLILogin()
	if ok && copilotSameAccount(cli, a) {
		return copilotDirect(ctx, cli)
	}
	if !copilotStaleToken(a.Token) {
		return s, err
	}
	who := ""
	if a.User != "" {
		who = " for " + CopilotAccountName(a.User, a.Host)
	}
	msg := strings.TrimSuffix(err.Error(), "; sign in to Copilot again") + ": the GitHub token Copilot's editors keep in " + a.src + who + " was refused, and it is read before any other sign-in; sign in to Copilot again in your editor or in magpie, or move that file aside"
	if ok {
		msg += " (the Copilot CLI is signed in to " + CopilotAccountName(cli.User, cli.Host) + ", another account, so it isn't used in its place)"
	}
	return copilotSession{}, errors.New(msg)
}

// copilotProvider is Copilot as one GitHub account serves it.
func copilotProvider(app copilotApp, plan string) Provider {
	acct := &Account{Agent: "copilot", User: app.User, Plan: plan}
	if acct.User == "" {
		acct.User = "GitHub"
	}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		s, err := app.session(ctx)
		if err != nil {
			return err
		}
		if s.Endpoints.API != "" || app.Host != "" {
			if u, err := url.Parse(s.apiBase(app.Host) + req.URL.Path); err == nil {
				req.URL, req.Host = u, u.Host
			}
		}
		model, err := copilotAutoSign(ctx, app, req, body)
		if err != nil {
			return err
		}
		copilotAccept(ctx, app, s, model)
		req.Header.Set("Authorization", "Bearer "+s.Token)
		for k, v := range s.headers() {
			req.Header.Set(k, v)
		}
		req.Header.Set("Openai-Intent", "conversation-panel")
		// a turn the user typed is billed as one; a tool's reply is not
		req.Header.Set("X-Initiator", "agent")
		if lastRole(body) == "user" {
			req.Header.Set("X-Initiator", "user")
		}
		if bytes.Contains(body, []byte(`"image_url"`)) || bytes.Contains(body, []byte(`"input_image"`)) || bytes.Contains(body, []byte(`"type":"image"`)) {
			req.Header.Set("Copilot-Vision-Request", "true")
		}
		return nil
	}
	acct.auto = func(ctx context.Context) (copilotAutoSession, error) {
		// the list says which APIs the picked model is served on
		copilotTermsMu.Lock()
		_, known := copilotTerms[app.Token]
		copilotTermsMu.Unlock()
		if !known {
			copilotModels(ctx, app)
		}
		return copilotAutoResolve(ctx, app, false)
	}
	acct.retry = func(ctx context.Context, model string, status int, body []byte) bool {
		return copilotRefused(ctx, app, model, status, body)
	}
	acct.unusable = func(model string) bool { return copilotRefuses(app.Token, model) }
	acct.explain = copilotExplain
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := copilotModels(ctx, app)
		if err != nil {
			return nil, err
		}
		// with what the other accounts' plans add (copilot_pool.go)
		ms = copilotPoolModels(ctx, acct.User, ms)
		return ms, catalog.SaveLive("copilot", copilotBase, ms)
	}
	// each model is served on some of these: the newest GPT models on
	// /responses alone, Claude's on /v1/messages and /chat/completions (its
	// model list says; see Provider.APIs)
	base := copilotBaseOf(app.Host)
	return Provider{ID: "copilot", Name: "Copilot", Icon: "githubcopilot", Chat: base, Responses: base, Anthropic: base, Website: "https://github.com/features/copilot", Account: acct}
}

// copilotNoEndpoint is Copilot's /v1/messages turning away a model it
// serves on other APIs alone; it reads as if the plan were refused (#754).
const copilotNoEndpoint = "no model endpoints available given user constraints"

// copilotExplain says what Copilot's own words leave out.
func copilotExplain(status int, body []byte) string {
	if status == http.StatusBadRequest && bytes.Contains(body, []byte(copilotNoEndpoint)) {
		return "Copilot doesn't serve this model on Anthropic's Messages API; it isn't about your plan"
	}
	return ""
}

// bodyModel is the model a request asks for.
func bodyModel(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	return v.Model
}

// lastRole is the role of the last message in a chat, Anthropic or
// Responses request. Tool results are "tool" in Anthropic's, where they
// ride in a user message, and have none in Responses.
func lastRole(body []byte) string {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	if len(v.Messages) > 0 {
		last := v.Messages[len(v.Messages)-1]
		var blocks []struct {
			Type string `json:"type"`
		}
		if last.Role == "user" && json.Unmarshal(last.Content, &blocks) == nil && len(blocks) > 0 {
			results := 0
			for _, b := range blocks {
				if b.Type == "tool_result" {
					results++
				}
			}
			if results == len(blocks) {
				return "tool"
			}
		}
		return last.Role
	}
	var text string
	if json.Unmarshal(v.Input, &text) == nil {
		return "user"
	}
	var items []struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(v.Input, &items) != nil || len(items) == 0 {
		return ""
	}
	return items[len(items)-1].Role
}

func copilotToken(ctx context.Context, app copilotApp) (copilotSession, error) {
	github := app.Token
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if s, ok := copilotSessions[github]; ok && time.Until(time.Unix(s.ExpiresAt, 0)) > 2*time.Minute {
		return s, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenURL(app.Host), nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+github)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotHeaders {
		req.Header.Set(k, v)
	}
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return copilotSession{}, errors.New("Copilot sign-in: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var s copilotSession
	if res.StatusCode != 200 || json.Unmarshal(b, &s) != nil || s.Token == "" {
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			copilotRefuse(github, res.StatusCode)
		}
		return copilotSession{}, errors.New("Copilot is signed out (" + APIError(b, res.Status) + "); sign in to Copilot again")
	}
	copilotSessions[github] = s
	copilotRefusals.Lock()
	delete(copilotRefusals.at, github)
	delete(copilotRefusals.status, github)
	copilotRefusals.Unlock()
	return s, nil
}

// copilotAPIs names the APIs of Copilot's supported_endpoints; the
// websocket one is left out, as is anything magpie doesn't speak. A chat
// model the list gives no supported_endpoints (GPT-4.1, GPT-4o and the other
// models before them, what a Student plan is served) is served on chat
// completions alone: Copilot's /v1/messages answers it "no model endpoints
// available given user constraints" and its /responses "not supported via
// Responses API" (#754).
func copilotAPIs(endpoints []string) []string {
	if len(endpoints) == 0 {
		return []string{string(Chat)}
	}
	return catalog.EndpointAPIs(endpoints)
}

// internal is a Copilot model id nobody picks by hand.
var copilotInternal = lazyre.New(`^(copilot-search|exec-agent|trajectory)|-(secondary|tertiary|4th|free-auto)$`)

// A model Copilot offers with terms of its own stays disabled until the
// account accepts them, as VS Code does when one is first picked; magpie
// lists it and accepts them the first time a request asks for it.
var (
	copilotTermsMu sync.Mutex
	copilotTerms   = map[string]map[string]bool{} // by GitHub token: models whose terms wait
	copilotPicks   = map[string][]string{}        // by GitHub token: models it may pick by hand, the likeliest served first
)

// copilotAccept enables model for the account when its terms still wait.
// A failure is left to the request, whose answer then says why.
func copilotAccept(ctx context.Context, app copilotApp, s copilotSession, model string) {
	if model == "" || model == CopilotAuto {
		return
	}
	copilotTermsMu.Lock()
	waiting, known := copilotTerms[app.Token]
	copilotTermsMu.Unlock()
	if !known {
		// not listed since magpie started; the list says which wait
		if _, err := copilotModels(ctx, app); err != nil {
			return
		}
		copilotTermsMu.Lock()
		waiting = copilotTerms[app.Token]
		copilotTermsMu.Unlock()
	}
	if !waiting[model] {
		return
	}
	base := s.apiBase(app.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/models/"+url.PathEscape(model)+"/policy", strings.NewReader(`{"state":"enabled"}`))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers() {
		req.Header.Set(k, v)
	}
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return
	}
	res.Body.Close()
	if res.StatusCode/100 == 2 {
		copilotTermsMu.Lock()
		delete(copilotTerms[app.Token], model)
		copilotTermsMu.Unlock()
	}
}

// copilotModels asks Copilot which chat models this account may use.
// copilotBaseModel is Copilot's base model, billed to no allowance on any
// plan.
const copilotBaseModel = "gpt-4.1"

func copilotModels(ctx context.Context, app copilotApp) ([]catalog.Model, error) {
	s, err := app.session(ctx)
	if err != nil {
		return nil, err
	}
	base := s.apiBase(app.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	for k, v := range s.headers() {
		req.Header.Set(k, v)
	}
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	var v struct {
		Data []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Vendor    string   `json:"vendor"`
			Picker    bool     `json:"model_picker_enabled"`
			Category  string   `json:"model_picker_category"`
			Default   bool     `json:"is_chat_default"`
			Fallback  bool     `json:"is_chat_fallback"`
			Endpoints []string `json:"supported_endpoints"`
			Billing   *struct {
				Premium bool `json:"is_premium"`
			} `json:"billing"`
			Capabilities struct {
				Type     string `json:"type"`
				Supports struct {
					Efforts []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
			Policy *struct {
				State string `json:"state"`
				Terms string `json:"terms"`
			} `json:"policy"`
		} `json:"data"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &v) != nil {
		return nil, errors.New("Copilot models: " + APIError(b, res.Status))
	}
	var out []catalog.Model
	var picks []string
	rank := map[string]int{} // how early a pick stands in for Auto
	waiting := map[string]bool{}
	copilotSeenMu.Lock()
	for _, m := range v.Data {
		if m.Capabilities.Type == "chat" {
			copilotSeen[m.ID] = copilotAPIs(m.Endpoints)
		}
	}
	copilotSeenMu.Unlock()
	for _, m := range v.Data {
		if copilotDrawsModel(m.Capabilities.Type, m.ID) && !copilotInternal.MatchString(m.ID) {
			// an image model (GPT Image 2.5 Flare in Copilot CLI): drawn at
			// the account's /images API, never offered to an agent's chat
			if m.Policy == nil || m.Policy.State == "enabled" {
				out = append(out, catalog.Model{ID: m.ID, Name: m.Name, Draws: true})
			}
			continue
		}
		if m.Capabilities.Type != "chat" || copilotInternal.MatchString(m.ID) {
			continue
		}
		// a model no picker offers is an old snapshot or Copilot's own; an
		// Experimental vendor's is Copilot's own too, unless the picker
		// offers it (#4343: mai-code-2-flash, an Enterprise account's,
		// served on /responses)
		if !m.Picker && (m.Category == "" || m.Vendor == "Experimental") {
			continue
		}
		switch {
		case m.Policy != nil && m.Policy.State == "enabled", m.Policy == nil && m.Picker:
			picks = append(picks, m.ID)
			// Copilot's base model (VS Code's copilot-base: the list's
			// is_chat_fallback), then its default, then gpt-4.1, which
			// the list no longer flags as either but is the one a
			// Student plan is served picked by hand (#256: it was last
			// of seven picks, the six before it refused), then one billed
			// to no premium allowance: what a plan that may pick little
			// is likeliest to be served
			switch {
			case m.Fallback:
				rank[m.ID] = 0
			case m.Default:
				rank[m.ID] = 1
			case m.ID == copilotBaseModel:
				rank[m.ID] = 2
			case m.Billing != nil && !m.Billing.Premium:
				rank[m.ID] = 3
			default:
				rank[m.ID] = 4
			}
		case m.Policy != nil && m.Policy.Terms != "":
			waiting[m.ID] = true
		default:
			continue // not the account's to enable: it answers 403
		}
		out = append(out, catalog.Model{ID: m.ID, Name: m.Name, Efforts: m.Capabilities.Supports.Efforts, APIs: copilotAPIs(m.Endpoints)})
	}
	// Auto, which Copilot's clients offer every account beside the models
	// it lists, and the only choice a Student plan has: an account whose
	// list leaves it nothing to pick by hand still has it
	out = append(out, copilotAutoModel)
	slices.SortStableFunc(picks, func(a, b string) int { return rank[a] - rank[b] })
	copilotTermsMu.Lock()
	copilotTerms[app.Token] = waiting
	copilotPicks[app.Token] = picks
	copilotTermsMu.Unlock()
	return out, nil
}

// forgetClaudeStatus drops what `claude auth status` said; tests use it.
func forgetClaudeStatus() {
	claudeStatusMu.Lock()
	claudeStatusAt, claudeStatusUser, claudeStatusPlan, claudeStatusOut = time.Time{}, "", "", false
	claudeStatusGen++
	claudeStatusBusy = false
	claudeStatusMu.Unlock()
}

// copilotDrawsModel is whether a /models entry of capabilities.type typ is
// an image model: Copilot types it for images, or it isn't a chat, embedding
// or other known type and its id names images.
func copilotDrawsModel(typ, id string) bool {
	switch strings.ToLower(typ) {
	case "image", "images", "image_generation", "image-generation":
		return true
	case "chat", "embeddings", "embedding", "completion", "completions":
		return false
	}
	return catalog.DrawsID(id)
}
