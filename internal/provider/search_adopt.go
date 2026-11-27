package provider

import (
	"context"
	"log"
	"slices"
	"time"
)

// AdoptSignedInSearch puts the search API of a plugin the user is signed
// in to on the search list (Kayphoon, #7632: agy-cli signed in, and
// magpie's web search still said none was set up), while the list has
// never held one: one the user set up, switched off or removed (the last
// of them included, SearchesSet) is theirs, and nothing is added then. It
// gives the API's id, "" when none was added. The gateway asks the search
// APIs only when no provider searches (or first, when the user put them
// first), so the one added adds a way to search and takes none away.
//
// A list that holds a withdrawn plugin's search API gets its successor's
// beside it instead, once (carrySearch).
func AdoptSignedInSearch() (string, error) {
	f, err := read()
	if err != nil {
		return "", err
	}
	if f.SearchesSet || len(f.Searches) > 0 {
		return carrySearch(f)
	}
	for _, v := range AllSearchVendors()[len(SearchVendors):] {
		if !signedInSearch(v) {
			continue
		}
		if err := SetSearchAPI(SearchAPI{Vendor: v.ID}); err != nil {
			return "", err
		}
		return v.ID, nil
	}
	return "", nil
}

// signedInSearch is a plugin's search API that rides its provider's
// sign-in (Auth, no URL of its own), while that provider is signed in.
func signedInSearch(v SearchVendor) bool {
	if v.Auth == "" || v.NeedURL {
		return false
	}
	_, p := SearchSignIn(v)
	return p != nil
}

// searchSuccessors are the search APIs of withdrawn plugins, each with the
// provider whose plugin took their accounts over: the Antigravity plugin's
// Google Search, which Kayphoon (#7632) had set up by hand while agy-cli
// had no search, goes on as agy's own search_web (agy-cli:agy). Its
// direct calls to Google are what got accounts turned away; agy's search
// goes through agy.
var searchSuccessors = map[string]string{"antigravity:antigravity": agyCLIID}

// carrySearch puts the successor's search API in front of a withdrawn
// plugin's on the list (searchSuccessors), once its provider is signed
// in: switched off or mixed as the old one is, the old one kept after it,
// so a search asks agy first and nothing the user set goes away. Each old
// API is carried once (SearchesCarried): the successor removed or moved
// after that stays as the user left it. It gives the successor's id, ""
// when none was added.
func carrySearch(f file) (string, error) {
	added := ""
	changed := false
	for i := 0; i < len(f.Searches); i++ {
		old := f.Searches[i]
		to, ok := searchSuccessors[old.Vendor]
		if !ok || slices.Contains(f.SearchesCarried, old.Vendor) {
			continue
		}
		var v SearchVendor
		for _, c := range AllSearchVendors()[len(SearchVendors):] {
			if c.Auth == to && signedInSearch(c) {
				v = c
				break
			}
		}
		if v.ID == "" {
			continue // not signed in yet: asked again the next time
		}
		f.SearchesCarried = append(f.SearchesCarried, old.Vendor)
		changed = true
		if slices.ContainsFunc(f.Searches, func(a SearchAPI) bool { return a.Vendor == v.ID }) {
			continue // the user set it up already, where they wanted it
		}
		f.Searches = slices.Insert(f.Searches, i, SearchAPI{Vendor: v.ID, Off: old.Off, Mix: old.Mix})
		i++
		added = v.ID
	}
	if !changed {
		return "", nil
	}
	if err := store(f); err != nil {
		return "", err
	}
	return added, nil
}

// KeepSearchAdopted runs AdoptSignedInSearch soon after the gateway starts
// (a plugin's searches are noted once its host has loaded it) and then
// every hour, for a sign-in made before the plugin gave a search API: a
// plugin's update adds its search hook to accounts signed in already.
func KeepSearchAdopted(ctx context.Context) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		adoptSearchLogged()
		t.Reset(time.Hour)
	}
}

func adoptSearchLogged() {
	id, err := AdoptSignedInSearch()
	switch {
	case err != nil:
		log.Printf("adding a signed-in plugin's web search: %s", err)
	case id != "":
		log.Printf("web search: added %s, the search API of a plugin signed in", id)
	}
}
