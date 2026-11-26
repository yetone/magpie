package provider

import (
	"context"
	"log"
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
func AdoptSignedInSearch() (string, error) {
	f, err := read()
	if err != nil || f.SearchesSet || len(f.Searches) > 0 {
		return "", err
	}
	for _, v := range AllSearchVendors()[len(SearchVendors):] {
		if v.Auth == "" || v.NeedURL {
			continue
		}
		if _, p := SearchSignIn(v); p == nil {
			continue
		}
		if err := SetSearchAPI(SearchAPI{Vendor: v.ID}); err != nil {
			return "", err
		}
		return v.ID, nil
	}
	return "", nil
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
		log.Printf("web search: added %s, the search API of a plugin signed in (none was set up)", id)
	}
}
