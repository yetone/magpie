package provider

import "slices"

// SignInMoves is how magpie moves an agent's own sign-in between its
// accounts, as NextLogin decides it, for the Providers page to say
// beside the accounts: a user who left a "keep signed in" tick on, or
// whose Claude Code went through the gateway, saw no switch and had no
// way to tell why (the owner, 11-11).
type SignInMoves struct {
	// Mode: "kept" (stays on one account, KeepLogin / KeepLoginAs),
	// "gateway" (Claude Code sends its requests to magpie's gateway,
	// which moves each to an account with room, its sign-in left
	// where the user put it), "off" (the subscription is off or hidden:
	// magpie leaves the sign-in alone) or "moves" (moved on once spent).
	Mode string `json:"mode"`
	// Share: the share of a window at which the account it is on counts
	// spent and is moved off (SpentShareOf); 100 is used up. Only "moves".
	Share float64 `json:"share,omitempty"`
	// Back: once the account moved off has room again, the agent is
	// signed back in to it (returnsToFirst). Only "moves".
	Back bool `json:"back,omitempty"`
	// Routed: kept, but Claude Code goes through the gateway, so turning
	// the switch on moves requests, not its sign-in (mode "gateway"
	// then). Only "kept".
	Routed bool `json:"routed,omitempty"`
}

// SignInMovesOf is SignInMoves for agent; nil for one whose sign-in magpie
// never moves.
func SignInMovesOf(agent string) *SignInMoves {
	if !slices.Contains(switchedAgents, agent) {
		return nil
	}
	if leftAlone(agent) {
		return &SignInMoves{Mode: "off"}
	}
	share, keep := loginSwitching(agent)
	routed := agent == "claude" && ClaudeThroughMagpie() && !SwitchOnly(agent)
	switch {
	case keep:
		return &SignInMoves{Mode: "kept", Routed: routed}
	case routed:
		return &SignInMoves{Mode: "gateway"}
	}
	return &SignInMoves{Mode: "moves", Share: share, Back: returnsToFirst(agent)}
}
