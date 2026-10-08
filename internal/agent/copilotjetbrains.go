package agent

// GitHub Copilot in JetBrains IDEs (IntelliJ IDEA, PyCharm, GoLand, …, and
// Android Studio; the github-copilot-intellij plugin) takes models of other
// vendors through Bring Your Own Key: Copilot Chat's model picker, Manage
// Models…, Add Custom Endpoint Provider (KamToHung on Discord). The plugin
// keeps none of it itself: it asks the Copilot language server it bundles
// (copilot-language-server, the npm @github/copilot-language-server), which
// keeps every provider in one JSON object, byok.json in
// $XDG_CONFIG_HOME/github-copilot when that is set (and absolute),
// %USERPROFILE%\AppData\Local\github-copilot on Windows and
// ~/.config/github-copilot elsewhere, read again at every use. A custom
// endpoint provider <name> is three keys there, as the server's own
// copilot/byok/saveCustomProviderConfig and saveModel write them:
//
//	"<name>-provider-config": {"groupName": <name>, "apiType": "chatCompletions"}
//	"<name>-api-key": "<key, in plain text>"
//	"<name>-models-config": {"<model id>": {"deploymentUrl": …, "isRegistered": true,
//	  "isCustomModel": true, "modelCapabilities": {"name": …, "maxInputTokens": …,
//	  "maxOutputTokens": …, "toolCalling": true, "vision": …}}}
//
// So magpie adds the provider "magpie" there: its catalog, each model at the
// gateway's /v1/chat/completions, sent with the key as a Bearer token (the
// key is the file's, not the IDE's password safe). Tool calling is on, as
// Copilot's agent mode lists only models that call tools; a model's
// maxInputTokens is the window the server fills, its output kept within it.
// The user's other providers and keys stay as they are.
//
// The IDE lists the server's providers when Copilot signs in, so the models
// show after the IDE restarts. The model is picked in Copilot Chat, kept in
// the IDE's project files, so what magpie sets is whether its models are in
// that picker. BYOK needs a GitHub sign-in with Copilot: the server offers
// it to a personal plan (Free, Pro, Pro+) and to Business or Enterprise
// where the organisation's policy allows it. Its requests say
// "GithubCopilot/<server version>" as every client of that server does, so
// they are told apart by their key. Xcode's and Eclipse's Copilot run the
// same server and read the same file: they list magpie's models too.
//
// Checked with the language server 1.545.1 bundled in plugin 1.18.0, under
// a sandbox HOME signed in to a real Copilot account: the file magpie
// writes is listed by copilot/byok/listCustomProviderConfigs and
// listModels, and a Chat turn on one of its models reached the gateway with
// magpie's key. Not with a JetBrains IDE's own window.

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// copilotJBID is the agent's id, and what its token names.
const copilotJBID = "copilot-jetbrains"

// The keys of magpie's provider in byok.json.
const (
	copilotJBConfig = magpieID + "-provider-config"
	copilotJBKey    = magpieID + "-api-key"
	copilotJBModels = magpieID + "-models-config"
)

// copilotJBPlugin is the folder the plugin is installed in, under an IDE's
// plugins folder.
const copilotJBPlugin = "github-copilot-intellij"

func copilotJetBrains(home string) *Agent {
	return copilotJetBrainsAt(copilotBYOKPath(home), func() bool { return copilotJBInstalled(home) })
}

// copilotBYOKPath is byok.json where the language server keeps it
// (getXdgConfigPath).
func copilotBYOKPath(home string) string {
	if x := appdir.Getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "github-copilot", "byok.json")
	}
	if runtime.GOOS == "windows" {
		// the server's os.homedir(), USERPROFILE, which home is too
		return filepath.Join(home, "AppData", "Local", "github-copilot", "byok.json")
	}
	return filepath.Join(home, ".config", "github-copilot", "byok.json")
}

// copilotJBInstalled reports whether some JetBrains IDE, or Android Studio,
// has the Copilot plugin installed: in the plugins folder of an IDE's
// config folder on macOS and Windows, in the IDE's data folder itself on
// Linux.
func copilotJBInstalled(home string) bool {
	var base, sub string
	switch runtime.GOOS {
	case "darwin":
		base, sub = filepath.Join(home, "Library", "Application Support"), "plugins"
	case "windows":
		if base = appdir.Getenv("APPDATA"); base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		sub = "plugins"
	default:
		if base = appdir.Getenv("XDG_DATA_HOME"); base == "" {
			base = filepath.Join(home, ".local", "share")
		}
	}
	for _, vendor := range []string{"JetBrains", "Google"} {
		ms, _ := filepath.Glob(filepath.Join(base, vendor, "*", sub, copilotJBPlugin))
		for _, m := range ms {
			if isDir(m) {
				return true
			}
		}
	}
	return false
}

func copilotJetBrainsAt(path string, installed func() bool) *Agent {
	wired := func() bool { _, ok := edit.GetJSON(path, copilotJBConfig); return ok }
	return atomic(&Agent{
		ID: copilotJBID, Name: "Copilot (JetBrains)", Icon: "githubcopilot",
		Aliases: []string{"jetbrains", "jetbrains-copilot", "copilot-idea", "copilot-intellij", "intellij", "idea"},
		Dir:     filepath.Dir(path), Path: path,
		// the folder is every Copilot's (VS Code's sign-in too): the
		// plugin installed in an IDE, or magpie's provider written, says
		// it is here
		detect: func() bool { return installed() || wired() },
		Notice: func() string {
			if !wired() {
				return ""
			}
			return "magpie's models are in Copilot Chat's model picker in your JetBrains IDE, under magpie, once the IDE restarts. They need Copilot signed in to GitHub on a plan with Bring Your Own Key: Copilot Free, Pro and Pro+ have it, Business and Enterprise when the organisation's policy allows it."
		},
		Sync: func() error {
			if !wired() {
				return nil
			}
			return copilotJBWrite(path)
		},
		Check: func() string {
			if !wired() {
				return ""
			}
			// every model at the gateway: the first that isn't, if any (none
			// listed while the catalog is empty)
			raw, _ := edit.GetJSON(path, copilotJBModels)
			url := vscodeURL()
			for _, m := range gjson.Parse(raw).Map() {
				if url = m.Get("deploymentUrl").String(); url != vscodeURL() {
					break
				}
			}
			return wiringOff("Copilot (JetBrains)", path, func(k string) (string, bool) {
				if k == "deploymentUrl" {
					return url, url != ""
				}
				return edit.GetJSON(path, k)
			}, "deploymentUrl", vscodeURL(), copilotJBKey, gateway.TokenFor(copilotJBID))
		},
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if wired() {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				if v == "" {
					return edit.DelJSON(path, copilotJBConfig, copilotJBKey, copilotJBModels)
				}
				return copilotJBWrite(path)
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in Copilot Chat's model picker"}}
			},
		}},
	}, path)
}

// copilotJBWrite writes magpie's provider into byok.json, made private as
// the language server makes it when there is none. A model of magpie's the
// user hid in the IDE's Manage Models stays hidden.
func copilotJBWrite(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			return err
		}
	}
	hidden := map[string]bool{}
	if raw, ok := edit.GetJSON(path, copilotJBModels); ok {
		for id, m := range gjson.Parse(raw).Map() {
			if r := m.Get("isRegistered"); r.Exists() && !r.Bool() {
				hidden[id] = true
			}
		}
	}
	models := copilotJBModelsJSON(hidden)
	kvs := []edit.KV{
		{Path: copilotJBConfig, Value: map[string]string{"groupName": magpieID, "apiType": "chatCompletions"}},
		{Path: copilotJBKey, Value: gateway.TokenFor(copilotJBID)},
		{Path: copilotJBModels, Value: models},
	}
	// written only where it differs, so a sync with nothing new leaves the
	// file as the server last wrote it
	var set []edit.KV
	for _, kv := range kvs {
		if cur, ok := edit.GetJSON(path, kv.Path); !ok || !sameJSON(cur, kv.Value) {
			set = append(set, kv)
		}
	}
	if len(set) == 0 {
		return nil
	}
	return edit.SetJSON(path, set...)
}

// copilotJBModelsJSON is magpie's models-config: the catalog as Copilot
// (JetBrains) is shown it, keyed by model id.
func copilotJBModelsJSON(hidden map[string]bool) map[string]any {
	out := map[string]any{}
	for _, m := range magpieModels(copilotJBID) {
		context := m.Context
		if context == 0 {
			context = 128000 // the server's own default for a custom endpoint
		}
		// the output is reserved out of the window the prompt fills
		output := maxTokens(m)
		if output <= 0 {
			output = 16000
		}
		output = min(output, context/4)
		out[m.ID] = map[string]any{
			"deploymentUrl": vscodeURL(),
			"isRegistered":  !hidden[m.ID],
			"isCustomModel": true,
			"modelCapabilities": map[string]any{
				"name": m.Name, "maxInputTokens": context, "maxOutputTokens": output,
				"toolCalling": true, "vision": m.Images,
			},
		}
	}
	return out
}
