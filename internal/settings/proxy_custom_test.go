package settings

import "testing"

// The remembered custom proxy holds the proxy's login, so it is this
// computer's own as the proxy is: a sync or a restored backup keeps the
// one here and never brings another computer's.
func TestProxyCustomOwn(t *testing.T) {
	from := Settings{Theme: "dark", Proxy: "direct", ProxyCustom: "socks5://a:b@there:1080"}
	from.KeepOwn(Settings{ProxyCustom: "socks5://c:d@here:1080"})
	if from.ProxyCustom != "socks5://c:d@here:1080" || from.Theme != "dark" {
		t.Fatalf("KeepOwn: %+v", from)
	}
	from.KeepOwn(Settings{})
	if from.ProxyCustom != "" {
		t.Fatal("another computer's proxy login taken here")
	}
}

// Plugin downloads' proxy, and the one remembered for its Custom, are
// this computer's own as the proxy is.
func TestPluginProxyOwn(t *testing.T) {
	from := Settings{Theme: "dark", PluginProxy: "socks5://a:b@there:1080", PluginProxyCustom: "socks5://a:b@there:1080"}
	from.KeepOwn(Settings{PluginProxy: "direct", PluginProxyCustom: "http://c:d@here:8080"})
	if from.PluginProxy != "direct" || from.PluginProxyCustom != "http://c:d@here:8080" || from.Theme != "dark" {
		t.Fatalf("KeepOwn: %+v", from)
	}
	from.KeepOwn(Settings{})
	if from.PluginProxy != "" || from.PluginProxyCustom != "" {
		t.Fatal("another computer's plugin proxy taken here")
	}
}

// Plugin downloads' proxy is checked as the proxy is, and the last of the
// user's own is remembered for Custom while it follows or goes direct.
func TestPluginProxySaved(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{PluginProxy: " socks5://u:p@127.0.0.1:1080 "}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.PluginProxy != "socks5://u:p@127.0.0.1:1080" || s.PluginProxyCustom != s.PluginProxy || s.Proxy != "" {
		t.Fatalf("%+v", s)
	}
	if err := Save(Settings{PluginProxy: "direct", PluginProxyCustom: Load().PluginProxyCustom}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.PluginProxy != "direct" || s.PluginProxyCustom != "socks5://u:p@127.0.0.1:1080" {
		t.Fatalf("%+v", s)
	}
	if Save(Settings{PluginProxy: "ftp://x:21"}) == nil {
		t.Fatal("an ftp proxy accepted")
	}
	if Load().PluginProxy != "direct" {
		t.Fatal("a refused value overwrote the saved one")
	}
}
