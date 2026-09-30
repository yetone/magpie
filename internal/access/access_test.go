package access

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestNamedKeysAndLegacyMigration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Update("add-key", Change{Name: "   "}); err == nil {
		t.Fatal("accepted empty name")
	}
	old := filepath.Join(filepath.Dir(Path()), "users.json")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`[{"id":"old","name":"Old user","off":true,"keys":[{"id":"disabled","name":"Legacy off","secret":"sk-magpie-user-disabled"}]},{"id":"active","name":"Other user","keys":[{"id":"legacy","name":"Legacy","secret":"sk-magpie-user-legacy"}]}]`)
	if err := os.WriteFile(old, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := List()
	if err != nil || len(keys) != 2 || !keys[0].Off || keys[0].Secret != "" {
		t.Fatal(keys, err)
	}
	if _, ok := Authenticate("sk-magpie-user-disabled"); ok {
		t.Fatal("disabled legacy key works")
	}
	if who, ok := Authenticate("sk-magpie-user-legacy"); !ok || who.KeyID != "legacy" {
		t.Fatal(who, ok)
	}
	secret, err := Update("add-key", Change{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ = List()
	if len(keys) != 3 || keys[2].Name != "Laptop" || keys[2].Secret != "" || !strings.Contains(keys[2].Masked, "…") {
		t.Fatal(keys)
	}
	who, ok := Authenticate(secret)
	if !ok || who.KeyID != keys[2].ID || who.KeyName != "Laptop" {
		t.Fatal(who, ok)
	}
	if _, ok := Authenticate(secret + "x"); ok {
		t.Fatal("accepted invalid key")
	}
	id := keys[2].ID
	Update("rename-key", Change{Key: id, Name: "Travel"})
	if who, ok := Authenticate(secret); !ok || who.KeyName != "Travel" {
		t.Fatal(who, ok)
	}
	Update("off-key", Change{Key: id})
	if _, ok := Authenticate(secret); ok {
		t.Fatal("disabled key works")
	}
	Update("on-key", Change{Key: id})
	if got, err := Update("copy-key", Change{Key: id}); err != nil || got != secret {
		t.Fatal("copy", err)
	}
	Update("remove-key", Change{Key: id})
	if _, ok := Authenticate(secret); ok {
		t.Fatal("removed key works")
	}
	if _, ok := Authenticate("sk-magpie-user-legacy"); !ok {
		t.Fatal("lost legacy key")
	}
	st, err := os.Stat(Path())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("permissions", err)
	}
	var persisted []Key
	b, _ := os.ReadFile(Path())
	if err := json.Unmarshal(b, &persisted); err != nil || len(persisted) != 2 {
		t.Fatal(err, persisted)
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
	if err != nil || len(keys) != 1 || keys[0].Name != "Local network" || !strings.HasPrefix(keys[0].Masked, "sk-magpie-…") {
		t.Fatal(keys, err)
	}
	if settings.Load().LANKey != "" || !settings.Load().LAN {
		t.Fatal("LAN sharing lost or old secret still in settings")
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
}

func TestLegacyLANKeyMigrationPreservesUserKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Load()
	s.LANKey = "sk-magpie-legacy"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(Path()), "users.json"), []byte(`[{"name":"Old","keys":[{"id":"client","name":"Client","secret":"sk-magpie-user-old"}]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	if len(keys) != 2 || keys[0].ID != "client" {
		t.Fatal(keys)
	}
	if _, ok := Authenticate("sk-magpie-user-old"); !ok {
		t.Fatal("lost old caller key")
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
	// The key write succeeded but clearing Settings was interrupted.
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

func TestLANDefaultNameUpgrade(t *testing.T) {
	for _, name := range []string{"Local network (legacy)", "Desk"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			s := settings.Load()
			s.LANKeyID = "lan"
			if err := settings.Save(s); err != nil {
				t.Fatal(err)
			}
			original := []Key{{ID: "lan", Name: name, Off: true, Secret: Prefix + "original"},
				{ID: "other", Name: "Local network (legacy)", Secret: Prefix + "other"}}
			if err := save(original); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := MigrateLegacyLANKey(); err != nil {
					t.Fatal(err)
				}
			}
			if name == "Local network (legacy)" {
				original[0].Name = "Local network"
			}
			keys, err := load()
			if err != nil || len(keys) != 2 || keys[0] != original[0] || keys[1] != original[1] {
				t.Fatal("name upgrade changed identity, credentials, state or another key", keys, err)
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
