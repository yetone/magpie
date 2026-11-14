package provider

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Many keys pasted at once (361 on Discord: a provider with many keys was
// tedious to fill one by one): split one a line, or by commas or spaces,
// trimmed of quotes, each once.
func TestSplitKeys(t *testing.T) {
	got := SplitKeys("sk-a\nsk-b, sk-c;\t\"sk-d\"\r\n\nsk-a  sk-e，sk-f　'sk-g'")
	want := []string{"sk-a", "sk-b", "sk-c", "sk-d", "sk-e", "sk-f", "sk-g"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if len(SplitKeys(" \n , ")) != 0 {
		t.Fatal("blank text has keys")
	}
}

// AddKeys saves the new ones after the provider's own, on, with the
// protocol given, in one save, and passes over the keys it has.
func TestAddKeys(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := Save(Provider{ID: "bulk", Name: "Bulk", Chat: "https://example.invalid/v1", Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	// the key it has is passed over, the others go after it
	added, had, err := AddKeys("bulk", []string{"k1", "k2", "k2", "k3"}, "")
	if err != nil || added != 2 || had != 2 {
		t.Fatalf("added %d had %d: %v", added, had, err)
	}
	p, _ := Find("bulk")
	if p.Key != "k1" || len(p.Keys) != 2 || p.Keys[0].Key != "k2" || p.Keys[1].Key != "k3" || p.Keys[0].Off {
		t.Fatalf("saved %q %+v", p.Key, p.Keys)
	}
	added, had, err = AddKeys("bulk", []string{"k3", "k4"}, Anthropic)
	if err != nil || added != 1 || had != 1 {
		t.Fatalf("added %d had %d: %v", added, had, err)
	}
	p, _ = Find("bulk")
	if l := p.Keys[len(p.Keys)-1]; l.Key != "k4" || l.Protocol != Anthropic {
		t.Fatalf("last %+v", l)
	}
	if _, _, err := AddKeys("bulk", []string{"k1", "k4"}, ""); err == nil {
		t.Fatal("only keys it had: no error")
	}
	if _, _, err := AddKeys("bulk", nil, ""); err == nil {
		t.Fatal("no keys: no error")
	}
	// the rest key the gateway gives each: the id alone with one key on
	if k := (Provider{ID: "x", Key: "a"}).KeyList()[0]; (Provider{ID: "x", Key: "a"}).RestKey(k) != "x" {
		t.Fatal("one key rests as the provider")
	}
	if k := p.KeyList()[1]; p.RestKey(k) != "bulk#"+KeyID("k2") {
		t.Fatalf("rest key %q", p.RestKey(k))
	}
}

// AllKeys gives every key a provider holds, on or off, in its list's
// order, each once, for another magpie to take in with SplitKeys
// (@emilochen on X); a signed-in account has none.
func TestAllKeys(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := Save(Provider{ID: "bulk", Name: "Bulk", Chat: "https://example.invalid/v1", Key: "k1", KeyName: "Team",
		Keys: []KeyAccount{{Key: "k2", Off: true}, {Name: "Spare", Key: "k3", Protocol: Anthropic}, {Key: "k1"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := AllKeys("bulk")
	if want := []string{"k1", "k2", "k3"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
	if !reflect.DeepEqual(SplitKeys(strings.Join(got, "\n")), got) {
		t.Fatal("what it gives is not what Paste several reads")
	}
	if _, err := AllKeys("nope"); err == nil {
		t.Fatal("no provider: no error")
	}
}
