package agent

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// Desktop's built-in SSH remote sessions run its Code tab on another
// machine, which is handed the profile's gateway address and key
// (Desktop 2.x, the sshRemoteSessions beta). They are refused when the
// address is this computer's own ("provider_loopback": that endpoint is
// this computer, which a remote host cannot reach), and a non-loopback
// address must be https (the profile's schema: "must use https (or http on
// loopback)"), and the profile's sshHostAllowlist, empty by default in 3p
// mode, says which hosts may be used at all (zyc-bit and ZYar-er on X,
// #4546). So Desktop can be given, instead of 127.0.0.1, one of this
// computer's https:// addresses (gateway.LANURLs) with the LAN sharing
// key, which the gateway takes from other machines: the setting is
// DesktopGateway, the "gateway" field of Desktop's row, offered while
// sharing is on and an address is https. Loopback stays the default.

// desktopLANURLs are the addresses Desktop may be pointed at beyond
// loopback: this computer's https:// ones, while the gateway is shared (its
// key beyond loopback is the sharing key, keyAt).
func desktopLANURLs() []string {
	if access.LANSecret() == "" {
		return nil
	}
	var out []string
	for _, u := range gateway.LANURLs() {
		if strings.HasPrefix(u, "https://") {
			out = append(out, u)
		}
	}
	return out
}

// desktopGateway is the address Desktop's profile gets: the one picked in
// DesktopGateway while it is still one of this computer's https addresses
// (the first, where the network's address changed since), else the gateway's
// own on loopback.
func desktopGateway() string {
	pick := settings.Shared().DesktopGateway
	if pick == "" {
		return gateway.URL()
	}
	urls := desktopLANURLs()
	switch {
	case slices.Contains(urls, pick):
		return pick
	case len(urls) > 0:
		return urls[0]
	}
	return gateway.URL()
}

// desktopKey is the key Desktop's profile gets at gw.
func desktopKey(gw string) string { return agentKeyAt("claude-desktop", gw) }

// desktopSeedSSH puts Desktop's sshHostAllowlist in the profile, seeded once with every
// host where the profile is pointed beyond loopback and has none: 3p mode
// refuses every host without it, and the address was picked to reach one.
// The user may narrow it in Desktop's window, or take it away; magpie never
// writes it again (desktopSeedOnce).
func desktopSeedSSH(p desktopPaths) error {
	return desktopSeedOnce(p, desktopSSH, "sshHostAllowlist", []string{"*"})
}

// desktopGatewayField picks where Desktop reaches the gateway: this
// computer (loopback), or one of its https addresses for SSH remote
// sessions.
func desktopGatewayField(ps []desktopPaths, apply func() error) Field {
	return Field{
		Key: "gateway", Label: "gateway",
		Get: func() string {
			if g := desktopGateway(); g != gateway.URL() {
				return g
			}
			return ""
		},
		Set: func(v string) error {
			v = strings.TrimRight(strings.TrimSpace(v), "/")
			if v != "" && !slices.Contains(desktopLANURLs(), v) {
				return fmt.Errorf("Claude Desktop can be pointed at %q only while magpie is shared on the local network and serves https:// there (Settings > Network and sharing: Share on local network, HTTPS)", v)
			}
			if err := settings.SetDesktopGateway(v); err != nil {
				return err
			}
			if !desktopWiredAny(ps) {
				return nil
			}
			return apply()
		},
		Options: func(map[string]string) []Option {
			urls := desktopLANURLs()
			if !desktopWiredAny(ps) || len(urls) == 0 {
				return nil
			}
			out := []Option{{Value: "", Label: "This computer", Note: gateway.URL()}}
			for _, u := range urls {
				out = append(out, Option{Value: u, Label: u})
			}
			return out
		},
	}
}

// desktopOurAddress: a is an address of this computer's own that other
// machines reach the gateway at (gateway.LANURLs, or the one Desktop is
// pointed at), as a profile exported here holds it.
func desktopOurAddress(a *url.URL) bool {
	for _, l := range append(gateway.LANURLs(), settings.Shared().DesktopGateway) {
		u, err := url.Parse(l)
		if err == nil && u.Hostname() == a.Hostname() && portOf(u) == portOf(a) {
			return true
		}
	}
	return false
}

// portOf is u's port, its scheme's own where it has none.
func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
