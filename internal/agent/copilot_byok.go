package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// Copilot CLI takes its own model providers from providers.json (BYOK, `copilot
// help providers`; docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-byok-models):
//
//	{"providers": [{"name", "type", "baseUrl", "apiKey", "wireApi"}],
//	 "models":    [{"id", "provider", "name", "maxPromptTokens", "maxOutputTokens",
//	                "capabilities": {"supports": {"vision", "reasoning_effort"}}, ...}]}
//
// and a model is picked as <provider>/<id> (settings.json "model"). It needs
// no GitHub sign-in. magpie owns the entry named magpie in providers and the
// entries of models that name it; every other entry, and any other key the
// file has, is the user's and stays, as does a key the user added to one of
// magpie's models that magpie doesn't write (copilotModelKeys).

// copilotFile is providers.json at path, read as it is: its top-level keys
// as they are, "providers" and "models" as lists. A file that does not parse
// is an error, never an empty file to write over.
func copilotFile(path string) (map[string]json.RawMessage, error) {
	b, err := edit.Read(path)
	if err != nil {
		return nil, err
	}
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(b)) == 0 {
		return top, nil
	}
	if err := json.Unmarshal(b, &top); err != nil || top == nil {
		return nil, fmt.Errorf("%s isn't a JSON object, so magpie leaves it as it is", path)
	}
	return top, nil
}

func copilotList(top map[string]json.RawMessage, key string) ([]json.RawMessage, error) {
	raw, ok := top[key]
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var l []json.RawMessage
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("providers.json's %s isn't a list, so magpie leaves it as it is", key)
	}
	return l, nil
}

// ofMagpie reports whether entry (a provider, or a model by its provider
// key) is magpie's.
func copilotOwn(entry json.RawMessage, key string) bool {
	var e map[string]any
	if json.Unmarshal(entry, &e) != nil {
		return false
	}
	n, _ := e[key].(string)
	return n == magpieID
}

// copilotProvider is magpie's provider entry in the file at path, empty
// when there is none.
func copilotProvider(path string) map[string]any {
	top, err := copilotFile(path)
	if err != nil {
		return map[string]any{}
	}
	l, _ := copilotList(top, "providers")
	for _, e := range l {
		if copilotOwn(e, "name") {
			var m map[string]any
			if json.Unmarshal(e, &m) == nil {
				return m
			}
		}
	}
	return map[string]any{}
}

// copilotBlock is magpie's provider and its models for the gateway at url
// (ending in /v1), as the catalog lists them for Copilot. The wire is
// OpenAI's chat completions, which the gateway translates for every model.
func copilotBlock(url, key string) (map[string]any, []map[string]any) {
	prov := map[string]any{"name": magpieID, "type": "openai", "baseUrl": url, "apiKey": key}
	ms := []map[string]any{}
	for _, m := range sharedModels("copilot") {
		e := map[string]any{"id": m.ID, "provider": magpieID}
		if m.Name != "" {
			e["name"] = m.Name
		}
		if out := maxTokens(m); out > 0 {
			e["maxOutputTokens"] = out
		}
		if m.Context > 0 {
			e["maxContextWindowTokens"] = m.Context
			prompt := m.Context
			if out := maxTokens(m); out > 0 && out < prompt {
				prompt -= out
			}
			e["maxPromptTokens"] = prompt
		}
		sup := map[string]any{"vision": m.Images}
		if l := copilotEfforts(m.Efforts); len(l) > 0 {
			sup["reasoning_effort"] = l
		}
		e["capabilities"] = map[string]any{"supports": sup}
		ms = append(ms, e)
	}
	return prov, ms
}

// copilotLevels are the reasoning levels Copilot's capabilities.supports.
// reasoning_effort takes, weakest first (the CLI's --reasoning-effort
// values, 1.0.116; docs.github.com's cli-config-dir-reference).
var copilotLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// copilotEfforts are a model's reasoning levels in Copilot's words and
// order. /model offers a model's effort (←/→) only from this list: with
// none, or with the boolean reasoningEffort, it shows "—" and keeps none
// (TonyRL on Discord). The gateway fits a level Copilot sends to the
// model's own (fitEffort), so a vendor's word for one (Trae's light) is
// listed as the level it is.
func copilotEfforts(efforts []string) []string {
	var out []string
	for _, l := range copilotLevels {
		if slices.ContainsFunc(efforts, func(e string) bool { return catalog.EffortLevel(e) == l }) {
			out = append(out, l)
		}
	}
	return out
}

// copilotModelKeys are the keys of a model entry magpie writes, each
// path into capabilities included: magpie's again on every write, so one
// the catalog no longer gives (a model's efforts gone) goes with it. Every
// other key the user put in one of magpie's models stays (wireModel,
// systemMessage, capabilities.supports.toolCalls, as /model's Edit
// writes them).
var copilotModelKeys = [][]string{
	{"id"}, {"provider"}, {"name"}, {"maxOutputTokens"}, {"maxContextWindowTokens"}, {"maxPromptTokens"},
	{"capabilities", "supports", "vision"}, {"capabilities", "supports", "reasoning_effort"},
}

// copilotModel is magpie's model entry want with what the user added to
// it in have, the file's entry of the same id.
func copilotModel(have json.RawMessage, want map[string]any) json.RawMessage {
	b, _ := json.Marshal(want)
	var h map[string]any
	d := json.NewDecoder(bytes.NewReader(have))
	d.UseNumber() // a number of the user's goes back as they wrote it
	if len(have) == 0 || d.Decode(&h) != nil || h == nil {
		return b
	}
	for _, path := range copilotModelKeys {
		at := h
		for _, k := range path[:len(path)-1] {
			at, _ = at[k].(map[string]any)
		}
		if at != nil {
			delete(at, path[len(path)-1])
		}
	}
	hb, _ := json.Marshal(h)
	var hm, wm map[string]json.RawMessage
	if json.Unmarshal(hb, &hm) != nil || json.Unmarshal(b, &wm) != nil {
		return b
	}
	out, err := json.Marshal(mergeTheirs(hm, wm, nil))
	if err != nil {
		return b
	}
	return out
}

// copilotWrite puts magpie's provider and models into providers.json,
// everything else in the file as it was. What the file already holds in
// magpie's own provider entry under a key magpie doesn't write stays.
func copilotWrite(path, url, key string) error {
	top, err := copilotFile(path)
	if err != nil {
		return err
	}
	provs, err := copilotList(top, "providers")
	if err != nil {
		return err
	}
	models, err := copilotList(top, "models")
	if err != nil {
		return err
	}
	prov, ms := copilotBlock(url, key)
	want, _ := json.Marshal(prov)
	var wm map[string]json.RawMessage
	json.Unmarshal(want, &wm)
	placed := false
	var outP []json.RawMessage
	for _, e := range provs {
		if !copilotOwn(e, "name") {
			outP = append(outP, e)
			continue
		}
		if placed {
			continue
		}
		placed = true
		var have map[string]json.RawMessage
		merged := wm
		if json.Unmarshal(e, &have) == nil && have != nil {
			merged = mergeTheirs(have, wm, nil)
		}
		b, _ := json.Marshal(merged)
		outP = append(outP, b)
	}
	if !placed {
		outP = append(outP, want)
	}
	var outM []json.RawMessage
	first := -1
	had := map[string]json.RawMessage{}
	for _, e := range models {
		if copilotOwn(e, "provider") {
			if first < 0 {
				first = len(outM)
			}
			var id struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(e, &id) == nil && id.ID != "" {
				if _, ok := had[id.ID]; !ok {
					had[id.ID] = e
				}
			}
			continue
		}
		outM = append(outM, e)
	}
	if first < 0 {
		first = len(outM)
	}
	var mine []json.RawMessage
	for _, m := range ms {
		mine = append(mine, copilotModel(had[m["id"].(string)], m))
	}
	outM = append(outM[:first], append(mine, outM[first:]...)...)
	top["providers"], _ = json.Marshal(outP)
	top["models"], _ = json.Marshal(outM)
	return copilotSave(path, top)
}

// copilotUnwrite takes magpie's provider and models out of providers.json,
// leaving the rest; a file left with nothing in it that magpie made is not
// deleted, as it may be the user's.
func copilotUnwrite(path string) error {
	top, err := copilotFile(path)
	if err != nil {
		return err
	}
	if len(top) == 0 {
		return nil
	}
	provs, err := copilotList(top, "providers")
	if err != nil {
		return err
	}
	models, err := copilotList(top, "models")
	if err != nil {
		return err
	}
	keep := func(l []json.RawMessage, key string) []json.RawMessage {
		out := []json.RawMessage{}
		for _, e := range l {
			if !copilotOwn(e, key) {
				out = append(out, e)
			}
		}
		return out
	}
	np, nm := keep(provs, "name"), keep(models, "provider")
	if len(np) == len(provs) && len(nm) == len(models) {
		return nil
	}
	top["providers"], _ = json.Marshal(np)
	top["models"], _ = json.Marshal(nm)
	return copilotSave(path, top)
}

// copilotSync brings magpie's entries up to the catalog as it is now, when
// the file has them; one that already says the same is not written.
func copilotSync(path, url, key string) error {
	top, err := copilotFile(path)
	if err != nil {
		return nil
	}
	provs, _ := copilotList(top, "providers")
	has := false
	for _, e := range provs {
		has = has || copilotOwn(e, "name")
	}
	if !has {
		return nil
	}
	return copilotWrite(path, url, key)
}

func copilotSave(path string, top map[string]json.RawMessage) error {
	b, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(path, append(b, '\n'))
}
