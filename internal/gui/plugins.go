package gui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/ghtoken"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// OpenCode's provider plugins (internal/plugin): the providers they sign
// in to are subscriptions in the add sheet, and Settings → Plugins adds,
// updates and removes them.

// pluginSubJSON is a plugin's provider as the add sheet lists it.
type pluginSubJSON struct {
	ID       string          `json:"id"`  // magpie's
	PID      string          `json:"pid"` // OpenCode's, which the plugin knows it by
	Name     string          `json:"name"`
	Icon     string          `json:"icon"`
	Spec     string          `json:"spec"` // the plugin
	Methods  []plugin.Method `json:"methods"`
	SignedIn bool            `json:"signedIn"`
	Single   bool            `json:"single,omitempty"`
	Models   int             `json:"models"`
	// Options are the switches the plugin declares (auth.options, #2038),
	// each with where it stands now
	Options []pluginOptionJSON `json:"options,omitempty"`
}

// pluginOptionJSON is a plugin's option with the user's value, else its
// default.
type pluginOptionJSON struct {
	plugin.Option
	On bool `json:"on"`
}

// pluginOptionKey is the key an option may have, as the host checks it.
var pluginOptionKey = lazyre.New(`^[a-z0-9_-]{1,40}$`)

// pluginIcon is the vendor's icon: the one the plugin gives its provider,
// else the one the plugin market gives the plugin or its provider, else
// that of the providers OpenCode names, a plain one for the rest.
func pluginIcon(pp plugin.Provider) string {
	if ic := provider.PluginIcon(pp); ic != "" {
		return ic
	}
	switch pp.ID {
	case "github-copilot", "github-copilot-enterprise":
		return "githubcopilot"
	case "anthropic":
		return "claude-color"
	case "openai":
		return "openai"
	case "google", "google-vertex":
		return "gemini-color"
	case "qwen", "alibaba":
		return "qwen-color"
	case "moonshotai", "kimi-for-coding":
		return "kimi"
	}
	return "generic"
}

func pluginSubs() []pluginSubJSON {
	out := []pluginSubJSON{}
	chosen := settings.Load().PluginOptions
	for _, pp := range plugin.Cached() {
		var opts []pluginOptionJSON
		for _, o := range pp.Options {
			opts = append(opts, pluginOptionJSON{o, plugin.OptionOn(chosen, pp.ID, o)})
		}
		out = append(out, pluginSubJSON{
			ID: provider.PluginID(pp.ID), PID: pp.ID, Name: pp.Name, Icon: pluginIcon(pp), Spec: pp.Spec,
			Methods: pp.Methods, SignedIn: pp.SignedIn, Single: pp.Single, Models: len(pp.Models), Options: opts,
		})
	}
	return out
}

// pluginEntryJSON is a plugin as Settings → Plugins lists it.
type pluginEntryJSON struct {
	plugin.Entry
	Error     string   `json:"error,omitempty"`   // why it didn't load
	Providers []string `json:"providers"`         // the names of those it signs in to
	Version   string   `json:"version,omitempty"` // installed
	Latest    string   `json:"latest,omitempty"`  // on npm, when the market asked
	// Icon is the picture its own package.json gives (magpie.icon), as
	// the page can show it: the plugin's, which its row shows before any
	// of its providers' own (#1775)
	Icon string `json:"icon,omitempty"`
	// Package is the package installed from a git repository, which its
	// own package.json names
	Package string `json:"package,omitempty"`
	// OwnName is the name its package.json gives itself (magpie.name),
	// which its row shows when the community's list doesn't name it
	OwnName string `json:"ownName,omitempty"`
	// Commit is the commit of its repository a git one was installed at,
	// and Head the commit the repository was at when last asked (asking
	// it again is /api/plugins/git's): an update is out only when they
	// differ, whatever the version in its package.json says
	Commit string `json:"commit,omitempty"`
	Head   string `json:"head,omitempty"`
	// Moved are the built-in subscriptions moved onto it, which stop
	// with it when it is removed or turned off (no built-in to go back to)
	Moved []string `json:"moved"`
	// Serves are the names of the other providers it registers, which
	// the row's "Serves" chip names after Moved's (#3902): only when the
	// two come to two or more, as one provider alone is the row's title
	Serves []string `json:"serves,omitempty"`
	// AutoUpdated is the update magpie made to it by itself lately
	AutoUpdated *plugin.Updated `json:"autoUpdated,omitempty"`
	// Middleware is the gateway middleware it has, run in the gateway
	// rather than the host: its hooks, how often they ran and how long
	// they took, and why it didn't load
	Middleware *middleware.State `json:"middleware,omitempty"`
	// Named is whether Providers is the plugins' own answer, rather than an
	// empty list kept from before they were last asked: only then can the
	// row say a plugin signs in to nothing (#1112)
	Named bool `json:"named,omitempty"`
	// OptionsExample is what its package suggests for its options, which
	// the options editor starts from when none are set
	OptionsExample map[string]any       `json:"optionsExample,omitempty"`
	Gateway        *plugin.GatewayState `json:"gateway,omitempty"`
	IsGateway      bool                 `json:"isGateway,omitzero"`
	GatewayOnly    bool                 `json:"gatewayOnly,omitzero"`
	// IsMiddleware is whether its package has gateway middleware, said of
	// one switched off too, which Middleware leaves out; MiddlewareOnly is
	// whether that is all it has, no provider to sign in to
	IsMiddleware   bool `json:"isMiddleware,omitempty"`
	MiddlewareOnly bool `json:"middlewareOnly,omitempty"`
	// Agent is the agent it adds (internal/agentplug), or why it isn't
	// listed; IsAgent is whether its package has one, said of one switched
	// off too; InMagpieOnly is whether all it has runs in magpie itself,
	// middleware and an agent, no provider to sign in to
	Agent        *pluginAgentJSON `json:"agent,omitempty"`
	IsAgent      bool             `json:"isAgent,omitempty"`
	InMagpieOnly bool             `json:"inMagpieOnly,omitempty"`
	// Page is the page it adds to the window (magpie.page), said of one
	// switched off too: what the row shows before it is turned on, with
	// what the page may read of magpie's
	Page *pluginPageInfoJSON `json:"page,omitempty"`
	// Panels are the panels it adds to magpie's own pages
	// (magpie.panels), said of one switched off too, and PanelReads what
	// they may read of magpie's
	Panels     []pluginPanelInfoJSON `json:"panels,omitempty"`
	PanelReads []string              `json:"panelReads,omitempty"`
	// Actions are the items its page adds to magpie's menus
	// (magpie.actions), said of one switched off too
	Actions []pluginActionInfoJSON `json:"actions,omitempty"`
	// Clashes are the providers it signs in to that another plugin signs
	// in to as well (a plugin of the user's own beside a third party's):
	// the host runs one plugin for each, and the row says which, with a
	// way to pick this one
	Clashes []pluginClashJSON `json:"clashes,omitempty"`
	// Tools are the tools it gives, which magpie's plugin tools MCP server
	// offers every agent under MCP's name: with their descriptions while
	// the host runs, by name alone from what it noted last when it doesn't
	Tools []pluginToolJSON `json:"tools,omitempty"`
	// ToolsServer is the Library's server of those tools, whose agents
	// the row turns them off and on for (chengming0v0 on Discord)
	ToolsServer *library.PluginServer `json:"toolsServer,omitempty"`
}

// pluginToolJSON is one tool a plugin gives, as its row lists it.
type pluginToolJSON struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MCP         string `json:"mcp"` // its name on the MCP server
}

// pluginPageInfoJSON is a plugin's page as its row says it.
type pluginPageInfoJSON struct {
	Title    string   `json:"title,omitempty"`
	Replaces string   `json:"replaces,omitempty"` // the built-in page it takes the place of
	Reads    []string `json:"reads"`
}

// pluginPanelInfoJSON is a panel a plugin adds, as its row says it.
type pluginPanelInfoJSON struct {
	Slot  string `json:"slot"`
	Title string `json:"title,omitempty"`
}

// pluginActionInfoJSON is a menu item a plugin's page adds, as its row
// says it.
type pluginActionInfoJSON struct {
	Menu  string `json:"menu"`
	Title string `json:"title,omitempty"`
}

// pluginClashJSON is a provider two plugins sign in to, as a row says it.
type pluginClashJSON struct {
	plugin.Clash
	Name string `json:"name"` // the provider's, as the plugin serving it names it
}

// autoUpdatedFor is how long a plugin's row says magpie updated it.
const autoUpdatedFor = 3 * 24 * time.Hour

type pluginsJSON struct {
	Plugins []pluginEntryJSON `json:"plugins"`
	Bun     bool              `json:"bun"` // Bun is here; adding the first plugin downloads it otherwise
	BunVer  string            `json:"bunVersion"`
	Error   string            `json:"error,omitempty"` // the plugins couldn't be asked
	// Picker is whether this magpie can show the system's folder picker:
	// `magpie web` has none, and the page then offers no button for it
	Picker bool `json:"picker"`
	// Movable are the built-ins with accounts a plugin could run, which
	// its card and its row offer to move
	Movable []provider.MoveCandidate `json:"movable"`
	// Mirror is the 「国内镜像」 switch: the list, npm and Bun asked of
	// mirrors in China first (settings.ChinaMirror)
	Mirror bool `json:"mirror"`
	// Registry is the npm registry plugins are installed from and npm's
	// answers asked of, and where it came from (plugin.Registry)
	Registry plugin.RegistryInUse `json:"registry"`
}

// registryJSON is plugin.Registry with its file's path as the page shows it.
func registryJSON() plugin.RegistryInUse {
	r := plugin.Registry()
	if r.Path != "" {
		r.Path = tilde(r.Path)
	}
	return r
}

func pluginsState(ctx context.Context, w Windows) pluginsJSON {
	s := pluginsJSON{Plugins: []pluginEntryJSON{}, Bun: plugin.HasBun(), BunVer: plugin.BunInUse(), Movable: provider.MoveCandidates(), Picker: w != nil && !isWeb(w), Mirror: settings.Load().ChinaMirror, Registry: registryJSON()}
	l := plugin.Load()
	errs := map[string]string{}
	names := map[string][]string{}
	clashes := map[string][]plugin.Clash{}
	idName := map[string]string{}
	var tools []plugin.PluginTools
	asked := false
	if len(l.Plugins) > 0 && (plugin.Running() || plugin.HasBun()) {
		loaded, err := plugin.Plugins(ctx)
		if err != nil {
			s.Error = err.Error()
		}
		for _, p := range loaded {
			errs[p.Spec] = p.Error
			if p.Error == "" && len(p.Tools) > 0 {
				tools = append(tools, plugin.PluginTools{Spec: p.Spec, Tools: p.Tools})
			}
		}
		clashes = plugin.Clashes(loaded)
		asked = err == nil
	}
	if !asked {
		for _, e := range l.Plugins {
			var ts []plugin.Tool
			for _, n := range plugin.ToolNames()[e.Spec] {
				ts = append(ts, plugin.Tool{Name: n})
			}
			if len(ts) > 0 && !e.Off {
				tools = append(tools, plugin.PluginTools{Spec: e.Spec, Tools: ts})
			}
		}
	}
	toolNames := gateway.ToolNames(tools)
	var servers map[string]library.PluginServer
	if len(tools) > 0 {
		servers = library.PluginServers()
	}
	// each plugin's subscriptions, by the names it gave last (plugin.Cached,
	// which holds them without starting the host or asking anyone, and asks
	// the plugins again in the background when a sign-in or the plugins
	// changed since). Asking here — plugin.Providers — walks the plugins one
	// at a time and has each fetch its vendor's model list over the network,
	// which is the whole of the providers RPC; the page here uses none of
	// those models, only the names, so waiting on them held the Installed
	// list for one vendor after another every time it was opened.
	provs := map[string][]plugin.Provider{}
	for _, p := range plugin.Cached() {
		names[p.Spec] = append(names[p.Spec], p.Name)
		provs[p.Spec] = append(provs[p.Spec], p)
		idName[p.ID] = p.Name
	}
	// whether that is the plugins' own answer or what was kept from before
	// they were last asked: an empty list means "none" only in the first case
	answered := plugin.Answered()
	// npm's newest, as it said last: asking it again is /api/plugins/npm's
	known := plugin.InfoCached(npmNames(l.Plugins))
	mws := middleware.States()
	gws := plugin.GatewayStates(ctx)
	var agentErrs map[string]string
	for _, e := range l.Plugins {
		if f, _ := plugin.Agent(plugin.Target(e.Spec)); f != "" && !e.Off {
			agentErrs = agent.PluginErrors()
			break
		}
	}
	for _, e := range l.Plugins {
		j := pluginEntryJSON{Entry: e, Error: errs[e.Spec], Providers: names[e.Spec], Version: plugin.Installed(e.Spec), Named: answered, OwnName: plugin.InstalledName(e.Spec)}
		if plugin.Gateway(plugin.Target(e.Spec)) != "" {
			j.IsGateway = true
			j.GatewayOnly = plugin.GatewayOnly(plugin.Target(e.Spec))
			if !e.Off {
				state := plugin.GatewayState{LastOutcome: "idle"}
				if found, ok := gws[e.Spec]; ok {
					state = found
				}
				j.Gateway = &state
			}
		}
		if npmPlugin(e.Spec) {
			j.Latest = known[plugin.Name(e.Spec)].Version
		}
		if j.Providers == nil {
			j.Providers = []string{}
		}
		if plugin.IsGit(e.Spec) {
			j.Package = plugin.Name(e.Spec)
			j.Commit, j.Head = plugin.ShortCommit(plugin.GitCommit(e.Spec)), plugin.ShortCommit(plugin.GitHeadCached(e.Spec))
		}
		if u, ok := plugin.LastUpdated(plugin.Name(e.Spec), time.Now().Add(-autoUpdatedFor)); ok && !plugin.IsPath(e.Spec) {
			j.AutoUpdated = &u
		}
		if m, ok := mws[e.Spec]; ok {
			j.Middleware = &m
		}
		if file, only := plugin.Middleware(plugin.Target(e.Spec)); file != "" {
			j.IsMiddleware, j.MiddlewareOnly = true, only
		}
		// every plugin's, a provider plugin's too (Devin's keepalive,
		// plugins#592): its row offers the same options editor. Not only
		// one whose providers are known, as those are named in the
		// background and the button would come and go with them
		j.OptionsExample = plugin.OptionsExample(plugin.Target(e.Spec))
		if file, _ := plugin.Agent(plugin.Target(e.Spec)); file != "" {
			j.IsAgent = true
			j.InMagpieOnly = plugin.InMagpieOnly(plugin.Target(e.Spec))
			if !e.Off {
				j.Agent = pluginAgentOf(e.Spec, agentErrs)
			}
		}
		if w, _ := plugin.Window(plugin.Target(e.Spec)); w != nil {
			reads := w.Reads
			if reads == nil {
				reads = []string{}
			}
			if w.Page != nil {
				j.Page = &pluginPageInfoJSON{Title: w.Page.Title, Replaces: w.Page.Replaces, Reads: reads}
			}
			for _, p := range w.Panels {
				j.Panels = append(j.Panels, pluginPanelInfoJSON{Slot: p.Slot, Title: p.Title})
			}
			if len(w.Panels) > 0 {
				j.PanelReads = reads
			}
			for _, a := range w.Actions {
				j.Actions = append(j.Actions, pluginActionInfoJSON{Menu: a.Menu, Title: a.Title})
			}
			j.InMagpieOnly = plugin.InMagpieOnly(plugin.Target(e.Spec))
		}
		for _, c := range clashes[e.Spec] {
			n := idName[c.ID]
			if n == "" {
				n = c.ID
			}
			j.Clashes = append(j.Clashes, pluginClashJSON{Clash: c, Name: n})
		}
		for _, pt := range tools {
			if pt.Spec != e.Spec || e.Off {
				continue
			}
			for _, x := range pt.Tools {
				j.Tools = append(j.Tools, pluginToolJSON{Name: x.Name, Description: x.Description, MCP: toolNames[e.Spec][x.Name]})
			}
			if sv, ok := servers[plugin.Name(e.Spec)]; ok && len(j.Tools) > 0 {
				j.ToolsServer = &sv
			}
		}
		j.Moved = provider.MovedOnto(e.Spec)
		if j.Moved == nil {
			j.Moved = []string{}
		}
		j.Serves = servesBeside(j.Moved, provs[e.Spec])
		s.Plugins = append(s.Plugins, j)
	}
	// each plugin's own picture, kept and checked as a listed one's; at
	// once: one still being fetched shows on the next ask
	said := make([]string, len(s.Plugins))
	for i, j := range s.Plugins {
		said[i] = plugin.InstalledIcon(j.Spec)
	}
	for i, ic := range provider.RepoIcons(said, 0) {
		s.Plugins[i].Icon = ic
	}
	return s
}

// servesBeside is what a plugin's "Serves" chip names besides the
// built-ins moved onto it: the providers it registers, each once, but for
// the moved ones (a moved built-in's id is the plugin's provider id, and
// the page names it as the built-in). Nothing when none is moved and it
// registers fewer than two: a lone provider is what the row's title
// already says (#3902).
func servesBeside(moved []string, ps []plugin.Provider) []string {
	var out []string
	for _, p := range ps {
		n := strings.TrimSpace(p.Name)
		if n == "" {
			n = p.ID
		}
		if slices.Contains(moved, p.ID) || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, n) }) {
			continue
		}
		out = append(out, n)
	}
	if len(moved) == 0 && len(out) < 2 {
		return nil
	}
	return out
}

// npmPlugin is whether npm has the plugin spec's versions: a plugin from a
// git repository is that repository's, whatever npm has under its name,
// and one from a folder is the folder's.
func npmPlugin(spec string) bool { return !plugin.IsPath(spec) && !plugin.IsGit(spec) }

func npmNames(es []plugin.Entry) []string {
	out := []string{}
	for _, e := range es {
		if npmPlugin(e.Spec) {
			out = append(out, plugin.Name(e.Spec))
		}
	}
	return out
}

// pluginListings are the plugins magpie suggests, each with what npm said
// of it last (asking npm again is /api/plugins/npm's), so Discover is drawn
// without waiting on npm.
func pluginListings(ctx context.Context) []pluginListingJSON {
	ls := plugin.Market(ctx)
	names := make([]string, 0, len(ls))
	for _, l := range ls {
		names = append(names, l.Package)
	}
	known := plugin.InfoCached(names)
	out := make([]pluginListingJSON, 0, len(ls))
	for _, l := range ls {
		j := pluginListingJSON{Listing: l}
		if n, ok := known[l.Package]; ok {
			j.NPM = &n
		}
		out = append(out, j)
	}
	// at once: a picture still being fetched shows on the next ask
	listingIcons(out, 0)
	return out
}

// pluginAgents are the suggested agent plugins not installed, by name.
// One installed and turned off is installed: Plugins turns it on.
func pluginAgents(ctx context.Context) []string {
	have := map[string]bool{}
	for _, e := range plugin.Load().Plugins {
		have[plugin.Name(e.Spec)] = true
	}
	out := []string{}
	// the community's alone, as Discover's Agents: an author's own is
	// found by a search
	for _, l := range plugin.Community(plugin.Market(ctx)) {
		if l.Kind == "agent" && !have[l.Package] {
			out = append(out, l.Name)
		}
	}
	return out
}

// listingIcons puts each listing's pictures as the page can show them:
// the one registry.json gives (Kilo Code's https://github.com/Kilo-Org.png)
// and the one its package.json gives, both kept as a GitHub-tagged
// plugin's is, in one wait. A bundled icon's name is left as it is; a
// picture not kept by wait is left out, so the card shows npm's picture
// or its kind's glyph until the next ask.
func listingIcons(ls []pluginListingJSON, wait time.Duration) {
	n := len(ls)
	said := make([]string, 2*n)
	for i, l := range ls {
		if provider.MarketPicture(l.Icon) {
			said[i] = l.Icon
		}
		if l.NPM != nil {
			said[n+i] = l.NPM.Icon
		}
	}
	got := provider.RepoIcons(said, wait)
	for i := range ls {
		if said[i] != "" {
			ls[i].Icon = got[i]
		}
		if ls[i].NPM != nil {
			ls[i].NPM.Icon = got[n+i]
		}
	}
}

// npmIcons puts the picture each package gives (magpie.icon) as the page
// can show it, kept here as a GitHub-tagged plugin's is: a data URI or an
// https URL isn't sent on to the page. One not kept by wait is left out.
func npmIcons(ns []*plugin.NPM, wait time.Duration) {
	said := make([]string, len(ns))
	for i, n := range ns {
		if n != nil {
			said[i] = n.Icon
		}
	}
	for i, ic := range provider.RepoIcons(said, wait) {
		if ns[i] != nil {
			ns[i].Icon = ic
		}
	}
}

// pluginMarketJSON is the plugin market: the plugins magpie suggests, what npm
// says of each, and those added.
type pluginMarketJSON struct {
	Listings []pluginListingJSON `json:"listings"`
	State    pluginsJSON         `json:"state"`
}

type pluginListingJSON struct {
	plugin.Listing
	NPM *plugin.NPM `json:"npm,omitempty"` // none while npm hasn't been asked
}

func pluginMarketState(ctx context.Context, w Windows) pluginMarketJSON {
	var ls []plugin.Listing
	var st pluginsJSON
	done := make(chan struct{})
	go func() { st = pluginsState(ctx, w); close(done) }()
	ls = plugin.Market(ctx)
	names := []string{}
	for _, l := range ls {
		names = append(names, l.Package)
	}
	<-done
	for _, e := range st.Plugins {
		if npmPlugin(e.Spec) {
			names = append(names, plugin.Name(e.Spec))
		}
	}
	info := plugin.Info(ctx, names)
	m := pluginMarketJSON{Listings: []pluginListingJSON{}, State: st}
	for _, l := range ls {
		j := pluginListingJSON{Listing: l}
		// npm not answered for (or the dist branch unread) is unknown:
		// no npm, not an empty one that reads as Coming soon
		if n, ok := info[l.Package]; ok {
			j.NPM = &n
		}
		m.Listings = append(m.Listings, j)
	}
	listingIcons(m.Listings, 3*time.Second)
	for i, e := range m.State.Plugins {
		if !plugin.IsGit(e.Spec) {
			m.State.Plugins[i].Latest = info[plugin.Name(e.Spec)].Version
		}
	}
	return m
}

// addPlugin, removePlugin, updatePlugins and upgradePlugin are what
// /api/plugins/{op} runs, for tests to stand in for.
var (
	addPlugin     = provider.AddPlugin
	removePlugin  = provider.RemovePlugin
	updatePlugins = plugin.Update
	upgradePlugin = plugin.Upgrade
)

func pluginRoutes(mux *http.ServeMux, w Windows) {
	pluginCountRoutes(mux)
	// the updates waiting for the reader, which put a dot on Plugins
	mux.HandleFunc("GET /api/plugins/updates", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, plugin.PendingUpdates())
	})
	// the market in parts, as the page draws it (#488): the plugins
	// suggested, at once, then what npm says of the packages named
	mux.HandleFunc("GET /api/plugins/listings", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{"listings": pluginListings(r.Context())})
	})
	// the agents a suggested plugin adds that aren't installed here: the
	// Agents page's Install another agent says Plugins has them
	mux.HandleFunc("GET /api/plugins/agents", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, pluginAgents(r.Context()))
	})
	// repositories on GitHub tagged magpie-plugin: nobody's list, shown
	// apart as not reviewed, installed from the repository
	mux.HandleFunc("GET /api/plugins/github", func(rw http.ResponseWriter, r *http.Request) {
		// Try again asks GitHub at once when the list standing isn't its
		// whole answer
		if r.URL.Query().Get("again") != "" {
			plugin.TaggedAskAgain()
		}
		got, err := plugin.TaggedRepos(r.Context())
		// each one's own picture as the page can show it, kept here as an
		// installed plugin's is (a data URI isn't sent on to the page)
		repos := append([]plugin.Tagged(nil), got...)
		said := make([]string, len(repos))
		for i := range repos {
			said[i] = repos[i].Icon
		}
		for i, ic := range provider.RepoIcons(said, 3*time.Second) {
			repos[i].Icon = ic
		}
		out := map[string]any{"repos": repos, "topic": plugin.Topic}
		// why the list isn't GitHub's whole answer: the page says so with
		// Try again, rather than the section reading as there being none
		if err != nil {
			out["error"] = err.Error()
			var m *plugin.TaggedMissing
			if errors.As(err, &m) {
				out["error"], out["missing"] = m.Why.Error(), m.Missing
			}
			// and what kind of why, for the page to say in the reader's
			// language (the raw error stays its hover)
			if w := taggedWhyKind(err); w != "" {
				out["why"] = w
			}
		}
		writeJSON(rw, out)
	})
	// the commit each installed git plugin's repository is at now (asked
	// at most every ten minutes), by spec; one that didn't answer is left
	// out
	mux.HandleFunc("GET /api/plugins/git", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		writeJSON(rw, map[string]any{"heads": plugin.GitHeads(ctx)})
	})
	mux.HandleFunc("GET /api/plugins/npm", func(rw http.ResponseWriter, r *http.Request) {
		names := []string{}
		for _, n := range strings.Split(r.URL.Query().Get("names"), ",") {
			if n = strings.TrimSpace(n); n != "" && len(names) < 100 {
				names = append(names, n)
			}
		}
		info := plugin.Info(r.Context(), names)
		keys := make([]string, 0, len(info))
		ns := make([]*plugin.NPM, 0, len(info))
		for k, n := range info {
			keys, ns = append(keys, k), append(ns, &n)
		}
		npmIcons(ns, 3*time.Second)
		for i, k := range keys {
			info[k] = *ns[i]
		}
		writeJSON(rw, map[string]any{"npm": info})
	})
	// the whole market at once, npm's answers and all
	mux.HandleFunc("GET /api/plugins/market", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		writeJSON(rw, pluginMarketState(ctx, w))
	})
	mux.HandleFunc("GET /api/plugins/search", func(rw http.ResponseWriter, r *http.Request) {
		hits, err := plugin.Search(r.Context(), r.URL.Query().Get("q"))
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"hits": hits})
	})
	mux.HandleFunc("GET /api/plugins/page", func(rw http.ResponseWriter, r *http.Request) {
		p, err := plugin.ReadmeIn(r.Context(), r.URL.Query().Get("name"), r.URL.Query().Get("lang"))
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, p)
	})
	mux.HandleFunc("GET /api/plugins", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		writeJSON(rw, pluginsState(ctx, w))
	})
	// a folder on this computer, from the system's picker; "" when the
	// user cancels it
	mux.HandleFunc("POST /api/plugins/choose", func(rw http.ResponseWriter, r *http.Request) {
		dir, err := w.ChooseFolder("Choose a plugin folder")
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"dir": dir})
	})
	// Check for updates: npm asked now for each plugin's newest version,
	// with the list as it then stands; nothing installed
	mux.HandleFunc("POST /api/plugins/check", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		c := plugin.CheckNow(ctx)
		writeJSON(rw, map[string]any{"at": c.At, "plugins": c.Plugins, "state": pluginsState(ctx, w)})
	})
	// the 「国内镜像」 switch: what the page downloads (the list, npm's
	// packages and answers, Bun) is asked of mirrors in China first
	mux.HandleFunc("POST /api/plugins/mirror", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := setChinaMirror(in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"mirror": in.On})
	})
	// the npm registry set in magpie, "" for none of its own: an address
	// that isn't an http(s) one is refused, not kept
	mux.HandleFunc("POST /api/plugins/registry", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if _, err := plugin.SetRegistry(in.URL); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"registry": registryJSON()})
	})
	// add, remove, update, turn on or off: each answers with the list
	mux.HandleFunc("POST /api/plugins/{op}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec    string
			Off     bool
			Options map[string]any
			// prefer's: the provider id the plugin is to serve
			Provider string
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil && err != io.EOF {
			fail(rw, err)
			return
		}
		// a first plugin downloads Bun, and npm installs it. It goes on
		// though the page's connection goes: a proxy or tunnel in front of
		// magpie web gives up first (Cloudflare after 100 seconds, #2032),
		// and bun stopped halfway leaves plugins/ half-written
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
		defer cancel()
		var err error
		switch r.PathValue("op") {
		case "add":
			_, err = addPlugin(ctx, in.Spec)
		case "remove":
			err = removePlugin(ctx, in.Spec)
		case "update":
			err = updatePlugins(ctx)
		case "upgrade":
			err = upgradePlugin(ctx, plugin.Name(in.Spec))
		case "off":
			err = provider.SetPluginOff(ctx, in.Spec, in.Off)
		case "options":
			err = plugin.SetOptions(in.Spec, in.Options)
		case "prefer":
			err = plugin.Prefer(in.Spec, in.Provider)
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		// a deprecated built-in the plugin now serves is the plugin's, so
		// it isn't listed twice; one with accounts moves in the background
		// loop (provider.KeepRetiringMoved)
		if op := r.PathValue("op"); op == "add" || op == "update" || op == "upgrade" {
			provider.HandOver(ctx, false)
		}
		writeJSON(rw, pluginsState(ctx, w))
	})
	// signing in to a plugin's provider: the method's questions one at a
	// time, then a browser (OAuth) or a key
	mux.HandleFunc("POST /api/plugin-signin/prompt", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Provider string
			Method   int
			Inputs   map[string]string
			Key      string // the question answered, with Value: checked first
			Value    string
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Inputs == nil {
			in.Inputs = map[string]string{}
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		if in.Key != "" {
			msg, err := plugin.Validate(ctx, in.Provider, in.Method, in.Key, in.Value)
			if err != nil {
				fail(rw, err)
				return
			}
			if msg != "" {
				writeJSON(rw, map[string]any{"error": msg})
				return
			}
			in.Inputs[in.Key] = in.Value
		}
		q, err := plugin.NextPrompt(ctx, in.Provider, in.Method, in.Inputs)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"prompt": q, "inputs": in.Inputs})
	})
	mux.HandleFunc("POST /api/plugin-signin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Provider string
			Method   int
			Inputs   map[string]string
			Key      string  // an "api" method's key
			Proxy    *string // the one picked to sign in through (#1663)
			User     string  // the account signed in to again
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		pp, ok := pluginProviderByID(in.Provider)
		if !ok || in.Method < 0 || in.Method >= len(pp.Methods) {
			fail(rw, errNoPluginMethod)
			return
		}
		if pp.Methods[in.Method].Type == "api" {
			ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
			defer cancel()
			id, err := provider.PluginAPIKey(ctx, in.Provider, in.Method, in.Inputs, in.Key)
			if err != nil {
				fail(rw, err)
				return
			}
			// no account named: the provider's name as its user was taken
			// for an account's by Hide accounts, and blurred wherever it
			// showed (lileo0668 on Discord); the page names the sign-in
			// by its subscription
			writeJSON(rw, provider.SignInState{Agent: id, State: "done"})
			return
		}
		st, err := provider.StartPluginSignInWith(in.Provider, in.Method, in.Inputs, provider.SignInOptions{Proxy: in.Proxy, User: in.User})
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, openSignIn(w, st, false))
	})
}

type pluginErr string

func (e pluginErr) Error() string { return string(e) }

const errNoPluginMethod = pluginErr("the plugin has no such way to sign in; reopen the add sheet")

func pluginProviderByID(id string) (plugin.Provider, bool) {
	for _, pp := range plugin.Cached() {
		if pp.ID == id {
			return pp, true
		}
	}
	return plugin.Provider{}, false
}

// setChinaMirror turns the 「国内镜像」 switch on or off; a plugin list
// fetched from GitHub's slow address, or not at all, is asked again.
func setChinaMirror(on bool) error {
	s := settings.Load()
	if s.ChinaMirror == on {
		return nil
	}
	s.ChinaMirror = on
	if err := settings.Save(s); err != nil {
		return err
	}
	if on {
		plugin.RefreshMarket()
	}
	return nil
}

// pluginAgentJSON is the agent a plugin adds, as its row says it.
type pluginAgentJSON struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Icon  string `json:"icon,omitempty"`
	Error string `json:"error,omitempty"`
	// Missing is an agent magpie doesn't find on this computer, so Agents
	// has no row for it (zzZ: Kimi's plugin installed and no Kimi there);
	// Looks is where it looks, and Notice the plugin's own word on what
	// the agent needs first
	Missing bool   `json:"missing,omitempty"`
	Looks   string `json:"looks,omitempty"`
	Notice  string `json:"notice,omitempty"`
}

func pluginAgentOf(spec string, errs map[string]string) *pluginAgentJSON {
	if err := errs[spec]; err != "" {
		return &pluginAgentJSON{Error: err}
	}
	for _, a := range agent.All() {
		if a.Plugin != spec {
			continue
		}
		j := &pluginAgentJSON{ID: a.ID, Name: a.Name, Icon: a.Icon}
		if !a.Detected() {
			j.Missing = true
			j.Looks = tilde(cmp.Or(a.Dir, a.Path))
			if a.Notice != nil {
				j.Notice = a.Notice()
			}
		}
		return j
	}
	return nil
}

// taggedWhyKind is what kind of failure keeps GitHub's list from being
// whole: "slow" when GitHub didn't answer in time, "limited" when it
// turned magpie away for its rate limit, "" for anything else (the page
// shows that error as it is).
func taggedWhyKind(err error) string {
	var lim ghtoken.Limited
	var ne net.Error
	switch {
	case errors.As(err, &lim):
		return "limited"
	case errors.Is(err, plugin.ErrNoAnswer), errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "slow"
	}
	return ""
}
