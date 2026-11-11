package backup

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A provider that names a saved proxy ("@id") works on the computer it is
// carried to with no setup: the list goes with the providers, the passwords
// only when their keys do. One that still names a proxy this computer lacks
// fails, and says which (@tsingchao on X).
func TestSavedProxiesTravelWithProviders(t *testing.T) {
	for _, keys := range []bool{false, true} {
		t.Run(map[bool]string{false: "keyless", true: "keys"}[keys], func(t *testing.T) {
			home(t)
			s := settings.Load()
			s.Proxies = []settings.NamedProxy{{ID: "px1", Name: "Office", URL: "http://10.0.0.1:8080", User: "me", Pass: "hunter2"}}
			if err := settings.Save(s); err != nil {
				t.Fatal(err)
			}
			id, err := provider.Add(provider.Provider{Name: "Via office", Chat: "https://a.example.com/v1", Key: "sk-a", Models: []string{"m"}, Proxy: "@px1"})
			if err != nil {
				t.Fatal(err)
			}
			b, err := Collect(keys, "test")
			if err != nil {
				t.Fatal(err)
			}
			if b.Proxies == nil || len(*b.Proxies) != 1 {
				t.Fatalf("the bundle holds no saved proxy: %+v", b.Proxies)
			}
			if b.Settings != nil && len(b.Settings.Proxies) != 0 {
				t.Fatal("the list rides in Settings too, passwords and all")
			}
			if got := (*b.Proxies)[0].Pass; keys != (got != "") {
				t.Fatalf("password in the bundle = %q with keys=%v", got, keys)
			}

			home(t) // the other computer, which has no proxies
			if _, err := Restore(b, Parts{Providers: true}); err != nil {
				t.Fatal(err)
			}
			if provider.ProxyGone(mustProvider(t, id)) != "" {
				t.Fatal("the provider's proxy is missing after the restore")
			}
			u, err := netproxy.Parse("@px1")
			if err != nil || u.Host != "10.0.0.1:8080" {
				t.Fatalf("Parse(@px1) = %v, %v", u, err)
			}
			if pass, _ := u.User.Password(); keys != (pass == "hunter2") {
				t.Fatalf("restored password %q with keys=%v", pass, keys)
			}

			// no list came (an older magpie): fails closed, naming the proxy it was
			s2 := settings.Load()
			s2.Proxies = nil
			if err := settings.Save(s2); err != nil {
				t.Fatal(err)
			}
			if _, err := netproxy.Parse("@px1"); err == nil || !strings.Contains(err.Error(), "Office") {
				t.Fatalf("a missing proxy's error must name it: %v", err)
			}
			if got := provider.ProxyGone(mustProvider(t, id)); got != "Office" {
				t.Fatalf("ProxyGone = %q, want Office", got)
			}
		})
	}
}

func mustProvider(t *testing.T, id string) provider.Provider {
	t.Helper()
	for _, p := range provider.All() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no provider %q", id)
	return provider.Provider{}
}

func TestMergeProxies(t *testing.T) {
	cur := []settings.NamedProxy{{ID: "a", Name: "Home", URL: "http://h:1", Pass: "mine"}, {ID: "b", Name: "Lab", URL: "http://l:1"}}
	in := []settings.NamedProxy{{ID: "a", Name: "Home", URL: "http://h:2"}, {ID: "c", Name: "lab", URL: "http://c:1", Pass: "theirs"}}
	got := settings.MergeProxies(cur, in, false)
	if len(got) != 3 || got[0].URL != "http://h:2" || got[0].Pass != "mine" || got[1].ID != "b" || got[2].Pass != "" || got[2].Name == "lab" {
		t.Fatalf("%+v", got)
	}
	if got = settings.MergeProxies(cur, in, true); got[0].Pass != "" && got[2].Pass != "theirs" {
		t.Fatalf("%+v", got)
	}
}
