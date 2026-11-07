package backup

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The search APIs (#419) go in a backup with the providers: their keys
// too, or none without keys, when the keys already here stay.
func TestBackupSearchAPIs(t *testing.T) {
	home(t)
	for _, a := range []provider.SearchAPI{{Vendor: "tavily", Key: "tvly-secret"}, {Vendor: "searxng", URL: "https://sx.example.com"}} {
		if err := provider.SetSearchAPI(a); err != nil {
			t.Fatal(err)
		}
	}
	with, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	without, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if without.Searches == nil || len(*without.Searches) != 2 || (*without.Searches)[0].Key != "" || (*without.Searches)[1].URL != "https://sx.example.com" {
		t.Fatalf("without keys: %+v", without.Searches)
	}
	data, err := Seal(with, "pass")
	if err != nil {
		t.Fatal(err)
	}

	home(t) // another machine
	got, err := Open(data, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(got, All); err != nil {
		t.Fatal(err)
	}
	if as := provider.SearchAPIs(); len(as) != 2 || as[0].Vendor != "tavily" || as[0].Key != "tvly-secret" || as[1].Vendor != "searxng" {
		t.Fatalf("restored %+v", as)
	}

	// one without keys keeps the key here, and one with no key here asks for it
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: "tvly-here"}); err != nil {
		t.Fatal(err)
	}
	*without.Searches = append(*without.Searches, provider.SearchAPI{Vendor: "brave"})
	r, err := Restore(without, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if as := provider.StoredSearchAPIs(); len(as) != 3 || as[0].Key != "tvly-here" {
		t.Fatalf("restored without keys %+v", as)
	}
	if !slices.Contains(r.NeedKey, "Brave Search") || slices.Contains(r.NeedKey, "Tavily") {
		t.Fatalf("need key %v", r.NeedKey)
	}

	// a backup from a magpie before them leaves them alone
	old := without
	old.Searches = nil
	if _, err := Restore(old, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	if len(provider.StoredSearchAPIs()) != 3 {
		t.Fatal("an older backup took the search APIs away")
	}
}

// A backup carries the search APIs' order and which are switched off
// (#2286): put back, they are asked as they were, and one only here after.
func TestBackupSearchAPIsOrderAndOff(t *testing.T) {
	home(t)
	for _, a := range []provider.SearchAPI{{Vendor: "tavily", Key: "t"}, {Vendor: "exa", Key: "e"}, {Vendor: "brave", Key: "b"}} {
		if err := provider.SetSearchAPI(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.ArrangeSearchAPIs([]string{"brave", "exa", "tavily"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SwitchSearchAPI("exa", false); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}

	home(t) // another machine, with its own in another order
	for _, a := range []provider.SearchAPI{{Vendor: "you"}, {Vendor: "tavily", Key: "t"}, {Vendor: "exa", Key: "e"}} {
		if err := provider.SetSearchAPI(a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Restore(b, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	var stored, asked []string
	for _, a := range provider.StoredSearchAPIs() {
		stored = append(stored, a.Vendor)
	}
	for _, a := range provider.SearchAPIs() {
		asked = append(asked, a.Vendor)
	}
	if !slices.Equal(stored, []string{"brave", "exa", "tavily", "you"}) || !slices.Equal(asked, []string{"brave", "tavily", "you"}) {
		t.Fatalf("stored %v, asked %v", stored, asked)
	}
}

// A key switched off in a search API's list (辰林鸭~ on Discord) goes in a
// backup with its keys and comes back off; one without keys carries none
// of them, on or off, and the list here stays whole.
func TestBackupSearchKeysOff(t *testing.T) {
	home(t)
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: "tvly-a,tvly-b"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SwitchSearchKey("tavily", provider.SearchKeyRef("tvly-a"), false); err != nil {
		t.Fatal(err)
	}
	want := []provider.SearchKey{{Key: "tvly-a", Off: true}, {Key: "tvly-b"}}
	with, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	without, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if s := *without.Searches; len(s) != 1 || s[0].HasKeys() {
		t.Fatalf("a backup without keys holds %+v", s)
	}
	data, err := Seal(with, "pass")
	if err != nil {
		t.Fatal(err)
	}
	home(t) // another machine
	got, err := Open(data, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(got, All); err != nil {
		t.Fatal(err)
	}
	if as := provider.StoredSearchAPIs(); len(as) != 1 || !slices.Equal(as[0].SearchKeys(), want) || as[0].Key != "tvly-b" {
		t.Fatalf("restored %+v", as)
	}
	if _, err := Restore(without, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	if as := provider.StoredSearchAPIs(); len(as) != 1 || !slices.Equal(as[0].SearchKeys(), want) {
		t.Fatalf("a backup without keys took keys here away: %+v", as)
	}
}
