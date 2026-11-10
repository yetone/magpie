package provider

import (
	"net"

	"github.com/yetone/magpie/internal/netproxy"
)

// SkipsRedaction says requests to p go unmasked: the user set Unredacted
// ("Send requests unmasked") and p has somewhere they go. It is the user's
// word for each provider whatever the address (AcMilan on Discord), a
// subscription's and a plugin's too (isee1_1 on Discord: a plugin's
// provider never had the switch): the editor cautions while it is on for
// one not on this computer or the local network, which a signed-in
// account's or a plugin's vendor never is, and nothing here looks one up.
// Another magpie (a Remote magpie) set so is sent the request as written,
// with no X-Magpie-Masked-As, so it masks by its own Settings › Privacy
// and its own providers' switches before anything reaches a vendor
// (CrazyMouse_TT on X: a model of the user's own behind that magpie);
// left off, it is sent the request masked here, as before.
func (p Provider) SkipsRedaction() bool {
	if !p.Unredacted {
		return false
	}
	if p.Account != nil {
		return true // the agent's sign-in or a plugin's: its vendor is where it goes
	}
	for _, b := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Decide} {
		if b != "" {
			return true
		}
	}
	return false
}

// LocalHost says host is on this machine or the local network: localhost,
// a .local name, a loopback, private or link-local IP, or a name every
// address of which is one (netproxy.LocalName, looked up now and kept a
// minute; one that doesn't resolve isn't). Only the editor's caution asks
// it (/api/local-hosts), never a request.
func LocalHost(host string) bool {
	if local(host) {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return netproxy.LocalIP(ip)
	}
	return netproxy.LocalName(host)
}
