package gui

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// The providers page: the vendors the user added, the presets they can add
// with one key, the gateway that fronts them, and who is routed where.

type modelJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`              // the user's name for it, if they gave one
	Default string   `json:"default,omitempty"` // its own name, when the user gave it another
	Kept    []string `json:"kept,omitempty"`    // the reasoning levels the user keeps of Efforts, when not all
	Efforts []string `json:"efforts,omitempty"`
	Given   bool     `json:"given,omitempty"` // its levels aren't known: Efforts are those it can be given, Kept those it was
	On      bool     `json:"on"`              // exposed to agents
	Context int      `json:"context,omitempty"`
	Max     int      `json:"max,omitempty"`  // the most its context may be set to, above Context
	Free    bool     `json:"free,omitempty"` // costs the subscription nothing
}

type providerJSON struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Icon      string            `json:"icon"`
	Preset    string            `json:"preset"`
	Host      string            `json:"host"`
	Chat      string            `json:"chat"`
	Responses string            `json:"responses"`
	Anthropic string            `json:"anthropic"`
	Decide    string            `json:"decide,omitempty"` // a decision API: it only routes groups
	Catalog   string            `json:"catalog"`
	Website   string            `json:"website"`
	KeysURL   string            `json:"keysUrl"`
	Headers   map[string]string `json:"headers,omitempty"`
	// where a custom provider's balance is asked (see provider.Balance)
	BalanceURL  string `json:"balanceURL,omitempty"`
	BalancePath string `json:"balancePath,omitempty"`
	ModelsURL   string `json:"modelsURL,omitempty"`
	// an account-wide balance token (provider.BalanceToken): whether the
	// vendor takes one, and whether one is saved; never the token itself
	BalanceToken struct {
		Takes bool `json:"takes"`
		Set   bool `json:"set"`
	} `json:"balanceToken"`
	Key struct {
		Set      bool   `json:"set"`
		Masked   string `json:"masked"`
		Optional bool   `json:"optional"`
	} `json:"key"`
	Ready     bool               `json:"ready"`
	Chosen    []string           `json:"chosen"`             // the user's explicit picks, if any
	Fallback  []string           `json:"fallback"`           // where requests go when this one can't take them
	Routing   string             `json:"routing"`            // how requests spread over its keys or accounts
	Affinity  string             `json:"affinity"`           // how long a conversation stays with who answered it
	Models    []modelJSON        `json:"models"`             // everything the vendor lists, exposed ones flagged
	Exposed   int                `json:"exposed"`            // how many reach the agents
	Draws     int                `json:"draws,omitempty"`    // how many of its models draw images (gateway.Drawers)
	DrawIDs   []string           `json:"drawIds,omitempty"`  // those models' ids, listed apart in its editor
	Unlisted  bool               `json:"unlisted"`           // its models serve only through routing groups
	Off       bool               `json:"off"`                // switched off: kept, but agents get none of its models
	Contexts  map[string]int     `json:"contexts,omitempty"` // the windows the user set, "*" for all its models
	Fetched   string             `json:"fetched"`            // "3h ago" when the list came from the vendor
	Agents    []providerAgent    `json:"agents"`             // detected agents, current ones flagged
	Sponsored bool               `json:"sponsored"`
	KeyList   []provider.KeyInfo `json:"keyList"`           // its keys, in the order requests try them
	Account   *accountJSON       `json:"account,omitempty"` // a signed-in agent, see provider.Account
}

type accountJSON struct {
	provider.Account
	Agent string `json:"agent"`     // the agent's id
	Name  string `json:"agentName"` // the agent's name, for "from Codex CLI's sign-in"
	Icon  string `json:"agentIcon"`
	// Logins are the agent's accounts magpie remembers, to switch between
	Logins []provider.Login `json:"logins,omitempty"`
}

type providerAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	Current bool   `json:"current"` // this agent is on one of this provider's models now
	Model   string `json:"model,omitempty"`
	Group   string `json:"group,omitempty"` // through this routing group, one of whose members it is
}

type presetJSON struct {
	provider.PresetDef
	Added bool `json:"added"`
}

type gatewayJSON struct {
	URL     string         `json:"url"`
	Running bool           `json:"running"`
	Mine    bool           `json:"mine"`   // this process serves it
	Window  bool           `json:"window"` // the magpie serving it shows its routing
	Models  int            `json:"models"`
	Calls   []gateway.Call `json:"calls"`
	Groups  []gwGroupJSON  `json:"groups"` // the catalog's routing groups, listed before the models
}

// gwGroupJSON is a routing group as the Gateway view lists it.
type gwGroupJSON struct {
	ID        string   `json:"id"` // group/<id>, what a request names
	Name      string   `json:"name"`
	Icons     []string `json:"icons"`     // its providers', one each
	Providers []string `json:"providers"` // their names, in the group's order
}

type excludedJSON struct {
	provider.Exclusion
	Name string `json:"agentName"`
	Icon string `json:"agentIcon"`
}

type providersJSON struct {
	Providers []providerJSON `json:"providers"`
	Presets   []presetJSON   `json:"presets"`
	Excluded  []excludedJSON `json:"excluded"` // sign-ins magpie found but will not share
	Gateway   gatewayJSON    `json:"gateway"`
	// CodexDaemon is the account Codex's background app-server is still
	// signed in to after Codex was switched to another; "" when none is
	// left behind (provider.CodexDaemonStale).
	CodexDaemon string `json:"codexDaemon,omitempty"`
}

// agentModel is the model an agent is on, as magpie's catalog names it.
func agentModel(a *agent.Agent) string {
	if len(a.Fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(a.Fields[0].Get(), "magpie/")
}

// agentUse is what an agent is on now: read once for the page, not again
// for each provider (reading it may ask the agent itself, as Alma's does).
type agentUse struct {
	*agent.Agent
	pid, model string
	group      provider.Group
	members    []provider.Member
	inGroup    bool
}

func agentUses(agents []*agent.Agent, findGroup func(string) (provider.Group, []provider.Member, bool)) []agentUse {
	out := make([]agentUse, 0, len(agents))
	for _, a := range agents {
		u := agentUse{Agent: a}
		v := agentModel(a)
		if strings.HasPrefix(v, provider.GroupPrefix) {
			u.group, u.members, u.inGroup = findGroup(v)
		} else if pid, model, ok := strings.Cut(v, "/"); ok {
			if _, err := provider.Find(pid); err == nil {
				u.pid, u.model = pid, model
			}
		}
		out = append(out, u)
	}
	return out
}

func providerInfo(p provider.Provider, agents []agentUse) providerJSON {
	out := providerJSON{
		ID: p.ID, Name: p.Name, Icon: p.Icon, Preset: p.Preset, Host: p.Host(),
		Chat: p.Chat, Responses: p.Responses, Anthropic: p.Anthropic, Decide: p.Decide,
		Catalog: p.Catalog, Website: p.Website, KeysURL: p.KeysURL,
		Headers: p.Headers, BalanceURL: p.BalanceURL, BalancePath: p.BalancePath, ModelsURL: p.ModelsURL,
		Ready: p.Ready(), Chosen: p.Models, Models: []modelJSON{}, Agents: []providerAgent{},
		Fallback: p.Fallback, Routing: p.Routing, Affinity: p.Affinity, Unlisted: p.Unlisted, Off: p.Off, Contexts: p.Contexts,
	}
	if out.Fallback == nil {
		out.Fallback = []string{}
	}
	if out.Chosen == nil {
		out.Chosen = []string{}
	}
	if pr := provider.Preset(p.Preset); pr != nil {
		out.Sponsored = pr.Sponsored
		out.Key.Optional = pr.NoKey
	}
	out.BalanceToken.Takes, out.BalanceToken.Set = provider.TakesBalanceToken(p), p.BalanceToken != ""
	out.Key.Set = p.Key != ""
	out.Key.Masked = provider.Mask(p.Key)
	out.KeyList = p.KeyList()
	if out.KeyList == nil {
		out.KeyList = []provider.KeyInfo{}
	}
	if !out.Key.Set && p.Ready() {
		out.Key.Optional = true
	}
	if a := p.Account; a != nil {
		out.Account = &accountJSON{Account: *a, Agent: a.Agent, Name: a.Agent, Icon: "generic"}
		if ag, err := agent.Find(a.Agent); err == nil {
			out.Account.Name, out.Account.Icon = ag.Name, ag.Icon
		} else if a.Agent == "cursor" {
			// a Cursor subscription is served by the gateway, not an agent magpie configures
			out.Account.Name, out.Account.Icon = "Cursor CLI", "cursor"
		} else if a.Agent == "kiro" {
			// Kiro's sign-in is magpie's own, kiro-cli's or the Kiro IDE's
			out.Account.Name, out.Account.Icon = "Kiro", "kiro-color"
		} else if a.Agent == "antigravity" {
			out.Account.Name, out.Account.Icon = "Antigravity", "antigravity-color"
		} else if a.Agent == provider.WorkBuddyAIID {
			// WorkBuddy AI, the international build, isn't an agent magpie configures
			out.Account.Name, out.Account.Icon = "WorkBuddy AI", "workbuddy-color"
		} else if a.Agent == provider.CommandCodePlanID {
			// Command Code's CLI keeps the key its sign-in made
			out.Account.Name, out.Account.Icon = "Command Code", "commandcode"
		}
		out.Account.Logins = provider.Logins(a.Agent)
	}
	exposed := map[string]bool{}
	for _, m := range p.Exposed() {
		exposed[m.ID] = true
	}
	seen := map[string]bool{}
	names, kept := p.ModelNames(), p.ModelEfforts()
	// a list fetched before magpie kept each model's most: the one Codex
	// CLI keeps says it
	var most []catalog.Model
	if p.Account != nil && p.Account.Agent == "codex" {
		most = catalog.Codex()
	}
	named := func(m catalog.Model, on bool) modelJSON {
		j := modelJSON{ID: m.ID, Name: m.Name, Efforts: provider.EffortsOf(m), On: on, Context: m.Context, Max: m.MaxContext, Free: m.Free}
		if i := slices.IndexFunc(most, func(c catalog.Model) bool { return c.ID == m.ID }); j.Max == 0 && i >= 0 {
			j.Max = most[i].MaxContext
		}
		if n, ok := names[m.ID]; ok {
			j.Default = cmp.Or(m.Name, m.ID)
			j.Name = n
		}
		if len(j.Efforts) == 0 {
			j.Efforts, j.Given = provider.Levels, true
		}
		if k, ok := kept[m.ID]; ok {
			j.Kept = slices.DeleteFunc(slices.Clone(j.Efforts), func(e string) bool { return !slices.Contains(k, e) })
		}
		return j
	}
	for _, m := range p.Available() {
		seen[m.ID] = true
		out.Models = append(out.Models, named(m, exposed[m.ID]))
	}
	// picks the vendor list does not know go first, so they are visible
	for _, m := range p.Exposed() {
		if !seen[m.ID] {
			out.Models = append([]modelJSON{named(m, true)}, out.Models...)
		}
	}
	out.Exposed = len(exposed)
	// its image models aren't among those agents chat with; the editor
	// lists them apart, as Settings → Images is where one is picked
	for _, m := range gateway.Drawers(p) {
		out.DrawIDs = append(out.DrawIDs, m.ID)
	}
	out.Draws = len(out.DrawIDs)
	if t, ok := p.Fetched(); ok {
		out.Fetched = ago(t)
	}
	for _, a := range agents {
		pa := providerAgent{ID: a.ID, Name: a.Name, Icon: a.Icon, Current: a.pid == p.ID, Model: a.model}
		if a.inGroup {
			for _, m := range a.members {
				if m.Provider.ID == p.ID {
					pa.Current, pa.Model, pa.Group = true, m.Model, a.group.Name
					break
				}
			}
		}
		out.Agents = append(out.Agents, pa)
	}
	return out
}

func providersState() providersJSON {
	agents := agent.Detected()
	s := providersJSON{Providers: []providerJSON{}, Presets: []presetJSON{}, Excluded: []excludedJSON{}}
	for _, x := range provider.Excluded() {
		e := excludedJSON{Exclusion: x, Name: x.Agent, Icon: "generic"}
		if a, err := agent.Find(x.Agent); err == nil {
			e.Name, e.Icon = a.Name, a.Icon
		}
		s.Excluded = append(s.Excluded, e)
	}
	findGroup := provider.GroupFinder()
	uses := agentUses(agents, findGroup)
	have := map[string]bool{}
	for _, p := range provider.All() {
		// a preset is added once any provider is its, whatever its id
		have[p.ID], have[p.Preset] = true, true
		s.Providers = append(s.Providers, providerInfo(p, uses))
	}
	for _, pr := range provider.Presets() {
		s.Presets = append(s.Presets, presetJSON{PresetDef: pr, Added: have[pr.ID]})
	}
	cat := provider.Catalog()
	s.Gateway = gatewayJSON{URL: gateway.URL(), Models: len(cat), Calls: []gateway.Call{}, Groups: []gwGroupJSON{}}
	for _, e := range cat {
		if e.Group == "" {
			continue
		}
		g := gwGroupJSON{ID: e.ID, Name: e.Name, Icons: e.Icons}
		if _, ms, ok := findGroup(e.ID); ok {
			for _, m := range ms {
				if !slices.Contains(g.Providers, m.Provider.Name) {
					g.Providers = append(g.Providers, m.Provider.Name)
				}
			}
		}
		s.Gateway.Groups = append(s.Gateway.Groups, g)
	}
	if gw := served.Load(); gw != nil {
		s.Gateway.Running, s.Gateway.Mine, s.Gateway.Window = true, true, true
		s.Gateway.Calls = gw.Recent()
	} else {
		s.Gateway.Running, s.Gateway.Window = gateway.Serving()
	}
	s.CodexDaemon = provider.CodexDaemonStale()
	return s
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Round(time.Minute).Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Round(time.Hour).Hours()))
	default:
		return t.Format("Jan 2")
	}
}

func providerRoutes(mux *http.ServeMux, w Windows) {
	importAppsRoutes(mux)
	traceRoutes(mux)
	groupRoutes(mux)
	mux.HandleFunc("GET /api/providers", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, providersState())
	})
	// a picture for a provider, picked in the editor: kept by content before
	// the provider is saved, which then points at it. The page sends it as
	// base64 in JSON — the app's web view hands a scheme handler no body for
	// a File or Blob, so a picture posted as it is arrived empty — and the
	// bytes as they are are still taken.
	mux.HandleFunc("POST /api/icons", func(rw http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 2*provider.MaxIcon))
		if err != nil {
			fail(rw, err)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var in struct{ Data string }
			if err := json.Unmarshal(b, &in); err != nil {
				fail(rw, err)
				return
			}
			if b, err = base64.StdEncoding.DecodeString(in.Data); err != nil {
				fail(rw, err)
				return
			}
		}
		icon, err := provider.StoreIcon(b)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon})
	})
	// the site's own icon, found from the provider's base URL (#12)
	mux.HandleFunc("POST /api/icons/favicon", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		icon, err := provider.FaviconFor(r.Context(), in.URL)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon})
	})
	mux.HandleFunc("GET /api/icons/{name}", func(rw http.ResponseWriter, r *http.Request) {
		f := provider.IconFile(r.PathValue("name"))
		if f == "" {
			http.NotFound(rw, r)
			return
		}
		// an SVG is only ever drawn as an image, never run
		rw.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		rw.Header().Set("Cache-Control", "max-age=31536000, immutable")
		http.ServeFile(rw, r, f)
	})
	mux.HandleFunc("POST /api/provider/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			provider.Provider
			// New is set by the editor's Add: the provider is one more, never
			// one replacing the provider that has its id or name
			New bool `json:"new"`
			// ClearBalanceToken drops the saved balance token, which a
			// blank one in the form otherwise keeps
			ClearBalanceToken bool `json:"clearBalanceToken"`
			// From is the id the provider had: another is a rename
			From string `json:"from"`
			// Model and ModelName, for name: the name the user gives one
			// of its models, "" for its own again
			Model     string `json:"model"`
			ModelName string `json:"modelName"`
			// Efforts, for efforts: the reasoning levels it offers, none
			// for all it has
			Efforts []string `json:"efforts"`
			// Test, for test: models to send a request each, in place of
			// one per endpoint
			Test []string `json:"test"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(rw, err)
			return
		}
		in := req.Provider
		switch r.PathValue("action") {
		case "show":
			// a signed-in account the user removed, back with its picks
			if err := provider.ShowAccount(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "quiet":
			// a removed account's "Add it back" line, dismissed (#116)
			if err := provider.QuietAccount(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "save":
			// a preset needs nothing but the key; a saved provider keeps
			// its key when the form left it blank
			if pr, err := provider.FromPreset(in.Preset); err == nil && in.Chat == "" && in.Responses == "" && in.Anthropic == "" {
				pr.Key, pr.Models, pr.Fallback, pr.Headers, pr.BalanceToken, pr.Contexts = in.Key, in.Models, in.Fallback, in.Headers, in.BalanceToken, in.Contexts
				if in.Name != "" {
					pr.Name = in.Name
				}
				if in.ID != "" {
					pr.ID = in.ID
				}
				// a decision API's address the user gave, such as the
				// Cloudflare one naming the account
				if d := strings.TrimSpace(in.Decide); d != "" && pr.Decide != "" {
					pr.Decide = d
				}
				in = pr
			}
			var old *provider.Provider
			if req.New {
				// a second one of a preset, or a name already in use, is
				// added beside the first under the next free id
				id, err := provider.Add(in)
				if err != nil {
					fail(rw, err)
					return
				}
				in.ID = id
			} else {
				// a rename saves the rest under the id it had, then moves it
				to := strings.ToLower(strings.TrimSpace(in.ID))
				rename := req.From != "" && req.From != to
				if rename {
					if to == "" || to != provider.Slug(to) {
						fail(rw, fmt.Errorf("a provider's id must be lowercase letters, digits and dashes, not %q", in.ID))
						return
					}
					in.ID = req.From
				}
				old, _ = provider.Find(in.ID)
				if in.Key == "" && old != nil {
					in.Key = old.Key
				}
				if in.BalanceToken == "" && old != nil && !req.ClearBalanceToken {
					in.BalanceToken = old.BalanceToken
				}
				if old != nil {
					// the other keys are kept apart, in the Accounts list
					in.Keys = old.Keys
					in.Routing = old.Routing // set on its own, with route
					in.Off = old.Off         // and this with off and on
					if in.Contexts == nil {
						in.Contexts = old.Contexts // a save that doesn't say
					}
					if in.Key == old.Key {
						in.KeyName, in.KeyProtocol = old.KeyName, old.KeyProtocol
					}
				}
				if in.Icon == "" && old != nil && in.Preset == "" {
					in.Icon = old.Icon
				}
				if err := provider.Save(in); err != nil {
					fail(rw, err)
					return
				}
				if rename {
					if _, err := agent.RenameProvider(in.ID, to); err != nil {
						fail(rw, err)
						return
					}
					in.ID = to
				}
			}
			provider.ForgetBalances()
			// a new key means a new vendor list is worth a try; keep it short
			if p, err := provider.Find(in.ID); err == nil && p.Ready() && (old == nil || old.Key != p.Key) {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				p.Fetch(ctx)
				cancel()
			}
		case "key":
			// the saved key, for the editor's Show button; it never
			// leaves this machine (the panel is served on loopback)
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]string{"key": p.Key})
			return
		case "name":
			// the agents' own model lists follow, through catalog.Changed
			if err := provider.SetModelName(in.ID+"/"+req.Model, req.ModelName); err != nil {
				fail(rw, err)
				return
			}
		case "efforts":
			if err := provider.SetModelEfforts(in.ID+"/"+req.Model, req.Efforts); err != nil {
				fail(rw, err)
				return
			}
		case "route":
			if err := provider.SetRouting(in.ID, in.Routing); err != nil {
				fail(rw, err)
				return
			}
		case "off", "on":
			// switched off, it stays with its keys, but agents are given
			// none of its models; the files they keep them in follow,
			// through catalog.Changed
			if err := provider.SetOff(in.ID, r.PathValue("action") == "off"); err != nil {
				fail(rw, err)
				return
			}
			provider.ForgetBalances()
		case "affinity":
			if err := provider.SetAffinity(in.ID, in.Affinity); err != nil {
				fail(rw, err)
				return
			}
		case "delete":
			if err := provider.Delete(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "test":
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			if len(req.Test) > 0 {
				// an image model's test draws a picture, which takes longer
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
				defer cancel()
				writeJSON(rw, map[string]any{"results": p.TestModels(ctx, req.Test)})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			writeJSON(rw, struct {
				Results  []provider.Result `json:"results"`
				Provider providerJSON      `json:"provider"`
			}{p.Test(ctx), providerInfo(*p, agentUses(agent.Detected(), provider.GroupFinder()))})
			return
		case "unfetch":
			// the vendor's list, forgotten until the next Refresh
			if err := catalog.SaveLive(in.ID, "", nil); err != nil {
				fail(rw, err)
				return
			}
		case "models":
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			var ms []catalog.Model
			if ms, err = p.Fetch(ctx); err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, struct {
				Count    int          `json:"count"`
				Provider providerJSON `json:"provider"`
			}{len(ms), providerInfo(*p, agentUses(agent.Detected(), provider.GroupFinder()))})
			return
		default:
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, providersState())
	})
	// How much of its allowance each of an agent's accounts has used.
	mux.HandleFunc("GET /api/login/usage", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		writeJSON(rw, provider.LoginUsage(ctx, r.URL.Query().Get("agent")))
	})
	// Switching the account an agent is signed in to, among those magpie
	// remembers, and forgetting one.
	mux.HandleFunc("POST /api/login/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, User string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		switch r.PathValue("action") {
		case "switch":
			err = provider.SwitchLogin(in.Agent, in.User)
		case "forget":
			err = provider.ForgetLogin(in.Agent, in.User)
		case "on", "off":
			err = provider.SetLoginOn(in.Agent, in.User, r.PathValue("action") == "on")
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		// an agent on its own models goes through magpie while more of
		// its accounts are on, and straight to its vendor again once not
		agent.SyncCatalog()
		writeJSON(rw, providersState())
	})
	// Codex's background app-server, left on the account before a switch:
	// restarting it (which ends the Codex sessions on it), or letting it be.
	mux.HandleFunc("POST /api/codex/daemon/{action}", func(rw http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "restart":
			ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
			defer cancel()
			if err := provider.RestartCodexDaemon(ctx); err != nil {
				fail(rw, err)
				return
			}
		case "dismiss":
			provider.DismissCodexDaemon()
		default:
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, providersState())
	})
	// A provider's several keys: add one, put one in use, name or remove it.
	mux.HandleFunc("POST /api/keys/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			ID, Key, Name, Ref string
			Protocol           provider.Protocol
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		switch r.PathValue("action") {
		case "add":
			err = provider.AddKey(in.ID, in.Name, in.Key, in.Protocol)
		case "protocol":
			err = provider.SetKeyProtocol(in.ID, in.Ref, in.Protocol)
		case "use":
			err = provider.UseKey(in.ID, in.Ref)
		case "remove":
			err = provider.RemoveKey(in.ID, in.Ref)
		case "rename":
			err = provider.RenameKey(in.ID, in.Ref, in.Name)
		case "on", "off":
			err = provider.SetKeyOn(in.ID, in.Ref, r.PathValue("action") == "on")
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		// a key that now takes requests has its models asked for: which
		// models a key sees is known only from its own list, and an off
		// key's isn't asked (#76), so until then a relay that hands out a
		// key per group would send the key nothing, or everything
		if a := r.PathValue("action"); a == "add" || a == "on" {
			if p, err := provider.Find(in.ID); err == nil && p.Ready() && len(p.KeysOn()) > 1 {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				p.Fetch(ctx)
				cancel()
			}
		}
		writeJSON(rw, providersState())
	})
	// Adding a subscription: magpie opens the vendor's sign-in in the
	// browser and the window follows it until the account is in.
	mux.HandleFunc("POST /api/signin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		st, err := provider.StartSignIn(in.Agent)
		if err != nil {
			fail(rw, err)
			return
		}
		if st.URL != "" {
			// one that installs a CLI first has no URL yet: the window
			// opens it once there is one
			w.OpenURL(st.URL)
		}
		writeJSON(rw, st)
	})
	mux.HandleFunc("GET /api/signin/{id}", func(rw http.ResponseWriter, r *http.Request) {
		st, ok := provider.SignInStatus(r.PathValue("id"))
		if !ok {
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, st)
	})
	mux.HandleFunc("POST /api/signin/{id}/cancel", func(rw http.ResponseWriter, r *http.Request) {
		provider.CancelSignIn(r.PathValue("id"))
		rw.WriteHeader(http.StatusNoContent)
	})
	// Accounts brought in from another tool's export (Antigravity's, from
	// Antigravity Cockpit, Antigravity Manager, CLIProxyAPI), each file's
	// text as it is; each checked with the vendor before it is kept.
	mux.HandleFunc("POST /api/signin/import", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string
			Files []string
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		res, err := provider.ImportGoogleAccounts(ctx, in.Agent, in.Files)
		if err != nil {
			fail(rw, err)
			return
		}
		agent.SyncCatalog()
		writeJSON(rw, struct {
			Results   []provider.ImportedAccount `json:"results"`
			Providers providersJSON              `json:"providers"`
		}{res, providersState()})
	})
	// the page copies through here first: in the app's window the
	// clipboard API is refused or missing, depending on the system
	mux.HandleFunc("POST /api/copy", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Text == "" || !w.Copy(in.Text) {
			http.Error(rw, "", http.StatusNotImplemented)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/open", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.HasPrefix(in.URL, "https://") || strings.HasPrefix(in.URL, "http://") || strings.HasPrefix(in.URL, agent.CindyScheme) {
			w.OpenURL(in.URL)
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}
