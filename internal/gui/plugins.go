package gui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

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
	Models   int             `json:"models"`
}

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
	for _, pp := range plugin.Cached() {
		out = append(out, pluginSubJSON{
			ID: provider.PluginID(pp.ID), PID: pp.ID, Name: pp.Name, Icon: pluginIcon(pp), Spec: pp.Spec,
			Methods: pp.Methods, SignedIn: pp.SignedIn, Models: len(pp.Models),
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
	// Package is the package installed from a git repository, which its
	// own package.json names
	Package string `json:"package,omitempty"`
	// Moved are the built-in subscriptions moved onto it, which go back
	// to themselves when it is removed or turned off
	Moved []string `json:"moved"`
	// AutoUpdated is the update magpie made to it by itself lately
	AutoUpdated *plugin.Updated `json:"autoUpdated,omitempty"`
	// Middleware is the gateway middleware it has, run in the gateway
	// rather than the host: its hooks, how often they ran and how long
	// they took, and why it didn't load
	Middleware *middleware.State `json:"middleware,omitempty"`
	// OptionsExample is what its package suggests for its options, which
	// the options editor starts from when none are set
	OptionsExample map[string]any `json:"optionsExample,omitempty"`
	// IsMiddleware is whether its package has gateway middleware, said of
	// one switched off too, which Middleware leaves out; MiddlewareOnly is
	// whether that is all it has, no provider to sign in to
	IsMiddleware   bool `json:"isMiddleware,omitempty"`
	MiddlewareOnly bool `json:"middlewareOnly,omitempty"`
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
}

func pluginsState(ctx context.Context, w Windows) pluginsJSON {
	s := pluginsJSON{Plugins: []pluginEntryJSON{}, Bun: plugin.HasBun(), BunVer: plugin.BunInUse(), Movable: provider.MoveCandidates(), Picker: w != nil && !isWeb(w), Mirror: settings.Load().ChinaMirror}
	l := plugin.Load()
	errs := map[string]string{}
	names := map[string][]string{}
	if len(l.Plugins) > 0 && (plugin.Running() || plugin.HasBun()) {
		loaded, err := plugin.Plugins(ctx)
		if err != nil {
			s.Error = err.Error()
		}
		for _, p := range loaded {
			errs[p.Spec] = p.Error
		}
		if ps, err := plugin.Providers(ctx); err == nil {
			for _, p := range ps {
				names[p.Spec] = append(names[p.Spec], p.Name)
			}
		}
	}
	// npm's newest, as it said last: asking it again is /api/plugins/npm's
	known := plugin.InfoCached(npmNames(l.Plugins))
	mws := middleware.States()
	for _, e := range l.Plugins {
		j := pluginEntryJSON{Entry: e, Error: errs[e.Spec], Providers: names[e.Spec], Version: plugin.Installed(e.Spec)}
		if npmPlugin(e.Spec) {
			j.Latest = known[plugin.Name(e.Spec)].Version
		}
		if j.Providers == nil {
			j.Providers = []string{}
		}
		if plugin.IsGit(e.Spec) {
			j.Package = plugin.Name(e.Spec)
		}
		if u, ok := plugin.LastUpdated(plugin.Name(e.Spec), time.Now().Add(-autoUpdatedFor)); ok && !plugin.IsPath(e.Spec) {
			j.AutoUpdated = &u
		}
		if m, ok := mws[e.Spec]; ok {
			j.Middleware = &m
		}
		if file, only := plugin.Middleware(plugin.Target(e.Spec)); file != "" {
			j.IsMiddleware, j.MiddlewareOnly = true, only
			j.OptionsExample = plugin.OptionsExample(plugin.Target(e.Spec))
		}
		j.Moved = provider.MovedOnto(e.Spec)
		if j.Moved == nil {
			j.Moved = []string{}
		}
		s.Plugins = append(s.Plugins, j)
	}
	return s
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
	return out
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
		n := info[l.Package]
		m.Listings = append(m.Listings, pluginListingJSON{Listing: l, NPM: &n})
	}
	for i, e := range m.State.Plugins {
		if !plugin.IsGit(e.Spec) {
			m.State.Plugins[i].Latest = info[plugin.Name(e.Spec)].Version
		}
	}
	return m
}

func pluginRoutes(mux *http.ServeMux, w Windows) {
	// the updates waiting for the reader, which put a dot on Plugins
	mux.HandleFunc("GET /api/plugins/updates", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, plugin.PendingUpdates())
	})
	// the market in parts, as the page draws it (#488): the plugins
	// suggested, at once, then what npm says of the packages named
	mux.HandleFunc("GET /api/plugins/listings", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{"listings": pluginListings(r.Context())})
	})
	// repositories on GitHub tagged magpie-plugin: nobody's list, shown
	// apart as not reviewed, installed from the repository
	mux.HandleFunc("GET /api/plugins/github", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{"repos": plugin.TaggedRepos(r.Context()), "topic": plugin.Topic})
	})
	mux.HandleFunc("GET /api/plugins/npm", func(rw http.ResponseWriter, r *http.Request) {
		names := []string{}
		for _, n := range strings.Split(r.URL.Query().Get("names"), ",") {
			if n = strings.TrimSpace(n); n != "" && len(names) < 100 {
				names = append(names, n)
			}
		}
		writeJSON(rw, map[string]any{"npm": plugin.Info(r.Context(), names)})
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
		p, err := plugin.Readme(r.Context(), r.URL.Query().Get("name"))
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
	// add, remove, update, turn on or off: each answers with the list
	mux.HandleFunc("POST /api/plugins/{op}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec    string
			Off     bool
			Options map[string]any
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil && err != io.EOF {
			fail(rw, err)
			return
		}
		// a first plugin downloads Bun, and npm installs it
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		var err error
		switch r.PathValue("op") {
		case "add":
			_, err = plugin.Add(ctx, in.Spec)
		case "remove":
			err = provider.RemovePlugin(ctx, in.Spec)
		case "update":
			err = plugin.Update(ctx)
		case "upgrade":
			err = plugin.Upgrade(ctx, plugin.Name(in.Spec))
		case "off":
			err = provider.SetPluginOff(ctx, in.Spec, in.Off)
		case "options":
			err = plugin.SetOptions(in.Spec, in.Options)
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
			Key      string // an "api" method's key
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
			writeJSON(rw, provider.SignInState{Agent: id, State: "done", User: pp.Name})
			return
		}
		st, err := provider.StartPluginSignIn(in.Provider, in.Method, in.Inputs)
		if err != nil {
			fail(rw, err)
			return
		}
		if st.URL != "" && w != nil {
			w.OpenURL(st.URL)
		}
		writeJSON(rw, st)
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
