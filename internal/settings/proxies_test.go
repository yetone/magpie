package settings

import (
	"strings"
	"testing"
)

func savedFixture() []NamedProxy {
	return []NamedProxy{
		{ID: "pxone", Name: "Office", URL: "127.0.0.1:7890", User: "me@corp", Pass: "p@ss:w/rd"},
		{ID: "pxtwo", Name: "Home", URL: "socks5://127.0.0.1:1080"},
	}
}

// A choice that names a saved proxy resolves to its address with the login
// in it; an inline address is as typed; a reference to a proxy that isn't
// there is not resolved, so a request fails and never goes direct.
func TestResolveProxy(t *testing.T) {
	if err := Save(Settings{Proxies: savedFixture()}); err != nil {
		t.Fatal(err)
	}
	got, ok := ResolveProxy("@pxone")
	if !ok || got != "http://me%40corp:p%40ss%3Aw%2Frd@127.0.0.1:7890" {
		t.Fatalf("resolved %q, %v", got, ok)
	}
	if got, ok := ResolveProxy("@pxtwo"); !ok || got != "socks5://127.0.0.1:1080" {
		t.Fatalf("resolved %q, %v", got, ok)
	}
	if got, ok := ResolveProxy("http://10.0.0.1:3128"); !ok || got != "http://10.0.0.1:3128" {
		t.Fatalf("inline: %q, %v", got, ok)
	}
	if got, ok := ResolveProxy("direct"); !ok || got != "direct" {
		t.Fatalf("direct: %q, %v", got, ok)
	}
	if _, ok := ResolveProxy("@gone"); ok {
		t.Fatal("a reference to a proxy that isn't saved resolved")
	}
}

func TestSavedProxiesAreChecked(t *testing.T) {
	bad := map[string][]NamedProxy{
		"no name":        {{ID: "a", URL: "127.0.0.1:1"}},
		"same id":        {{ID: "a", Name: "x", URL: "127.0.0.1:1"}, {ID: "a", Name: "y", URL: "127.0.0.1:2"}},
		"same name":      {{ID: "a", Name: "x", URL: "127.0.0.1:1"}, {ID: "b", Name: "X", URL: "127.0.0.1:2"}},
		"login in url":   {{ID: "a", Name: "x", URL: "http://u:p@127.0.0.1:1"}},
		"not an address": {{ID: "a", Name: "x", URL: "ftp://127.0.0.1:1"}},
		"direct":         {{ID: "a", Name: "x", URL: "direct"}},
		"a reference":    {{ID: "a", Name: "x", URL: "@b"}},
		"an id with a @": {{ID: "@a", Name: "x", URL: "127.0.0.1:1"}},
	}
	for name, ps := range bad {
		if err := Save(Settings{Proxies: ps}); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	if err := Save(Settings{Proxies: savedFixture()}); err != nil {
		t.Fatal(err)
	}
}

// Settings' own two choices may name a saved proxy, and only one that exists.
func TestSettingsProxyNamesASavedOne(t *testing.T) {
	if err := Save(Settings{Proxy: "@pxone", Proxies: savedFixture()}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Proxy != "@pxone" || s.ProxyCustom != "" {
		t.Fatalf("saved %+v", s)
	}
	if err := Save(Settings{Proxy: "@nope", Proxies: savedFixture()}); err == nil {
		t.Fatal("a reference to a proxy that isn't saved was saved")
	}
	err := Save(Settings{PluginProxy: "@nope", Proxies: savedFixture()})
	if err == nil || !strings.Contains(err.Error(), "plugin downloads") {
		t.Fatalf("plugin downloads' reference to nothing: %v", err)
	}
	if err := Save(Settings{Proxy: "http://1.2.3.4:5"}); err != nil {
		t.Fatalf("an inline address as it always was: %v", err)
	}
}

// The list is this computer's own: a restore or a sync leaves it as it is.
func TestSavedProxiesAreKept(t *testing.T) {
	if err := Save(Settings{Proxies: savedFixture()}); err != nil {
		t.Fatal(err)
	}
	cur := Load()
	var s Settings
	s.KeepOwn(cur)
	if len(s.Proxies) != 2 || s.Proxies[0].Pass != "p@ss:w/rd" {
		t.Fatalf("KeepOwn left %+v", s.Proxies)
	}
}
