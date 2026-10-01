package gui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
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
	// Moved are the built-in subscriptions moved onto it, which go back
	// to themselves when it is removed or turned off
	Moved []string `json:"moved"`
}

type pluginsJSON struct {
	Plugins []pluginEntryJSON `json:"plugins"`
	Bun     bool              `json:"bun"` // Bun is here; adding the first plugin downloads it otherwise
	BunVer  string            `json:"bunVersion"`
	Error   string            `json:"error,omitempty"` // the plugins couldn't be asked
	// Movable are the built-ins with accounts a plugin could run, which
	// its card and its row offer to move
	Movable []provider.MoveCandidate `json:"movable"`
}

func pluginsState(ctx context.Context) pluginsJSON {
	s := pluginsJSON{Plugins: []pluginEntryJSON{}, Bun: plugin.HasBun(), BunVer: plugin.BunVersion, Movable: provider.MoveCandidates()}
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
	for _, e := range l.Plugins {
		j := pluginEntryJSON{Entry: e, Error: errs[e.Spec], Providers: names[e.Spec], Version: plugin.Installed(e.Spec)}
		if j.Providers == nil {
			j.Providers = []string{}
		}
		j.Moved = provider.MovedOnto(e.Spec)
		if j.Moved == nil {
			j.Moved = []string{}
		}
		s.Plugins = append(s.Plugins, j)
	}
	return s
}

// pluginMarketJSON is the plugin market: the plugins magpie suggests, what npm
// says of each, and those added.
type pluginMarketJSON struct {
	Listings []pluginListingJSON `json:"listings"`
	State    pluginsJSON         `json:"state"`
}

type pluginListingJSON struct {
	plugin.Listing
	NPM plugin.NPM `json:"npm"`
}

func pluginMarketState(ctx context.Context) pluginMarketJSON {
	var ls []plugin.Listing
	var st pluginsJSON
	done := make(chan struct{})
	go func() { st = pluginsState(ctx); close(done) }()
	ls = plugin.Market(ctx)
	names := []string{}
	for _, l := range ls {
		names = append(names, l.Package)
	}
	<-done
	for _, e := range st.Plugins {
		if !plugin.IsPath(e.Spec) {
			names = append(names, plugin.Name(e.Spec))
		}
	}
	info := plugin.Info(ctx, names)
	m := pluginMarketJSON{Listings: []pluginListingJSON{}, State: st}
	for _, l := range ls {
		m.Listings = append(m.Listings, pluginListingJSON{Listing: l, NPM: info[l.Package]})
	}
	for i, e := range m.State.Plugins {
		m.State.Plugins[i].Latest = info[plugin.Name(e.Spec)].Version
	}
	return m
}

func pluginRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/plugins/market", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		writeJSON(rw, pluginMarketState(ctx))
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
		writeJSON(rw, pluginsState(ctx))
	})
	// add, remove, update, turn on or off: each answers with the list
	mux.HandleFunc("POST /api/plugins/{op}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec string
			Off  bool
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
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, pluginsState(ctx))
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
