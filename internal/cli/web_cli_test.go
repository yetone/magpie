package cli

import "testing"

// magpie web on loopback behind a reverse proxy (#1479) prints the proxy's
// link with the key when told the proxy's address, so nobody joins
// "/?k=…" onto it by hand: MAGPIE_WEB_URL, else MAGPIE_PUBLIC_URL with no
// port, or https on a port while magpie serves http (#8014); one with a
// port otherwise names a published Docker port. None without either.
func TestWebProxyLink(t *testing.T) {
	key := "/?k=0123456789abcdef"
	t.Setenv("MAGPIE_WEB_URL", "")
	for _, c := range []struct{ env, want string }{
		{"https://magpie.example.com", "https://magpie.example.com/?k=0123456789abcdef"},
		{"https://magpie.example.com/", "https://magpie.example.com/?k=0123456789abcdef"},
		{"magpie.example.com", "http://magpie.example.com/?k=0123456789abcdef"},
		{"https://example.com/magpie/", "https://example.com/magpie/?k=0123456789abcdef"},
		{"http://192.168.1.20:3425", ""},
		{"https://magpie.example.com:666", "https://magpie.example.com:666/?k=0123456789abcdef"}, // #8014
		{"", ""},
	} {
		t.Setenv("MAGPIE_PUBLIC_URL", c.env)
		if got := proxyLink(key); got != c.want {
			t.Errorf("MAGPIE_PUBLIC_URL=%q: %q, want %q", c.env, got, c.want)
		}
	}
	// the page's own proxy address, when the gateway's is elsewhere
	t.Setenv("MAGPIE_PUBLIC_URL", "https://api.example.com")
	for _, c := range []struct{ env, want string }{
		{"https://magpie.example.com/", "https://magpie.example.com/?k=0123456789abcdef"},
		{"magpie.example.com:8443", "https://magpie.example.com:8443/?k=0123456789abcdef"},
		{"https://magpie.example.com/?x=1", ""},
		{"ftp://magpie.example.com", ""},
	} {
		t.Setenv("MAGPIE_WEB_URL", c.env)
		if got := proxyLink(key); got != c.want {
			t.Errorf("MAGPIE_WEB_URL=%q: %q, want %q", c.env, got, c.want)
		}
	}
}

// `magpie web` opens the app's page when the app serves it on the port it
// would take, and starts its own on any other.
func TestWebSamePort(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"127.0.0.1:3430", "127.0.0.1:3430", true},
		{"0.0.0.0:3430", "127.0.0.1:3430", true},
		{"127.0.0.1:3430", "0.0.0.0:3431", false},
		{"", "127.0.0.1:3430", false},
		{"127.0.0.1:3430", "3430", false},
	} {
		if got := samePort(c.a, c.b); got != c.want {
			t.Errorf("samePort(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
