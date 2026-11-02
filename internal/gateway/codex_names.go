package gateway

import (
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A ChatGPT account's backend refuses a tool name outside
// ^[a-zA-Z0-9_.-]+\z, a call's in the history as much as a declared
// tool's, and the account's send gives it another (provider's
// CodexToolNames, wing3556 on Discord). A call the model makes to a
// declared tool so renamed comes back to the agent by the name it
// declared: from a translated reply's events (decoderFor), and from a
// Responses reply relayed as it came (nsTidy's back).

// codexNamesBack maps each declared tool's name the ChatGPT backend is
// given in its place back to the agent's; nil when none is renamed.
func codexNamesBack(tools []Tool) map[string]string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return provider.CodexToolNamesBack(names)
}

// codexRenamedIn is codexNamesBack for body, a Responses request: the
// functions and custom tools it declares in tools or in an
// additional_tools item.
func codexRenamedIn(body []byte) map[string]string {
	var names []string
	add := func(ts gjson.Result) {
		ts.ForEach(func(_, t gjson.Result) bool {
			if k := t.Get("type").String(); k == "function" || k == "custom" {
				if n := t.Get("name"); n.Type == gjson.String {
					names = append(names, n.String())
				}
			}
			return true
		})
	}
	top := gjson.GetManyBytes(body, "tools", "input")
	add(top[0])
	top[1].ForEach(func(_, it gjson.Result) bool {
		if it.Get("type").String() == additionalToolsItem {
			add(it.Get("tools"))
		}
		return true
	})
	return provider.CodexToolNamesBack(names)
}
