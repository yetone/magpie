package agent

import (
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
)

// keyAt is the key an agent that reaches the gateway at gw sends it. On
// this machine's loopback that is gateway.Token, which the gateway takes
// from this computer only; from another machine — a WSL distro under NAT,
// which asks Windows' address — the gateway turns a request without a
// named key away, so it is the LAN sharing key, while sharing is on
// (whqtian on Discord: omp, then OpenCode, in WSL were refused). At the
// address Windows has on a WSL network, which the gateway listens on while
// it is on loopback, it is the WSL key (gateway.WSLKey), sharing or not
// (zaidu259434 on X: Codex in WSL failed with sharing off).
func keyAt(gw string) string {
	if onAnotherMachine(gw) {
		if gateway.OnWSLNet(gw) {
			if k := gateway.WSLKey(); k != "" {
				return k
			}
		}
		if k := access.LANSecret(); k != "" {
			return k
		}
	}
	return gateway.Token
}

// KeyAt is keyAt, for what else magpie gives an agent that reaches the
// gateway at gw (the library's MCP servers relayed through it).
func KeyAt(gw string) string { return keyAt(gw) }

// gwKey is keyAt for the agent at p.
func (p place) gwKey() string { return keyAt(p.gw()) }

// agentKeyAt is keyAt for an agent whose key on this machine names it
// (gateway.TokenFor), for requests that carry nothing else of it.
func agentKeyAt(agent, gw string) string {
	k := keyAt(gw)
	if wk := gateway.WSLKeyMade(); k == wk && wk != "" {
		return wk + "-" + agent // the gateway reads it as TokenFor(agent)
	}
	if k != gateway.Token {
		return k
	}
	return gateway.TokenFor(agent)
}

// hereKey is keyAt for the gateway's own address, gateway.URL(): an agent
// on this machine given that address sends the gateway's own token, unless
// MAGPIE_ADDR pins it to an address beyond loopback, which the gateway
// takes only a named key from (#1647, ggbdpq).
func hereKey() string { return keyAt(gateway.URL()) }

// agentKey is agentKeyAt for the gateway's own address, as hereKey.
func agentKey(agent string) string { return agentKeyAt(agent, gateway.URL()) }

// ourKey says k is a key magpie gives an agent: gateway.Token, the LAN
// sharing key one that reaches the gateway from elsewhere is given, or the
// WSL key one in a WSL distro under NAT is.
func ourKey(k string) bool {
	if k == gateway.Token {
		return true
	}
	return k != "" && (k == access.LANSecret() || k == gateway.WSLKeyMade())
}

// gatewayTakes says the gateway would authenticate a request carrying k: a
// caller key of this magpie's own (one issued, or a value the user brought),
// so an agent holding it in its store of its own still goes through magpie
// (#1332). A key of another magpie's, which this gateway would refuse, is
// not.
func gatewayTakes(k string) bool {
	who, ok := access.Authenticate(k)
	// Magpie Browser's key reaches no model, so an agent holding it
	// doesn't go through magpie
	return ok && !who.Browser
}

// gatewayKey says want is a key magpie gives an agent for its gateway:
// gateway.Token, the LAN sharing key, or one naming the agent
// (gateway.TokenFor).
func gatewayKey(want string) bool {
	if ourKey(want) || strings.HasPrefix(want, gateway.Token+"-") && !strings.ContainsAny(want, "/:") {
		return true
	}
	wk := gateway.WSLKeyMade()
	return wk != "" && strings.HasPrefix(want, wk+"-") && !strings.ContainsAny(want, "/:")
}

// keyHolds says the value v an agent's config has where magpie wrote want
// still reaches magpie: want itself, or, where want is magpie's key for its
// gateway, a key of the gateway's own the user put there instead (Gateway
// keys), which the gateway takes just the same (PaulT on Discord: Hermes
// with a key of his own was said not to go through magpie).
func keyHolds(v, want string) bool {
	return v == want || v != "" && gatewayKey(want) && gatewayTakes(v)
}
