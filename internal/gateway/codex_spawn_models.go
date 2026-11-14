package gateway

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
)

// Codex's spawn_agent tool names only the first five models of its
// catalog to its lead (codex-rs core/src/agent/child_config.rs
// MAX_SPAWN_AGENT_MODEL_OVERRIDES, in multi_agents_spec.rs
// spawn_agent_models_description and, with model_catalog_in_context,
// context/world_state/model_catalog.rs), Codex's own ahead of magpie's,
// though it takes any model of the catalog named in spawn_agent's model
// argument (find_spawn_agent_model_name). So a lead could put its
// subagents on five models at most, with "Codex subagents on any model"
// on or not (Gearclickist on Discord). spawnAgentModels has the tool name
// the rest of the catalog too, the models a subagent spawned from that
// lead can work on: every one, but only a ChatGPT account's when the lead
// is answered by the ChatGPT backend in multi-agent V2, which seals its
// subagents' tasks for a ChatGPT account alone (#619).

// spawnMore heads the models spawnAgentModels adds to spawn_agent's
// description; one that has it already is left as it is. It goes upstream
// with the rest of the prompt, to OpenAI too when the lead is a ChatGPT
// account, so it stays neutral: it names neither magpie nor Codex's limit,
// and says nothing of who the agent is.
const spawnMore = "More models that may be set as `model`:"

// spawnMost is the most models spawnAgentModels adds, to keep the tool's
// description in bounds.
const spawnMost = 200

type spawnChoice struct{ slug, name string }

// spawnAgentModels is body, a Codex request at CodexPath (codexPath) or at
// /v1/responses (Codex with magpie as its model provider), with its
// spawn_agent tool naming every model of the catalog that Codex was handed
// there, not only the first five. lead is the request's model.
func spawnAgentModels(r *http.Request, body []byte, lead string, codexPath bool) []byte {
	listed := bodyHas(body, `"spawn_agent"`) && bodyHas(body, "Available model overrides")
	if !listed && !hasSpawnCatalog(body) {
		return body
	}
	choices := spawnChoices(r, lead, codexPath)
	if len(choices) == 0 {
		return body
	}
	// Codex sends spawn_agent on every request, so only the members that
	// change are decoded and put back, the rest of the body (the
	// conversation) left as it is: decoding the whole body and encoding it
	// again walked the conversation twice a request (#3394).
	// A Lite request (one of OpenAI's own models, use_responses_lite) has
	// no tools member: its tools are in the input
	tools, found, ok := topMember(body, "tools")
	if !ok {
		return body
	}
	set := map[string][]byte{}
	if found {
		if t, ok := moreSpawnTools(tools, choices); ok {
			set["tools"] = t
		}
	}
	// A model that can search tools is handed spawn_agent only when it
	// searches for it: Codex defers its multi-agent V1 tools behind
	// tool_search (codex-rs core/src/tools/spec_plan.rs, Deferred when
	// supports_search_tool), and the tool comes back in the conversation,
	// in a tool_search_output, not in tools (Gearclickist on Discord, after
	// v0.1.1214). Codex sends that output as it got it on every turn, so
	// it is widened the same way on each. A Lite request hands the model
	// the tools it doesn't defer in an additional_tools input item, which
	// is where Codex 0.169 has spawn_agent in multi-agent V2; and with
	// features.model_catalog_in_context the models are in a
	// <model_catalog> developer message, not in spawn_agent at all
	// (@m0eaki on X, Codex 0.169).
	if input, found, _ := topMember(body, "input"); found && (bodyHas(input, "tool_search_output") || bodyHas(input, "additional_tools") || hasSpawnCatalog(input)) {
		if in, ok := moreSpawnInput(input, choices); ok {
			set["input"] = in
		}
	}
	if len(set) == 0 {
		return body
	}
	return withMembers(body, set)
}

// moreSpawnInput is input, a request's input list, with each
// tool_search_output's spawn_agent naming choices too; false when there is
// none to widen. Only those items are decoded and written again.
func moreSpawnInput(input []byte, choices []spawnChoice) ([]byte, bool) {
	raw := trimJSON(input)
	if len(raw) == 0 || raw[0] != '[' {
		return input, false
	}
	var out []byte
	last := 0
	i := skipJSONSpace(raw, 1)
	for raw[i] != ']' {
		at := i
		i = skipJSONValue(raw, i)
		if el := raw[at:i]; el[0] == '{' && (bodyHas(el, "tool_search_output") || bodyHas(el, "additional_tools") || hasSpawnCatalog(el)) {
			if it, ok := moreSpawnItem(el, choices); ok {
				out = append(append(out, raw[last:at]...), it...)
				last = i
			}
		}
		if i = skipJSONSpace(raw, i); raw[i] == ',' {
			i = skipJSONSpace(raw, i+1)
		}
	}
	if out == nil {
		return input, false
	}
	return append(out, raw[last:]...), true
}

// moreSpawnItem is el, an input item, when it is a tool_search_output or
// an additional_tools item whose spawn_agent can name more choices, or a
// developer message with Codex's <model_catalog>, written again naming
// them.
func moreSpawnItem(el []byte, choices []spawnChoice) ([]byte, bool) {
	var it map[string]json.RawMessage
	if json.Unmarshal(el, &it) != nil {
		return nil, false
	}
	var kind, role string
	json.Unmarshal(it["type"], &kind)
	json.Unmarshal(it["role"], &role)
	if kind == "message" && role == "developer" {
		return moreSpawnCatalog(it, choices)
	}
	if kind != "tool_search_output" && kind != "additional_tools" {
		return nil, false
	}
	tools, ok := moreSpawnTools(it["tools"], choices)
	if !ok {
		return nil, false
	}
	it["tools"] = tools
	return rawJSON(it), true
}

// withMembers is body, a valid JSON object, with the members set names
// given set's values, every other byte of it as it was (a name the body
// has twice is given the value at both).
func withMembers(body []byte, set map[string][]byte) []byte {
	raw := trimJSON(body)
	out := make([]byte, 0, len(raw)+len(set["tools"])+len(set["input"]))
	last := 0
	eachMemberAt(raw, func(k0, k1, v0, v1 int) bool {
		name, ok := unquoteJSON(raw[k0:k1])
		if v, has := set[name]; ok && has {
			out = append(append(out, raw[last:v0]...), v...)
			last = v1
		}
		return true
	})
	return append(out, raw[last:]...)
}

// moreSpawnTools is raw, a list of tools (a request's, or the ones a
// tool_search_output hands the model), with the spawn_agent among them,
// on its own or in Codex's collaboration namespace, naming choices too;
// false when there is none to widen.
func moreSpawnTools(raw json.RawMessage, choices []spawnChoice) (json.RawMessage, bool) {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return raw, false
	}
	changed := false
	for _, t := range tools {
		if moreSpawnModels(t, choices) {
			changed = true
			continue
		}
		// Codex's collaboration namespace holds it
		var kind string
		json.Unmarshal(t["type"], &kind)
		var inner []map[string]json.RawMessage
		if kind != "namespace" || json.Unmarshal(t["tools"], &inner) != nil {
			continue
		}
		in := false
		for _, it := range inner {
			in = moreSpawnModels(it, choices) || in
		}
		if in {
			t["tools"] = rawJSON(inner)
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	return rawJSON(tools), true
}

// rawJSON is v encoded as it is, its < > & left as they are.
func rawJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return nil
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// moreSpawnModels adds to t, when it is spawn_agent with a model
// argument and Codex's list of models in its description, the choices
// that list doesn't name.
func moreSpawnModels(t map[string]json.RawMessage, choices []spawnChoice) bool {
	var name, kind, desc string
	json.Unmarshal(t["name"], &name)
	json.Unmarshal(t["type"], &kind)
	json.Unmarshal(t["description"], &desc)
	if name != "spawn_agent" || kind != "function" || !strings.Contains(desc, "Available model overrides") || strings.Contains(desc, spawnMore) {
		return false
	}
	var params struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(t["parameters"], &params) != nil || params.Properties["model"] == nil {
		return false
	}
	lines := spawnLines(desc, choices)
	if len(lines) == 0 {
		return false
	}
	// after Codex's own list, its header's line and a "- `slug`: …" line
	// each, ahead of the text that follows it
	end := strings.Index(desc, "Available model overrides")
	for {
		nl := strings.IndexByte(desc[end:], '\n')
		if nl < 0 {
			end = len(desc)
			break
		}
		end += nl
		if !strings.HasPrefix(desc[end+1:], "- `") {
			break
		}
		end++
	}
	add := "\n" + spawnMore + "\n" + strings.Join(lines, "\n")
	t["description"] = rawJSON(desc[:end] + add + desc[end:])
	return true
}

// spawnLines is a "- `slug`: name" line for each of choices that text,
// Codex's list of models, doesn't name, spawnMost at most.
func spawnLines(text string, choices []spawnChoice) []string {
	var lines []string
	for _, c := range choices {
		if strings.Contains(text, "`"+c.slug+"`") {
			continue
		}
		line := "- `" + c.slug + "`"
		if c.name != "" && c.name != c.slug {
			line += ": " + c.name
		}
		lines = append(lines, line)
		if len(lines) == spawnMost {
			break
		}
	}
	return lines
}

// spawnCatalog opens the developer message part in which Codex, with
// features.model_catalog_in_context, lists the models spawn_agent may be
// set to (codex-rs core/src/context/world_state/model_catalog.rs), its
// first five as well, in place of spawn_agent's description.
const spawnCatalog = "<model_catalog>"

// hasSpawnCatalog says b, JSON, may hold a <model_catalog> listing: as Codex
// writes it, or with its angle brackets escaped, as Go's encoder writes a
// body magpie has written again (withModel).
func hasSpawnCatalog(b []byte) bool {
	return bodyHas(b, spawnCatalog) || bodyHas(b, `\u003cmodel_catalog\u003e`)
}

// moreSpawnCatalog is it, a developer message, with each of its
// <model_catalog> listings naming choices too, ahead of its closing tag;
// false when it has none to widen. One saying the listing no longer
// applies names no model and is left as it is.
func moreSpawnCatalog(it map[string]json.RawMessage, choices []spawnChoice) ([]byte, bool) {
	var parts []map[string]json.RawMessage
	if json.Unmarshal(it["content"], &parts) != nil {
		return nil, false
	}
	changed := false
	for _, p := range parts {
		var text string
		json.Unmarshal(p["text"], &text)
		end := strings.LastIndex(text, "</model_catalog>")
		if !strings.HasPrefix(text, spawnCatalog) || end < 0 || !strings.Contains(text, "\n- `") || strings.Contains(text, spawnMore) {
			continue
		}
		lines := spawnLines(text, choices)
		if len(lines) == 0 {
			continue
		}
		head := text[:end]
		if !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		p["text"] = rawJSON(head + spawnMore + "\n" + strings.Join(lines, "\n") + "\n" + text[end:])
		changed = true
	}
	if !changed {
		return nil, false
	}
	it["content"] = rawJSON(parts)
	return rawJSON(it), true
}

// spawnChoices is the models of the catalog Codex was handed, at CodexPath
// or at /v1, that a subagent of a lead on lead can work on, in the
// catalog's order.
func spawnChoices(r *http.Request, lead string, codexPath bool) []spawnChoice {
	sealed := !codexcat.V1() && (codexPath && !strings.Contains(lead, "/") || provider.CodexAccountModel(lead))
	var out []spawnChoice
	if codexPath {
		// Codex's own, as its backend lists them to it, less the ones
		// taken out of its list on the Agents page
		hidden := provider.CodexNativeHidden()
		// and those the codex provider's picks leave out, every one under
		// Usage only (#2530), as codexModels leaves them out of its list
		keep, narrowed := provider.CodexNativePicked()
		type own struct {
			spawnChoice
			at int
		}
		var owns []own
		cached := codexcat.CacheEntries()
		for slug, e := range cached {
			if v, _ := e["visibility"].(string); v != "list" || hidden[slug] || narrowed && !keep[slug] {
				continue
			}
			name, _ := e["display_name"].(string)
			at, _ := e["priority"].(float64)
			owns = append(owns, own{spawnChoice{slug, name}, int(at)})
		}
		slices.SortFunc(owns, func(a, b own) int { return cmp.Or(a.at-b.at, strings.Compare(a.slug, b.slug)) })
		for _, o := range owns {
			out = append(out, o.spawnChoice)
		}
		if sealed || codexOwnOnly() {
			return out
		}
		// and a ChatGPT account's own the sign-in's list lacks, by
		// magpie's id, as codexMerged lists them
		backend := make(map[string]bool, len(cached))
		for slug := range cached {
			backend[slug] = true
		}
		for _, m := range provider.CodexListedBeside(backend) {
			out = append(out, spawnChoice{m.ID, m.Name})
		}
		return out
	}
	shown, _ := provider.CatalogFor("codex")
	for _, m := range provider.CodexCatalog(keyListed(r, shown, provider.ModelOff("codex"))) {
		if sealed && !provider.CodexAccountModel(m.ID) {
			continue
		}
		out = append(out, spawnChoice{m.ID, m.Name})
	}
	return out
}
