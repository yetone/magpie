// Package provider holds the model vendors magpie can reach: where each one
// lives, which protocols it speaks, the API key the user typed in, and which
// of its models should show up in the agents' pickers.
//
// Nothing here reads environment variables. A provider is exactly what the
// user entered, kept in ~/.config/magpie/providers.json (mode 0600).
package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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
	ID     string `json:"id"`
	Name   string `json:"name"`
	Icon   string `json:"icon,omitempty"`
	Preset string `json:"preset,omitempty"` // preset this was created from, if any
	Key    string `json:"key"`              // API key, as typed by the user

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

	// Headers are extra HTTP request headers sent to the vendor, exactly as
	// the user typed them. They ride on every request magpie makes to a plain
	// key+URL provider — forwarded calls, connectivity tests, and model-list
	// fetches — applied after the auth headers, so the user can override those
	// when a gateway insists on a private scheme. Signed-in agent accounts
	// ignore them: their auth is the agent's own.
	Headers map[string]string `json:"headers,omitempty"`

	// BalanceURL, when set, is where the vendor tells what is left on a
	// key, asked with the key the way a chat request carries it; BalancePath
	// picks the amount out of the JSON reply (see balance.go). The vendors
	// magpie knows need neither.
	BalanceURL  string `json:"balanceURL,omitempty"`
	BalancePath string `json:"balancePath,omitempty"`

	// Models the user chose to expose. Empty means "the preset's picks, or
	// everything the vendor lists when that list is short".
	Models []string `json:"models,omitempty"`

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

	// Account is set when the provider is an agent the user signed in to
	// (see account.go); it is derived, never stored.
	Account *Account `json:"-"`
}

type file struct {
	Providers []Provider `json:"providers"`
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
	return os.Chmod(p, 0o600)
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
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			picks[p.ID] = p
			continue
		}
		out = append(out, p)
	}
	for _, a := range Accounts() {
		if _, taken := find(out, a.ID); taken || picks[a.ID].Hidden {
			continue
		}
		a.Models, a.Fallback, a.Routing = picks[a.ID].Models, picks[a.ID].Fallback, picks[a.ID].Routing
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
	for _, p := range All() {
		if p.ID == q || strings.ToLower(p.Name) == q {
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
	if p.Name == "" {
		p.Name = p.ID
	}
	if a, ok := find(Accounts(), p.ID); ok {
		// an account keeps only the user's model picks; the rest is the
		// agent's own sign-in. Saving it again brings a removed one back.
		p = Provider{ID: a.ID, Models: p.Models, Fallback: p.Fallback, Routing: p.Routing}
	} else {
		if slices.Contains(accountIDs, p.ID) && !stored(p.ID) {
			// taken, it would hide that subscription once signed in
			return fmt.Errorf("%q is the id of the %s subscription; pick another name", p.ID, p.ID)
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return errors.New("a provider needs a base URL")
		}
		if p.Key == "" && !keyOptional(p) {
			return fmt.Errorf("%s needs an API key", p.Name)
		}
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = p
			return store(f)
		}
	}
	f.Providers = append(f.Providers, p)
	return store(f)
}

// accountIDs are the ids of the subscriptions magpie can list (account.go).
var accountIDs = []string{"claude", "codex", "copilot", "cursor", "devin", "grok"}

func stored(id string) bool {
	for _, p := range load().Providers {
		if p.ID == id {
			return true
		}
	}
	return false
}

// ShowAccount brings back the signed-in account of an agent the user had
// removed from magpie.
func ShowAccount(id string) error {
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == id && f.Providers[i].Hidden {
			f.Providers[i].Hidden = false
			return store(f)
		}
	}
	return nil
}

// Delete removes a provider. An account is only hidden from magpie (its
// model picks kept); signing out is the agent's job.
func Delete(id string) error {
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
	for _, u := range []*string{&p.Chat, &p.Responses, &p.Anthropic, &p.Website, &p.KeysURL} {
		*u = strings.TrimRight(strings.TrimSpace(*u), "/")
		if *u != "" && !strings.Contains(*u, "://") {
			*u = "https://" + *u
		}
	}
	p.Models = cleanList(p.Models)
	p.Fallback = cleanList(p.Fallback)
	if p.Routing != Ordered && p.Routing != Rotate && p.Routing != LeastUsed {
		p.Routing = ""
	}
	p.Catalog = strings.Join(p.Catalogs(), ", ")
	p.Headers = cleanHeaders(p.Headers)
	if p.Preset != "" {
		// Presets own their request shape; custom headers are supported only
		// for user-defined providers.
		p.Headers = nil
	}
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
	}
	return ""
}

// Speaks lists the protocols the vendor serves natively, preferred first.
func (p Provider) Speaks() []Protocol {
	var out []Protocol
	for _, pr := range Protocols {
		if p.Base(pr) != "" {
			out = append(out, pr)
		}
	}
	return out
}

// Host is the vendor's API host, for display.
func (p Provider) Host() string {
	for _, pr := range Protocols {
		if u := p.Base(pr); u != "" {
			return HostOf(u)
		}
	}
	return ""
}

// IsOpenCode reports whether the provider is OpenCode's gateway (Zen or Go),
// which routes and caches by conversation and turns away requests that do
// not name one in x-opencode-session.
func (p Provider) IsOpenCode() bool {
	h := p.Host()
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
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
