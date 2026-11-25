package gateway

import (
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// AccountsPath lists every account and key of every provider, whether or
// not its vendor tells a quota (@tmr11235 on X: a plugin testing whether
// an account works could find only the accounts /v1/magpie/quotas has a
// card for, never an API key or a ChatGPT API account). Each is named by
// its identifiers alone, never a key or a token, with the value
// X-Magpie-Account takes to pin a request to it (pin).
//
// ?models=1 adds each one's models: every model it can be asked for, as
// "<provider>/<model>" takes them, not the first 24 a provider with no
// picks offers agents (Exposed, manyModels; @tmr11235 again: a plugin
// testing each model wants the whole list).
const AccountsPath = "/v1/magpie/accounts"

// listedAccount is one account or key as AccountsPath tells it.
type listedAccount struct {
	Provider     string `json:"provider"`
	ProviderName string `json:"providerName,omitempty"`
	// ID is the account's stable id as a gateway key's account list knows
	// it (#905): a signed-in account's login id, a key's fingerprint
	// (provider.KeyID, a hash, never the key)
	ID string `json:"id"`
	// Pin is "<provider>/<id>", for X-Magpie-Account
	Pin   string `json:"pin"`
	Kind  string `json:"kind"`           // "account" or "key"
	Label string `json:"label"`          // the name it is shown by: the user's name for it, its email or login, a key's name or its masked self
	User  string `json:"user,omitempty"` // a signed-in account's email or login
	Plan  string `json:"plan,omitempty"`
	// On says requests may go to it: the provider is on and so is it
	On bool `json:"on"`
	// Models, asked for with ?models=1, are the ids of every model it can
	// be asked for: its provider's whole list (Available), less a model on
	// none of the provider's APIs and one the user's list for it leaves
	// out (#474), and for a held key those the key may use. Each is sent
	// as "<provider>/<id>", listed to agents or not (provider.Resolve).
	Models []string `json:"models,omitempty"`
}

// accountsList answers AccountsPath. It names the accounts as the quotas
// do, so it is asked as they are: this machine, or another with a key of
// a gateway shared on the local network; a key held to a budget, models
// or accounts only when its owner lets it be told quotas, and then only
// the accounts it may use (scopeQuotas).
func (s *Server) accountsList(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's accounts are told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	scope, ok := scopeQuotas(w, r)
	if !ok {
		return
	}
	only := strings.TrimSpace(r.URL.Query().Get("provider"))
	withModels := queryYes(r.URL.Query().Get("models"))
	out := []listedAccount{}
	for _, p := range provider.All() {
		if only != "" && !strings.EqualFold(only, p.ID) {
			continue
		}
		if !scope.provider(p.ID) {
			continue
		}
		on := accountsOnNow(p)
		keysOn := map[string]bool{}
		if p.Account == nil {
			for _, k := range p.KeyList() {
				keysOn[k.ID] = k.On
			}
		}
		var models []string
		if withModels {
			models = modelsListed(p, scope)
		}
		for _, a := range p.AccountIDs() {
			if scope != nil && !scope.who.AllowsAccount(p.ID, a.ID) {
				continue
			}
			l := listedAccount{Provider: p.ID, ProviderName: p.Name, ID: a.ID, Pin: p.ID + "/" + a.ID, Label: a.Shown(p), Plan: a.Plan}
			ref := a.User
			if a.Key {
				l.Kind, l.On, ref = "key", keysOn[a.ID] && !p.Off, a.ID
			} else {
				l.Kind, l.User, l.On = "account", a.User, on[strings.ToLower(a.User)] && !p.Off
			}
			if withModels {
				l.Models = []string{}
				for _, m := range models {
					if p.AccountServes(ref, m) {
						l.Models = append(l.Models, m)
					}
				}
			}
			out = append(out, l)
		}
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": out})
}

// queryYes says a query's yes: 1, true or yes.
func queryYes(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// modelsListed is the ids of every model p can be asked for (Available,
// read once a provider), less those on none of its APIs, and for a held
// key those the key may use.
func modelsListed(p provider.Provider, scope *quotaScope) []string {
	noAPI := p.NoAPI()
	var out []string
	for _, m := range p.Available() {
		if noAPI(m.ID) || scope != nil && !modelListed(scope.who, p, m.ID) {
			continue
		}
		out = append(out, m.ID)
	}
	return out
}

// accountsOnNow is the users of p's accounts requests may take
// (AccountsOn), lowercased.
func accountsOnNow(p provider.Provider) map[string]bool {
	on := map[string]bool{}
	if p.Account == nil {
		return on
	}
	for _, a := range p.AccountsOn() {
		if a.Account != nil {
			on[strings.ToLower(a.Account.User)] = true
		}
	}
	return on
}

// pinTarget is an account or key X-Magpie-Account names by its stable id
// (a listed account's pin, "<provider>/<id>", or its id alone): the
// provider, and the account's user or the key's fingerprint.
type pinTarget struct {
	provider, user, key string
}

// pinTargets are the accounts and keys want names by id among the
// providers of ws. Asked only for a pinned request: it reads logins.json.
func pinTargets(want string, ws []Weighed) []pinTarget {
	pid, id, scoped := strings.Cut(want, "/")
	if !scoped {
		pid, id = "", want
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	var out []pinTarget
	seen := map[string]bool{}
	for _, w := range ws {
		if seen[w.Provider] || pid != "" && !strings.EqualFold(pid, w.Provider) {
			continue
		}
		seen[w.Provider] = true
		p, err := provider.Find(w.Provider)
		if err != nil {
			continue
		}
		for _, a := range p.AccountIDs() {
			if !strings.EqualFold(a.ID, id) {
				continue
			}
			t := pinTarget{provider: p.ID}
			if a.Key {
				t.key = a.ID
			} else {
				t.user = a.User
			}
			out = append(out, t)
		}
	}
	return out
}

// pinnedBy says w, with c its candidate when there is one, is an account
// or key of ts.
func pinnedBy(ts []pinTarget, w Weighed, c *candidate) bool {
	for _, t := range ts {
		if !strings.EqualFold(t.provider, w.Provider) {
			continue
		}
		if t.user != "" && w.Kind == "account" && strings.EqualFold(t.user, w.Who) {
			return true
		}
		if t.key == "" {
			continue
		}
		// a key is told by its own fingerprint: a provider's one key on is
		// planned as the provider, whatever other keys it has off
		if c != nil && c.p.Account == nil && c.p.Key != "" {
			if provider.KeyID(c.p.Key) == t.key {
				return true
			}
			continue
		}
		if w.ID == w.Provider+"#"+t.key {
			return true
		}
	}
	return false
}
