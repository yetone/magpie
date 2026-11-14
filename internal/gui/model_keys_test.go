package gui

import (
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A provider whose keys each list their own models (cliddO11 on X) says
// in its editor which keys list a model, when not all of them in use do:
// a model every key lists, one only an off key lists, and one of a list
// not asked per key carry none.
func TestModelSaysItsKeysWhenTheyDiffer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a, b, c, d := provider.KeyID("sk-aaaa1111"), provider.KeyID("sk-bbbb2222"), provider.KeyID("sk-cccc3333"), provider.KeyID("sk-dddd4444")
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-aaaa1111", Keys: []provider.KeyAccount{
		{Key: "sk-bbbb2222", Name: "Team"}, {Key: "sk-cccc3333", Off: true}, {Key: "sk-dddd4444"},
	}}
	if err := catalog.SaveLive("relay", "https://relay.example/v1", []catalog.Model{
		{ID: "every", Keys: []string{d, c, b, a}},
		{ID: "two", Keys: []string{d, a}},
		{ID: "one", Keys: []string{b}},
		{ID: "off-only", Keys: []string{c}},
		{ID: "unasked"},
	}); err != nil {
		t.Fatal(err)
	}
	got := map[string]modelJSON{}
	for _, m := range providerInfo(p, nil).Models {
		got[m.ID] = m
	}
	want := map[string][]string{"every": nil, "two": {a, d}, "one": {b}, "off-only": nil, "unasked": nil}
	for id, keys := range want {
		m, ok := got[id]
		if !ok {
			t.Fatalf("%s not listed: %v", id, got)
		}
		if !slices.Equal(m.Keys, keys) || m.KeyCount != 0 {
			t.Errorf("%s: keys %v (count %d), want %v", id, m.Keys, m.KeyCount, keys)
		}
	}

	// one key in use: nothing to tell apart
	p.Keys = nil
	for _, m := range providerInfo(p, nil).Models {
		if m.Keys != nil {
			t.Errorf("single key: %s says keys %v", m.ID, m.Keys)
		}
	}
}

// Past keysShown a model names no more keys and says how many: a provider
// of a thousand keys sends no thousand ids with each model.
func TestModelKeysCapped(t *testing.T) {
	var on []string
	at := map[string]int{}
	for i := range 1000 {
		id := fmt.Sprintf("k%04d", i)
		at[id] = len(on)
		on = append(on, id)
	}
	ids, n := keysApart(slices.Concat(on[:999], []string{"gone"}), on, at)
	if n != 999 || !slices.Equal(ids, on[:keysShown]) {
		t.Fatalf("999 of 1000: %v, %d", ids, n)
	}
	if ids, n := keysApart(on, on, at); ids != nil || n != 0 {
		t.Fatalf("every key: %v, %d", ids, n)
	}
	if ids, n := keysApart([]string{"k0002", "k0001", "k0002"}, on, at); n != 2 || !slices.Equal(ids, []string{"k0001", "k0002"}) {
		t.Fatalf("in the keys' order, once each: %v, %d", ids, n)
	}
}
