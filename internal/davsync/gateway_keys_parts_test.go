package davsync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestGatewayKeysSettingsHash(t *testing.T) {
	s := settings.Settings{Theme: "dark", Proxy: "direct", Window: []int{900, 700}}
	b := backup.Bundle{Keys: true, Settings: &s}
	legacy := s
	legacy.KeepOwn(settings.Settings{})
	j, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if hashes(b)["settings"] != sum(j) {
		t.Fatal("a bundle with no gateway-key field changed the legacy hash")
	}
	b.GatewayKeys = &[]access.Key{}
	empty := hashes(b)
	if empty["settings"] == sum(j) {
		t.Error("an explicit empty store is indistinguishable from an older bundle")
	}
	b.GatewayKeys = &[]access.Key{{ID: "client", Name: "Client", Secret: "fixture-secret"}}
	full := hashes(b)
	if empty["settings"] == full["settings"] {
		t.Error("gateway credentials are absent from the settings hash")
	}
	for _, part := range Parts {
		if part != "settings" && empty[part] != full[part] {
			t.Errorf("gateway keys changed the %s part", part)
		}
	}
	s.Proxy, s.Window = "http://localhost:7890", []int{1, 2}
	if full["settings"] != hashes(b)["settings"] {
		t.Error("this computer's own preferences changed the sync hash")
	}
}

func TestGatewayKeysTakeSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys *[]access.Key
	}{
		{"replace", &[]access.Key{{ID: "new", Secret: "fixture-new"}}},
		{"clear", &[]access.Key{}},
		{"legacy", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &[]access.Key{{ID: "old", Secret: "fixture-old"}}
			to := backup.Bundle{Settings: &settings.Settings{Theme: "dark"}, GatewayKeys: old}
			from := backup.Bundle{Keys: true, Settings: &settings.Settings{Theme: "light"}, GatewayKeys: tc.keys}
			take(&to, from, "settings")
			want := tc.keys
			if want == nil {
				want = old // a missing field gives no instruction to replace it
			}
			if to.Settings != from.Settings || !reflect.DeepEqual(to.GatewayKeys, want) || to.Keys || to.SettingsKeys == nil || !*to.SettingsKeys {
				t.Error("settings did not retain the gateway store and credential flag")
			}
		})
	}
}

func TestGatewayKeysKeylessSettings(t *testing.T) {
	for _, sameEndpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "different collector", true: "same collector"}[sameEndpoint], func(t *testing.T) {
			cur := settings.Settings{Theme: "dark", LANKey: "fixture-lan", LANKeyID: "default", GitHubToken: "fixture-github", BarkKey: "fixture-bark"}
			cur.OTel = settings.OTel{Endpoint: "https://collector.example.com", Headers: map[string]string{"Authorization": "fixture-otel"}}
			from := settings.Settings{Theme: "light"}
			from.OTel.Endpoint = "https://different.example.com"
			if sameEndpoint {
				from.OTel.Endpoint = cur.OTel.Endpoint
			}
			keys := &[]access.Key{{ID: "client", Secret: "fixture-client"}}
			to := backup.Bundle{Keys: true, Settings: &cur, GatewayKeys: keys}
			take(&to, backup.Bundle{Settings: &from}, "settings")
			got := to.Settings
			if got.Theme != "light" || got.LANKey != cur.LANKey || got.LANKeyID != cur.LANKeyID || got.GitHubToken != cur.GitHubToken || got.BarkKey != cur.BarkKey || !reflect.DeepEqual(to.GatewayKeys, keys) || !to.Keys {
				t.Error("a keyless theme upload erased credentials already on the server")
			}
			if sameEndpoint && !reflect.DeepEqual(got.OTel.Headers, cur.OTel.Headers) {
				t.Error("the same collector lost its credentials")
			}
			if !sameEndpoint && len(got.OTel.Headers) != 0 {
				t.Error("a different collector received the old collector's credentials")
			}
			if from.GitHubToken != "" || from.BarkKey != "" || from.LANKey != "" || from.OTel.Headers != nil {
				t.Error("merging modified the keyless source bundle")
			}
		})
	}
}

// A later key-store edit wins a settings conflict when keys are included.
// With keys excluded, its timestamp must not influence the settings winner.
func TestGatewayKeysConflict(t *testing.T) {
	for _, keys := range []bool{false, true} {
		for _, localNewer := range []bool{false, true} {
			name := "without keys"
			if keys {
				name = "with keys"
			}
			if localNewer {
				name += "/key newer"
			} else {
				name += "/remote newer"
			}
			t.Run(name, func(t *testing.T) {
				fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}, cond: true, putETag: true}
				srv := httptest.NewServer(fake)
				defer srv.Close()
				cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "gateway conflict fixture", Keys: keys}
				newComputer(t).use(t)
				appdir.UseExecutable("")
				if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
					t.Fatal(err)
				}
				old, err := access.Update("add-key", access.Change{Name: "Client"})
				if err != nil {
					t.Fatal(err)
				}
				who, ok := access.Authenticate(old)
				if !ok {
					t.Fatal("invalid fixture credential")
				}
				if err := Configure(cfg); err != nil {
					t.Fatal(err)
				}
				if err := Now(context.Background()); err != nil {
					t.Fatal(err)
				}
				path := "/dav/magpie/magpie.magpie-backup"
				fake.mu.Lock()
				data := append([]byte(nil), fake.files[path]...)
				fake.mu.Unlock()
				remote, err := backup.Open(data, cfg.Passphrase)
				if err != nil {
					t.Fatal(err)
				}
				next, err := access.Update("rotate-key", access.Change{Key: who.KeyID})
				if err != nil {
					t.Fatal(err)
				}
				if err := settings.Save(settings.Settings{Theme: "system"}); err != nil {
					t.Fatal(err)
				}
				base := time.Now().Add(-3 * time.Hour)
				keyTime := base.Add(time.Hour)
				remote.Created = base.Add(2 * time.Hour)
				if localNewer {
					keyTime, remote.Created = remote.Created, keyTime
				}
				for file, changed := range map[string]time.Time{settings.Path(): base, access.Path(): keyTime} {
					if err := os.Chtimes(file, changed, changed); err != nil {
						t.Fatal(err)
					}
				}
				remote.Settings.Theme = "light"
				data, err = backup.Seal(remote, cfg.Passphrase)
				if err != nil {
					t.Fatal(err)
				}
				fake.mu.Lock()
				fake.files[path], fake.etags[path] = data, `"remote-change"`
				fake.mu.Unlock()
				if err := Now(context.Background()); err != nil {
					t.Fatal(err)
				}
				wantTheme, accepted, rejected := "light", old, next
				if localNewer && keys {
					wantTheme, accepted, rejected = "system", next, old
				}
				if !keys {
					accepted, rejected = next, old
				}
				if settings.Load().Theme != wantTheme {
					t.Error("key-store timestamp did not follow the settings conflict policy")
				}
				if _, ok := access.Authenticate(accepted); !ok {
					t.Error("the winning credential is not active")
				}
				if _, ok := access.Authenticate(rejected); ok {
					t.Error("the losing credential remains active")
				}
			})
		}
	}
}

// A computer sending no keys neither uploads its private store nor erases
// credentials another computer already put in the encrypted S3 backup.
func TestGatewayKeysExcluded(t *testing.T) {
	for _, serverKeys := range []bool{false, true} {
		name := "keyless server"
		if serverKeys {
			name = "server with keys"
		}
		t.Run(name, func(t *testing.T) {
			f, srv := newFakeS3(t)
			cfg := f.config(srv)
			cfg.Keys, cfg.Agents = serverKeys, false
			a, b := newComputer(t), newComputer(t)
			use := func(c computer) {
				t.Helper()
				c.use(t)
				appdir.UseExecutable("")
				provider.ForgetAccounts()
			}
			t.Cleanup(provider.ForgetAccounts)
			now := func() {
				t.Helper()
				if err := Now(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			countPuts := func() int {
				f.mu.Lock()
				defer f.mu.Unlock()
				var n int
				for _, request := range f.log {
					if strings.HasPrefix(request, "PUT ") {
						n++
					}
				}
				return n
			}
			use(a)
			if err := access.ConfigureLAN(true, false); err != nil {
				t.Fatal(err)
			}
			original := settings.Load()
			original.Theme, original.GitHubToken = "dark", "fixture-server-github"
			original.OTel = settings.OTel{Endpoint: "https://collector.example.com", Headers: map[string]string{"Authorization": "fixture-server-otel"}}
			if err := settings.Save(original); err != nil {
				t.Fatal(err)
			}
			initial, err := access.Export()
			if err != nil {
				t.Fatal(err)
			}
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			now()
			use(b)
			keyless := cfg
			keyless.Keys = false
			if err := Configure(keyless); err != nil {
				t.Fatal(err)
			}
			now()
			private, err := access.Update("add-key", access.Change{Name: "Private client"})
			if err != nil {
				t.Fatal(err)
			}
			puts := countPuts()
			now()
			if countPuts() != puts {
				t.Error("a private key-store edit caused a keyless upload")
			}
			s := settings.Load()
			s.Theme, s.GitHubToken = "light", "fixture-private-github"
			s.OTel.Headers = map[string]string{"Authorization": "fixture-private-otel"}
			if err := settings.Save(s); err != nil {
				t.Fatal(err)
			}
			now()
			if countPuts() != puts+1 {
				t.Error("a keyless theme change did not upload exactly once")
			}
			f.mu.Lock()
			data := append([]byte(nil), f.objects["team x+y/magpie/magpie.magpie-backup"]...)
			f.mu.Unlock()
			remote, err := backup.Open(data, cfg.Passphrase)
			if err != nil {
				t.Fatal(err)
			}
			if remote.Keys != serverKeys || remote.Settings == nil || remote.Settings.Theme != "light" {
				t.Fatal("the keyless upload changed the server's credential flag or lost the theme")
			}
			if serverKeys {
				if remote.GatewayKeys == nil || !reflect.DeepEqual(*remote.GatewayKeys, initial) || remote.Settings.LANKey != original.LANKey || remote.Settings.LANKeyID != original.LANKeyID || remote.Settings.GitHubToken != original.GitHubToken || !reflect.DeepEqual(remote.Settings.OTel.Headers, original.OTel.Headers) {
					t.Error("a keyless theme upload replaced credentials already on the server")
				}
			} else if remote.GatewayKeys != nil || remote.Settings.LANKey != "" || remote.Settings.LANKeyID != "" || remote.Settings.GitHubToken != "" || len(remote.Settings.OTel.Headers) != 0 {
				t.Error("a keyless backup exposed private credentials")
			}
			if _, ok := access.Authenticate(private); !ok {
				t.Error("uploading settings changed this computer's private key")
			}
			use(a)
			now()
			if settings.Load().Theme != "light" {
				t.Error("computer A did not receive the keyless theme change")
			}
			if got, err := access.Export(); err != nil || !reflect.DeepEqual(got, initial) {
				t.Error("a keyless theme sync replaced computer A's gateway keys")
			}
			if _, ok := access.Authenticate(private); ok {
				t.Error("computer B's private key was synced to computer A")
			}
		})
	}
}
