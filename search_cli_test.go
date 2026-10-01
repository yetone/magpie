package main

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/provider"
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
	if len(as) != 3 || as[0] != (provider.SearchAPI{Vendor: "tavily", Key: "tvly-1"}) || as[1].URL != "https://sx.example.com" ||
		as[2] != (provider.SearchAPI{Vendor: "brave", Key: "b-1", URL: "https://brave2.example.com"}) {
		t.Fatalf("%+v", as)
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
