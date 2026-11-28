package gui

import "github.com/yetone/magpie/internal/agent"

// modelMaker is the logo of a model's maker, known by its id (a relay's
// vendor/ prefix taken off), else by its name (Bedrock's us.anthropic.
// ids); "" for a maker magpie doesn't know. A provider's editor groups its
// models by it (WJaunita45604 on X), and an agent's model list does when
// asked to (elis_pina on X), so the two never tell a model's maker apart.
func modelMaker(id, name string) string {
	if m := agent.ModelIcon(id); m != "" || name == "" {
		return m
	}
	return agent.ModelIcon(name)
}
