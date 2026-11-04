package provider

// Search APIs answer a model's web search when no provider can (#419):
// Tavily, Brave Search, Exa, Firecrawl, You.com, DeepSeek, Volcengine's
// Doubao Search, Tencent Cloud's Yuanbao Search, a SearXNG of the user's. They are
// kept in providers.json beside the providers, their keys with theirs.

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/plugin"
)

// SearchAPI is a web search API the user gave magpie a key to.
type SearchAPI struct {
	Vendor string `json:"vendor"` // one of SearchVendors
	// Key is its key, or several separated by commas (Keys), each tried
	// when the one before is refused (#1477).
	Key string `json:"key,omitempty"`
	// Pick is how a search picks among several keys: "" takes them in
	// turn (SearchPickTurns), each search the next one; SearchPickOrder
	// asks the first until it is refused, then the next (SlouchyQuill on
	// Discord). Either way a key refused lately is asked last.
	Pick string `json:"pick,omitempty"`
	// URL is where it is: a SearXNG's own, or another address for one of
	// the others (a Firecrawl run by the user); empty is the vendor's.
	URL string `json:"url,omitempty"`
	// Off keeps it, key and all, out of every search until it is switched
	// on again (#2286): to try one API alone, or rest one whose credit is
	// nearly spent, without taking its key away.
	Off bool `json:"off,omitempty"`
}

// How a search picks among a search API's keys (SearchAPI.Pick).
const (
	SearchPickTurns = ""      // in turn, each search the next key
	SearchPickOrder = "order" // the first until it is refused
)

// SearchVendor is a search API magpie can ask.
type SearchVendor struct {
	ID, Name string
	Base     string // its address, "" for one the user runs
	KeysURL  string // where its keys are made
	// KeyOptional: it answers with no key too (a SearXNG; You.com's free
	// search, YouFreeSearch)
	KeyOptional bool
	// Paths are what magpie puts after its base URL to search (the
	// gateway's askSearchAPI): a base URL given ending in one of them is
	// that base, so the path isn't asked twice (/search/search, #509)
	Paths []string
	// Plugin is the spec of the plugin that gives this API (its search
	// hook), "" for one of magpie's own; PluginID is its id there. The
	// plugin asks it (plugin.RunSearch); ID is plugin.PluginSearch.VendorID.
	Plugin, PluginID string
	// NeedURL: a plugin's API the user gives an address for, as a SearXNG
	NeedURL bool
}

// WantsURL is whether the user must give its address (a SearXNG's, or a
// plugin's that asked for one).
func (v SearchVendor) WantsURL() bool { return v.Base == "" && (v.Plugin == "" || v.NeedURL) }

// YouFreeSearch is You.com's search that takes no key (#1626): 100
// searches a day for each address, then 429. With a key, You.com is
// asked at its Base, ydc-index.io, instead.
const YouFreeSearch = "https://api.you.com/v1/agents/search"

// SearchVendors are the search APIs magpie can ask, in the order offered.
var SearchVendors = []SearchVendor{
	{ID: "tavily", Name: "Tavily", Base: "https://api.tavily.com", KeysURL: "https://app.tavily.com/home", Paths: []string{"/search"}},
	{ID: "brave", Name: "Brave Search", Base: "https://api.search.brave.com", KeysURL: "https://api-dashboard.search.brave.com/app/keys", Paths: []string{"/res/v1/web/search"}},
	{ID: "exa", Name: "Exa", Base: "https://api.exa.ai", KeysURL: "https://dashboard.exa.ai/api-keys", Paths: []string{"/search"}},
	{ID: "firecrawl", Name: "Firecrawl", Base: "https://api.firecrawl.dev", KeysURL: "https://www.firecrawl.dev/app/api-keys", Paths: []string{"/v2/search"}},
	{ID: "you", Name: "You.com", Base: "https://ydc-index.io", KeysURL: "https://you.com/platform", KeyOptional: true, Paths: []string{"/v1/search", "/v1/agents/search"}},
	{ID: "deepseek", Name: "DeepSeek", Base: "https://api.deepseek.com", KeysURL: "https://platform.deepseek.com/api_keys", Paths: []string{"/v1/tools/search"}},
	// Volcengine's Doubao Search Custom (豆包搜索 Custom版, once 联网搜索 /
	// 融合信息搜索), with an API key from its console (CyberCaius on Discord)
	{ID: "volcengine", Name: "Doubao Search (Volcengine)", Base: "https://open.feedcoopapi.com", KeysURL: "https://console.volcengine.com/search-infinity/api-key", Paths: []string{"/search_api/web_search"}},
	// Tencent Cloud's Yuanbao Search (元宝搜索, wsa) by its service API
	// key (服务 API KEY 方式, cloud.tencent.com/document/product/1806/130615),
	// made in its console's 概览 › 服务 API KEY 方式 (plugins#509)
	{ID: "wsa", Name: "Yuanbao Search (Tencent Cloud)", Base: "https://api.wsa.cloud.tencent.com", KeysURL: "https://console.cloud.tencent.com/wsapi/index", Paths: []string{"/SearchPro"}},
	{ID: "searxng", Name: "SearXNG", KeyOptional: true, Paths: []string{"/search"}},
}

// AllSearchVendors are SearchVendors and the search APIs the plugins that
// are on give (their search hook), after them. A plugin note that can't be
// read adds none.
func AllSearchVendors() []SearchVendor {
	out := slices.Clone(SearchVendors)
	ps, _ := plugin.NotedSearches()
	for _, p := range ps {
		out = append(out, SearchVendor{ID: p.VendorID(), Name: p.Name, KeysURL: p.KeysURL, KeyOptional: p.KeyOptional,
			Plugin: p.Spec, PluginID: p.ID, NeedURL: p.NeedURL})
	}
	return out
}

// SearchVendorOf is the vendor by its id: one of magpie's, or a plugin's.
func SearchVendorOf(id string) (SearchVendor, bool) {
	i := slices.IndexFunc(SearchVendors, func(v SearchVendor) bool { return v.ID == id })
	if i >= 0 {
		return SearchVendors[i], true
	}
	if !strings.Contains(id, ":") {
		return SearchVendor{}, false
	}
	all := AllSearchVendors()
	if i := slices.IndexFunc(all[len(SearchVendors):], func(v SearchVendor) bool { return v.ID == id }); i >= 0 {
		return all[len(SearchVendors)+i], true
	}
	return SearchVendor{}, false
}

// Keys are the keys in Key: one, or several the user gave separated by
// commas, spaces or lines, each once, in the order given.
func (a SearchAPI) Keys() []string {
	var out []string
	for _, k := range strings.FieldsFunc(a.Key, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// MaskedKey is Key as shown: each of its keys masked (MaskSearchKey), so a
// list of them shows no more of any one than a single key does.
func (a SearchAPI) MaskedKey() string {
	ks := a.Keys()
	for i, k := range ks {
		ks[i] = MaskSearchKey(k)
	}
	return strings.Join(ks, ", ")
}

// MaskSearchKey is one search API key as shown: as Mask, but a Tavily
// key keeps its whole "tvly-" tag (tvly-…abcd), which says what it is
// and nothing secret of it.
func MaskSearchKey(k string) string {
	if strings.HasPrefix(k, "tvly-") && len(k) >= 16 {
		return "tvly-…" + k[len(k)-4:]
	}
	return Mask(k)
}

// Name is the vendor's name.
func (a SearchAPI) Name() string {
	if v, ok := SearchVendorOf(a.Vendor); ok {
		return v.Name
	}
	return a.Vendor
}

// Base is the address it is asked at, without a slash at its end, nor
// the vendor's own path (SearchVendor.Paths) when its URL was given with
// it: one saved so before is asked right too (#509).
func (a SearchAPI) Base() string {
	if a.URL != "" {
		return a.ownBase()
	}
	v, _ := SearchVendorOf(a.Vendor)
	return v.Base
}

// ownBase is URL without a slash at its end, nor the vendor's path at
// its end (…/search for Tavily): magpie puts that path after it.
func (a SearchAPI) ownBase() string {
	u := strings.TrimRight(a.URL, "/")
	v, _ := SearchVendorOf(a.Vendor)
	for _, p := range v.Paths {
		if len(u) > len(p) && strings.EqualFold(u[len(u)-len(p):], p) {
			return strings.TrimRight(u[:len(u)-len(p)], "/")
		}
	}
	return u
}

// Ready is whether it can be asked: a vendor magpie knows, at an address,
// with a key where it needs one (a SearXNG, You.com, may not).
func (a SearchAPI) Ready() bool {
	v, known := SearchVendorOf(a.Vendor)
	return known && (a.Base() != "" || (v.Plugin != "" && !v.NeedURL)) && (a.Key != "" || v.KeyOptional)
}

// check says what is wrong with a search API the user gives.
func (a SearchAPI) check() error {
	v, ok := SearchVendorOf(a.Vendor)
	if !ok {
		var ids []string
		for _, v := range AllSearchVendors() {
			ids = append(ids, v.ID)
		}
		return fmt.Errorf("no search API %q: one of %s", a.Vendor, strings.Join(ids, ", "))
	}
	if a.URL != "" {
		u, err := url.Parse(a.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s: %q is not an http:// or https:// address", a.Name(), a.URL)
		}
		// the API's own paths go after it (/search), so it can't end in a
		// query or a fragment
		if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return fmt.Errorf("%s: %q has a ?query or #fragment; give its base URL alone", a.Name(), a.URL)
		}
	}
	if a.Pick != SearchPickTurns && a.Pick != SearchPickOrder {
		return fmt.Errorf("%s: pick %q is not turns or order", a.Name(), a.Pick)
	}
	if a.Vendor == "searxng" && a.URL == "" {
		return fmt.Errorf("SearXNG needs its address, as url=https://…")
	}
	if v.Plugin != "" && v.NeedURL && a.URL == "" {
		return fmt.Errorf("%s needs its address, as url=https://…", a.Name())
	}
	if a.Key == "" && !v.KeyOptional {
		return fmt.Errorf("%s needs its API key", a.Name())
	}
	return nil
}

// SearchAPIs are the search APIs that can be asked, in the order they are
// tried: ready, and not switched off.
func SearchAPIs() []SearchAPI {
	return slices.DeleteFunc(slices.Clone(load().Searches), func(a SearchAPI) bool { return !a.Ready() || a.Off })
}

// SearchAPIsSet counts the search APIs that can be asked; a
// providers.json that can't be read is an error, not none.
func SearchAPIsSet() (int, error) {
	f, err := read()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range f.Searches {
		if a.Ready() {
			n++
		}
	}
	return n, nil
}

// StoredSearchAPIs are the search APIs as saved, keys and all.
func StoredSearchAPIs() []SearchAPI { return load().Searches }

// SetSearchAPI adds a search API, last, or changes the one of its vendor
// where it is; a change with no key keeps the key it has, and one with no
// Pick the pick it has ("turns" puts the default back). One switched off
// stays off.
func SetSearchAPI(a SearchAPI) error {
	a.Vendor = strings.ToLower(strings.TrimSpace(a.Vendor))
	a.Key, a.URL = strings.Join(a.Keys(), ","), strings.TrimSpace(a.URL)
	pick := strings.ToLower(strings.TrimSpace(a.Pick))
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Searches, func(x SearchAPI) bool { return x.Vendor == a.Vendor })
	if i >= 0 && a.Key == "" {
		a.Key = f.Searches[i].Key
	}
	if i >= 0 {
		a.Off = f.Searches[i].Off
	}
	switch {
	case pick == "turns":
		a.Pick = SearchPickTurns
	case pick == "" && i >= 0:
		a.Pick = f.Searches[i].Pick
	default:
		a.Pick = pick
	}
	if err := a.check(); err != nil {
		return err
	}
	if a.URL != "" {
		// kept as its base, the field showing what is asked
		a.URL = a.ownBase()
	}
	if i >= 0 {
		f.Searches[i] = a
	} else {
		f.Searches = append(f.Searches, a)
	}
	return store(f)
}

// RemoveSearchAPI takes the vendor's search API away.
func RemoveSearchAPI(vendor string) error {
	f, err := read()
	if err != nil {
		return err
	}
	n := len(f.Searches)
	f.Searches = slices.DeleteFunc(f.Searches, func(x SearchAPI) bool { return x.Vendor == vendor })
	if len(f.Searches) == n {
		return fmt.Errorf("no %s search API is set up", vendor)
	}
	return store(f)
}

// SwitchSearchAPI switches the vendor's search API on or off (#2286),
// keeping its key, address and place.
func SwitchSearchAPI(vendor string, on bool) error {
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Searches, func(x SearchAPI) bool { return x.Vendor == vendor })
	if i < 0 {
		return fmt.Errorf("no %s search API is set up", vendor)
	}
	if f.Searches[i].Off == !on {
		return nil
	}
	f.Searches[i].Off = !on
	return store(f)
}

// ArrangeSearchAPIs puts the search APIs in the order of the vendors named
// (#2286), the order they are tried in. One not named keeps its place
// after them; a name of none set up is refused, so an order sent from a
// page drawn before one was removed changes nothing.
func ArrangeSearchAPIs(order []string) error {
	f, err := read()
	if err != nil {
		return err
	}
	out := make([]SearchAPI, 0, len(f.Searches))
	for _, v := range order {
		i := slices.IndexFunc(f.Searches, func(x SearchAPI) bool { return x.Vendor == v })
		if i < 0 {
			return fmt.Errorf("no %s search API is set up", v)
		}
		if slices.ContainsFunc(out, func(x SearchAPI) bool { return x.Vendor == v }) {
			return fmt.Errorf("%s is named twice", v)
		}
		out = append(out, f.Searches[i])
	}
	for _, a := range f.Searches {
		if !slices.Contains(order, a.Vendor) {
			out = append(out, a)
		}
	}
	f.Searches = out
	return store(f)
}

// RestoreSearchAPIs puts search APIs from a backup in, each replacing the
// one of its vendor here; one that came without a key keeps the key here.
// They are asked in the backup's order, switched on or off as they were
// there (#2286), and one only here is asked after them.
func RestoreSearchAPIs(as []SearchAPI) error {
	if len(as) == 0 {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	out := []SearchAPI{}
	for _, a := range as {
		if _, ok := SearchVendorOf(a.Vendor); !ok || slices.ContainsFunc(out, func(x SearchAPI) bool { return x.Vendor == a.Vendor }) {
			continue
		}
		if i := slices.IndexFunc(f.Searches, func(x SearchAPI) bool { return x.Vendor == a.Vendor }); i >= 0 && a.Key == "" {
			a.Key = f.Searches[i].Key
		}
		out = append(out, a)
	}
	for _, a := range f.Searches {
		if !slices.ContainsFunc(out, func(x SearchAPI) bool { return x.Vendor == a.Vendor }) {
			out = append(out, a)
		}
	}
	f.Searches = out
	return store(f)
}

// MirrorSearchAPIs makes the search APIs exactly these, as sync brings
// them; one that came without a key keeps the key it has here.
func MirrorSearchAPIs(as []SearchAPI) error {
	f, err := read()
	if err != nil {
		return err
	}
	out := []SearchAPI{}
	for _, a := range as {
		if _, ok := SearchVendorOf(a.Vendor); !ok {
			continue
		}
		if i := slices.IndexFunc(f.Searches, func(x SearchAPI) bool { return x.Vendor == a.Vendor }); i >= 0 && a.Key == "" {
			a.Key = f.Searches[i].Key
		}
		out = append(out, a)
	}
	if len(out) == 0 && len(f.Searches) == 0 {
		return nil
	}
	f.Searches = out
	return store(f)
}
