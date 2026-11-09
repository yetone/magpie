package gui

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A community plugin's own page in magpie's window (P4 of
// docs/subsystems/plugin-runtime.md): package.json's magpie.page, shown in
// a sandboxed frame (pluginpages.js), and its panels in magpie's own pages
// (magpie.panels, P5), each in a frame of its own shut in the same way.
// Their files are served under
// pluginPagePrefix and a token only this run of magpie hands out, with a
// policy that lets the page load nothing but its own files and reach
// nothing over the network; it has no way to magpie's /api (apiFramed),
// and what it may read of magpie's it asks the window for, which asks
// /api/plugin-pages/read for the reads its package declared.

// pluginPagePrefix is where a plugin's page's files are served:
// pluginPagePrefix + token + "/" + the file's path in the page's folder.
const pluginPagePrefix = "/_magpie/plugin/"

// pluginBridgePath is the window's side of the page's messages, served
// beside every page's files: a page loads it as
// <script src="_magpie/bridge.js">.
const pluginBridgePath = "_magpie/bridge.js"

//go:embed pagebridge.js
var pageBridgeJS []byte

// pageSecret keys this run's tokens: a token from an earlier run, or one
// made up, serves nothing.
var pageSecret = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}()

func pageToken(spec string) string {
	m := hmac.New(sha256.New, pageSecret)
	m.Write([]byte(spec))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// pluginPage is a plugin's page, of a plugin that is on.
type pluginPage struct {
	Spec  string `json:"spec"`
	Name  string `json:"name"`  // its package's
	Key   string `json:"key"`   // the window's page key
	Title string `json:"title"` // the page's, or the package's magpie.name or short name
	// Titles are its title by the window's language (plugin.PageSpec)
	Titles map[string]string `json:"titles,omitempty"`
	URL    string            `json:"url"` // the page, under pluginPagePrefix
	Icon   string            `json:"icon,omitempty"`
	// Replaces is the built-in page whose place it takes: the first
	// plugin's in plugins.json's order to ask for it
	Replaces string   `json:"replaces,omitempty"`
	Reads    []string `json:"reads"` // what it may read, of plugin.PageReads
	// Actions are its items in magpie's menus (plugin.ActionSpec); each
	// opens this page
	Actions []pluginAction `json:"actions,omitempty"`
	// Service is where a browser reaches the page's local service
	// (plugin.ServiceSpec) through this server, relative to the page's own
	// address: only on a page served to a browser (servePageService)
	Service string `json:"service,omitempty"`
	// KeepAlive keeps its frame while the reader is on another page
	// (plugin.PageSpec); else the window lets it go after a while hidden
	KeepAlive bool `json:"keepAlive,omitempty"`
	token     string
	dir       string
	file      string // its HTML, relative to dir, with forward slashes
	hosted    bool   // its plugin has a part for the host too (pageHost)
	service   *plugin.ServiceSpec
	options   map[string]any // its plugin's, as plugins.json keeps them
}

// pluginAction is an item a plugin's page adds to one of magpie's menus.
type pluginAction struct {
	ID     string            `json:"id"`
	Menu   string            `json:"menu"`  // of plugin.ActionMenus
	Title  string            `json:"title"` // its own, or its page's
	Titles map[string]string `json:"titles,omitempty"`
}

// pluginPanel is a panel a plugin that is on adds to one of magpie's own
// pages (plugin.PanelSlots).
type pluginPanel struct {
	Spec   string            `json:"spec"`
	Name   string            `json:"name"`  // its package's
	Key    string            `json:"key"`   // the panel's, among every plugin's
	Slot   string            `json:"slot"`  // the page it is in
	Title  string            `json:"title"` // the panel's, or the package's magpie.name or short name
	Titles map[string]string `json:"titles,omitempty"`
	URL    string            `json:"url"`   // the panel, under pluginPagePrefix
	Reads  []string          `json:"reads"` // what it may read, of plugin.PageReads
	token  string
	dir    string
	file   string
	hosted bool
}

// pageKeyChars are what a window page's key can't have
var pageKeyChars = strings.NewReplacer("@", "", "/", "-", ".", "-", "_", "-", " ", "-")

// windowParts are the pages and the panels of the plugins that are on, in
// plugins.json's order. A plugin switched off has none: its files aren't
// served.
func windowParts() (pages []pluginPage, panels []pluginPanel) {
	seen := map[string]bool{}
	replaced := map[string]bool{}
	for _, e := range plugin.Load().Plugins {
		if e.Off {
			continue
		}
		w, only := plugin.Window(plugin.Target(e.Spec))
		if w == nil {
			continue
		}
		name := plugin.PackageName(e.Spec)
		key := "plugin-" + strings.Trim(pageKeyChars.Replace(strings.ToLower(plugin.ShortName(name))), "-")
		key = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
				return r
			}
			return -1
		}, key)
		for k, n := key, 2; seen[key]; n++ {
			key = fmt.Sprintf("%s-%d", k, n)
		}
		seen[key] = true
		reads := w.Reads
		if reads == nil {
			reads = []string{}
		}
		if spec := w.Page; spec != nil {
			tok := pageToken(e.Spec)
			rel, _ := filepath.Rel(spec.Dir, spec.File)
			p := pluginPage{Spec: e.Spec, Name: name, Key: key, Title: spec.Title, Titles: spec.Titles, Reads: reads, token: tok, dir: spec.Dir,
				URL: pluginPagePrefix + tok + "/" + filepath.ToSlash(rel), file: filepath.ToSlash(rel), hosted: !only, options: e.Options, KeepAlive: spec.KeepAlive}
			if !only {
				p.service = spec.Service
			}
			if p.Title == "" {
				p.Title = cmp.Or(plugin.InstalledName(e.Spec), plugin.ShortName(name))
			}
			if spec.Icon != "" {
				p.Icon = pluginPagePrefix + tok + "/" + spec.Icon
			}
			if r := spec.Replaces; r != "" && !replaced[r] {
				replaced[r] = true
				p.Replaces = r
			}
			for _, a := range w.Actions {
				act := pluginAction{ID: a.ID, Menu: a.Menu, Title: a.Title, Titles: a.Titles}
				// one that names no title is the page's own, in each language
				if act.Title == "" {
					act.Title, act.Titles = p.Title, p.Titles
				}
				p.Actions = append(p.Actions, act)
			}
			pages = append(pages, p)
		}
		for i, spec := range w.Panels {
			tok := pageToken(e.Spec + "\x00panel\x00" + strconv.Itoa(i))
			rel, _ := filepath.Rel(spec.Dir, spec.File)
			p := pluginPanel{Spec: e.Spec, Name: name, Key: fmt.Sprintf("%s-%d", key, i), Slot: spec.Slot, Title: spec.Title, Titles: spec.Titles, Reads: reads,
				token: tok, dir: spec.Dir, URL: pluginPagePrefix + tok + "/" + filepath.ToSlash(rel), file: filepath.ToSlash(rel), hosted: !only}
			if p.Title == "" {
				p.Title = cmp.Or(plugin.InstalledName(e.Spec), plugin.ShortName(name))
			}
			panels = append(panels, p)
		}
	}
	return pages, panels
}

// pluginPages are the pages of the plugins that are on.
func pluginPages() []pluginPage {
	pages, _ := windowParts()
	return pages
}

// windowOf is a plugin that is on and adds a page or a panel to the
// window: its package's name and the reads it declared.
func windowOf(spec string) (name string, reads []string, ok bool) {
	pages, panels := windowParts()
	for _, p := range pages {
		if p.Spec == spec {
			return p.Name, p.Reads, true
		}
	}
	for _, p := range panels {
		if p.Spec == spec {
			return p.Name, p.Reads, true
		}
	}
	return "", nil, false
}

// pagePolicy is what a plugin's page may load and reach: its own files,
// inline styles, data: pictures, and nothing over the network. sandbox
// keeps a page opened by itself (not in the window's frame) as shut in as
// the frame keeps it.
func pagePolicy(r *http.Request, tok string) string {
	own := "'self'"
	if r.Host != "" {
		// the page's own folder: not the window's files, nor /api
		own = strings.Join(ownSources(r, pluginPagePrefix+tok+"/"), " ")
	}
	return strings.Join([]string{
		"sandbox allow-scripts",
		"default-src 'none'",
		"script-src " + own,
		"style-src " + own + " 'unsafe-inline'",
		"img-src " + own + " data:",
		"font-src " + own,
		"media-src " + own,
		"connect-src 'none'",
		"frame-src 'none'",
		"worker-src 'none'",
		"object-src 'none'",
		"form-action 'none'",
		"base-uri 'none'",
		"frame-ancestors 'self'",
	}, "; ")
}

// ownSources are the sources, with their scheme, that name path on the
// address r came to. A sandboxed frame's origin is opaque, and a source
// with no scheme takes its scheme from that origin: iOS Safari (iOS 18.7)
// matched the scheme-less host/path to nothing and never asked for a
// page's scripts, so the page was broken at 10s (#4198, orinsid and
// YangHonghang). Chromium, and the newer WebKit Playwright runs, take
// the scheme from the URL loaded, which is why only the iPhone failed.
//
// The desktop window names its own transport (ownWindow): wails://, or
// http:// on Windows (#2501). A browser's page (magpie web) can come over
// http or https, directly or through a proxy that ends TLS before magpie
// (nginx, Cloudflare), and nothing magpie trusts says which, so both are
// named: the same host, port and folder either way, and an https page
// can't load http:// files anyway (mixed content).
func ownSources(r *http.Request, path string) []string {
	if scheme, _ := r.Context().Value(windowSchemeKey{}).(string); scheme != "" {
		return []string{scheme + "://" + r.Host + path}
	}
	return []string{"https://" + r.Host + path, "http://" + r.Host + path}
}

// pageHostWait is how long a page's HTML waits for its plugin's host,
// short of the window's 10 seconds for the page to say hello
// (pluginpages.js's HELLO_MS).
var pageHostWait = 7 * time.Second

// warmHost starts the plugin host if it isn't up; tests replace it.
var warmHost = plugin.Warm

// pageHost has the plugin host up for a page whose plugin has a part for
// it too, waiting pageHostWait at most. Such a page can be the way into
// a service that part runs: Magiczerowxy/magpie-chat's page sends its
// frame to the chat server its host part starts. The host starts only on
// a call, and every Restart (a plugin installed, updated or set, a Bun
// update) stops it until the next one, so with no other plugin asking,
// the page went to a server that wasn't there: Chromium shows its
// connection error, and WebKit (the Mac's and Linux's window) leaves the
// frame on the page's own "connecting…" for good. The host starts in the
// background under its own two minutes, as every caller's does, so a
// slow one isn't cut off when the page stops waiting.
func pageHost(ctx context.Context) {
	done := make(chan struct{})
	warm := warmHost // read here: the start can outlive the page's wait
	go func() {
		defer close(done)
		c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := warm(c); err != nil {
			log.Printf("plugin page: starting the plugin host: %v", err)
		}
	}()
	t := time.NewTimer(pageHostWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
}

// servePluginPage serves a file of a plugin's page, by its token, and on
// a page served to a browser (web) the page's local service too.
func servePluginPage(rw http.ResponseWriter, r *http.Request, web bool) {
	rest := strings.TrimPrefix(r.URL.Path, pluginPagePrefix)
	tok, rel, ok := strings.Cut(rest, "/")
	if !ok || len(tok) != 32 {
		http.NotFound(rw, r)
		return
	}
	// the page's or the panel's folder, by its token
	dir, entry, hosted := "", "", false
	var page *pluginPage
	pages, panels := windowParts()
	for i, p := range pages {
		if hmac.Equal([]byte(p.token), []byte(tok)) {
			dir, entry, hosted = p.dir, p.file, p.hosted
			page = &pages[i]
		}
	}
	// the service's paths are never a file of the page's
	if strings.HasPrefix(rel, pageServicePath) {
		if !web || page == nil || page.servicePort() == 0 {
			http.NotFound(rw, r)
			return
		}
		servePageService(rw, r, page, tok)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	for _, p := range panels {
		if hmac.Equal([]byte(p.token), []byte(tok)) {
			dir, entry, hosted = p.dir, p.file, p.hosted
		}
	}
	if dir == "" {
		http.NotFound(rw, r)
		return
	}
	// the page itself, of a plugin with a part for the host: that part is
	// up before the page is (pageHost)
	if hosted && rel == entry {
		pageHost(r.Context())
	}
	h := rw.Header()
	h.Set("Content-Security-Policy", pagePolicy(r, tok))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// no Cross-Origin-Resource-Policy: to the page's sandboxed frame, an
	// origin of its own, its own files are another origin's, and
	// same-origin (or same-site) would block every one of them
	h.Set("Cache-Control", "no-cache")
	if rel == pluginBridgePath {
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		http.ServeContent(rw, r, "bridge.js", time.Time{}, strings.NewReader(string(pageBridgeJS)))
		return
	}
	file, ok := plugin.FileIn(dir, rel)
	if !ok {
		http.NotFound(rw, r)
		return
	}
	// ServeContent, not ServeFile: ServeFile sends .../index.html to ./,
	// which a page's own links needn't follow.
	f, err := os.Open(file)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(rw, r)
		return
	}
	http.ServeContent(rw, r, filepath.Base(file), st.ModTime(), f)
}

// apiFramed is whether r is a request for /api that no page of the window's
// own makes: one from a sandboxed frame (Origin: null, which a plugin's
// page's fetch would send were its policy not to stop it), one that loads
// /api into a frame, or one another site's page sends. The desktop
// window's /api has no key, so this is what keeps a plugin's page out.
func apiFramed(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	if r.Header.Get("Origin") == "null" {
		return true
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "iframe", "frame", "embed", "object":
		return true
	}
	return r.Header.Get("Sec-Fetch-Site") == "cross-site" && r.Header.Get("Sec-Fetch-Dest") != "document"
}

// pluginPageRoutes are the routes of the plugins' pages and panels. web is
// whether the window is a browser's (isWeb): only there is a page's local
// service served through this server (servePageService), since only there
// can 127.0.0.1 be another computer than this one.
func pluginPageRoutes(mux *http.ServeMux, web bool) {
	mux.HandleFunc(pluginPagePrefix, func(rw http.ResponseWriter, r *http.Request) { servePluginPage(rw, r, web) })
	// a picture a page drew, to the clipboard or to Downloads (pageimage.go)
	pageImageRoutes(mux)
	mux.HandleFunc("GET /api/plugin-pages", func(rw http.ResponseWriter, r *http.Request) {
		out := pluginPages()
		if out == nil {
			out = []pluginPage{}
		}
		for i := range out {
			if web && out[i].servicePort() > 0 {
				out[i].Service = strings.Repeat("../", strings.Count(out[i].file, "/")) + pageServicePath
			}
		}
		writeJSON(rw, out)
	})
	mux.HandleFunc("GET /api/plugin-panels", func(rw http.ResponseWriter, r *http.Request) {
		_, out := windowParts()
		if out == nil {
			out = []pluginPanel{}
		}
		writeJSON(rw, out)
	})
	// what a page or a panel asked the window to read: only what its
	// package declared, and never a secret
	mux.HandleFunc("POST /api/plugin-pages/read", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec   string         `json:"spec"`
			Read   string         `json:"read"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		name, reads, ok := windowOf(in.Spec)
		if !ok {
			fail(rw, errors.New("no plugin page "+in.Spec))
			return
		}
		if !slices.Contains(reads, in.Read) {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(rw).Encode(map[string]string{"error": fmt.Sprintf("%s's package doesn't ask to read %q", name, in.Read)})
			return
		}
		v, err := pageRead(in.Read, in.Params)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// a page's or a panel's own options, as plugins.json keeps them: no
	// other plugin's
	mux.HandleFunc("POST /api/plugin-pages/options", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec    string          `json:"spec"`
			Options json.RawMessage `json:"options"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if _, _, ok := windowOf(in.Spec); !ok {
			fail(rw, errors.New("no plugin page "+in.Spec))
			return
		}
		if len(in.Options) > 0 && string(in.Options) != "null" {
			var opts map[string]any
			if err := json.Unmarshal(in.Options, &opts); err != nil {
				fail(rw, errors.New("a page's options are an object"))
				return
			}
			// a page that sets them over and over would start the
			// host, every plugin's, again each time
			if !pageSetAllowed(in.Spec, time.Now()) {
				http.Error(rw, "a page sets its options once a second at most", http.StatusTooManyRequests)
				return
			}
			if err := plugin.SetPageOptions(in.Spec, opts); err != nil {
				fail(rw, err)
				return
			}
		}
		for _, e := range plugin.Load().Plugins {
			if e.Spec == in.Spec {
				opts := e.Options
				if opts == nil {
					opts = map[string]any{}
				}
				writeJSON(rw, opts)
				return
			}
		}
		writeJSON(rw, map[string]any{})
	})
}

var pageSets = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// pageSetAllowed is whether spec's page may set its options now: a second
// after it last did.
func pageSetAllowed(spec string, now time.Time) bool {
	pageSets.Lock()
	defer pageSets.Unlock()
	if last, ok := pageSets.at[spec]; ok && now.Sub(last) < time.Second {
		return false
	}
	pageSets.at[spec] = now
	return true
}

// pageRead is one of plugin.PageReads.
func pageRead(read string, params map[string]any) (any, error) {
	switch read {
	case "providers:list":
		// a name and an id each: no key, no account, no address
		type prov struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		out := []prov{}
		for _, p := range provider.All() {
			out = append(out, prov{ID: p.ID, Name: p.Name})
		}
		return out, nil
	case "usage:day":
		// one day, by agent and model (pageday.go)
		return pageDay(params)
	case "usage:summary":
		period := usage.Today
		if s, _ := params["period"].(string); s != "" {
			period = usage.Period(s)
		}
		if !slices.Contains([]usage.Period{usage.Today, usage.Week, usage.Month}, period) {
			return nil, fmt.Errorf("no period %q: today, 7d or 30d", period)
		}
		s := usage.Summarize(period)
		type model struct {
			Model  string  `json:"model"`
			Calls  int     `json:"calls"`
			Input  int     `json:"input"`
			Output int     `json:"output"`
			Cost   float64 `json:"cost"`
		}
		// the totals and each model's, never a key's, an account's or a
		// session's
		out := struct {
			Period string    `json:"period"`
			Since  time.Time `json:"since"`
			Calls  int       `json:"calls"`
			Errors int       `json:"errors"`
			Input  int       `json:"input"`
			Output int       `json:"output"`
			Cache  int       `json:"cacheRead"`
			Cost   float64   `json:"cost"`
			Models []model   `json:"models"`
		}{Period: string(period), Since: s.Since, Calls: s.Calls, Errors: s.Errors, Input: s.Input, Output: s.Output, Cache: s.CacheRead, Cost: s.Cost, Models: []model{}}
		for i, g := range s.Models {
			if i == 20 {
				break
			}
			m := g.Model
			if m == "" {
				m = g.ID
			}
			out.Models = append(out.Models, model{Model: m, Calls: g.Calls, Input: g.Input, Output: g.Output, Cost: g.Cost})
		}
		return out, nil
	}
	return nil, fmt.Errorf("no read %q", read)
}
