package cli

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// magpie search add, with a key and an address in either order, and rm.
func TestSearchCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := searchCmd([]string{"add", "tavily"}); err == nil {
		t.Error("tavily added without a key")
	}
	if err := searchCmd([]string{"add", "google", "k"}); err == nil {
		t.Error("an unknown search API added")
	}
	if err := searchCmd([]string{"add", "searxng"}); err == nil {
		t.Error("SearXNG added without its address")
	}
	for _, args := range [][]string{{"add", "Tavily", "tvly-1"}, {"add", "searxng", "url=https://sx.example.com"}, {"add", "brave", "url=https://brave.example.com", "b-1"}} {
		if err := searchCmd(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	// a new address alone keeps the key
	if err := searchCmd([]string{"add", "brave", "url=https://brave2.example.com"}); err != nil {
		t.Fatal(err)
	}
	as := provider.SearchAPIs()
	if len(as) != 3 || !reflect.DeepEqual(as[0], provider.SearchAPI{Vendor: "tavily", Key: "tvly-1"}) || as[1].URL != "https://sx.example.com" ||
		!reflect.DeepEqual(as[2], provider.SearchAPI{Vendor: "brave", Key: "b-1", URL: "https://brave2.example.com"}) {
		t.Fatalf("%+v", as)
	}
	// a new key alone keeps the base URL the user set (#1580); url= with
	// nothing puts the vendor's own back
	if err := searchCmd([]string{"add", "brave", "b-2"}); err != nil {
		t.Fatal(err)
	}
	if as := provider.SearchAPIs(); !reflect.DeepEqual(as[2], provider.SearchAPI{Vendor: "brave", Key: "b-2", URL: "https://brave2.example.com"}) {
		t.Fatalf("a new key took the base URL away: %+v", as[2])
	}
	if err := searchCmd([]string{"add", "brave", "url="}); err != nil {
		t.Fatal(err)
	}
	if as := provider.SearchAPIs(); !reflect.DeepEqual(as[2], provider.SearchAPI{Vendor: "brave", Key: "b-2"}) || as[2].Base() != "https://api.search.brave.com" {
		t.Fatalf("url= kept the base URL: %+v", as[2])
	}
	for _, bad := range []string{"url=ftp://x.example.com", "url=https://x.example.com/?k=1", "url=https://x.example.com/#a", "url=x.example.com"} {
		if err := searchCmd([]string{"add", "tavily", "tvly-2", bad}); err == nil {
			t.Errorf("%s taken", bad)
		}
	}
	if err := searchCmd(nil); err != nil {
		t.Fatal(err)
	}
	if err := searchCmd([]string{"rm", "tavily"}); err != nil {
		t.Fatal(err)
	}
	if err := searchCmd([]string{"rm", "tavily"}); err == nil {
		t.Error("removed twice")
	}
	if as := provider.SearchAPIs(); len(as) != 2 || as[0].Vendor != "searxng" {
		t.Fatalf("after rm %+v", as)
	}
}

// magpie search add you, with no key, adds You.com's free search (#1626).
func TestSearchCLIYouWithoutKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := searchCmd([]string{"add", "you"}); err != nil {
		t.Fatal(err)
	}
	if as := provider.SearchAPIs(); len(as) != 1 || !reflect.DeepEqual(as[0], provider.SearchAPI{Vendor: "you"}) {
		t.Fatalf("%+v", as)
	}
}

// magpie search off keeps an API, key and all, out of every search, on
// puts it back, and move puts it at another place in the order (#2286).
func TestSearchCLIOffAndMove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"add", "tavily", "tvly-1"}, {"add", "exa", "e-1"}, {"add", "brave", "b-1"}} {
		if err := searchCmd(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	vendors := func(as []provider.SearchAPI) (vs []string) {
		for _, a := range as {
			vs = append(vs, a.Vendor)
		}
		return vs
	}
	if err := searchCmd([]string{"off", "exa"}); err != nil {
		t.Fatal(err)
	}
	if got := vendors(provider.SearchAPIs()); !slices.Equal(got, []string{"tavily", "brave"}) {
		t.Fatalf("exa off, asked %v", got)
	}
	if s := provider.StoredSearchAPIs(); len(s) != 3 || s[1].Key != "e-1" {
		t.Fatalf("off lost the key: %+v", s)
	}
	// a new key leaves it off
	if err := searchCmd([]string{"add", "exa", "e-2"}); err != nil {
		t.Fatal(err)
	}
	if got := vendors(provider.SearchAPIs()); !slices.Equal(got, []string{"tavily", "brave"}) {
		t.Fatalf("a new key switched exa on: %v", got)
	}
	if err := searchCmd([]string{"off", "google"}); err == nil {
		t.Error("switched off one not set up")
	}
	if err := searchCmd([]string{"on", "exa"}); err != nil {
		t.Fatal(err)
	}
	if err := searchCmd([]string{"move", "brave", "1"}); err != nil {
		t.Fatal(err)
	}
	if got := vendors(provider.SearchAPIs()); !slices.Equal(got, []string{"brave", "tavily", "exa"}) {
		t.Fatalf("after move %v", got)
	}
	if err := searchCmd([]string{"move", "brave", "3"}); err != nil {
		t.Fatal(err)
	}
	if got := vendors(provider.SearchAPIs()); !slices.Equal(got, []string{"tavily", "exa", "brave"}) {
		t.Fatalf("after move to last %v", got)
	}
	for _, bad := range [][]string{{"move", "brave", "4"}, {"move", "brave", "0"}, {"move", "brave"}, {"move", "google", "1"}} {
		if err := searchCmd(bad); err == nil {
			t.Errorf("%v taken", bad)
		}
	}
}

// magpie search count (#5545, mdwsk88): a number from 1 to 20 is written,
// "default" puts 6 back, and a flag, a word or a number out of range is
// refused with nothing written.
func TestSearchCLICount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := searchCmd([]string{"count"}); err != nil {
		t.Fatal(err)
	}
	if n := settings.Load().SearchPages(); n != 6 {
		t.Fatalf("by default %d", n)
	}
	for _, c := range []struct {
		arg  string
		want int
	}{{"12", 12}, {"20", 20}, {"1", 1}, {"6", 6}, {"default", 0}} {
		if err := searchCmd([]string{"count", c.arg}); err != nil {
			t.Fatalf("%s: %v", c.arg, err)
		}
		if got := settings.Load().SearchCount; got != c.want {
			t.Errorf("count %s wrote %d, want %d", c.arg, got, c.want)
		}
	}
	if err := searchCmd([]string{"count", "9"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"count", "0"}, {"count", "21"}, {"count", "-3"}, {"count", "--help"}, {"count", "many"}, {"count", "7.5"}, {"count", "8", "9"}} {
		if err := searchCmd(bad); err == nil {
			t.Errorf("%v taken", bad)
		}
		if got := settings.Load().SearchCount; got != 9 {
			t.Fatalf("%v wrote %d", bad, got)
		}
	}
	s := settings.Load()
	s.SearchCount = 21
	if err := settings.Save(s); err == nil {
		t.Error("settings took a count of 21")
	}
}

// magpie search mix names two or more search APIs to ask together, and
// off puts them back in order (#2845).
func TestSearchCLIMix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"add", "brave", "b-1"}, {"add", "exa", "e-1"}, {"add", "you"}} {
		if err := searchCmd(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, bad := range [][]string{{"mix"}, {"mix", "brave"}, {"mix", "brave,google"}, {"mix", "brave,brave"}} {
		if err := searchCmd(bad); err == nil {
			t.Errorf("%v taken", bad)
		}
	}
	mixed := func() (out []string) {
		for _, a := range provider.StoredSearchAPIs() {
			if a.Mix {
				out = append(out, a.Vendor)
			}
		}
		return out
	}
	if err := searchCmd([]string{"mix", "You, Brave"}); err != nil {
		t.Fatal(err)
	}
	if got := mixed(); !slices.Equal(got, []string{"brave", "you"}) {
		t.Fatalf("mixed %v", got)
	}
	if err := searchCmd(nil); err != nil {
		t.Fatal(err)
	}
	if err := searchCmd([]string{"mix", "off"}); err != nil {
		t.Fatal(err)
	}
	if got := mixed(); len(got) != 0 {
		t.Fatalf("after mix off %v", got)
	}
}
