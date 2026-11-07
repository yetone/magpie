package provider

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func storedTavily(t *testing.T) SearchAPI {
	t.Helper()
	for _, a := range StoredSearchAPIs() {
		if a.Vendor == "tavily" {
			return a
		}
	}
	t.Fatal("no tavily")
	return SearchAPI{}
}

// A search API's keys are a list (辰林鸭~ on Discord): added one or several
// at once, switched off one by one and kept, put in order and removed,
// each named by its ref. Key stays the keys that are on, so the gateway
// and an older magpie never ask one switched off; the list is written
// only while a key is off.
func TestSearchKeyList(t *testing.T) {
	claudeHome(t)
	if err := SetSearchAPI(SearchAPI{Vendor: "tavily", Key: "tvly-a, tvly-b"}); err != nil {
		t.Fatal(err)
	}
	// a key or a list saved before carries over as it was, every key on
	a := storedTavily(t)
	if want := []SearchKey{{Key: "tvly-a"}, {Key: "tvly-b"}}; !reflect.DeepEqual(a.SearchKeys(), want) || a.KeyList != nil {
		t.Fatalf("old keys: %+v, list %+v", a.SearchKeys(), a.KeyList)
	}
	added, had, err := AddSearchKeys("tavily", "tvly-b\ntvly-c;tvly-d")
	if err != nil || added != 2 || had != 1 {
		t.Fatalf("added %d, had %d, %v", added, had, err)
	}
	ref := SearchKeyRef
	if err := SwitchSearchKey("tavily", ref("tvly-b"), false); err != nil {
		t.Fatal(err)
	}
	a = storedTavily(t)
	if a.Key != "tvly-a,tvly-c,tvly-d" || !reflect.DeepEqual(a.Keys(), []string{"tvly-a", "tvly-c", "tvly-d"}) {
		t.Fatalf("a key switched off is still asked: %q", a.Key)
	}
	if want := []SearchKey{{Key: "tvly-a"}, {Key: "tvly-b", Off: true}, {Key: "tvly-c"}, {Key: "tvly-d"}}; !reflect.DeepEqual(a.SearchKeys(), want) {
		t.Fatalf("list %+v", a.SearchKeys())
	}
	if k, err := SearchKeyOf("tavily", ref("tvly-b")); err != nil || k != "tvly-b" {
		t.Fatalf("an off key's value: %q %v", k, err)
	}
	// put in order: those named first, the rest after in their places
	if err := ArrangeSearchKeys("tavily", []string{ref("tvly-d"), ref("tvly-b")}); err != nil {
		t.Fatal(err)
	}
	a = storedTavily(t)
	if want := []SearchKey{{Key: "tvly-d"}, {Key: "tvly-b", Off: true}, {Key: "tvly-a"}, {Key: "tvly-c"}}; !reflect.DeepEqual(a.SearchKeys(), want) || a.Key != "tvly-d,tvly-a,tvly-c" {
		t.Fatalf("arranged %+v, %q", a.SearchKeys(), a.Key)
	}
	for _, bad := range [][]string{{ref("tvly-a"), ref("tvly-a")}, {ref("tvly-zz")}} {
		if err := ArrangeSearchKeys("tavily", bad); err == nil {
			t.Errorf("order %v taken", bad)
		}
	}
	if err := RemoveSearchKey("tavily", ref("tvly-a")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSearchKey("tavily", ref("tvly-a")); err == nil {
		t.Error("a key removed twice")
	}
	// the last key on can't be switched off or removed: the API's own
	// switch, or Remove, is for that
	if err := SwitchSearchKey("tavily", ref("tvly-d"), false); err != nil {
		t.Fatal(err)
	}
	if err := SwitchSearchKey("tavily", ref("tvly-c"), false); err == nil || !strings.Contains(err.Error(), "needs a key on") {
		t.Fatalf("every key off: %v", err)
	}
	if err := RemoveSearchKey("tavily", ref("tvly-c")); err == nil {
		t.Fatal("the last key on removed")
	}
	if a := storedTavily(t); a.Key != "tvly-c" || len(a.SearchKeys()) != 3 {
		t.Fatalf("a refused change was saved: %+v", a)
	}
	// a change of address with no key keeps every key, those off too
	if err := SetSearchAPI(SearchAPI{Vendor: "tavily", URL: "https://t.example.com"}); err != nil {
		t.Fatal(err)
	}
	if a := storedTavily(t); a.URL != "https://t.example.com" || len(a.SearchKeys()) != 3 || a.Key != "tvly-c" {
		t.Fatalf("an address took keys away: %+v", a)
	}
	// every key on again: the file is as an older magpie writes it
	for _, k := range []string{"tvly-b", "tvly-d"} {
		if err := SwitchSearchKey("tavily", ref(k), true); err != nil {
			t.Fatal(err)
		}
	}
	if a := storedTavily(t); a.KeyList != nil || a.Key != "tvly-d,tvly-b,tvly-c" {
		t.Fatalf("all on: %+v", a)
	}
	b, err := os.ReadFile(Path())
	if err == nil && strings.Contains(string(b), "keyList") {
		t.Errorf("a list with every key on is written: %s", b)
	}
	// a key typed in place of the list is the list, every one on
	if err := SetSearchAPI(SearchAPI{Vendor: "tavily", Key: "tvly-new"}); err != nil {
		t.Fatal(err)
	}
	if a := storedTavily(t); !reflect.DeepEqual(a.SearchKeys(), []SearchKey{{Key: "tvly-new"}}) {
		t.Fatalf("replaced %+v", a.SearchKeys())
	}
}

// A Key written by a magpie that knows no list (an older one, or one this
// one syncs with) is the keys on, in its order; a key the list had off
// that it doesn't name stays, off, after them.
func TestSearchKeysKeyWins(t *testing.T) {
	a := SearchAPI{Vendor: "tavily", Key: "tvly-x,tvly-c", KeyList: []SearchKey{{Key: "tvly-c"}, {Key: "tvly-b", Off: true}}}
	want := []SearchKey{{Key: "tvly-x"}, {Key: "tvly-c"}, {Key: "tvly-b", Off: true}}
	if got := a.SearchKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	// and one it switched on again is on, once
	a = SearchAPI{Vendor: "tavily", Key: "tvly-c,tvly-b", KeyList: []SearchKey{{Key: "tvly-c"}, {Key: "tvly-b", Off: true}}}
	if got := a.SearchKeys(); !reflect.DeepEqual(got, []SearchKey{{Key: "tvly-c"}, {Key: "tvly-b"}}) {
		t.Fatalf("%+v", got)
	}
	if !a.HasKeys() || a.WithoutKeys().HasKeys() {
		t.Fatal("WithoutKeys")
	}
	var c SearchAPI
	c.KeepKeysOf(a)
	if c.Key != a.Key || !reflect.DeepEqual(c.KeyList, a.KeyList) {
		t.Fatalf("kept %+v", c)
	}
	if SearchKeyRef("tvly-c") == SearchKeyRef("tvly-b") || strings.Contains(SearchKeyRef("tvly-c"), "tvly") {
		t.Fatal("ref")
	}
}
