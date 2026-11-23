package gateway

import (
	"net/http"
	"testing"
)

// A TLS record that doesn't decrypt is the stream under it mangled, by the
// far side or by a box between (netproxy.recordError). The vendor never read
// a request it could answer, so the account is not the one that failed: read
// as the account's own failure, a proxy between magpie and a healthy vendor
// rested the provider, and a group lost a member that was never asked.
func TestTLSRecordErrorDoesNotRest(t *testing.T) {
	for _, body := range []string{
		`Post "https://api.commandcode.ai/provider/v1/responses": remote error: tls: bad record MAC`,
		`local error: tls: bad record MAC`,
		`tls: received record with version eaf1 when expecting version 303`,
		`tls: oversized record received with length 20000`,
	} {
		if got := failure(http.StatusBadGateway, []byte(body)); got != failProxy {
			t.Errorf("failure(%q) = %q, want %q: the stream was mangled before the vendor could answer it", body, got, failProxy)
		}
	}
	// A vendor's own failure is still its own: only the 502 a mangled TLS
	// stream makes is nobody's.
	if got := failure(http.StatusInternalServerError, []byte("tls: bad record MAC")); got == failProxy {
		t.Errorf("a 500 read as the proxy's: %q", got)
	}
	if got := failure(http.StatusBadGateway, []byte(`{"error":{"message":"upstream is down"}}`)); got == failProxy {
		t.Errorf("a plain 502 read as the proxy's: %q", got)
	}
}
