package access

import (
	"errors"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestLegacyLANFallbackBeforeMigration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Settings{LAN: true, LANKey: "sk-magpie-old-client"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	before, ok := Authenticate(s.LANKey)
	if !ok || before.KeyID == "" || before.KeyName != "Magpie" {
		t.Fatal("unmigrated legacy key lost access", before, ok)
	}
	if _, err := os.Stat(Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("authentication must not create a key store", err)
	}
	for _, secret := range []string{"", "wrong", "magpie", s.LANKey + "x"} {
		if _, ok := Authenticate(secret); ok {
			t.Fatal("legacy fallback accepted another token")
		}
	}
	if err := MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	if after, ok := Authenticate(s.LANKey); !ok || after != before {
		t.Fatal("migration split the legacy caller's usage identity", before, after, ok)
	}
}

func TestLegacyLANFallbackDoesNotBypassRevocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  settings.Settings
		keys []Key
	}{
		{"not-shared", settings.Settings{LANKey: "legacy"}, nil},
		{"completed", settings.Settings{LAN: true, LANKey: "legacy", LANKeyID: "removed"}, nil},
		{"revoked-mirror", settings.Settings{LAN: true, LANKey: revokedLANPrefix + "old"}, nil},
		{"disabled", settings.Settings{LAN: true, LANKey: "legacy"}, []Key{{ID: "default", LAN: true, Secret: "legacy", Off: true}}},
		{"rotated", settings.Settings{LAN: true, LANKey: "legacy"}, []Key{{ID: "default", LAN: true, Secret: "new"}}},
		{"disabled-matching-key", settings.Settings{LAN: true, LANKey: "legacy"}, []Key{{ID: "other", Secret: "legacy", Off: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if err := settings.Save(tc.cfg); err != nil {
				t.Fatal(err)
			}
			if tc.keys != nil {
				if err := Restore(tc.keys); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := Authenticate(tc.cfg.LANKey); ok {
				t.Fatal("fallback accepted a revoked or inactive legacy credential")
			}
		})
	}
}

func TestLegacyLANFallbackWithReadOnlyDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Settings{LAN: true, LANKey: "sk-magpie-readonly"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(settings.Dir(), 0o700) })
	if err := MigrateLegacyLANKey(); err == nil {
		t.Skip("configuration directory remains writable")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	for range 3 {
		if who, ok := Authenticate(s.LANKey); !ok || who.KeyID == "" || who.KeyName != "Magpie" {
			t.Fatal("failed key-store creation broke legacy access", who, ok)
		}
	}
}

func TestLegacyLANFallbackRefusesCorruptStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Settings{LAN: true, LANKey: "legacy"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(s.LANKey); ok {
		t.Fatal("an unreadable revocation state must not enable legacy fallback")
	}
}
