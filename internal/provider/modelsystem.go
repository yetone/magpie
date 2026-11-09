package provider

// A model's own system prompt (liucl on X): text the user gives one model
// of a provider, kept in settings' ModelSystemPrompts by "<provider
// id>/<model id>", which the gateway adds after the agent's own system
// prompt on every request for that model, whichever agent sends it. It
// only appends: the agent's prompt, and an identity it starts with, stay
// first and as they were. A signed-in account's model and a plugin's take
// it too, as the gateway adds it before a request is translated for them.

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// ModelSystemPrompt is the system prompt the user gave provider pid's
// model, "" for none. It reads the held settings, as it is asked on every
// try of a request.
func ModelSystemPrompt(pid, model string) string {
	if pid == "" || model == "" {
		return ""
	}
	return heldSettings().ModelSystemPrompts[pid+"/"+model]
}

// maxModelSystemPrompt bounds one model's system prompt.
const maxModelSystemPrompt = 64 << 10

// SetModelSystemPrompt gives a provider's model, spelt "provider/model",
// the system prompt text, in place of the one it had; "" takes it away.
func SetModelSystemPrompt(ref, text string) error {
	_, err := setModelSystemPrompt(ref, text)
	return err
}

func setModelSystemPrompt(ref, text string) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	text = strings.TrimSpace(text)
	if text != "" {
		if !p.serves(model) {
			return false, fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
		}
		if len(text) > maxModelSystemPrompt {
			return false, fmt.Errorf("a model's system prompt is at most %d KB", maxModelSystemPrompt>>10)
		}
		if strings.ContainsRune(text, 0) {
			return false, fmt.Errorf("a system prompt has no NUL")
		}
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if s.ModelSystemPrompts[key] == text {
		return false, nil
	}
	if text == "" {
		delete(s.ModelSystemPrompts, key)
	} else {
		if s.ModelSystemPrompts == nil {
			s.ModelSystemPrompts = map[string]string{}
		}
		s.ModelSystemPrompts[key] = text
	}
	if err := settings.Save(s); err != nil {
		return false, err
	}
	return true, nil
}
