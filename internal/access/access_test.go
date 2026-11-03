package access

import (
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestNamedKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Update("add-key", Change{Name: "   "}); err == nil {
		t.Fatal("accepted empty name")
	}
	keys, err := List()
	if err != nil || len(keys) != 0 {
		t.Fatal(keys, err)
	}
	secret, err := Update("add-key", Change{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	keys, err = List()
	if err != nil || len(keys) != 1 || keys[0].Name != "Laptop" || keys[0].Secret != "" || !strings.HasPrefix(keys[0].Masked, Prefix+"…") {
		t.Fatal(keys, err)
	}
	id := keys[0].ID
	if who, ok := Authenticate(secret); !ok || who.KeyID != id || who.KeyName != "Laptop" {
		t.Fatal(who, ok)
	}
	if _, ok := Authenticate(secret + "x"); ok {
		t.Fatal("accepted invalid key")
	}
	for _, step := range []struct {
		action, name string
		valid        bool
	}{
		{"rename-key", "Travel", true}, {"off-key", "", false}, {"on-key", "", true},
	} {
		if _, err := Update(step.action, Change{Key: id, Name: step.name}); err != nil {
			t.Fatal(err)
		}
		if who, ok := Authenticate(secret); ok != step.valid || (ok && who.KeyName != "Travel") {
			t.Fatal(step.action, who, ok)
		}
	}
	if got, err := Update("copy-key", Change{Key: id}); err != nil || got != secret {
		t.Fatal("copy", err)
	}
	st, err := os.Stat(Path())
	if err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatal("permissions", err)
	}
	var persisted []Key
	b, _ := os.ReadFile(Path())
	if err := json.Unmarshal(b, &persisted); err != nil || len(persisted) != 1 || persisted[0].ID != id || persisted[0].Secret != secret {
		t.Fatal("key was not persisted", err)
	}
	if _, err := Update("remove-key", Change{Key: id}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(secret); ok {
		t.Fatal("removed key works")
	}
}

func TestConcurrentKeyCreationAndCorruptStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Update("add-key", Change{Name: "Key"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	keys, _ := List()
	if len(keys) != 12 {
		t.Fatal("lost concurrent changes")
	}
	secret, _ := Update("copy-key", Change{Key: keys[0].ID})
	os.WriteFile(Path(), []byte("broken"), 0o600)
	if _, ok := Authenticate(secret); ok {
		t.Fatal("corrupt store accepted a key")
	}
	if _, err := Update("add-key", Change{Name: "New"}); err == nil {
		t.Fatal("overwrote corrupt store")
	}
}

func TestLegacyLANKeyMigrationIsIdempotentAndRevocable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Load()
	s.LAN, s.LANKey = true, "sk-magpie-legacy-lan-secret"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	keys, err := List()
	if err != nil || len(keys) != 1 || keys[0].Name != "Magpie" || !strings.HasPrefix(keys[0].Masked, "sk-magpie-…") {
		t.Fatal(keys, err)
	}
	if settings.Load().LANKey != s.LANKey || !settings.Load().LAN {
		t.Fatal("migration broke older Magpie's sharing settings")
	}
	if who, ok := Authenticate(s.LANKey); !ok || who.KeyID != keys[0].ID {
		t.Fatal(who, ok)
	}
	if err := MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	keys, _ = List()
	if len(keys) != 1 {
		t.Fatal("duplicate migration", keys)
	}
	if _, err := Update("off-key", Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(s.LANKey); ok {
		t.Fatal("disabled LAN key still works")
	}
	if _, err := Update("remove-key", Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(s.LANKey); ok {
		t.Fatal("removed LAN key still works")
	}
	if err := MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	if keys, _ := List(); len(keys) != 0 {
		t.Fatal("retained legacy credential resurrected a removed key", keys)
	}
}

func TestLegacyLANKeyMigrationPreservesNamedKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret, err := Update("add-key", Change{Name: "Client"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	id := keys[0].ID
	s := settings.Load()
	s.LANKey = "sk-magpie-legacy"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	keys, _ = List()
	if len(keys) != 2 || keys[0].ID != id || keys[1].Name != "Magpie" {
		t.Fatal(keys)
	}
	if _, ok := Authenticate(secret); !ok {
		t.Fatal("lost named key")
	}
	if _, ok := Authenticate(s.LANKey); !ok {
		t.Fatal("lost LAN key")
	}
}

func TestLegacyLANKeyMigrationRetryAndFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Load()
	s.LANKey = "sk-magpie-test-legacy-lan"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	// The key write succeeded but saving the migration marker was interrupted.
	mu.Lock()
	err := save([]Key{{ID: "migrated", Name: "Remote", Off: true, Secret: s.LANKey}})
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	keys, err := List()
	if err != nil || len(keys) != 1 || keys[0].ID != "migrated" || !keys[0].Off || settings.Load().LANKeyID != "migrated" {
		t.Fatal("retry duplicated or re-enabled a key", keys, err)
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyLANKey(); err == nil {
		t.Fatal("migration accepted a corrupt store")
	}
	if settings.Load().LANKey != s.LANKey {
		t.Fatal("failed migration cleared the old credential")
	}
}

func TestNamedLANKeyKeepsItsName(t *testing.T) {
	for _, name := range []string{"Local network", "Magpie", "Desk"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			s := settings.Load()
			s.LANKeyID = "lan"
			if err := settings.Save(s); err != nil {
				t.Fatal(err)
			}
			original := []Key{{ID: "lan", Name: name, Off: true, Secret: Prefix + "original"},
				{ID: "other", Name: "Other", Secret: Prefix + "other"},
				{ID: "named", Name: "Local network", Secret: Prefix + "named"}}
			if err := save(original); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := MigrateLegacyLANKey(); err != nil {
					t.Fatal(err)
				}
			}
			keys, err := load()
			if err != nil || !reflect.DeepEqual(keys, original) {
				t.Fatal("migration changed an existing named key", keys, err)
			}
		})
	}
}

func TestCallerKeyRotationPreservesIdentityAndState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old, err := Update("add-key", Change{Name: "Desk"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := Update("add-key", Change{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	id := keys[0].ID
	for _, off := range []bool{false, true} {
		if off {
			if _, err := Update("off-key", Change{Key: id}); err != nil {
				t.Fatal(err)
			}
		}
		secret, err := Update("rotate-key", Change{Key: id})
		if err != nil || secret == old || !strings.HasPrefix(secret, Prefix) {
			t.Fatal("key was not rotated", err)
		}
		if _, ok := Authenticate(old); ok {
			t.Fatal("old key still authenticates")
		}
		if who, ok := Authenticate(secret); ok == off || (!off && (who.KeyID != id || who.KeyName != "Desk")) {
			t.Fatal("rotation changed identity or enabled state", who, ok)
		}
		keys, _ = List()
		if keys[0].ID != id || keys[0].Name != "Desk" || keys[0].Off != off || keys[0].Secret != "" {
			t.Fatal(keys)
		}
		if _, ok := Authenticate(other); !ok {
			t.Fatal("rotation revoked another key")
		}
		old = secret
	}
	if _, err := Update("rotate-key", Change{Key: "missing"}); err == nil {
		t.Fatal("rotated a missing key")
	}
}

func TestLANRotationKeepsOlderMagpieWorking(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	before := settings.Load()
	if before.LANKey == "" {
		t.Fatal("older Magpie has no sharing key")
	}
	next, err := Update("rotate-key", Change{Key: before.LANKeyID})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Load().LANKey != next {
		t.Fatal("rotation left the old version with a stale key")
	}
	if err := MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	if keys, _ := List(); len(keys) != 1 || keys[0].ID != before.LANKeyID {
		t.Fatal("rotation duplicated the default key", keys)
	}
	if _, ok := Authenticate(before.LANKey); ok {
		t.Fatal("old key resurrected")
	}
}

// A key can be given a value of the user's own, one their clients already
// send (love1sbug on X), checked as a header can carry it and unlike any
// other key's.
func TestOwnKeyValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const own = "cpa-family-key-0042"
	if Named(own) {
		t.Fatal("named before it is a key")
	}
	got, err := Update("add-key", Change{Name: "Family", Secret: "  " + own + "\n"})
	if err != nil || got != own {
		t.Fatal(got, err)
	}
	keys, err := List()
	if err != nil || len(keys) != 1 || keys[0].Masked != "…0042" || keys[0].Secret != "" {
		t.Fatal(keys, err)
	}
	if who, ok := Authenticate(own); !ok || who.KeyName != "Family" {
		t.Fatal(who, ok)
	}
	if !Named(own) || Named(own+"x") || Named("") || !Named(Prefix+"abc") {
		t.Fatal("Named")
	}
	for _, bad := range []string{"short", own, "has a space in it", "tab\there-and-more", "非ascii-key-value", "sk-magpie-key-mine-1234", strings.Repeat("k", 257)} {
		if _, err := Update("add-key", Change{Name: "Other", Secret: bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if keys, _ := List(); len(keys) != 1 {
		t.Fatal("a refused key was kept", keys)
	}
	// rotating it gives it one magpie makes; the old value no longer works
	rotated, err := Update("rotate-key", Change{Key: keys[0].ID})
	if err != nil || !strings.HasPrefix(rotated, Prefix) {
		t.Fatal(rotated, err)
	}
	if _, ok := Authenticate(own); ok || Named(own) {
		t.Fatal("the old value still works")
	}
	short, err := Update("add-key", Change{Name: "Short", Secret: "abcd1234"})
	if err != nil {
		t.Fatal(err)
	}
	if keys, _ := List(); keys[1].Masked != "…" {
		t.Fatal("a short key shows its end", keys[1].Masked, short)
	}
}

// Authenticate tells the sharing key magpie made itself (the one it writes
// into this computer's own agents) from every other gateway key, so a
// plugin's ctx.origin can tell the owner's agents from a friend's key
// (plugins#526): marked LAN, named by LANKeyID only, or the older
// settings-only key the fallback takes.
func TestAuthenticateTellsTheDefaultKey(t *testing.T) {
	t.Run("made by ConfigureLAN", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		friend, err := Update("add-key", Change{Name: "Friend"})
		if err != nil {
			t.Fatal(err)
		}
		brought, err := Update("add-key", Change{Name: "Brought", Secret: "a-much-longer-key-of-my-own"})
		if err != nil {
			t.Fatal(err)
		}
		if err := ConfigureLAN(true, false); err != nil {
			t.Fatal(err)
		}
		if who, ok := Authenticate(LANSecret()); !ok || !who.Default {
			t.Fatal("the default sharing key isn't Default", who, ok)
		}
		for _, k := range []string{friend, brought} {
			if who, ok := Authenticate(k); !ok || who.Default {
				t.Fatal("a named key reads as the default one", who, ok)
			}
		}
	})
	t.Run("named by LANKeyID only", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		s := settings.Load()
		s.LANKeyID = "lan"
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		if err := save([]Key{{ID: "lan", Name: "Desk", Secret: Prefix + "lan"}, {ID: "other", Name: "Other", Secret: Prefix + "other"}}); err != nil {
			t.Fatal(err)
		}
		if who, ok := Authenticate(Prefix + "lan"); !ok || !who.Default {
			t.Fatal(who, ok)
		}
		if who, ok := Authenticate(Prefix + "other"); !ok || who.Default {
			t.Fatal(who, ok)
		}
	})
	t.Run("older magpie's settings key", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		s := settings.Load()
		s.LAN, s.LANKey = true, "sk-magpie-legacy-lan-secret"
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		if who, ok := Authenticate(s.LANKey); !ok || !who.Default {
			t.Fatal(who, ok)
		}
	})
}

// A key magpie made can be given a value of the user's own when it is
// rotated (isaac_bai on Discord: one easy to remember and pass on),
// checked as add-key's is, and never a key magpie sends a provider.
func TestRotateToOwnValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	made, err := Update("add-key", Change{Name: "Family"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update("add-key", Change{Name: "Other", Secret: "other-key-of-mine"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-first-key", Keys: []provider.KeyAccount{{Key: "upstream-second-key"}}, Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	id := keys[0].ID
	for _, bad := range []string{"short", "has a space in it", "非ascii-key-value", "sk-magpie-key-mine-1234", "other-key-of-mine", "upstream-first-key", "upstream-second-key", strings.Repeat("k", 257)} {
		if _, err := Update("rotate-key", Change{Key: id, Secret: bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if who, ok := Authenticate(made); !ok || who.KeyName != "Family" {
		t.Fatal("a refused value changed the key", who, ok)
	}
	const own = "family-easy-to-share"
	got, err := Update("rotate-key", Change{Key: id, Secret: " " + own + "\n"})
	if err != nil || got != own {
		t.Fatal(got, err)
	}
	if who, ok := Authenticate(own); !ok || who.KeyName != "Family" || who.KeyID != id {
		t.Fatal(who, ok)
	}
	if _, ok := Authenticate(made); ok || !Named(own) {
		t.Fatal("the old value still works, or the new one isn't named")
	}
	if keys, _ := List(); keys[0].Masked != "…hare" || keys[0].Secret != "" {
		t.Fatal(keys[0])
	}
	// its own value again is no other key's
	if _, err := Update("rotate-key", Change{Key: id, Secret: own}); err != nil {
		t.Fatal(err)
	}
	// none: magpie makes one again
	if again, err := Update("rotate-key", Change{Key: id}); err != nil || !strings.HasPrefix(again, Prefix) {
		t.Fatal(again, err)
	}
	// a new key can't take a provider's key either
	if _, err := Update("add-key", Change{Name: "Leak", Secret: "upstream-first-key"}); err == nil {
		t.Fatal("add-key took a provider's key")
	}
}
