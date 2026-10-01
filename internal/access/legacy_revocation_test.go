package access

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestDefaultKeyRevocationSurvivesDowngrade(t *testing.T) {
	for _, action := range []string{"off-key", "remove-key"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if err := ConfigureLAN(true, false); err != nil {
				t.Fatal(err)
			}
			before := settings.Load()
			if _, err := Update(action, Change{Key: before.LANKeyID}); err != nil {
				t.Fatal(err)
			}
			after := settings.Load()
			if after.LANKey == "" || after.LANKey == before.LANKey {
				t.Fatal("older Magpie would accept the revoked credential")
			}
			if _, ok := Authenticate(after.LANKey); ok {
				t.Fatal("legacy replacement is an active gateway key")
			}
			// An older binary saves settings without the new migration marker.
			after.LANKeyID = ""
			if err := settings.Save(after); err != nil {
				t.Fatal(err)
			}
			if err := MigrateLegacyLANKey(); err != nil {
				t.Fatal(err)
			}
			keys, err := List()
			if err != nil || (action == "remove-key" && len(keys) != 0) || (action == "off-key" && (len(keys) != 1 || !keys[0].Off)) {
				t.Fatal("downgrade resurrected a key", keys, err)
			}
			if action == "off-key" {
				if _, err := Update("rotate-key", Change{Key: before.LANKeyID}); err != nil {
					t.Fatal(err)
				}
				current, _ := Update("copy-key", Change{Key: before.LANKeyID})
				if settings.Load().LANKey == current {
					t.Fatal("rotation re-enabled the disabled legacy credential")
				}
				if _, err := Update("on-key", Change{Key: before.LANKeyID}); err != nil {
					t.Fatal(err)
				}
				if settings.Load().LANKey != current {
					t.Fatal("re-enabling did not restore the legacy mirror")
				}
			}
		})
	}
}

func TestReadOnlyMigrationDoesNotRetryCompletedKeyWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Settings{LAN: true, LANKey: "sk-magpie-fixture-legacy"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings.Path(), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(settings.Path(), 0o600) })
	if err := settings.Save(s); err == nil {
		t.Skip("settings.json remains writable")
	}
	if err := MigrateLegacyLANKey(); err == nil {
		t.Fatal("initial marker write unexpectedly succeeded")
	}
	for range 3 {
		if err := MigrateLegacyLANKey(); err != nil {
			t.Fatal("completed key migration retried read-only settings", err)
		}
	}
	if who, ok := Authenticate(s.LANKey); !ok || who.KeyName != "Magpie" {
		t.Fatal("legacy key was not usable after partial migration", who, ok)
	}
}

func TestReadOnlyDefaultKeyMutationExplainsLegacyMirror(t *testing.T) {
	for _, action := range []string{"rotate-key", "off-key", "remove-key"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if err := ConfigureLAN(true, false); err != nil {
				t.Fatal(err)
			}
			s := settings.Load()
			independent, err := Update("add-key", Change{Name: "Laptop"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(settings.Path(), 0o400); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(settings.Path(), 0o600) })
			if err := settings.Save(s); err == nil {
				t.Skip("settings.json remains writable")
			}
			before, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			_, err = Update(action, Change{Key: s.LANKeyID})
			if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "older Magpie") || !strings.Contains(err.Error(), "writable") || !strings.Contains(err.Error(), "unchanged") {
				t.Fatal("default-key error does not explain the recovery or downgrade risk", err)
			}
			after, err := os.ReadFile(Path())
			if err != nil || string(after) != string(before) {
				t.Fatal("failed legacy mirror write changed active keys", err)
			}
			if _, ok := Authenticate(s.LANKey); !ok {
				t.Fatal("operation reported failure but revoked the default key")
			}
			who, ok := Authenticate(independent)
			if !ok {
				t.Fatal("independent key is missing")
			}
			if _, err := Update(action, Change{Key: who.KeyID}); err != nil {
				t.Fatal("read-only settings blocked an independent key", err)
			}
			if _, ok := Authenticate(independent); ok {
				t.Fatal("independent key kept its old credential")
			}
		})
	}
}
