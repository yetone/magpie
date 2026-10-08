package agent

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Gemini CLI only speaks Google's own API, so "provider" here is how it
// authenticates: Google sign-in, a Gemini API key, or Vertex AI. The choice
// lives in settings.json (security.auth.selectedType). A Google provider
// added in magpie lends its key through ~/.gemini/.env, which the CLI loads.
//
// Any catalog model works too: the gateway serves the Gemini API, so magpie
// points GOOGLE_GEMINI_BASE_URL at it, uses the gateway token as the API
// key and names the catalog model in model.name.

func gemini(home string) *Agent { return geminiIn(here(home)) }

// geminiIn is Gemini CLI at a place: this machine's home, or a WSL
// distro's (see wsl.go), whose .env names the gateway as the distro
// reaches it, with the key it takes from there.
func geminiIn(at place) *Agent {
	dir := filepath.Join(at.home, ".gemini")
	path := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	auth := jsonGet(path, "security.auth.selectedType")
	model := jsonGet(path, "model.name")
	base := func() string { v, _ := edit.GetEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL"); return v }
	envKey := func() string { v, _ := edit.GetEnvFile(envPath, "GEMINI_API_KEY"); return v }
	// routed: the .env names the gateway, or names it at an address it
	// no longer has (a WSL distro's under NAT changes as WSL restarts) with
	// magpie's key: either way what is there is magpie's, never stashed as
	// the user's own
	routed := func() bool {
		b, k := base(), envKey()
		return b == at.gw() || b != "" && (ourKey(k) || k == at.gwKey())
	}
	magpieKey := func() string {
		for _, id := range []string{"google", "gemini"} {
			if p, err := provider.Find(id); err == nil && p.Key != "" {
				return p.Key
			}
		}
		return ""
	}
	// unroute puts back what routing through the gateway replaced
	unroute := func() error {
		if !routed() {
			return nil
		}
		if err := edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"); err != nil {
			return err
		}
		var env []edit.KV
		if u := unstash(at.key("gemini.base_url")); u != "" {
			env = append(env, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: u})
		}
		if k := unstash(at.key("gemini.api_key")); k != "" {
			env = append(env, edit.KV{Path: "GEMINI_API_KEY", Value: k})
		}
		if len(env) > 0 {
			if err := edit.SetEnvFile(envPath, env...); err != nil {
				return err
			}
		}
		if a := unstash(at.key("gemini.auth")); a != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: a}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if m := unstash(at.key("gemini.model")); m != "" {
			return edit.SetJSON(path, edit.KV{Path: "model.name", Value: m})
		}
		return delModelName(path)
	}
	setModel := func(v string) error {
		if v == "" {
			if routed() {
				forget(at.key("gemini.base_url"), at.key("gemini.api_key"), at.key("gemini.auth"), at.key("gemini.model"))
				if err := edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"); err != nil {
					return err
				}
				if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
					return err
				}
			}
			return delModelName(path)
		}
		if isMagpie(v) {
			if !routed() {
				stash(map[string]string{at.key("gemini.base_url"): base(), at.key("gemini.api_key"): envKey(), at.key("gemini.auth"): auth(), at.key("gemini.model"): model()})
			}
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: at.gw()}, edit.KV{Path: "GEMINI_API_KEY", Value: at.gwKey()}); err != nil {
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"}, edit.KV{Path: "model.name", Value: v})
		}
		if err := unroute(); err != nil {
			return err
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
	}

	current := func() string {
		if routed() {
			return magpieID
		}
		if base() != "" {
			return "custom"
		}
		switch auth() {
		case "":
			// none chosen: the CLI asks on its first run, as with no CLI at
			// all — the default, so the row folds away with the others
			return ""
		case "gemini-api-key":
			return "api-key"
		case "vertex-ai":
			return "vertex"
		}
		return "google"
	}
	use := func(id string) error {
		if id == magpieID {
			if routed() {
				return nil
			}
			return fmt.Errorf("pick a model via magpie instead; that routes Gemini CLI through the gateway")
		}
		if err := unroute(); err != nil {
			return err
		}
		switch id {
		case "":
			// back to the CLI's own first-run choice
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "google":
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "oauth-personal"}); err != nil {
				return err
			}
		case "vertex":
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "vertex-ai"}); err != nil {
				return err
			}
		case "api-key":
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"}); err != nil {
				return err
			}
			if k := magpieKey(); k != "" && envKey() != k {
				if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: k}); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unknown provider %q", id)
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	return &Agent{
		ID: "gemini", Name: "Gemini CLI", Icon: "geminicli-color", Aliases: []string{"gemini-cli"},
		UA:  []string{"geminicli", "gemini-cli"},
		Bin: "gemini", Dir: dir, Path: path,
		// its model's default forgets what it had before magpie, its
		// sign-in's default forgets the sign-in: this puts both back
		Unwire: unroute,
		// Gemini CLI is its binary. What it leaves in ~/.gemini stays when it
		// is uninstalled (#230), and Antigravity keeps its folders there too
		// (antigravity, antigravity-cli, config) and reads GEMINI.md (#330),
		// so nothing there says Gemini CLI is here: a row for it would set
		// up a CLI that can't run. The desktop app's PATH has the user's
		// shell's (proc.UserPath).
		detect: func() bool {
			if Taken(dir) {
				return false
			}
			_, err := exec.LookPath("gemini")
			return err == nil
		},
		Check: func() string {
			if !isMagpie(model()) {
				return ""
			}
			if d := wiringOff("Gemini CLI", envPath, func(k string) (string, bool) { return edit.GetEnvFile(envPath, k) },
				"GOOGLE_GEMINI_BASE_URL", at.gw(), "GEMINI_API_KEY", at.gwKey()); d != "" {
				return d
			}
			if a := auth(); a != "gemini-api-key" {
				return "Gemini CLI signs in with " + orDefault(a) + " rather than magpie's key, so it asks Google directly"
			}
			return ""
		},
		Notice: func() string {
			if Running(`(^|/)gemini( |$)`) {
				return "Gemini CLI reads its settings at start-up — restart open gemini sessions to see this."
			}
			return ""
		},
		Fields: []Field{
			{
				Key: "provider", Label: "auth",
				Get: current,
				Set: use,
				Options: func(map[string]string) []Option {
					key := Option{Value: "api-key", Label: "API key", Icon: "gemini-color"}
					switch {
					case magpieKey() != "":
						key.Note = "the Google key from magpie's providers"
					case envKey() != "":
						key.Note = "GEMINI_API_KEY from ~/.gemini/.env"
					default:
						key.Note = "needs GEMINI_API_KEY — add Google Gemini in magpie's providers"
					}
					out := []Option{
						{Value: "google", Label: "Google", Icon: "gemini-color", Note: "Google account · OAuth sign-in"},
						key,
						{Value: "vertex", Label: "Vertex AI", Icon: "googlecloud-color", Note: "Vertex AI · $GOOGLE_CLOUD_PROJECT"},
					}
					switch current() {
					case "custom":
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					case magpieID:
						out = append(out, Option{Value: magpieID, Label: "magpie", Note: "the local gateway · every provider's models"})
					}
					return out
				},
			},
			{
				Key: "model", Label: "model",
				Get: model,
				Set: setModel,
				Options: func(map[string]string) []Option {
					var ms []catalog.Model
					for _, m := range catalog.Provider("google") {
						if strings.HasPrefix(m.ID, "gemini-") {
							ms = append(ms, m)
						}
					}
					own := group("Gemini CLI", options(ms, ""))
					return append(own, viaMagpie("gemini", "")...)
				},
			},
		},
	}
}

// delModelName takes model.name out of Gemini CLI's settings.json, and the
// model it was in once nothing else is there: the "model": {} it would
// leave was never the user's.
func delModelName(path string) error {
	if err := edit.DelJSON(path, "model.name"); err != nil {
		return err
	}
	if v, _ := edit.GetJSON(path, "model"); strings.TrimSpace(strings.Trim(strings.TrimSpace(v), "{}")) == "" && strings.HasPrefix(strings.TrimSpace(v), "{") {
		return edit.DelJSON(path, "model")
	}
	return nil
}
