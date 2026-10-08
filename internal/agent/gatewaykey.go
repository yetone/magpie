package agent

import (
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
)

// keyAt is the key an agent that reaches the gateway at gw sends it. On
// this machine's loopback that is gateway.Token, which the gateway takes
// from this computer only; from another machine — a WSL distro under NAT,
// which asks Windows' address — the gateway turns a request without a
// named key away, so it is the LAN sharing key, while sharing is on
// (whqtian on Discord: omp, then OpenCode, in WSL were refused).
func keyAt(gw string) string {
	if onAnotherMachine(gw) {
		if k := access.LANSecret(); k != "" {
			return k
		}
	}
	return gateway.Token
}

// gwKey is keyAt for the agent at p.
func (p place) gwKey() string { return keyAt(p.gw()) }

// agentKeyAt is keyAt for an agent whose key on this machine names it
// (gateway.TokenFor), for requests that carry nothing else of it.
func agentKeyAt(agent, gw string) string {
	if k := keyAt(gw); k != gateway.Token {
		return k
	}
	return gateway.TokenFor(agent)
}

// ourKey says k is a key magpie gives an agent: gateway.Token, or the LAN
// sharing key one that reaches the gateway from elsewhere is given.
func ourKey(k string) bool {
	if k == gateway.Token {
		return true
	}
	return k != "" && k == access.LANSecret()
}

// gatewayTakes says the gateway would authenticate a request carrying k: a
// caller key of this magpie's own (one issued, or a value the user brought),
// so an agent holding it in its store of its own still goes through magpie
// (#1332). A key of another magpie's, which this gateway would refuse, is
// not.
func gatewayTakes(k string) bool {
	_, ok := access.Authenticate(k)
	return ok
}
