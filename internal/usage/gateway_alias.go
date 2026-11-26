package usage

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// MagpieAlias says id is one of magpie's own alias ids for a model, the names
// the gateway lists Claude Desktop, Claude Code under it and Claude Science
// by: anthropic/magpie-<number>, mythos-magpie-<number>, claude-magpie-<number>
// and magpie-<number>.anthropic.<Claude model>, with or without Claude Code's
// "[1m]", the number ten digits (gateway's aliasNumber). Only the gateway
// knows them, so a call that names one went through it. A test in the
// gateway keeps this in step with the ids it lists.
func MagpieAlias(id string) bool {
	id = strings.TrimSuffix(id, "[1m]")
	for _, p := range []string{"anthropic/magpie-", "mythos-magpie-", "claude-magpie-"} {
		if n, ok := strings.CutPrefix(id, p); ok {
			return tenDigits(n)
		}
	}
	if rest, ok := strings.CutPrefix(id, "magpie-"); ok {
		if n, _, ok := strings.Cut(rest, ".anthropic."); ok {
			return tenDigits(n)
		}
	}
	return false
}

func tenDigits(s string) bool {
	if len(s) != 10 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// viaGateway is a call its agent's session file records under one of
// magpie's alias ids (Claude Desktop's Code tab writes the id the gateway
// listed it by). The gateway's own log counts that call under the model that
// served it, so the file's is not another call but the same one: counted
// beside it, it showed twice, once under an alias that is no model
// (CrazyJal on X). A call that never went through the gateway never names
// an alias.
//
// A routing group's id (group/<id>) is the same: only magpie serves one, so
// a call under it went through magpie whether or not it paired with the
// gateway's record (magpieOnly). Qoder CN's calls under group/cheap-model
// were listed again as a local session's (Toby5080 on X).
func viaGateway(c sessions.Call) bool {
	return MagpieAlias(c.Model) || magpieOnly(c.Model) || c.Requested != "" && (MagpieAlias(c.Requested) || magpieOnly(c.Requested))
}

// magpieOnly says id names a routing group, "group/<id>" (any case, as
// access reads it), alone or after magpie's own provider id as an agent
// that writes "<provider>/<model>" puts it. No vendor serves such a model.
// A found group's "auto/<model>" isn't taken: a relay may name a model so.
func magpieOnly(id string) bool {
	id = strings.TrimPrefix(id, "magpie/")
	return len(id) > len(provider.GroupPrefix) && strings.EqualFold(id[:len(provider.GroupPrefix)], provider.GroupPrefix)
}

// ownCalls is cs without the calls the gateway's log counts (viaGateway). cs
// is left as it is: a reader's slice may be one it keeps.
func ownCalls(cs []sessions.Call) []sessions.Call {
	n := 0
	for _, c := range cs {
		if viaGateway(c) {
			n++
		}
	}
	if n == 0 {
		return cs
	}
	out := make([]sessions.Call, 0, len(cs)-n)
	for _, c := range cs {
		if !viaGateway(c) {
			out = append(out, c)
		}
	}
	return out
}
