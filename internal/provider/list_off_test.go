package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// axelburks on Discord: Volcengine Ark's list names models by ids that
// can't be asked for, and each is asked by an endpoint id the user made.
// With its list off, a provider's vendor is never asked for a list, by a
// Fetch, a refetch, the add form's List, every list's Refresh, a test or a
// new key's prefill; a list fetched before and models.dev's are not
// offered, and the ids the user added are served alone.
func TestListOffAsksNoListAndServesTheIDsAdded(t *testing.T) {
	prefillHome(t)
	var lists atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			lists.Add(1)
			_, _ = rw.Write([]byte(groupAll))
			return
		}
		http.Error(rw, "no", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	// fetched once while the list was on
	if err := Save(Provider{ID: "ark", Name: "Ark", Chat: srv.URL + "/v1", Key: "ark-k", Catalog: "openai"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("ark")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil || lists.Load() != 1 {
		t.Fatalf("on: fetch err %v, asked %d times", err, lists.Load())
	}
	p.ListOff, p.Models = true, []string{"ep-20261108-abcde"}
	p.Keys = []KeyAccount{{Key: "ark-k2"}}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, err = Find("ark"); err != nil || !p.ListOff {
		t.Fatalf("saved: %+v %v", p, err)
	}
	lists.Store(0)
	ctx := context.Background()
	if _, err := p.Fetch(ctx); !errors.Is(err, ErrListOff) {
		t.Fatalf("Fetch: %v, want ErrListOff", err)
	}
	if _, _, err := p.Refetch(ctx); !errors.Is(err, ErrListOff) {
		t.Fatalf("Refetch: %v", err)
	}
	if _, err := p.List(ctx); !errors.Is(err, ErrListOff) {
		t.Fatalf("List: %v", err)
	}
	RefreshLists(ctx)
	p.Test(ctx)
	PrefillNewKeys(ctx, map[string]map[string]bool{"ark": {}}, "ark")
	if n := lists.Load(); n != 0 {
		t.Fatalf("list off: the vendor's list was asked %d times", n)
	}
	if p.ListAsks() {
		t.Fatal("ListAsks: a resting key would be probed with a list")
	}
	if _, ok := p.Fetched(); ok {
		t.Fatal("the list fetched before is still read")
	}
	if av := p.Available(); len(av) != 0 {
		t.Fatalf("Available = %v, want none (not models.dev's, not the old list)", modelIDs(av))
	}
	if got := modelIDs(p.Exposed()); !slices.Equal(got, []string{"ep-20261108-abcde"}) {
		t.Fatalf("Exposed = %v, want the id added alone", got)
	}
	if !ServesModel(*p, "ep-20261108-abcde") || ServesModel(*p, "gpt-5.5") {
		t.Fatal("the id added is served, the old list's model isn't")
	}

	// on again: the list is asked for as before, once a key
	p.ListOff = false
	if _, err := p.Fetch(ctx); err != nil || lists.Load() == 0 {
		t.Fatalf("on again: err %v, asked %d times", err, lists.Load())
	}
}

// A signed-in account and a plugin's provider list their models as their
// sign-in does: ListOff is a key's provider's alone.
func TestListOffIsAKeysProvidersAlone(t *testing.T) {
	if (Provider{ListOff: true, Account: &Account{}}).ListsOff() {
		t.Fatal("an account's list went off")
	}
	if !(Provider{ListOff: true, Chat: "https://x/v1"}).ListsOff() {
		t.Fatal("a key's provider's list stayed on")
	}
}
