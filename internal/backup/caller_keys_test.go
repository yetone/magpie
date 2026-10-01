package backup

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

func TestGatewayKeysBackupRoundTrip(t *testing.T) {
	home(t)
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	defaultKey := settings.Load()
	client, err := access.Update("add-key", access.Change{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	if _, err := access.Update("off-key", access.Change{Key: defaultKey.LANKeyID}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Seal(b, "gateway passphrase")
	if err != nil || strings.Contains(string(data), client) || strings.Contains(string(data), defaultKey.LANKey) {
		t.Fatal("backup did not seal gateway credentials", err)
	}
	home(t)
	b, err = Open(data, "gateway passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(b, Parts{Settings: true}); err != nil {
		t.Fatal(err)
	}
	got, err := access.List()
	if err != nil || len(got) != len(keys) || got[0].ID != defaultKey.LANKeyID || !got[0].Off || got[1].ID != keys[1].ID || got[1].Name != "Laptop" {
		t.Fatal("named key identity/state lost during restore", got, err)
	}
	if who, ok := access.Authenticate(client); !ok || who.KeyID != keys[1].ID {
		t.Fatal("restored client cannot authenticate", who, ok)
	}
	if _, ok := access.Authenticate(defaultKey.LANKey); ok {
		t.Fatal("restore resurrected the disabled default key")
	}
	if err := access.MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	if got, _ := access.List(); len(got) != len(keys) {
		t.Fatal("restore imported a duplicate default key", got)
	}
	if _, err := access.Update("on-key", access.Change{Key: defaultKey.LANKeyID}); err != nil {
		t.Fatal(err)
	}
	if settings.Load().LANKey != defaultKey.LANKey {
		t.Fatal("restored default key lost its legacy link")
	}
	info, err := os.Stat(access.Path())
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal("restored key-store permissions", info, err)
	}
}

func TestGatewayKeysWithoutSecretsOrSettingsRestore(t *testing.T) {
	home(t)
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	secret := settings.Load().LANKey
	full, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	without, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(without)
	if strings.Contains(string(plain), secret) || without.Settings.LANKey != "" || without.Settings.LANKeyID != "" {
		t.Fatal("--no-keys backup contains a gateway credential")
	}
	home(t)
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	current := settings.Load()
	if _, err := Restore(full, Parts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(without, Parts{Settings: true}); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.LANKey != current.LANKey || got.LANKeyID != current.LANKeyID {
		t.Fatal("keyless restore replaced this machine's LAN credential")
	}
	if _, ok := access.Authenticate(current.LANKey); !ok {
		t.Fatal("restore without gateway keys revoked this machine's key")
	}
}

func TestOlderBackupRecoversLegacyGatewayCredential(t *testing.T) {
	home(t)
	b := Bundle{Version: 1, Keys: true, Settings: &settings.Settings{LAN: true, LANKey: "{{API_KEY_88kqppzx}}", LANKeyID: "id-from-incomplete-backup"}}
	if _, err := Restore(b, Parts{Settings: true}); err != nil {
		t.Fatal(err)
	}
	if who, ok := access.Authenticate(b.Settings.LANKey); !ok || who.KeyName != "Magpie" {
		t.Fatal("old backup's migration marker lost its credential", who, ok)
	}
}

func TestOlderBackupKeepsExistingGatewayKeyStore(t *testing.T) {
	home(t)
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	cur := settings.Load()
	b := Bundle{Version: 1, Keys: true, Settings: &settings.Settings{LAN: true, LANKey: "fixture-older-backup-key", LANKeyID: "old-marker"}}
	if _, err := Restore(b, Parts{Settings: true}); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.LANKey != cur.LANKey || got.LANKeyID != cur.LANKeyID {
		t.Fatal("backup without a key store detached the existing default key")
	}
	if _, ok := access.Authenticate(cur.LANKey); !ok {
		t.Fatal("backup without a key store revoked the existing gateway key")
	}
}

func TestGatewayKeysBackupWithoutSettingsFile(t *testing.T) {
	home(t)
	secret, err := access.Update("add-key", access.Change{Name: "CLI client"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "test")
	if err != nil || b.Settings != nil {
		t.Fatal("CLI-only key fixture unexpectedly has settings", err)
	}
	home(t)
	if _, err := Restore(b, Parts{Settings: true}); err != nil {
		t.Fatal(err)
	}
	if who, ok := access.Authenticate(secret); !ok || who.KeyName != "CLI client" {
		t.Fatal("standalone CLI key was not restored", who, ok)
	}
}

func TestGatewayKeyRestoreFailureKeepsSettingsAndKeys(t *testing.T) {
	for _, failure := range []string{"invalid-settings", "readonly-settings", "readonly-store"} {
		t.Run(failure, func(t *testing.T) {
			home(t)
			if err := access.ConfigureLAN(true, false); err != nil {
				t.Fatal(err)
			}
			cur := settings.Load()
			before, err := os.ReadFile(access.Path())
			if err != nil {
				t.Fatal(err)
			}
			incoming := cur
			incoming.Theme, incoming.LANKeyID, incoming.LANKey = "dark", "restored", "sk-magpie-restored"
			keys := []access.Key{{ID: incoming.LANKeyID, Name: "Restored", Secret: incoming.LANKey, LAN: true}}
			if failure == "invalid-settings" {
				incoming.Theme = "invalid-theme"
			} else {
				path := settings.Path()
				if failure == "readonly-store" {
					path = access.Path()
				}
				if err := os.Chmod(path, 0o400); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(path, 0o600) })
				f, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err == nil {
					f.Close()
					t.Skip("file remains writable")
				}
				if !errors.Is(err, os.ErrPermission) {
					t.Fatal(err)
				}
			}
			r, err := Restore(Bundle{Version: 1, Keys: true, Settings: &incoming, GatewayKeys: &keys}, Parts{Settings: true})
			if err == nil || r.Settings {
				t.Fatal("failed restore reported success", r, err)
			}
			if failure != "invalid-settings" && !errors.Is(err, os.ErrPermission) {
				t.Fatal("restore lost permission error", err)
			}
			after, err := os.ReadFile(access.Path())
			if err != nil || string(after) != string(before) || !reflect.DeepEqual(settings.Load(), cur) {
				t.Fatal("failed restore left settings and keys inconsistent", err)
			}
			if _, ok := access.Authenticate(cur.LANKey); !ok {
				t.Fatal("failed restore revoked the current key")
			}
			if _, ok := access.Authenticate(incoming.LANKey); ok {
				t.Fatal("failed restore activated the incoming key")
			}
		})
	}
}
