package provider

import (
	"context"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// A Remote magpie's other addresses (evjohnicu on X) are kept as its
// address is, however typed: a bare host and port is http, an API's path
// comes off, each once, none the same as its own; what can't be asked is
// refused. Another provider keeps none.
func TestRemoteMagpieOtherAddressesSaved(t *testing.T) {
	azureHome(t)
	err := Save(Provider{ID: "office", Preset: RemoteMagpiePreset, Key: "sk-magpie-x", Chat: "192.168.1.20:3425",
		Addresses: []string{"100.101.2.3:3425/v1", " ", "http://192.168.1.20:3425/", "HTTP://100.101.2.3:3425/", "https://office.example.ts.net/magpie/v1/messages"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("office")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"http://100.101.2.3:3425", "https://office.example.ts.net/magpie"}; !slices.Equal(p.Addresses, want) {
		t.Errorf("saved %q, want %q", p.Addresses, want)
	}
	// its certificate settings reach the other addresses' hosts too
	if hosts := p.UncheckedHosts(); !slices.Contains(hosts, "https://office.example.ts.net/magpie") {
		t.Errorf("unchecked hosts %q", hosts)
	}
	for _, bad := range [][]string{{"ftp://100.101.2.3"}, {"http://a b:3425"}, {"http://a:1/?b=1"}} {
		if err := Save(Provider{ID: "office", Preset: RemoteMagpiePreset, Key: "sk-magpie-x", Chat: "192.168.1.20:3425", Addresses: bad}); err == nil {
			t.Errorf("%q saved", bad)
		}
	}
	var many []string
	for i := range maxAddresses + 1 {
		many = append(many, fmt.Sprintf("http://10.0.0.%d:3425", i+1))
	}
	if err := Save(Provider{ID: "office", Preset: RemoteMagpiePreset, Key: "sk-magpie-x", Chat: "192.168.1.20:3425", Addresses: many}); err == nil {
		t.Errorf("%d addresses saved", len(many))
	}
	if q := normalize(Provider{ID: "relay", Chat: "https://relay.example.com/v1", Addresses: []string{"http://10.0.0.1"}}); q.Addresses != nil {
		t.Errorf("a relay kept %q", q.Addresses)
	}
}

// What is asked of a Remote magpie (here its cards) is asked at the
// address that takes the connection: its own is on a network this
// computer isn't on (the fake dial refuses every address but loopback).
func TestRemoteMagpieAskedAtAnotherAddress(t *testing.T) {
	peer := &fakePeer{cards: []SubscriptionQuota{{Provider: "codex", Name: "Codex", Kind: "subscription", Windows: []QuotaWindow{{Name: "5h", Used: 40}}}}}
	azureHome(t)
	dialed := dialsOut(t)
	srv := httptest.NewServer(peer)
	t.Cleanup(srv.Close)
	if err := Save(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: "sk-magpie-office", Chat: "http://192.168.77.5:3425", Addresses: []string{srv.URL}}); err != nil {
		t.Fatal(err)
	}
	ForgetRemoteCardsForTest()
	t.Cleanup(ForgetRemoteCardsForTest)
	subs, _, _ := quotas(context.Background())
	var found bool
	for _, q := range subs {
		found = found || q.Provider == "office/codex"
	}
	if !found || peer.gets != 1 {
		t.Fatalf("cards %+v, the other address asked %d times", subs, peer.gets)
	}
	// its own address was tried (by each request then under way), and
	// the next look goes to the one that answered
	before := dialed()
	if len(before) == 0 || !strings.HasPrefix(before[0], "192.168.77.5") {
		t.Fatalf("dialled %q; want its own address tried first", before)
	}
	ForgetRemoteCardsForTest()
	quotas(context.Background())
	if d := dialed(); len(d) != len(before) || peer.gets != 2 {
		t.Errorf("dialled %q after %q, the other address asked %d times; want it asked at once", d, before, peer.gets)
	}
}
