package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// advisorTools fits Claude Code's advisor (/advisor, --advisor) to the
// upstream a Messages request goes to as it is (passthrough). Claude Code
// declares it as Anthropic's server tool, {"type": "advisor_20260301",
// "name": "advisor", "model": …}, and names the advisor's model as it
// names its own: on magpie, by magpie's id ("anthropic/claude-opus-5-5",
// or the tier an alias like opus is on), which no vendor knows, so the
// whole request was turned away (JackChou on Discord).
//
// The advisor runs at the vendor, on the same account: its model goes as
// that upstream's own name for it when it is p's, as the request's own
// model does (wires). An advisor magpie can't give that upstream (another
// provider's model, or not a Claude model) is left out, and so is every
// advisor when the request's model is not Claude's (a vendor's Messages
// API knows no such tool): the turn is answered without one, as a model
// that isn't on magpie would be. What Claude Code put in the conversation
// for an advisor left out goes with it (withoutAdvisor). allowed says
// whether the calling key may use one of p's models (keyAllows): the
// advisor's model is the client's to name, and the vendor runs it on the
// user's account, so a key held to some models is held here too.
func advisorTools(body []byte, p provider.Provider, upstream string, wires map[string]string, allowed func(model string) bool) []byte {
	if !bytes.Contains(body, []byte(`"advisor_`)) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	var top map[string]json.RawMessage
	var list []map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || json.Unmarshal([]byte(tools.Raw), &list) != nil {
		return body
	}
	claude := anthropicModel.MatchString(upstream)
	changed := false
	gone := map[string]bool{}
	kept := list[:0]
	for _, t := range list {
		var typ, model string
		json.Unmarshal(t["type"], &typ)
		if !strings.HasPrefix(typ, "advisor_") {
			kept = append(kept, t)
			continue
		}
		json.Unmarshal(t["model"], &model)
		name, ok := advisorModel(p, model, wires)
		if ok && allowed != nil && !allowed(advisorKeyModel(model)) {
			ok = false
		}
		if !claude || !ok {
			var n string
			json.Unmarshal(t["name"], &n)
			gone[n] = true
			changed = true
			continue
		}
		if name != model {
			t["model"], _ = json.Marshal(name)
			changed = true
		}
		kept = append(kept, t)
	}
	if !changed {
		return body
	}
	if len(kept) == 0 {
		delete(top, "tools")
		delete(top, "tool_choice")
	} else {
		top["tools"], _ = json.Marshal(kept)
	}
	if m, ok := withoutAdvisor(top["messages"], gone); ok {
		top["messages"] = m
	}
	out, err := json.Marshal(top)
	if err != nil {
		return body
	}
	return out
}

// advisorKeyModel is the model id a key's models are checked against for
// an advisor named model: magpie's model id when it names one, else the
// bare id as it was sent.
func advisorKeyModel(model string) string {
	model = strings.TrimSuffix(strings.TrimSpace(model), "[1m]")
	if _, m, ok := provider.Resolve(model); ok && !provider.IsGroupRef(model) {
		return m
	}
	return model
}

// advisorModel is the name p's upstream knows the advisor model by, and
// whether p can run it: one of p's models, by magpie's id, or a Claude
// model's own id that names no provider.
func advisorModel(p provider.Provider, model string, wires map[string]string) (string, bool) {
	model = strings.TrimSuffix(strings.TrimSpace(model), "[1m]")
	if model == "" {
		return "", false
	}
	if q, m, ok := provider.Resolve(model); ok && !provider.IsGroupRef(model) {
		if q.ID != p.ID {
			return "", false
		}
		name := provider.UpstreamNameIn(wires, p.ID, m)
		return name, anthropicModel.MatchString(name)
	}
	if strings.Contains(model, "/") {
		return "", false
	}
	return model, anthropicModel.MatchString(model)
}

// advisorNote opens the instructions Claude Code puts in the conversation
// when its advisor is on ("# Advisor Tool\n\nYou have access to an
// `advisor` tool … Call advisor BEFORE substantive work"), at the end of a
// {role: "system"} message's text (2.1.300), and advisorGone is what Claude
// Code itself says there once the advisor is turned off.
const (
	advisorNote = "# Advisor Tool\n"
	advisorGone = "The advisor tool is no longer available; disregard the earlier advisor instructions."
)

// advisorGoneIn is text with the advisor's instructions, up to the end of
// the reminder they are in or of the text, said to be void as Claude Code
// says it, and whether it held them.
func advisorGoneIn(text string) (string, bool) {
	i := strings.Index(text, advisorNote)
	if i < 0 {
		return text, false
	}
	end := len(text)
	if j := strings.Index(text[i:], "</system-reminder>"); j >= 0 {
		end = i + j
	}
	note := text[i:end]
	trail := note[len(strings.TrimRight(note, " \t\r\n")):]
	return text[:i] + advisorGone + trail + text[end:], true
}

// withoutAdvisor is a Messages request's messages without what Claude Code
// put there for an advisor tool that is left out, named in gone: the
// tool_addition (or tool_removal, tool_reference) naming it, and a message
// that leaves empty; and its instructions, said to be void (advisorGoneIn).
// Left there, the reference named a tool the request no longer has, and
// the instructions had the model call advisor() as one of the agent's own
// tools, which Claude Code answers "No such tool available: advisor"
// (JackChou on Discord).
func withoutAdvisor(raw json.RawMessage, gone map[string]bool) (json.RawMessage, bool) {
	if len(gone) == 0 || !bytes.Contains(raw, []byte("dvisor")) {
		return raw, false
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return raw, false
	}
	changed := false
	out := msgs[:0]
	for _, m := range msgs {
		var s string
		if json.Unmarshal(m["content"], &s) == nil {
			if t, ok := advisorGoneIn(s); ok {
				m["content"], _ = marshalPlain(t)
				changed = true
			}
			out = append(out, m)
			continue
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(m["content"], &blocks) != nil {
			out = append(out, m)
			continue
		}
		moved := false
		kept := blocks[:0]
		for _, b := range blocks {
			var typ string
			json.Unmarshal(b["type"], &typ)
			if deferredBlock(typ) && gone[advisorRefName(b)] {
				moved = true
				continue
			}
			var text string
			if typ == "text" && json.Unmarshal(b["text"], &text) == nil {
				if t, ok := advisorGoneIn(text); ok {
					b["text"], _ = marshalPlain(t)
					moved = true
				}
			}
			kept = append(kept, b)
		}
		if !moved {
			out = append(out, m)
			continue
		}
		changed = true
		if len(kept) == 0 {
			continue // it said only that the advisor came or went
		}
		m["content"], _ = marshalPlain(kept)
		out = append(out, m)
	}
	if !changed {
		return raw, false
	}
	b, err := marshalPlain(out)
	if err != nil {
		return raw, false
	}
	return b, true
}

// advisorRefName is the tool a deferred-tool block names: a tool_addition's
// or tool_removal's tool, or a tool_reference's own name.
func advisorRefName(b map[string]json.RawMessage) string {
	var n string
	if t, ok := b["tool"]; ok {
		var tool map[string]json.RawMessage
		if json.Unmarshal(t, &tool) == nil {
			json.Unmarshal(tool["name"], &n)
		}
		return n
	}
	if json.Unmarshal(b["name"], &n) == nil && n != "" {
		return n
	}
	json.Unmarshal(b["tool_name"], &n)
	return n
}
