package provider

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// Codex's lead picks the model of each subagent it spawns by itself, in
// spawn_agent's model argument (willz on Discord: gpt-6-astra), and its
// own config can't overrule that pick: [agents] default_subagent_model is
// only the default for a spawn that names none. settings.CodexSubagentModel
// is the model magpie puts Codex's subagents on, whatever the lead asked
// for: the gateway rewrites a subagent's request (x-openai-subagent
// collab_spawn) to it. Any model magpie serves may be set (#1633: a lead
// on a chat provider's model, its subagents wanted on that provider too).
// A subagent's task the ChatGPT backend sealed, its lead having run there,
// opens only on a ChatGPT account again: the gateway leaves such a
// subagent on the model asked for when the one set can't open it, and the
// route trace says so. "" leaves every subagent on the model its lead
// asked for. CodexSubagentLead puts every subagent on the model its lead's
// own turns go on, even when the lead names another (cleverpigo on X: a
// lead on WorkBuddy's DeepSeek named gpt-5.6-sol, the first of Codex's own
// models spawn_agent lists, and its subagent went to ChatGPT).

// CodexSubagentLead is the value of settings.CodexSubagentModel that puts
// Codex's subagents on their lead's model, whatever the lead names.
const CodexSubagentLead = settings.CodexSubagentLead

// CodexSubagentModel is the model Codex's subagents are put on, "" for
// the one the lead asks for, CodexSubagentLead for the lead's own.
func CodexSubagentModel() string {
	return strings.TrimSpace(heldSettings().CodexSubagentModel)
}

// CodexSubagentModels are the models and routing groups Codex's subagents
// may be put on, the ones Codex's own lists show (a model taken out of
// them, or not picked, isn't offered here either): the ChatGPT accounts'
// first, as they open a task sealed for them too, then the rest in the
// catalog's order. The list is kept while the catalog is held, only for
// reading: Codex's Agents row asked it each poll, a copy of Codex's whole
// catalog and the list made from it, 17 MB of forty polls with real
// accounts.
func CodexSubagentModels() []Entry {
	return keptOf("codex-subagent-models", codexSubagentModels)
}

func codexSubagentModels() []Entry {
	var chatgpt, rest []Entry
	for _, e := range catalogKept("codex").shown {
		if chatGPTEntry(e) {
			chatgpt = append(chatgpt, e)
		} else {
			rest = append(rest, e)
		}
	}
	return slices.Clip(append(chatgpt, rest...))
}

func chatGPTEntry(e Entry) bool {
	return e.Group == "" && e.Provider.Account != nil && e.Provider.Account.Agent == "codex"
}

// CodexSubagentUnserved says why Codex's subagents can't be put on id now,
// "" when magpie serves it.
func CodexSubagentUnserved(id string) string {
	served := Served()
	for _, e := range served {
		if e.ID == id {
			return ""
		}
	}
	if !CodexAccountModel(id) {
		return "magpie doesn't serve it now"
	}
	for _, e := range served {
		if chatGPTEntry(e) {
			return "no ChatGPT account on in magpie serves it"
		}
	}
	return "no ChatGPT account is on in magpie"
}

// SetCodexSubagentModel puts Codex's subagents on id, a model or routing
// group of magpie's (provider/model, group/<id>); "" leaves them on the
// model their lead asks for, CodexSubagentLead puts them on the lead's. One magpie doesn't serve now is kept all the
// same — a backup or profile put back on a computer whose providers
// differ, or an account signed in again later — and the gateway leaves
// subagents on the model asked for until it is served, saying why in the
// route trace. One of Codex's own bare names is refused: it names no
// model of magpie's to put the request on.
func SetCodexSubagentModel(id string) error {
	id = strings.TrimSpace(id)
	if pid, model, ok := strings.Cut(id, "/"); id != "" && id != CodexSubagentLead && (!ok || pid == "" || model == "") {
		return fmt.Errorf("Codex's subagents can't be put on %q: pick one of magpie's models (provider/model)", id)
	}
	s := settings.Load()
	if s.CodexSubagentModel == id {
		return nil
	}
	s.CodexSubagentModel = id
	if err := settings.Save(s); err != nil {
		return err
	}
	Changed() // a held settings read sees it
	return nil
}

// CodexAccountModel says id names a model of a ChatGPT account's, served
// now or not: codex/<model>, or one of a provider that is a ChatGPT
// account.
func CodexAccountModel(id string) bool {
	pid, model, ok := strings.Cut(id, "/")
	if !ok || model == "" {
		return false
	}
	if pid == "codex" {
		return true
	}
	p, err := Find(pid)
	return err == nil && p.Account != nil && p.Account.Agent == "codex"
}
