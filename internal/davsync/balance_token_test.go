package davsync

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// A computer sending no keys leaves the server's balance token in place,
// just as it leaves its API keys. Sending keys replaces the token too.
func TestTakeBalanceToken(t *testing.T) {
	for _, c := range []struct {
		name                     string
		serverKeys, incomingKeys bool
		token, want              string
	}{
		{"keyless update", true, false, "", "balance-server"},
		{"explicit token", true, false, "balance-new", "balance-new"},
		{"with keys", true, true, "balance-new", "balance-new"},
		{"with keys without token", true, true, "", ""},
		{"keyless server", false, false, "", ""},
		{"adding keys", false, true, "balance-new", "balance-new"},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := provider.Provider{ID: "relay", Name: "Old", Chat: "https://old.example.com/v1"}
			if c.serverKeys {
				server.Key, server.BalanceToken = "sk-server", "balance-server"
			}
			incoming := provider.Provider{ID: "relay", Name: "New", Chat: "https://new.example.com/v1", BalanceToken: c.token}
			if c.incomingKeys {
				incoming.Key = "sk-new"
			}
			to := backup.Bundle{Keys: c.serverKeys, Providers: []provider.Provider{server}}
			from := backup.Bundle{Keys: c.incomingKeys, Providers: []provider.Provider{incoming}}
			take(&to, from, "providers")
			if len(to.Providers) != 1 {
				t.Fatalf("providers: %+v", to.Providers)
			}
			got := to.Providers[0]
			if got.BalanceToken != c.want || got.Name != incoming.Name || got.Chat != incoming.Chat {
				t.Fatalf("merged: %+v, want balance token %q and the incoming name and URL", got, c.want)
			}
			wantKey := incoming.Key
			if c.serverKeys && !c.incomingKeys {
				wantKey = server.Key
			}
			if got.Key != wantKey || to.Keys != (c.serverKeys || c.incomingKeys) {
				t.Fatalf("keys after merge: %+v", to)
			}
			if !reflect.DeepEqual(from.Providers, []provider.Provider{incoming}) {
				t.Fatalf("merge changed the incoming bundle: %+v", from.Providers)
			}
		})
	}
}

// Exercise the encrypted remote and two separate computers. Keyless
// uploads keep each computer's token, and any token already on the server.
func TestSyncBalanceToken(t *testing.T) {
	for _, serverKeys := range []bool{false, true} {
		t.Run(fmt.Sprintf("serverKeys=%v", serverKeys), func(t *testing.T) {
			f, srv := newFakeS3(t)
			cfg := f.config(srv)
			cfg.Keys, cfg.Agents = serverKeys, false
			a, b := newComputer(t), newComputer(t)
			use := func(c computer) {
				c.use(t)
				t.Setenv("USERPROFILE", string(c))
			}
			now := func() {
				t.Helper()
				if err := Now(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			remote := func() backup.Bundle {
				t.Helper()
				got, err := backup.Open(f.objects["team x+y/magpie/magpie.magpie-backup"], cfg.Passphrase)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			use(a)
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1",
				Key: "sk-a", BalanceToken: "balance-a"}); err != nil {
				t.Fatal(err)
			}
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			now()
			wantKey, wantToken := "", ""
			if serverKeys {
				wantKey, wantToken = "sk-a", "balance-a"
			}
			if got := remote(); got.Keys != serverKeys || len(got.Providers) != 1 || got.Providers[0].Key != wantKey || got.Providers[0].BalanceToken != wantToken {
				t.Fatalf("first upload: %+v", got)
			}

			use(b)
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay b", Chat: "https://relay.example.com/v1",
				Key: "sk-b", BalanceToken: "balance-b"}); err != nil {
				t.Fatal(err)
			}
			keyless := cfg
			keyless.Keys = false
			if err := Configure(keyless); err != nil {
				t.Fatal(err)
			}
			now()
			localKey, localToken := "sk-b", "balance-b"
			if serverKeys {
				localKey, localToken = "sk-a", "balance-a"
			}
			ps, _ := provider.Stored()
			if len(ps) != 1 || ps[0].Key != localKey || ps[0].BalanceToken != localToken {
				t.Fatalf("b after download: %+v", ps)
			}
			p := ps[0]
			p.Name, p.BalanceToken = "Renamed", "balance-b-new"
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			now()
			if got := remote(); got.Keys != serverKeys || len(got.Providers) != 1 || got.Providers[0].Name != "Renamed" || got.Providers[0].Key != wantKey || got.Providers[0].BalanceToken != wantToken {
				t.Fatalf("after b's keyless upload: %+v", got)
			}
			if ps, _ := provider.Stored(); len(ps) != 1 || ps[0].BalanceToken != "balance-b-new" {
				t.Fatalf("upload changed b's token: %+v", ps)
			}
			use(a)
			now()
			if ps, _ := provider.Stored(); len(ps) != 1 || ps[0].Name != "Renamed" || ps[0].Key != "sk-a" || ps[0].BalanceToken != "balance-a" {
				t.Fatalf("a after download: %+v", ps)
			}
		})
	}
}
