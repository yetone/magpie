package provider

import (
	"slices"
	"testing"
)

func TestSeveralKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Team", "sk-team-2222", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "", "sk-team-2222", ""); err == nil {
		t.Fatal("the same key was added twice")
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if p.Key != "sk-personal-1111" || len(ks) != 2 || !ks[0].Active || ks[1].Name != "Team" {
		t.Fatalf("keys %+v", ks)
	}
	if !ks[1].On || len(p.KeysOn()) != 2 {
		t.Fatalf("a new key is on: %+v", ks)
	}
	if err := RenameKey("relay", ks[0].ID, "Personal"); err != nil {
		t.Fatal(err)
	}
	// first in line, the other stays on behind it
	if err := UseKey("relay", ks[1].ID); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-team-2222" || p.KeyName != "Team" || len(p.Keys) != 1 || p.Keys[0] != (KeyAccount{Name: "Personal", Key: "sk-personal-1111"}) {
		t.Fatalf("after use: %+v %q %+v", p.Key, p.KeyName, p.Keys)
	}
	// off: the next one on takes the lead; the last one on stays
	if err := SetKeyOn("relay", keyID("sk-team-2222"), false); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-personal-1111" || len(p.KeysOn()) != 1 || !p.Keys[0].Off || p.Keys[0].Name != "Team" {
		t.Fatalf("after off: %+v", p)
	}
	if err := SetKeyOn("relay", keyID("sk-personal-1111"), false); err == nil {
		t.Fatal("turned off the last key in use")
	}
	if err := RemoveKey("relay", keyID("sk-personal-1111")); err == nil {
		t.Fatal("removed the last key in use")
	}
	if err := SetKeyOn("relay", keyID("sk-team-2222"), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID("sk-personal-1111")); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); len(p.Keys) != 0 || p.Key != "sk-team-2222" || p.KeyName != "Team" {
		t.Fatalf("after remove: %+v", p)
	}
}

func TestKeyProtocol(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-oai-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "", "sk-x-3333", "carrier-pigeon"); err == nil {
		t.Fatal("took an unknown protocol")
	}
	if err := SetKeyProtocol("relay", keyID("sk-oai-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if ks[0].Protocol != Chat || ks[1].Protocol != Anthropic {
		t.Fatalf("keys %+v", ks)
	}
	// it goes with the key when the order changes
	if err := UseKey("relay", keyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-ant-2222" || p.KeyProtocol != Anthropic || p.Keys[0].Protocol != Chat {
		t.Fatalf("after use: %+v", p)
	}
	q := p.WithKey(p.Keys[0])
	if q.Key != "sk-oai-1111" || q.Chat == "" || q.Anthropic != "" || q.Responses != "" {
		t.Fatalf("with the OpenAI key: %+v", q)
	}
	if q := p.WithKey(KeyAccount{Key: "k"}); q.Chat == "" || q.Anthropic == "" {
		t.Fatalf("a key for any protocol: %+v", q)
	}
}

// A relay's key made for its Gemini API (corddis_114 on Discord): it can
// be set so, and is then used on the Gemini API only, while a key made for
// another protocol is never sent to the Gemini API.
func TestKeyProtocolGemini(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Gemini: "https://relay.test/v1beta", Key: "sk-ant-1111", KeyProtocol: Anthropic}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "gemini", "sk-gem-2222", Gemini); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "", "sk-oai-3333", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-oai-3333"), Gemini); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-oai-3333"), Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if ks[1].Protocol != Gemini || ks[2].Protocol != Chat {
		t.Fatalf("keys %+v", ks)
	}
	if q := p.WithKey(p.Keys[0]); !slices.Equal(q.Speaks(), []Protocol{Gemini}) || q.Base(Gemini) != "https://relay.test/v1beta" {
		t.Fatalf("the Gemini key speaks %v", q.Speaks())
	}
	for _, k := range []KeyAccount{p.first(), p.Keys[1]} {
		if q := p.WithKey(k); q.Base(Gemini) != "" || len(q.Speaks()) == 0 {
			t.Fatalf("the %s key speaks %v", k.Protocol, q.Speaks())
		}
	}
	if q := p.WithKey(KeyAccount{Key: "k"}); q.Base(Gemini) == "" || len(q.Speaks()) != 3 {
		t.Fatalf("a key for any protocol speaks %v", q.Speaks())
	}
}
