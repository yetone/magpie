package provider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
)

// Antigravity's CLI, agy, on a Gemini API key of the user's own: with
// "modelProvider": "gemini" in ~/.gemini/antigravity-cli/settings.json it
// skips the Google sign-in and asks the Gemini API with GEMINI_API_KEY
// from its environment (GOOGLE_GEMINI_BASE_URL for another endpoint), and
// `agy models` lists the Gemini API's models, Gemini 4 Carbon and Hydrogen
// among them. magpie's agy-cli subscription runs agy on its Google
// sign-in, whose models are the account's in Antigravity, so that key is
// served nowhere in magpie (shenggxhz on X: agy listed Gemini 4, magpie's
// refresh didn't). The key is a Gemini API key, and the Providers page
// offers it as the Google Gemini provider, which serves those models.
//
// The variables are agy's environment: magpie's own, else the login
// shell's (proc.GeminiKeyVars), as a terminal's agy has them. They are
// only read.

// agyLoginEnv is what a terminal opened now has for a variable (a var for
// tests).
var agyLoginEnv = proc.LoginEnv

func agySettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// agyEnv is a variable as agy started in a terminal has it.
func agyEnv(k string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return strings.TrimSpace(agyLoginEnv(k))
}

func readAgySettings(path string) ([]AppImport, error) {
	raw, err := edit.Read(path)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var s map[string]any
	if err := json.Unmarshal(jsonc.ToJSON(edit.TrimBOM(raw)), &s); err != nil {
		return nil, errors.New("agy's settings.json: " + err.Error())
	}
	// agy on its Google sign-in: the agy-cli subscription's
	if mp, _ := s["modelProvider"].(string); !strings.EqualFold(strings.TrimSpace(mp), "gemini") {
		return nil, nil
	}
	it := AppImport{Ref: "gemini-api-key", From: "settings.json"}
	var eps endpoints
	if base := agyEnv("GOOGLE_GEMINI_BASE_URL"); base != "" {
		eps.gemini = geminiCLIBase(base)
	} else {
		pr := Preset("google")
		eps.chat, eps.responses, eps.anthropic = pr.Chat, pr.Responses, pr.Anthropic
	}
	key := agyEnv("GEMINI_API_KEY")
	if key == "" {
		// GEMINI_API_KEY isn't set: agy itself refuses to start
		it.Provider, it.Skip = Provider{Name: "Google Gemini"}, "it has no API key"
		return []AppImport{it}, nil
	}
	p, skip := imported("Google Gemini", key, eps, nil)
	if skip != "" {
		it.Provider, it.Skip = Provider{Name: "Google Gemini"}, skip
		return []AppImport{it}, nil
	}
	it.Provider = p
	return []AppImport{it}, nil
}
