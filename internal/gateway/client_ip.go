package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/usageapi"
)

// clientIP is the address a request came from, for its usage row: the
// connection's own (peerAddr), never a forwarding header, which anyone can
// write. "" when RemoteAddr isn't an address.
func clientIP(r *http.Request) string {
	a, ok := peerAddr(r)
	if !ok || !a.IsValid() {
		return ""
	}
	return a.String()
}

// withoutClientIPs is a ledger page with the address each call came from
// taken out: the gateway's usage API answers other computers (Remote
// magpie, a shared gateway's key holders), and where this computer's
// clients connect from stays on it. The Usage page reads it from the GUI's
// own /api/usage/requests.
func withoutClientIPs(l usageapi.Ledger) usageapi.Ledger {
	for i := range l.Rows {
		l.Rows[i].ClientIP = ""
	}
	return l
}
