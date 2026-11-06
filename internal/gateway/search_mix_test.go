package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// mixAPIs is a fake of Tavily, Exa and Brave, each finding the pages set
// for it. The vendors in together each wait, a while at most, until all of
// them have been asked, so only APIs asked at once answer in time.
type mixAPIs struct {
	*httptest.Server
	mu       sync.Mutex
	asked    []string
	pages    map[string][]string // vendor: the URLs of the pages it finds
	down     map[string]bool
	together []string
	arrived  int
	all      chan struct{}
}

func newMixAPIs(t *testing.T) *mixAPIs {
	f := &mixAPIs{pages: map[string][]string{}, down: map[string]bool{}, all: make(chan struct{})}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vendor, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		f.mu.Lock()
		f.asked = append(f.asked, vendor)
		pages, down := f.pages[vendor], f.down[vendor]
		wait := slices.Contains(f.together, vendor)
		if wait {
			if f.arrived++; f.arrived == len(f.together) {
				close(f.all)
			}
		}
		f.mu.Unlock()
		if wait {
			select {
			case <-f.all:
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
				http.Error(w, `{"error":"asked alone"}`, http.StatusInternalServerError)
				return
			}
		}
		if down {
			http.Error(w, `{"error":"down"}`, http.StatusInternalServerError)
			return
		}
		var items []string
		for i, u := range pages {
			items = append(items, fmt.Sprintf(`{"title":"%s %d","url":%q,"content":"found","text":"found","description":"found"}`, vendor, i+1, u))
		}
		list := "[" + strings.Join(items, ",") + "]"
		w.Header().Set("Content-Type", "application/json")
		if vendor == "brave" {
			io.WriteString(w, `{"web":{"results":`+list+`}}`)
		} else {
			io.WriteString(w, `{"results":`+list+`}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// set adds the vendors' search APIs at the fake, in that order.
func (f *mixAPIs) set(t *testing.T, vendors ...string) {
	t.Helper()
	keys := map[string]string{"tavily": "tvly-k", "exa": "exa-k", "brave": "brave-k"}
	for _, v := range vendors {
		if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: v, Key: keys[v], URL: f.URL + "/" + v}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *mixAPIs) askedOf() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

func mixSet(t *testing.T, vendors ...string) {
	t.Helper()
	if err := provider.MixSearchAPIs(vendors); err != nil {
		t.Fatal(err)
	}
}

func mixHitURLs(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.URL)
	}
	return out
}

// Search APIs marked mix are asked at once (#2845). The model gets their
// pages in turn, in the APIs' order, a page two of them found once; one
// not mixed isn't asked while they find something.
func TestSearchAPIsMixedAskedTogether(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newMixAPIs(t)
	f.pages["tavily"] = []string{"https://a.example/1", "https://go.dev/dl/", "https://a.example/3"}
	f.pages["exa"] = []string{"https://b.example/1", "https://GO.dev/dl#files", "https://b.example/3"}
	f.pages["brave"] = []string{"https://c.example/1"}
	f.together = []string{"tavily", "exa"}
	f.set(t, "tavily", "exa", "brave")
	mixSet(t, "exa", "tavily")
	said, hits, err := New().apiSearch(context.Background(), "latest go")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://a.example/1", "https://b.example/1", "https://go.dev/dl/", "https://a.example/3", "https://b.example/3"}
	if got := mixHitURLs(hits); !slices.Equal(got, want) {
		t.Fatalf("pages %v, want %v\n%s", got, want, said)
	}
	if asked := f.askedOf(); len(asked) != 2 || slices.Contains(asked, "brave") {
		t.Errorf("asked %v", asked)
	}
}

// One mixed API down leaves the other's pages, with no error.
func TestSearchAPIsMixedOneDown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newMixAPIs(t)
	f.pages["tavily"] = []string{"https://a.example/1"}
	f.pages["exa"] = []string{"https://b.example/1"}
	f.down["tavily"] = true
	f.set(t, "tavily", "exa")
	mixSet(t, "tavily", "exa")
	_, hits, err := New().apiSearch(context.Background(), "latest go")
	if err != nil || !slices.Equal(mixHitURLs(hits), []string{"https://b.example/1"}) {
		t.Fatalf("%v %v", mixHitURLs(hits), err)
	}
}

// With every mixed API down, the ones not mixed are asked, in order.
func TestSearchAPIsMixedAllDownAsksTheRest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newMixAPIs(t)
	f.pages["brave"] = []string{"https://c.example/1"}
	f.down["tavily"], f.down["exa"] = true, true
	f.set(t, "tavily", "exa", "brave")
	mixSet(t, "tavily", "exa")
	_, hits, err := New().apiSearch(context.Background(), "latest go")
	if err != nil || !slices.Equal(mixHitURLs(hits), []string{"https://c.example/1"}) {
		t.Fatalf("%v %v", mixHitURLs(hits), err)
	}
	if asked := f.askedOf(); len(asked) != 3 || asked[2] != "brave" {
		t.Errorf("asked %v", asked)
	}
}

// A mixed search gives the model two APIs' worth of the count it asks for
// at most, each API's first ones first: 12 by default, twice the count the
// user set (#5545) or the search named, and never more than one search may
// name (20).
func TestSearchAPIsMixedCapped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// the package's tests share one settings.json: the count is put back
	was := settings.Load().SearchCount
	t.Cleanup(func() {
		s := settings.Load()
		s.SearchCount = was
		settings.Save(s)
	})
	f := newMixAPIs(t)
	for _, v := range []string{"tavily", "exa", "brave"} {
		for i := range searchHitsMost {
			f.pages[v] = append(f.pages[v], fmt.Sprintf("https://%s.example/%d", v, i+1))
		}
	}
	f.set(t, "tavily", "exa", "brave")
	mixSet(t, "tavily", "exa", "brave")
	for _, c := range []struct {
		set, named, want int
	}{{0, 0, 12}, {9, 0, 18}, {15, 0, 20}, {9, 3, 6}} {
		s := settings.Load()
		s.SearchCount = c.set
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if c.named > 0 {
			ctx = withSearchCount(ctx, c.named)
		}
		_, hits, err := New().apiSearch(ctx, "latest go")
		if err != nil {
			t.Fatal(err)
		}
		got := mixHitURLs(hits)
		if len(got) != c.want || !slices.Equal(got[:3], []string{"https://tavily.example/1", "https://exa.example/1", "https://brave.example/1"}) {
			t.Errorf("count set %d, named %d: %d pages, want %d: %v", c.set, c.named, len(got), c.want, got)
		}
	}
}

// MixSearchAPIs takes two or more APIs set up, each named once, and a new
// key leaves the mark. A mixed API switched off isn't asked, and with one
// mixed left a search goes in order. None puts every one back.
func TestMixSearchAPIs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newMixAPIs(t)
	f.pages["tavily"] = []string{"https://a.example/1"}
	f.pages["exa"] = []string{"https://b.example/1"}
	f.set(t, "tavily", "exa")
	for _, bad := range [][]string{{"tavily"}, {"tavily", "tavily"}, {"tavily", "google"}} {
		if err := provider.MixSearchAPIs(bad); err == nil {
			t.Errorf("%v mixed", bad)
		}
	}
	mixSet(t, "tavily", "exa")
	// a new key, as Settings sends it, keeps the mark
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: "tvly-2", URL: f.URL + "/tavily"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range provider.StoredSearchAPIs() {
		if !a.Mix {
			t.Errorf("%s lost its mark", a.Vendor)
		}
	}
	if err := provider.SwitchSearchAPI("exa", false); err != nil {
		t.Fatal(err)
	}
	_, hits, err := New().apiSearch(context.Background(), "latest go")
	if err != nil || !slices.Equal(mixHitURLs(hits), []string{"https://a.example/1"}) || slices.Contains(f.askedOf(), "exa") {
		t.Fatalf("exa off: %v %v, asked %v", mixHitURLs(hits), err, f.askedOf())
	}
	mixSet(t)
	for _, a := range provider.StoredSearchAPIs() {
		if a.Mix {
			t.Errorf("%s still mixed", a.Vendor)
		}
	}
}
