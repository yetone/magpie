package agent

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// pickedInstead is the model an agent's model id moves to when the user
// has since unchecked it in its provider's model list (provider.Unpicked):
// the first of that provider's models magpie lists for the agent now. ""
// leaves it where it is: a model still picked, one of a provider with no
// picks (its list is the vendor's, and a request may name a model it
// lacks), or a provider with none of its models listed any more.
//
// JNDliuliuM on X: picking new models for a provider changed Codex's list,
// but Codex CLI, restarted too, still started on the old model, which
// magpie had left in its config, and Resolve still served it. The user
// picked that model through magpie and has since taken it off the
// provider's list, so the provider's picks are what they chose last.
func pickedInstead(agent, id string) string {
	pid, ok := provider.Unpicked(id)
	if !ok {
		return ""
	}
	for _, m := range sharedModels(agent) {
		if strings.HasPrefix(m.ID, pid+"/") && m.ID != id {
			return m.ID
		}
	}
	return ""
}
