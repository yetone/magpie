package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// SearchDef is a web search API a plugin gives: its search hook
// ({search: {<id>: {name, keysURL, keyOptional, url, execute}}}). magpie
// lists it beside Tavily, Brave and the rest, keeps its keys and order, and
// asks it through the gateway's search path (internal/gateway/search_api.go).
type SearchDef struct {
	ID          string `json:"id"` // [a-z0-9-], at most 32
	Name        string `json:"name"`
	KeysURL     string `json:"keysURL,omitempty"`
	KeyOptional bool   `json:"keyOptional,omitempty"`
	// NeedURL: the user gives an address (a SearXNG's), passed to the
	// plugin as url
	NeedURL bool `json:"needURL,omitempty"`
	// Auth is the provider id the API signs in as, one the same plugin's
	// auth hook signs in to: it takes no key, and each call is one of that
	// provider's accounts (RunSearchAs)
	Auth string `json:"auth,omitempty"`
}

// PluginSearch is a search API with the plugin that gives it.
type PluginSearch struct {
	Spec string `json:"spec"`
	SearchDef
}

// VendorID is how the search API is known in magpie's list and in
// providers.json: the plugin's short name and its own id, so no plugin's
// id can take a built-in's place or another plugin's.
func (p PluginSearch) VendorID() string {
	short := strings.Trim(unsafeVendorChars.ReplaceAllString(strings.ToLower(ShortName(Name(p.Spec))), "-"), "-")
	return short + ":" + p.ID
}

// unsafeVendorChars are what a vendor id leaves out: the CLI lowercases it
// and it is a word in a space-separated key id.
var unsafeVendorChars = lazyre.New(`[^a-z0-9-]+`)

// SearchPage is one page a plugin's search found.
type SearchPage struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Text  string `json:"text"`
	Age   string `json:"age"`
}

// SearchRefused is a plugin's search API turning the key away: the status
// it threw (401, 402, 403, 429…), which magpie treats as it does a built-in
// API's.
type SearchRefused struct {
	Status     int
	Msg        string
	RetryAfter string
}

func (e SearchRefused) Error() string { return e.Msg }

// searchSlack is how long Go waits past the time the host is given.
const searchSlack = 5 * time.Second

// RunSearch asks the plugin spec's search API id for the query, within the
// time ctx has (60s with none), starting the host if need be. A refusal of
// the key is a SearchRefused.
func RunSearch(ctx context.Context, spec, id, query string, count int, key, url string) ([]SearchPage, error) {
	return runSearch(ctx, map[string]any{"spec": spec, "id": id, "query": query, "count": count, "key": key, "url": url})
}

// RunSearchAs asks a search API that signs in (SearchDef.Auth) as the
// account at its host key (provider or provider#slot), through the
// account's proxy in ctx. The host renews the sign-in first if due, within
// the time ctx has, and the hook reads that account alone (ctx.getAuth).
// One signed out is an error, never a sign-in to another account.
func RunSearchAs(ctx context.Context, spec, id, query string, count int, account string) ([]SearchPage, error) {
	if account == "" {
		return nil, errors.New("no account to search as")
	}
	return runSearch(ctx, map[string]any{"spec": spec, "id": id, "query": query, "count": count, "account": account, "proxy": proxyOf(ctx)})
}

func runSearch(ctx context.Context, params map[string]any) ([]SearchPage, error) {
	ms := int64(60_000)
	if d, ok := ctx.Deadline(); ok {
		ms = max(time.Until(d).Milliseconds(), 1)
	}
	var out struct {
		Pages      []SearchPage `json:"pages"`
		Status     int          `json:"status"`
		Error      string       `json:"error"`
		RetryAfter string       `json:"retryAfter"`
	}
	params["ms"] = ms
	err := callWithTimeout(ctx, "search", params, &out, time.Duration(ms)*time.Millisecond+searchSlack)
	if err != nil {
		return nil, err
	}
	if out.Status != 0 {
		return nil, SearchRefused{out.Status, out.Error, out.RetryAfter}
	}
	return out.Pages, nil
}

// searchesPath is where the search APIs the host last loaded are noted, by
// plugin: provider.SearchVendorOf reads it, which mustn't start the host.
func searchesPath() string { return filepath.Join(settings.Dir(), "plugin-searches.json") }

// SearchesPath is the note NotedSearches reads.
func SearchesPath() string { return searchesPath() }

var searchesMu sync.Mutex

// noteSearches notes which plugins the host loaded give search APIs,
// written whole and only when it changed.
func noteSearches(loaded []Loaded) {
	m := map[string][]SearchDef{}
	for _, l := range loaded {
		if l.Error == "" && len(l.Searches) > 0 {
			m[l.Spec] = l.Searches
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	searchesMu.Lock()
	defer searchesMu.Unlock()
	old, err := steady.ReadFile(searchesPath())
	if err == nil && strings.TrimSpace(string(old)) == strings.TrimSpace(string(b)) {
		return
	}
	if len(m) == 0 && errors.Is(err, fs.ErrNotExist) {
		return
	}
	defer filememo.Forget()
	if err := writeWhole(searchesPath(), append(b, '\n')); err != nil {
		log.Printf("plugin searches: noting them: %v", err)
	}
}

// NotedSearches are the search APIs the plugins that are on in plugins.json
// gave when the host last loaded them, in plugins.json's order. It does not
// start the host. A note that can't be read or parsed gives none (and an
// error): the plugin's APIs are then unknown, not removed.
func NotedSearches() ([]PluginSearch, error) {
	m, err := filememo.Read("plugin-searches", searchesPath(), func(b []byte) (map[string][]SearchDef, error) {
		var m map[string][]SearchDef
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", searchesPath(), err)
		}
		return m, nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []PluginSearch
	seen := map[string]bool{}
	for _, e := range Load().Plugins {
		if e.Off {
			continue
		}
		for _, d := range m[e.Spec] {
			p := PluginSearch{Spec: e.Spec, SearchDef: d}
			if !seen[p.VendorID()] {
				seen[p.VendorID()] = true
				out = append(out, p)
			}
		}
	}
	return out, nil
}
