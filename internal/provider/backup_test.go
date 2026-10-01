package provider

import (
	"path/filepath"
	"slices"
	"testing"
)

// Restoring and syncing a keyless provider keep this machine's balance
// token. A token supplied explicitly, or keys supplied with none, replaces it.
func TestBackupBalanceToken(t *testing.T) {
	for _, op := range []struct {
		name string
		put  func([]Provider) error
	}{
		{"restore", func(ps []Provider) error { _, _, err := Restore(ps); return err }},
		{"mirror", func(ps []Provider) error { return Mirror(ps, nil) }},
	} {
		t.Run(op.name, func(t *testing.T) {
			for _, c := range []struct {
				name     string
				incoming Provider
				want     string
			}{
				{"without keys", Provider{}, "balance-here"},
				{"explicit token without keys", Provider{BalanceToken: "balance-new"}, "balance-new"},
				{"primary key with token", Provider{Key: "sk-new", BalanceToken: "balance-new"}, "balance-new"},
				{"primary key without token", Provider{Key: "sk-new"}, ""},
				{"spare key with token", Provider{Keys: []KeyAccount{{Key: "sk-new"}}, BalanceToken: "balance-new"}, "balance-new"},
				{"spare key without token", Provider{Keys: []KeyAccount{{Key: "sk-new"}}}, ""},
			} {
				t.Run(c.name, func(t *testing.T) {
					isolate(t)
					h := t.TempDir()
					t.Setenv("HOME", h)
					t.Setenv("USERPROFILE", h)
					t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
					t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
					t.Setenv("PATH", h)
					here := Provider{ID: "relay", Name: "Relay old", Chat: "https://old.example.com/v1",
						Key: "sk-here", KeyName: "main", KeyProtocol: Chat,
						Keys: []KeyAccount{{Name: "spare", Key: "sk-spare", Protocol: Anthropic}}, BalanceToken: "balance-here"}
					if err := Save(here); err != nil {
						t.Fatal(err)
					}
					p := c.incoming
					p.ID, p.Name, p.Chat = "relay", "Relay new", "https://new.example.com/v1"
					if err := op.put([]Provider{p}); err != nil {
						t.Fatal(err)
					}
					ps, _ := Stored()
					if len(ps) != 1 {
						t.Fatalf("providers: %+v", ps)
					}
					got := ps[0]
					if got.BalanceToken != c.want || got.Name != p.Name || got.Chat != p.Chat {
						t.Fatalf("restored: %+v, want balance token %q and the incoming name and URL", got, c.want)
					}
					wantKeys := p
					if p.Key == "" && len(p.Keys) == 0 {
						wantKeys = here
					}
					if got.Key != wantKeys.Key || got.KeyName != wantKeys.KeyName || got.KeyProtocol != wantKeys.KeyProtocol || !slices.Equal(got.Keys, wantKeys.Keys) {
						t.Fatalf("keys: %+v, want %+v", got, wantKeys)
					}
				})
			}
		})
	}
}
