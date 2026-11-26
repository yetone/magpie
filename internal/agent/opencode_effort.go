package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// OpenCode's default reasoning effort is a variant of the model, one of
// those magpie writes for each of its models (openCodeVariants), picked
// where each OpenCode reads one (0xRichardH on X):
//
//   - OpenCode 2 after the model itself, "provider/model#variant"
//     (packages/schema/src/config/model.ts, ConfigModel.Selection, at
//     v2.3.2). Its TUI starts a session on it unless the user picked a
//     variant of the model there, or an agent's own model says one
//     (tui/src/context/local.tsx, preferredSelection).
//   - OpenCode 1 as an agent's, agent.<name>.variant, used only when that
//     agent's own model is the one asked (opencode/src/session/prompt.ts,
//     createUserMessage, 1.4 to 1.19.6). A variant after the model it
//     doesn't take: 1.19.6 drops it as unsupported (config/v2-compat.ts),
//     earlier ones read it as part of the model's id. So magpie writes the
//     model and the variant for its primary agents, build and plan, where
//     an agent has no model of the user's own, and remembers which models
//     it put there, to move them with the model or take them out.
//
// The model field reads the model without its variant, so a model with one
// is still magpie's to every check of the value.

// openCodeAgents are OpenCode 1's primary agents, the ones a session runs.
var openCodeAgents = []string{"build", "plan"}

// openCodeCut is an OpenCode model, "provider/model#variant", as the model
// and its variant, "" when it names none.
func openCodeCut(v string) (model, variant string) {
	model, variant, _ = strings.Cut(v, "#")
	return model, variant
}

// openCodeJoin is openCodeCut's other way.
func openCodeJoin(model, variant string) string {
	if variant == "" || model == "" {
		return model
	}
	return model + "#" + variant
}

// openCodeEffortKey is the stash's word that magpie put agent's model in
// the OpenCode config at path, for its variant.
func openCodeEffortKey(path, agent string) string {
	return "opencode-effort:" + path + ":" + agent
}

// openCodeOffered are the variants OpenCode has for model v, one of
// magpie's as the config at path names it (v1 the gateway as the agent
// reaches it), from the catalog id's list; levels leaves the fast ones out.
// nil when v is not one of magpie's.
func openCodeOffered(id, path, v1, v string, levels bool) []string {
	p, ref, ok := strings.Cut(v, "/")
	if !ok || p == "" || p != magpieID && ownGatewayProvider(path, ref, v1) != p {
		return nil
	}
	for _, m := range sharedModels(id) {
		if m.ID != ref {
			continue
		}
		var out []string
		for _, p := range openCodeVariants(m.Efforts, m.CodexFast && !levels) {
			out = append(out, p.k)
		}
		return out
	}
	return nil
}

// ocEffort is the default variant of the model in the OpenCode config at
// path, as the OpenCode reading it takes one.
type ocEffort struct {
	path string
	file func() ocFile
	// offered are the variants of a model, nil when they aren't known
	offered func(model string) []string
}

// model is the file's model, without a variant.
func (e ocEffort) model() string {
	raw, ok := edit.GetJSONRaw(e.path, "model")
	if !ok {
		return ""
	}
	m, _ := openCodeCut(openCodeSelection(raw))
	return m
}

// get is the variant the model is asked at when a session names none.
func (e ocEffort) get() string {
	m := e.model()
	if m == "" {
		return ""
	}
	if e.file().two {
		if raw, ok := edit.GetJSONRaw(e.path, "model"); ok {
			if v := openCodeSelectionVariant(raw); v != "" {
				return v
			}
		}
	}
	for _, a := range openCodeAgents {
		if am, _ := edit.GetJSON(e.path, "agent."+a+".model"); am == m {
			if v, _ := edit.GetJSON(e.path, "agent."+a+".variant"); v != "" {
				return v
			}
		}
	}
	return ""
}

// set makes v the model's variant, "" none.
func (e ocEffort) set(v string) error {
	m := e.model()
	if m == "" {
		if v == "" {
			return nil
		}
		return fmt.Errorf("pick OpenCode's model first; the effort is kept with it")
	}
	if e.file().two {
		if err := e.agentsOff(); err != nil {
			return err
		}
		return edit.SetJSON(e.path, edit.KV{Path: "model", Value: openCodeJoin(m, v)})
	}
	for _, a := range openCodeAgents {
		am, _ := edit.GetJSON(e.path, "agent."+a+".model")
		mine := unstashPeek(openCodeEffortKey(e.path, a)) != ""
		switch {
		case am != "" && am != m:
			// a model of the user's own for this agent: its variant is
			// theirs too
			continue
		case v == "" && mine:
			if err := e.agentOff(a); err != nil {
				return err
			}
		case v == "" && am == "":
			// a variant with no model of its own OpenCode doesn't use, and
			// magpie didn't write
			continue
		case v == "":
			if err := edit.DelJSON(e.path, "agent."+a+".variant"); err != nil {
				return err
			}
		default:
			kv := []edit.KV{{Path: "agent." + a + ".variant", Value: v}}
			if am == "" {
				kv = append(kv, edit.KV{Path: "agent." + a + ".model", Value: m})
				stash(map[string]string{openCodeEffortKey(e.path, a): "1"})
			}
			if err := edit.SetJSON(e.path, kv...); err != nil {
				return err
			}
		}
	}
	return nil
}

// moved carries the variant along when the model becomes to, the way the
// model field's Set writes it: to with a variant of its own keeps that,
// else the one the model had stays where to has it too. It writes the
// model, "" taking it out.
func (e ocEffort) moved(to string, write func(string) error) error {
	from, was := e.model(), e.get()
	if to == "" {
		if err := write(""); err != nil {
			return err
		}
		return e.agentsOff()
	}
	to, variant := openCodeCut(to)
	if variant == "" && was != "" && (to == from || slices.Contains(e.offered(to), was)) {
		variant = was
	}
	if e.file().two {
		if err := e.agentsOff(); err != nil {
			return err
		}
		return write(openCodeJoin(to, variant))
	}
	if err := write(to); err != nil {
		return err
	}
	// as written: one of magpie's may be named by a provider of the file's
	// own (openCodeRefAt)
	to = e.model()
	for _, a := range openCodeAgents {
		if unstashPeek(openCodeEffortKey(e.path, a)) == "" {
			continue
		}
		am, _ := edit.GetJSON(e.path, "agent."+a+".model")
		switch {
		case am != from || variant == "":
			if err := e.agentOff(a); err != nil {
				return err
			}
		default:
			if err := edit.SetJSON(e.path, edit.KV{Path: "agent." + a + ".model", Value: to}, edit.KV{Path: "agent." + a + ".variant", Value: variant}); err != nil {
				return err
			}
		}
	}
	return nil
}

// agentsOff takes out each agent's model and variant magpie put there.
func (e ocEffort) agentsOff() error {
	for _, a := range openCodeAgents {
		if unstashPeek(openCodeEffortKey(e.path, a)) != "" {
			if err := e.agentOff(a); err != nil {
				return err
			}
		}
	}
	return nil
}

// agentOff takes out agent a's model and variant, which magpie put there,
// and the agent when nothing else of it is left.
func (e ocEffort) agentOff(a string) error {
	forget(openCodeEffortKey(e.path, a))
	if err := edit.DelJSON(e.path, "agent."+a+".model", "agent."+a+".variant"); err != nil {
		return err
	}
	return e.file().dropEmpty("agent."+a, "agent")
}

// openCodeSelectionVariant is the variant of a model as OpenCode 2 has
// one, raw (openCodeSelection), "" when it names none.
func openCodeSelectionVariant(raw string) string {
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		_, v := openCodeCut(s)
		return v
	}
	var o struct {
		Variant string `json:"variant"`
	}
	if json.Unmarshal([]byte(raw), &o) == nil {
		return o.Variant
	}
	return ""
}
