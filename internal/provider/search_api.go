package provider

// Search APIs answer a model's web search when no provider can (#419):
// Tavily, Brave Search, Exa, Firecrawl, a SearXNG of the user's. They are
// kept in providers.json beside the providers, their keys with theirs.

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// SearchAPI is a web search API the user gave magpie a key to.
type SearchAPI struct {
	Vendor string `json:"vendor"` // one of SearchVendors
	Key    string `json:"key,omitempty"`
	// URL is where it is: a SearXNG's own, or another address for one of
	// the others (a Firecrawl run by the user); empty is the vendor's.
	URL string `json:"url,omitempty"`
}

// SearchVendor is a search API magpie can ask.
type SearchVendor struct {
	ID, Name string
	Base     string // its address, "" for one the user runs
	KeysURL  string // where its keys are made
}

// SearchVendors are the search APIs magpie can ask, in the order offered.
var SearchVendors = []SearchVendor{
	{ID: "tavily", Name: "Tavily", Base: "https://api.tavily.com", KeysURL: "https://app.tavily.com/home"},
	{ID: "brave", Name: "Brave Search", Base: "https://api.search.brave.com", KeysURL: "https://api-dashboard.search.brave.com/app/keys"},
	{ID: "exa", Name: "Exa", Base: "https://api.exa.ai", KeysURL: "https://dashboard.exa.ai/api-keys"},
	{ID: "firecrawl", Name: "Firecrawl", Base: "https://api.firecrawl.dev", KeysURL: "https://www.firecrawl.dev/app/api-keys"},
	{ID: "searxng", Name: "SearXNG"},
}

// SearchVendorOf is the vendor by its id.
func SearchVendorOf(id string) (SearchVendor, bool) {
	i := slices.IndexFunc(SearchVendors, func(v SearchVendor) bool { return v.ID == id })
	if i < 0 {
		return SearchVendor{}, false
	}
	return SearchVendors[i], true
}

// Name is the vendor's name.
func (a SearchAPI) Name() string {
	if v, ok := SearchVendorOf(a.Vendor); ok {
		return v.Name
	}
	return a.Vendor
}

// Base is the address it is asked at, without a slash at its end.
func (a SearchAPI) Base() string {
	if a.URL != "" {
		return strings.TrimRight(a.URL, "/")
	}
	v, _ := SearchVendorOf(a.Vendor)
	return v.Base
}

// Ready is whether it can be asked: a vendor magpie knows, at an address,
// with a key (a SearXNG may need none).
func (a SearchAPI) Ready() bool {
	_, known := SearchVendorOf(a.Vendor)
	return known && a.Base() != "" && (a.Key != "" || a.Vendor == "searxng")
}

// check says what is wrong with a search API the user gives.
func (a SearchAPI) check() error {
	if _, ok := SearchVendorOf(a.Vendor); !ok {
		var ids []string
		for _, v := range SearchVendors {
			ids = append(ids, v.ID)
		}
		return fmt.Errorf("no search API %q: one of %s", a.Vendor, strings.Join(ids, ", "))
	}
	if a.URL != "" {
		u, err := url.Parse(a.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s: %q is not an http:// or https:// address", a.Name(), a.URL)
		}
	}
	if a.Vendor == "searxng" && a.URL == "" {
		return fmt.Errorf("SearXNG needs its address, as url=https://…")
	}
	if a.Key == "" && a.Vendor != "searxng" {
		return fmt.Errorf("%s needs its API key", a.Name())
	}
	return nil
}

// SearchAPIs are the search APIs that can be asked, in the order they are
// tried.
func SearchAPIs() []SearchAPI {
	return slices.DeleteFunc(slices.Clone(load().Searches), func(a SearchAPI) bool { return !a.Ready() })
}

// StoredSearchAPIs are the search APIs as saved, keys and all.
func StoredSearchAPIs() []SearchAPI { return load().Searches }

// SetSearchAPI adds a search API, last, or changes the one of its vendor
// where it is; a change with no key keeps the key it has.
func SetSearchAPI(a SearchAPI) error {
	a.Vendor = strings.ToLower(strings.TrimSpace(a.Vendor))
	a.Key, a.URL = strings.TrimSpace(a.Key), strings.TrimSpace(a.URL)
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Searches, func(x SearchAPI) bool { return x.Vendor == a.Vendor })
	if i >= 0 && a.Key == "" {
		a.Key = f.Searches[i].Key
	}
	if err := a.check(); err != nil {
		return err
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

// RestoreSearchAPIs puts search APIs from a backup in, each replacing the
// one of its vendor here; one that came without a key keeps the key here.
func RestoreSearchAPIs(as []SearchAPI) error {
	if len(as) == 0 {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	for _, a := range as {
		if _, ok := SearchVendorOf(a.Vendor); !ok {
			continue
		}
		if i := slices.IndexFunc(f.Searches, func(x SearchAPI) bool { return x.Vendor == a.Vendor }); i >= 0 {
			if a.Key == "" {
				a.Key = f.Searches[i].Key
			}
			f.Searches[i] = a
		} else {
			f.Searches = append(f.Searches, a)
		}
	}
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
