package davsync

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A providers-only upload must not turn the remote's redacted settings and
// library into instructions to clear another computer's private credentials.
func TestProviderKeysPreserveOtherSecretsSync(t *testing.T) {
	for _, libraryOff := range []bool{false, true} {
		t.Run(fmt.Sprintf("libraryOff=%v", libraryOff), func(t *testing.T) {
			f, srv := newFakeS3(t)
			cfg := f.config(srv)
			cfg.Keys, cfg.Agents = false, false
			a, b, c := newComputer(t), newComputer(t), newComputer(t)
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
			remote := func() backup.Bundle {
				t.Helper()
				f.mu.Lock()
				data := slices.Clone(f.objects["team x+y/magpie/magpie.magpie-backup"])
				f.mu.Unlock()
				got, err := backup.Open(data, cfg.Passphrase)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			puts := func() int {
				f.mu.Lock()
				defer f.mu.Unlock()
				n := 0
				for _, request := range f.log {
					if strings.HasPrefix(request, "PUT") {
						n++
					}
				}
				return n
			}
			private := func(name string) {
				t.Helper()
				s := settings.Load()
				s.Theme, s.GitHubToken = "dark", "fixture-"+name+"-github"
				if err := settings.Save(s); err != nil {
					t.Fatal(err)
				}
				if _, err := library.SaveServer("", library.Server{Name: "github", Transport: "stdio", Command: "fixture-mcp",
					Env: map[string]string{"GITHUB_TOKEN": "fixture-" + name + "-mcp"}, Agents: []string{}}); err != nil {
					t.Fatal(err)
				}
			}
			say := func(text string) {
				t.Helper()
				if _, err := library.SaveInstructions(library.InstructionsChange{Shared: &text}); err != nil {
					t.Fatal(err)
				}
			}

			use(a)
			private("a")
			say("Initial shared instructions.")
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "fixture-a-provider"}); err != nil {
				t.Fatal(err)
			}
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			now()
			initial := remote()
			if initial.Keys || initial.Library == nil || len(initial.Library.MCP) != 1 || initial.Library.MCP[0].Env["GITHUB_TOKEN"] != "" || initial.Settings.GitHubToken != "" {
				t.Fatal("the initial remote was not keyless")
			}

			use(b)
			private("b")
			say("Private instructions.")
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Local", Chat: "https://relay.example.com/v1", Key: "fixture-b-provider"}); err != nil {
				t.Fatal(err)
			}
			keyed := cfg
			keyed.Keys = true
			if libraryOff {
				off := false
				keyed.Library = &off
			}
			if err := Configure(keyed); err != nil {
				t.Fatal(err)
			}
			now()
			before := puts()
			now()
			if puts() != before || settings.Load().GitHubToken != "fixture-b-github" {
				t.Fatal("joining the keyless remote uploaded a change or erased a local credential")
			}
			p, err := provider.Find("relay")
			if err != nil {
				t.Fatal(err)
			}
			p.Name = "Renamed by B"
			if err := provider.Save(*p); err != nil {
				t.Fatal(err)
			}
			now()
			got := remote()
			if puts() != before+1 || got.Providers[0].Name != p.Name || got.Providers[0].Key != "fixture-b-provider" {
				t.Fatal("the providers-only change was not uploaded once")
			}
			t.Logf("after providers-only upload: Keys=%v, remote MCP token present=%v, remote GitHub token present=%v", got.Keys,
				got.Library.MCP[0].Env["GITHUB_TOKEN"] != "", got.Settings.GitHubToken != "")
			if !got.Keys || !reflect.DeepEqual(got.Library, initial.Library) || !reflect.DeepEqual(got.Settings, initial.Settings) {
				t.Error("a keyed providers upload left the whole-bundle bit an older reader needs, or changed unselected parts")
			}
			// Join immediately after that upload, before any settings upload
			// has written an explicit settingsKeys marker. A different theme
			// ensures the redacted settings are actually applied on this peer.
			use(newComputer(t))
			private("d")
			different := settings.Load()
			different.Theme = "light"
			if err := settings.Save(different); err != nil {
				t.Fatal(err)
			}
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			now()
			if got := settings.Load(); got.Theme != "dark" || got.GitHubToken != "fixture-d-github" {
				t.Error("joining immediately after a providers-only upload cleared D's private GitHub token")
			}

			use(a)
			now()
			say("Updated shared instructions.")
			s := settings.Load()
			s.Theme = "light"
			if err := settings.Save(s); err != nil {
				t.Fatal(err)
			}
			now()
			if puts() != before+2 || remote().Providers[0].Key != "fixture-b-provider" {
				t.Fatal("a keyless upload lost the server's provider key or did not upload the other parts once")
			}
			use(b)
			now()
			if got := settings.Load(); got.Theme != "light" || got.GitHubToken != "fixture-b-github" {
				t.Error("a later settings download cleared B's private GitHub token")
			}
			lib, err := library.Collect()
			if err != nil || lib.MCP[0].Env["GITHUB_TOKEN"] != "fixture-b-mcp" {
				t.Error("a later library download cleared B's private MCP token", err)
			}
			wantText := "Updated shared instructions."
			if libraryOff {
				wantText = "Private instructions."
			}
			if lib.Texts["default"] != wantText {
				t.Error("the library did not honor the selected sync parts")
			}

			use(c)
			private("c")
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			now()
			lib, err = library.Collect()
			if err != nil || lib.Texts["default"] != "Updated shared instructions." || lib.MCP[0].Env["GITHUB_TOKEN"] != "fixture-c-mcp" {
				t.Error("joining after the providers upload cleared C's private MCP token", err)
			}
			if got := settings.Load(); got.Theme != "light" || got.GitHubToken != "fixture-c-github" {
				t.Error("joining after the providers upload cleared C's private GitHub token")
			}
			now()
			if puts() != before+2 {
				t.Error("a downloaded or unchanged part was uploaded again")
			}
		})
	}
}

func TestTakeScopedPartCredentials(t *testing.T) {
	for _, part := range []string{"providers", "library"} {
		for _, server := range []string{"keyless", "full", "scoped"} {
			for _, source := range []string{"keyless", "full", "scoped", "clear"} {
				t.Run(part+"/"+server+"/"+source, func(t *testing.T) {
					makeBundle := func(policy, value string) backup.Bundle {
						b := backup.Bundle{Version: backup.BundleVersion, Keys: policy == "full", SettingsKeys: backup.Flag(true),
							Settings:  &settings.Settings{GitHubToken: value},
							Providers: []provider.Provider{{ID: "relay", Name: value, Key: value, BalanceToken: value}},
							Searches:  &[]provider.SearchAPI{{Vendor: "tavily", Key: value}},
							Library: &library.Bundle{MCP: []*library.Server{{Name: "github", Transport: "stdio", Command: "fixture-mcp",
								Env: map[string]string{"GITHUB_TOKEN": value}, Headers: map[string]string{"Authorization": value}}}}}
						if policy == "scoped" || policy == "clear" {
							// Use the wire field so these tests also run on an older reader.
							if err := json.Unmarshal(fmt.Appendf(nil, `{"%sKeys":true}`, part), &b); err != nil {
								t.Fatal(err)
							}
						}
						return b
					}
					old, next := "fixture-server", "fixture-source"
					if server == "keyless" {
						old = ""
					}
					if source == "keyless" || source == "clear" {
						next = ""
					}
					to, from := makeBundle(server, old), makeBundle(source, next)
					beforeTo, _ := json.Marshal(to)
					beforeFrom, _ := json.Marshal(from)
					merged := to
					take(&merged, from, part)
					if merged.SettingsKeys != to.SettingsKeys || merged.Settings != to.Settings {
						t.Error("taking one part changed another part's credential policy or settings")
					}
					if part == "providers" {
						// the whole-bundle bit follows the providers' own when
						// this upload carried them, so a magpie that reads no
						// per-part ones still keeps the server's keys when it
						// uploads after a keyed one
						if (from.Keys || (from.ProvidersKeys != nil && *from.ProvidersKeys)) && (merged.ProvidersKeys != nil && *merged.ProvidersKeys) && !merged.Keys {
							t.Error("the providers carry their scope without the whole-bundle bit, which an older reader would lose the keys to")
						}
					} else if merged.Keys != to.Keys {
						t.Error("taking the library changed the providers' or the whole bundle's credential policy")
					}
					want := next
					if source == "keyless" && server != "keyless" {
						want = old
					}
					if part == "library" && source == "clear" {
						// an empty secret is not a clear: it is mostly a
						// redaction the uploader downloaded and never held
						want = old
					}
					if part == "providers" {
						p := merged.Providers[0]
						if p.Key != want || p.BalanceToken != want || (*merged.Searches)[0].Key != want || merged.Library != to.Library {
							t.Error("provider/search credentials were lost, an explicit clear was ignored, or the library changed")
						}
					} else if merged.Library.MCP[0].Env["GITHUB_TOKEN"] != want || merged.Library.MCP[0].Headers["Authorization"] != want || !reflect.DeepEqual(merged.Providers, to.Providers) {
						t.Error("library credentials were lost, an explicit clear was ignored, or providers changed")
					}
					plain, _ := json.Marshal(merged)
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(plain, &fields); err != nil {
						t.Fatal(err)
					}
					if (merged.Keys || string(fields[part+"Keys"]) == "true") != (server != "keyless" || source != "keyless") {
						t.Error("the selected part did not retain its own credential scope")
					}
					afterTo, _ := json.Marshal(to)
					afterFrom, _ := json.Marshal(from)
					if !slices.Equal(beforeTo, afterTo) || !slices.Equal(beforeFrom, afterFrom) {
						t.Error("taking a part mutated an input bundle")
					}
				})
			}
		}
	}
}

// A providers upload carries the saved proxies they name, and a keyless one
// keeps the passwords the server already has.
func TestTakeSavedProxies(t *testing.T) {
	to := backup.Bundle{Version: backup.BundleVersion, Keys: true, ProvidersKeys: backup.Flag(true),
		Proxies: &[]settings.NamedProxy{{ID: "px1", Name: "Office", URL: "http://o:1", Pass: "server"}}}
	from := backup.Bundle{Version: backup.BundleVersion, ProvidersKeys: backup.Flag(false),
		Proxies: &[]settings.NamedProxy{{ID: "px1", Name: "Office", URL: "http://o:2"}, {ID: "px2", Name: "Home", URL: "http://h:1"}}}
	take(&to, from, "providers")
	if to.Proxies == nil || len(*to.Proxies) != 2 || (*to.Proxies)[0].URL != "http://o:2" || (*to.Proxies)[0].Pass != "server" {
		t.Fatalf("%+v", to.Proxies)
	}
	if backup.Bundle(to).Proxies == nil || hashes(to)["providers"] == hashes(backup.Bundle{Version: backup.BundleVersion})["providers"] {
		t.Fatal("a change in the saved proxies doesn't change the providers' hash")
	}
}
