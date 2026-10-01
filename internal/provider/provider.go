// Package provider holds the model vendors magpie can reach: where each one
// lives, which protocols it speaks, the API key the user typed in, and which
// of its models should show up in the agents' pickers.
//
// Provider keys are never read from environment variables. A provider is
// exactly what the user entered, kept in ~/.config/magpie/providers.json
// (mode 0600).
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// Protocol is a wire API magpie can speak to an upstream.
type Protocol string

const (
	Chat      Protocol = "chat"      // OpenAI Chat Completions
	Responses Protocol = "responses" // OpenAI Responses
	Anthropic Protocol = "anthropic" // Anthropic Messages
	Gemini    Protocol = "gemini"    // Google Gemini; only served to clients, never spoken upstream
)

// Protocols in the order magpie prefers them when it has to translate.
var Protocols = []Protocol{Chat, Responses, Anthropic}

// Provider is one configured vendor.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Was are ids the provider had before it was renamed (see Rename):
	// a model still picked by one of them reaches it.
	Was    []string `json:"was,omitempty"`
	Icon   string   `json:"icon,omitempty"`
	Preset string   `json:"preset,omitempty"` // preset this was created from, if any
	Key    string   `json:"key"`              // API key, as typed by the user

	// KeyName names the key in use, and Keys are the provider's other
	// accounts: keys saved to switch to (see keys.go).
	KeyName string       `json:"keyName,omitempty"`
	Keys    []KeyAccount `json:"keys,omitempty"`
	// KeyProtocol, when set, is the one protocol the first key is good
	// for: a relay that hands out one key for Anthropic and another for
	// OpenAI (see KeyAccount.Protocol).
	KeyProtocol Protocol `json:"keyProtocol,omitempty"`

	// Base URLs, one per protocol the vendor serves natively. magpie appends
	// the usual paths: chat/responses bases end in /v1 (OpenAI style),
	// the Anthropic base is the root (what ANTHROPIC_BASE_URL takes).
	Chat      string `json:"chat,omitempty"`
	Responses string `json:"responses,omitempty"`
	Anthropic string `json:"anthropic,omitempty"`
	// Decide is the base of a decision API (TypeSafe's System One, which
	// Jev answers): a provider with it serves no conversation, only the
	// routing groups' choices of model and effort (see decide.go).
	Decide string `json:"decide,omitempty"`

	// Fallback is where a request goes when this provider can't take it —
	// out of quota, rate limited, overloaded or down — before any of the
	// reply has been sent: models as agents pick them (provider/model),
	// tried in order.
	Fallback []string `json:"fallback,omitempty"`

	// Routing is how requests spread over the keys or accounts it has on:
	// "" smart, the first while it has quota to spare, then whichever has
	// the most; "order" in order, the next one only when the one before
	// can't take it; "rotate" each in turn; "usage" the least used first.
	// Whichever it is, one out of credit, out of quota, rate limited or
	// failing is passed over for as long as that lasts.
	Routing string `json:"routing,omitempty"`

	// Affinity is how long a conversation stays with the key or account
	// that answered it, so the vendor's prompt cache it filled is read
	// again rather than lost (see Affinities): "" auto, "session",
	// "turn", "off".
	Affinity string `json:"affinity,omitempty"`

	// Headers are extra HTTP request headers sent to the vendor, exactly as
	// the user typed them. They ride on every request magpie makes to a plain
	// key+URL provider — forwarded calls, connectivity tests, and model-list
	// fetches — applied after the auth headers, so the user can override those
	// when a gateway insists on a private scheme. Signed-in agent accounts
	// ignore them: their auth is the agent's own.
	Headers map[string]string `json:"headers,omitempty"`

	// Searches says the vendor answers a web search tool offered on its
	// Anthropic or Responses API by itself (web_search_20250305,
	// web_search): a relay in front of Anthropic's or OpenAI's API, which
	// magpie can't tell from its host. A request offering one then goes to
	// it as the client sent it, rather than given magpie's search (#359).
	Searches bool `json:"searches,omitempty"`

	// Proxy is the proxy magpie's requests to this provider go through
	// (#237: Codex through one, a vendor at home without): "" follows
	// the global one (Settings' Proxy, the environment's, the system's),
	// "direct" none, anything else the proxy's address (http://, https://,
	// socks5://; host:port means http). Signed-in accounts keep it too.
	Proxy string `json:"proxy,omitempty"`
	// AccountProxies is, for a subscription holding several accounts
	// (Codex's, Claude Code's…), the proxy of each account that has one
	// of its own, by its name in lower case, as Proxy takes one; an
	// account not in it follows Proxy (see ProxyChoice).
	AccountProxies map[string]string `json:"accountProxies,omitempty"`

	// BalanceURL, when set, is where the vendor tells what is left on a
	// key, asked with the key the way a chat request carries it; BalancePath
	// picks the amount out of the JSON reply (see balance.go). The vendors
	// magpie knows need neither.
	BalanceURL  string `json:"balanceURL,omitempty"`
	BalancePath string `json:"balancePath,omitempty"`
	// BalanceToken is what a vendor tells the whole account's balance to,
	// where a key is told only what is left on itself: AiHubMix's system
	// access token, or a new-api relay's for its /api/user/self named in
	// BalanceURL (see TakesBalanceToken). It is asked with nothing else but
	// the provider's headers, when the endpoint is one named.
	BalanceToken string `json:"balanceToken,omitempty"`
	// ZhipuTeam, for a Zhipu or Z.ai key on a team's GLM Coding Plan, is
	// the team's organization and project, from the BigModel console: the
	// team's windows are told to the key only with them (see
	// zhipuKeyTeamWindows). nil for a key of the user's own plan.
	ZhipuTeam *ZhipuTeam `json:"zhipuTeam,omitempty"`

	// ModelsURL, when set, is where the vendor lists its models, for one
	// that lists them away from the base URL requests go to (Xiaomi MiMo's
	// plans are served at their own hosts, the list at api.xiaomimimo.com).
	ModelsURL string `json:"modelsURL,omitempty"`

	// Models the user chose to expose. Empty means "the preset's picks, or
	// everything the vendor lists when that list is short".
	Models []string `json:"models,omitempty"`
	// Unlisted keeps the provider's own models out of the list agents see:
	// it serves only through the routing groups it is in, and by its
	// "provider/model" ids.
	Unlisted bool `json:"unlisted,omitempty"`
	// Off switches the provider off without removing it: its keys and
	// settings stay, but agents aren't given its models, no request,
	// routing group or fallback goes to it, and its balance isn't asked,
	// until it is switched on again (#163) — for a key out of quota.
	Off bool `json:"off,omitempty"`
	// Contexts is how long a request the user says a model takes, in
	// tokens, over what the vendor or models.dev says: by model id, "*"
	// for all the provider's models. Agents are told it.
	Contexts map[string]int `json:"contexts,omitempty"`
	// Family is a tag the provider's models go by in which agents are
	// shown them (settings' Visible), with the provider's id.
	Family string `json:"family,omitempty"`

	Catalog string `json:"catalog,omitempty"` // models.dev id, for names and reasoning levels
	Website string `json:"website,omitempty"`
	KeysURL string `json:"keysUrl,omitempty"`

	// IconURL is a picture the vendor named in an import link, to be fetched
	// once the user confirms. It is only a carrier between parsing and that
	// fetch: Save drops it, so it never reaches providers.json.
	IconURL string `json:"iconUrl,omitempty"`

	// Hidden is set on an account the user removed from magpie; the
	// agent stays signed in, magpie just leaves it alone.
	Hidden bool `json:"hidden,omitempty"`
	// Quiet is set on a removed account whose "Add it back" line the user
	// dismissed: it is offered again only from the Add sheet (#116).
	Quiet bool `json:"quiet,omitempty"`

	// Account is set when the provider is an agent the user signed in to
	// (see account.go); it is derived, never stored.
	Account *Account `json:"-"`
}

type file struct {
	Providers []Provider `json:"providers"`
	Groups    []Group    `json:"groups,omitempty"`
}

// Path is the file the user's providers live in.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "magpie", "providers.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "magpie", "providers.json")
}

func load() file {
	var f file
	if b, err := os.ReadFile(Path()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func store(f file) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	pruneIcons(f)
	if err := os.Chmod(p, 0o600); err != nil {
		return err
	}
	// what agents were handed of the catalog may be out of date now
	catalog.Touched()
	return nil
}

// All lists the configured providers in the order they were added, then
// the signed-in agents. An entry in the file with no URL is only the
// model picks for one of those accounts.
func All() []Provider {
	stored := load().Providers
	picks := map[string]Provider{}
	var out []Provider
	for _, p := range stored {
		p = normalize(p)
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" && p.Decide == "" {
			picks[p.ID] = p
			continue
		}
		out = append(out, p)
	}
	for _, a := range Accounts() {
		if _, taken := find(out, a.ID); taken || picks[a.ID].Hidden {
			continue
		}
		pk := picks[a.ID]
		a.Models, a.Unlisted, a.Off, a.Fallback, a.Routing, a.Affinity, a.Contexts, a.Family = pk.Models, pk.Unlisted, pk.Off, pk.Fallback, pk.Routing, pk.Affinity, pk.Contexts, pk.Family
		a.Proxy, a.AccountProxies = pk.Proxy, pk.AccountProxies
		if a.ID == "cursor" { // picked before its efforts were one model
			a.Models = cursorPicks(a.Models)
		}
		if a.ID == "antigravity" { // picked before its levels were one model
			a.Models = antigravityPicks(a.Models)
		}
		out = append(out, a)
	}
	return out
}

// Hidden lists the signed-in accounts the user removed from magpie.
func Hidden() []Provider {
	var out []Provider
	for _, a := range Accounts() {
		for _, p := range load().Providers {
			if p.ID == a.ID && p.Hidden {
				a.Quiet = p.Quiet
				out = append(out, a)
			}
		}
	}
	return out
}

func find(ps []Provider, id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Find looks a provider up by id (or name, case-insensitively).
func Find(id string) (*Provider, error) {
	q := strings.ToLower(strings.TrimSpace(id))
	all := All()
	// an id before a name: a provider of the user's called WorkBuddy isn't
	// the workbuddy subscription
	if i := slices.IndexFunc(all, func(p Provider) bool { return p.ID == q }); i >= 0 {
		return &all[i], nil
	}
	for _, p := range all {
		if strings.ToLower(p.Name) == q {
			return &p, nil
		}
	}
	for _, p := range all {
		if slices.Contains(p.Was, q) {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no provider %q — magpie providers lists them", id)
}

var idRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug derives an id from a name: "My Relay" → "my-relay".
func Slug(name string) string {
	return strings.Trim(idRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// Save adds or replaces a provider.
func Save(p Provider) error {
	p = normalize(p)
	p.IconURL = "" // import-only: never stored
	if p.ID == "" {
		p.ID = Slug(p.Name)
	}
	if p.ID == "" || p.ID != Slug(p.ID) {
		return fmt.Errorf("provider id must be lowercase letters, digits and dashes, not %q", p.ID)
	}
	if p.ID == "magpie" {
		return errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	if p.ID == strings.TrimSuffix(GroupPrefix, "/") {
		return errors.New(`"group" starts the ids of routing groups; pick another id`)
	}
	if err := settings.CheckProxy(p.Proxy); err != nil {
		return err
	}
	if err := checkAccountProxies(p.AccountProxies); err != nil {
		return err
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if _, ok := find(Accounts(), p.ID); ok || p.ID == "kiro" && (p.Key != "" || stored(p.ID)) {
		// an account keeps only the user's model picks; the rest is the
		// agent's own sign-in. One the user removed stays removed: only
		// ShowAccount brings it back. Kiro's alone also keeps a key, which
		// it takes in place of a sign-in — so saving one is how a Kiro
		// that isn't signed in is added.
		key := ""
		if p.ID == "kiro" {
			key = p.Key
		}
		p = Provider{ID: p.ID, Key: key, Models: p.Models, Unlisted: p.Unlisted, Off: p.Off, Fallback: p.Fallback, Routing: p.Routing, Affinity: p.Affinity, Contexts: p.Contexts, Family: p.Family, Proxy: p.Proxy, AccountProxies: p.AccountProxies, Hidden: hiddenAccount(p.ID), Quiet: quietAccount(p.ID)}
	} else {
		p.AccountProxies = nil // a provider of a key has no accounts to proxy apart
		if slices.Contains(accountIDs, p.ID) && !stored(p.ID) {
			// taken, it would hide that subscription once signed in
			return fmt.Errorf("%q is the id of the %s subscription; pick another name", p.ID, p.ID)
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" && p.Decide == "" {
			if p.Preset == AzurePreset {
				return errors.New("Azure OpenAI needs your resource's endpoint, e.g. https://<resource>.openai.azure.com")
			}
			return errors.New("a provider needs a base URL")
		}
		if p.Key == "" && !keyOptional(p) {
			return fmt.Errorf("%s needs an API key", p.Name)
		}
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			if p.Was == nil {
				p.Was = f.Providers[i].Was
			}
			f.Providers[i] = p
			return store(f)
		}
	}
	f.Providers = append(f.Providers, p)
	return store(f)
}

// Add saves a provider the user just added, beside those already here: an
// id in use — the preset's, or the one its name slugs to — moves on to the
// next free one (anthropic-2), and a name in use gets the same number, so a
// second key of a vendor, or one key for another workspace, is a provider of
// its own rather than one replacing the first. It answers the id saved.
func Add(p Provider) (string, error) {
	return add(p, true)
}

func add(p Provider, once bool) (string, error) {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	if p.ID == "" {
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		// a name with no Latin letters or digits in it (中转站) slugs to
		// nothing: the id is the site's instead
		p.ID = hostID(p)
	}
	// the same key on the same host with the same headers is the one
	// already here, not another: adding it twice would only split its usage.
	// A copy the user asked for is taken (AddCopy).
	for _, h := range All() {
		if once && h.Account == nil && sameProvider(h, normalize(p)) {
			return "", fmt.Errorf("%s is already added with that key (%s); magpie provider key %s <key> changes its key", h.Name, h.ID, h.ID)
		}
	}
	p.ID, p.Name = freeID(p.ID), freeName(p.Name)
	return p.ID, Save(p)
}

// AddCopy adds p, a copy the user made of the provider from (#268), beside
// it: what the form doesn't carry — the keys, the balance token, how
// requests spread over the keys, where they fall back to — is from's where
// p leaves it out. The same key on the same host is taken, the copy being
// asked for (another model list, another endpoint). A signed-in account is
// never copied: its sign-in is the agent's.
func AddCopy(p Provider, from string) (string, error) {
	src, err := Find(from)
	if err != nil {
		return "", err
	}
	if src.Account != nil {
		return "", fmt.Errorf("%s is a signed-in account, which can't be copied", src.Name)
	}
	if p.Key == "" {
		p.Key, p.KeyName, p.KeyProtocol = src.Key, src.KeyName, src.KeyProtocol
		p.Keys = slices.Clone(src.Keys)
		p.Routing, p.Affinity = src.Routing, src.Affinity
	}
	if p.BalanceToken == "" {
		p.BalanceToken = src.BalanceToken
	}
	if p.ZhipuTeam == nil {
		p.ZhipuTeam = src.ZhipuTeam
	}
	if p.Fallback == nil {
		p.Fallback = slices.Clone(src.Fallback)
	}
	p.Unlisted = p.Unlisted || src.Unlisted
	p.Searches = p.Searches || src.Searches
	if p.Website == "" {
		p.Website = src.Website
	}
	if p.KeysURL == "" {
		p.KeysURL = src.KeysURL
	}
	return add(p, false)
}

// hostID is an id for a provider from the host it is on: api.relay.com is
// relay, and one on an IP address, or with no address, is custom.
func hostID(p Provider) string {
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic} {
		h := hostOf(u)
		if host, _, err := net.SplitHostPort(h); err == nil {
			h = host
		}
		if h = strings.Trim(h, "[]"); h == "" || net.ParseIP(h) != nil {
			continue
		}
		labels := strings.Split(h, ".")
		if len(labels) > 1 {
			labels = labels[:len(labels)-1] // the .com
		}
		for len(labels) > 1 && (labels[0] == "api" || labels[0] == "www") {
			labels = labels[1:]
		}
		if id := Slug(strings.Join(labels, "-")); id != "" {
			return id
		}
	}
	return "custom"
}

// freeName is name, or "name 2", "name 3"… whichever no provider is called,
// since providers are found by name as well as id.
func freeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	taken := map[string]bool{}
	for _, p := range All() {
		taken[strings.ToLower(p.Name)] = true
	}
	for n, try := 2, name; ; n++ {
		if !taken[strings.ToLower(try)] {
			return try
		}
		try = name + " " + itoa(n)
	}
}

// accountIDs are the ids of the subscriptions magpie can list (account.go).
var accountIDs = []string{"antigravity", "claude", "codex", CommandCodePlanID, "copilot", "cursor", "devin", "factory", "gemini", "grok", "kiro", MiMoID, "qoder", QoderCNID, "workbuddy", WorkBuddyAIID, "zcode", "zed"}

func stored(id string) bool {
	for _, p := range load().Providers {
		if p.ID == id {
			return true
		}
	}
	return false
}

func hiddenAccount(id string) bool {
	for _, p := range load().Providers {
		if p.ID == id {
			return p.Hidden
		}
	}
	return false
}

func quietAccount(id string) bool {
	for _, p := range load().Providers {
		if p.ID == id {
			return p.Quiet
		}
	}
	return false
}

// QuietAccount stops reminding the user of an account they removed: its
// "Add it back" line goes, and it is offered only from the Add sheet.
func QuietAccount(id string) error {
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == id && f.Providers[i].Hidden {
			f.Providers[i].Quiet = true
			return store(f)
		}
	}
	return nil
}

// ShowAccount brings back the signed-in account of an agent the user had
// removed from magpie.
func ShowAccount(id string) error {
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == id && f.Providers[i].Hidden {
			f.Providers[i].Hidden, f.Providers[i].Quiet = false, false
			return store(f)
		}
	}
	return nil
}

// Delete removes a provider. An account is only hidden from magpie (its
// model picks kept); signing out is the agent's job.
func Delete(id string) error {
	if p, ok := find(Accounts(), id); ok && p.IsPlugin() && !Moved(p.Account.plugin.ID) {
		// a plugin's sign-in is magpie's own: removing it signs out. A
		// built-in moved onto its plugin is only hidden, as the built-in
		// was, its accounts and model picks kept.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return plugin.SignOut(ctx, p.Account.plugin.ID, "")
	}
	if _, ok := find(Accounts(), id); ok {
		f := load()
		for i := range f.Providers {
			if f.Providers[i].ID == id {
				f.Providers[i].Hidden = true
				return store(f)
			}
		}
		f.Providers = append(f.Providers, Provider{ID: id, Hidden: true})
		return store(f)
	}
	f := load()
	keep := f.Providers[:0]
	found := false
	for _, p := range f.Providers {
		if p.ID == id {
			found = true
			continue
		}
		keep = append(keep, p)
	}
	if !found {
		return fmt.Errorf("no provider %q", id)
	}
	f.Providers = keep
	return store(f)
}

// keyOptional is true for local servers, which usually have no key.
func keyOptional(p Provider) bool {
	if pr := Preset(p.Preset); pr != nil && pr.NoKey {
		return true
	}
	h := p.Host()
	return strings.HasPrefix(h, "localhost") || strings.HasPrefix(h, "127.0.0.1") || strings.HasPrefix(h, "0.0.0.0")
}

func normalize(p Provider) Provider {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	p.Name = strings.TrimSpace(p.Name)
	p.Key = strings.TrimSpace(p.Key)
	p.Proxy = strings.TrimSpace(p.Proxy)
	p.AccountProxies = normalAccountProxies(p.AccountProxies)
	p.ZhipuTeam = p.ZhipuTeam.normal()
	p.remoteMagpieEndpoints()
	for _, u := range []*string{&p.Chat, &p.Responses, &p.Anthropic, &p.Decide, &p.Website, &p.KeysURL} {
		*u = strings.TrimRight(strings.TrimSpace(*u), "/")
		if *u != "" && !strings.Contains(*u, "://") {
			*u = "https://" + *u
		}
	}
	// the Anthropic base is the root /v1/messages is asked at: one given as
	// .../v1 or .../v1/messages, as vendors' docs often show it, would have
	// the version sent twice and every message turned away (404) while the
	// model list, asked at both, still answers
	for _, suf := range []string{"/v1/messages", "/v1"} {
		if b, ok := strings.CutSuffix(p.Anthropic, suf); ok && strings.Contains(b, "://") && len(b) > len("https://") {
			p.Anthropic = b
			break
		}
	}
	p.Models = cleanList(p.Models)
	p.Fallback = cleanList(p.Fallback)
	// a provider saved under the id the qianfan preset carried its first
	// day (qianfan-token-plan, v0.1.394) is the preset since renamed:
	// its own id stays, so whatever the agents wired to it keeps routing
	if p.Preset == "qianfan-token-plan" {
		p.Preset = "baidu-qianfan"
	}
	if p.Routing != Ordered && p.Routing != Rotate && p.Routing != LeastUsed {
		p.Routing = ""
	}
	if !slices.Contains(Affinities, p.Affinity) {
		p.Affinity = ""
	}
	p.Catalog = strings.Join(p.Catalogs(), ", ")
	// a Bedrock provider saved before the preset had its Responses API
	// (#176) gets it where its chat completions are: the runtime serves both
	// at /openai/v1
	if p.Responses == "" && strings.HasSuffix(p.Chat, "/openai/v1") && p.IsBedrock() {
		p.Responses = p.Chat
	}
	// Azure OpenAI's resource, however its endpoint was pasted, is asked
	// on its v1 API, chat completions and Responses both (azure.go)
	p.azureEndpoints()
	// a preset's provider keeps its headers too: the preset gives the
	// endpoints and catalog, the headers say which workspace or app it is
	p.Headers = cleanHeaders(p.Headers)
	if pr := Preset(p.Preset); pr != nil {
		if p.Icon == "" {
			p.Icon = pr.Icon
		}
		if p.Catalog == "" {
			p.Catalog = pr.Catalog
		}
		if p.Website == "" {
			p.Website = pr.Website
		}
		if p.KeysURL == "" {
			p.KeysURL = pr.KeysURL
		}
		// a region's own key page goes with its endpoints (Qianfan's pay
		// as you go makes its keys on the IAM page, the plans at the
		// plan console)
		for _, r := range pr.Regions {
			if r.KeysURL != "" && p.atRegion(r) {
				p.KeysURL = r.KeysURL
			}
		}
	}
	return p
}

// Catalogs are the models.dev ids the provider's models are looked up in,
// first match wins: a gateway that resells several vendors names them all
// ("openai, deepseek").
func (p Provider) Catalogs() []string {
	return cleanList(strings.FieldsFunc(strings.ToLower(p.Catalog), func(r rune) bool { return r == ',' || r == ' ' }))
}

func cleanList(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// cleanHeaders trims header names and values and drops entries with an empty
// name, returning nil when nothing is left so the field stays out of the JSON.
func cleanHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Base returns the base URL for a protocol, or "" when the vendor lacks it.
func (p Provider) Base(proto Protocol) string {
	switch proto {
	case Chat:
		return p.Chat
	case Responses:
		return p.Responses
	case Anthropic:
		return p.Anthropic
	case CodeAssist:
		if p.Account != nil {
			return p.Account.codeAssist
		}
	}
	return ""
}

// Speaks lists the protocols the vendor serves natively, preferred first.
func (p Provider) Speaks() []Protocol {
	// a Google sign-in speaks Code Assist, and only that
	if p.Account != nil && p.Account.codeAssist != "" && !p.IsPlugin() {
		return []Protocol{CodeAssist}
	}
	var out []Protocol
	for _, pr := range Protocols {
		if p.Base(pr) != "" {
			out = append(out, pr)
		}
	}
	// a plugin's Gemini models, beside what else it serves
	if p.IsPlugin() && p.Account.codeAssist != "" {
		out = append(out, CodeAssist)
	}
	return out
}

// ResponsesFirst: an OpenAI model on OpenAI's API, Copilot's or Bedrock's,
// which is best asked on the Responses API though Chat serves it too.
func (p Provider) ResponsesFirst(model string) bool {
	if p.Responses != "" && p.IsBedrock() {
		return bedrockGPT(model)
	}
	// Azure OpenAI's deployments are named as the user likes; one named
	// for its model (gpt-5-codex, o4-mini) is taken for it
	if p.Responses == "" || (p.ID != "copilot" && HostOf(p.Responses) != "api.openai.com" && !p.IsAzure()) {
		return false
	}
	m := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

// Native is the API model is best asked on at this provider: one it serves
// the model on itself, so a request on it is relayed as it is rather than
// translated — Responses for a ChatGPT sign-in, or an OpenAI model on
// OpenAI's API or Copilot's; Chat where that is served. "" when every
// request is translated anyway: a sign-in served through its agent's own
// API (Claude Code's binary, Cursor, Devin, Kiro, Code Assist).
func (p Provider) Native(model string) Protocol {
	if p.Account != nil {
		switch p.Account.Agent {
		case "claude", "cursor", "devin", "kiro":
			return ""
		}
	}
	apis := p.APIs(model)
	var out []Protocol
	for _, pr := range p.Speaks() {
		if slices.Contains(Protocols, pr) && (apis == nil || slices.Contains(apis, pr)) {
			out = append(out, pr)
		}
	}
	if len(out) == 0 {
		return ""
	}
	if p.ResponsesFirst(model) && slices.Contains(out, Responses) {
		return Responses
	}
	return out[0]
}

// Host is the vendor's API host, for display.
func (p Provider) Host() string {
	if p.Account != nil && p.Account.moved {
		return p.Account.wasHost // not plugin://<id>
	}
	for _, pr := range p.Speaks() {
		if u := p.Base(pr); u != "" {
			return HostOf(u)
		}
	}
	if p.Decide != "" {
		return HostOf(p.Decide)
	}
	return ""
}

// Where is what the provider's calls go to, as usage keeps it: the API's
// host, and for a subscription who is signed in there too. The id alone
// can't tell: it can be given to another vendor or account later.
func (p Provider) Where() string {
	h := p.Host()
	if p.Account != nil && p.Account.User != "" {
		if h == "" {
			return p.Account.User
		}
		return h + " as " + p.Account.User
	}
	return h
}

// IsOpenCode reports whether the provider is OpenCode's gateway (Zen or Go),
// which routes and caches by conversation and turns away requests that do
// not name one in x-opencode-session.
func (p Provider) IsOpenCode() bool {
	h := p.Host()
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// IsBedrock reports whether the provider is Amazon Bedrock's runtime: made
// from its preset, or at its host.
func (p Provider) IsBedrock() bool {
	if p.Preset == "bedrock" {
		return true
	}
	h := p.Host()
	return strings.HasPrefix(h, "bedrock-runtime.") && strings.HasSuffix(h, ".amazonaws.com")
}

// bedrockClaude is a Bedrock id of a Claude model: a model id
// (anthropic.claude-opus-4-8) or an inference profile's
// (apac.anthropic.claude-opus-5-5).
func bedrockClaude(model string) bool {
	return strings.Contains(strings.ToLower(model), "anthropic.claude")
}

// bedrockGPT is a Bedrock id of one of OpenAI's closed GPT models
// (global.openai.gpt-6-luna), which its runtime serves on the Responses
// API as well as chat completions, and with function tools and reasoning
// on Responses alone (#176: "Function tools with reasoning_effort are not
// supported for global.openai.gpt-6-luna in /v1/chat/completions"). Not
// gpt-oss, which the runtime serves on chat completions alone.
func bedrockGPT(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "openai.gpt-") && !strings.Contains(m, "gpt-oss")
}

// HostOf pulls the host out of a URL, for display.
func HostOf(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return strings.ToLower(u)
}

// Mask hides all but the ends of a secret.
func Mask(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("•", len(s))
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// Ready reports whether the provider can be used: it has a key, needs
// none, or is a signed-in agent.
func (p Provider) Ready() bool { return p.Account != nil || p.Key != "" || keyOptional(p) }

// On is whether the provider takes requests: ready, and not switched off.
func (p Provider) On() bool { return p.Ready() && !p.Off }

// SetOff switches a provider off, or on again (see Provider.Off). Saving
// it brings the model lists written into agents' files up to date.
func SetOff(id string, off bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Off = off
	return Save(*p)
}

// SwitchedOff is the provider switched off in magpie that a model id
// names: as "provider/model", or a model only switched-off providers list.
// A request for it is refused as that, not as a model magpie doesn't know.
func SwitchedOff(id string) (Provider, bool) {
	id = strings.TrimSuffix(strings.TrimSpace(id), "[1m]")
	if pid, _, ok := strings.Cut(id, "/"); ok {
		if p, err := Find(pid); err == nil && p.Off {
			return *p, true
		}
	}
	for _, p := range All() {
		if !p.Off || !p.Ready() {
			continue
		}
		if slices.ContainsFunc(p.Exposed(), func(m catalog.Model) bool { return m.ID == id }) {
			return p, true
		}
	}
	return Provider{}, false
}
