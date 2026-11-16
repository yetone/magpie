package provider

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// CodexSuggestionsModel is the model the Codex app's prompt suggestions
// (its background ambient_suggestions turn and their safety check) are
// put on, "" for the model Codex sends them on. A provider renamed since
// it was set is named by its new id (RenamedRef).
func CodexSuggestionsModel() string {
	id := strings.TrimSpace(heldSettings().CodexSuggestions)
	if id == "" {
		return ""
	}
	return RenamedRef(id)
}

// SetCodexSuggestionsModel puts Codex's prompt suggestions on id, a model
// or routing group of magpie's (provider/model, group/<id>); "" leaves
// them on the model Codex sends them on.
func SetCodexSuggestionsModel(id string) error {
	id = strings.TrimSpace(id)
	if pid, model, ok := strings.Cut(id, "/"); id != "" && (!ok || pid == "" || model == "") {
		return fmt.Errorf("Codex's prompt suggestions can't be put on %q: pick one of magpie's models (provider/model)", id)
	}
	s := settings.Load()
	if s.CodexSuggestions == id {
		return nil
	}
	s.CodexSuggestions = id
	if err := settings.Save(s); err != nil {
		return err
	}
	Changed()
	return nil
}
