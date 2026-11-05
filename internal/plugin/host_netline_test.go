package plugin

import (
	"strings"
	"testing"
)

// The line a host's start and a moved proxy log says the proxy without
// its password, NO_PROXY, and how Bun is told which roots to trust.
func TestNetLineSaysNoSecret(t *testing.T) {
	got := netLine([]string{"PATH=/bin", "HTTPS_PROXY=http://me:s3cret@127.0.0.1:7897", "http_proxy=http://127.0.0.1:7897",
		"NO_PROXY=localhost,*.cn", "NODE_USE_SYSTEM_CA=1", "NODE_EXTRA_CA_CERTS=ca.pem", "SSL_CERT_DIR="})
	want := "HTTPS_PROXY=http://127.0.0.1:7897 HTTP_PROXY=http://127.0.0.1:7897 NO_PROXY=localhost,*.cn NODE_EXTRA_CA_CERTS=ca.pem NODE_USE_SYSTEM_CA=1"
	if got != want {
		t.Errorf("netLine = %q\nwant      %q", got, want)
	}
	if strings.Contains(got, "s3cret") || strings.Contains(got, "me:") {
		t.Errorf("netLine says the proxy's user or password: %q", got)
	}
	if got := netLine(strings.Split(hostProxy([]string{"NO_PROXY=localhost"}), "\x00")); got != "no proxy NO_PROXY=localhost" {
		t.Errorf("netLine with no proxy = %q", got)
	}
}
