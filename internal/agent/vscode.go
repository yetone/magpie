package agent

// VS Code's own chat (GitHub Copilot Chat, built into VS Code) takes models
// of other vendors through its Custom Endpoint provider, in VS Code Stable
// since 1.122, with no GitHub sign-in or Copilot plan needed: groups of
// models in chatLanguageModels.json beside its settings.json (the default
// profile's, in its User folder), a JSONC array VS Code watches and reads
// again when it changes. magpie adds a group of its own there,
// {"vendor": "customendpoint", "name": "magpie"}, listing the catalog: each
// model at the gateway's /v1/chat/completions with tool calling on (agent
// mode lists only models that call tools). A group's apiKey can't be
// written in the file: VS Code reads it from its secret storage alone, so
// each model sends magpie's token in requestHeaders' Authorization, which a
// Custom Endpoint model may set and which then stands in for the key; the
// user is asked for none. The model a new chat starts on is the
// chat.defaultModel setting (a model id: the catalog id here); picked in
// Chat's model picker, a model is kept in VS Code's storage, not here.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// vscodeDefault is chat.defaultModel, a key with a dot in it, escaped for
// edit's key paths.
const vscodeDefault = `chat\.defaultModel`

// vscodeUA is how VS Code's chat names itself to a Custom Endpoint: its
// fetcher's GitHubCopilotChat/<version>, which a model's requestHeaders
// can't change.
const vscodeUA = "githubcopilotchat"

// vscodeGroup is how magpie's group is found among the user's.
var vscodeGroup = map[string]string{"vendor": "customendpoint", "name": magpieID}

// VS Code keeps its User folder under Application Support on macOS, Roaming
// AppData on Windows and XDG on Linux.
func vscode(home, cfg string) *Agent {
	switch runtime.GOOS {
	case "darwin":
		cfg = filepath.Join(home, "Library", "Application Support")
	case "windows":
		cfg = os.Getenv("APPDATA")
		if cfg == "" {
			cfg = filepath.Join(home, "AppData", "Roaming")
		}
	}
	return vscodeAt(filepath.Join(cfg, "Code", "User"))
}

func vscodeAt(dir string) *Agent {
	path := filepath.Join(dir, "settings.json")
	models := filepath.Join(dir, "chatLanguageModels.json")
	key := "vscode:" + path + ":"
	get := func() string { v, _ := edit.GetJSON(path, vscodeDefault); return v }
	ours := func() (string, bool) { return edit.GetJSONItem(models, vscodeGroup) }
	joined := func() bool { _, ok := ours(); return ok }
	// inGroup: a model id is one of those magpie's group lists
	inGroup := func(id string) bool {
		g, ok := ours()
		if !ok || id == "" {
			return false
		}
		for _, m := range gjson.Get(g, "models").Array() {
			if strings.EqualFold(m.Get("id").String(), id) {
				return true
			}
		}
		return false
	}
	model := func() string {
		v := get()
		if inGroup(v) {
			return magpieID + "/" + v
		}
		return v
	}
	// restore puts back the chat.defaultModel the user had before magpie's
	restore := func() error {
		if was := unstash(key + "model"); was != "" {
			return edit.SetJSON(path, edit.KV{Path: vscodeDefault, Value: was})
		}
		return edit.DelJSON(path, vscodeDefault)
	}
	return atomic(&Agent{
		ID: "vscode", Name: "VS Code", Icon: "vscode", Aliases: []string{"vs-code", "copilot-chat", "vscode-chat"},
		Bin: "code", Dir: dir, Path: path,
		UA: []string{vscodeUA},
		Notice: func() string {
			if joined() {
				return "magpie's models are in VS Code's Chat model picker, under magpie (VS Code 1.122 or later). If they don't show, run Developer: Reload Window in VS Code."
			}
			return ""
		},
		// magpie's group joins the user's own models: picking one of VS
		// Code's in magpie keeps it, and only switching off takes it out
		Joined: joined,
		Unwire: func() error {
			if inGroup(get()) {
				if err := restore(); err != nil {
					return err
				}
			}
			forget(key + "model")
			return edit.DelJSONItem(models, vscodeGroup)
		},
		Sync: func() error {
			cur, ok := ours()
			if !ok {
				return nil
			}
			v := vscodeGroupJSON()
			if sameJSON(cur, v) {
				return nil
			}
			return edit.SetJSONItem(models, vscodeGroup, v)
		},
		Check: func() string {
			g, ok := ours()
			if !ok || !gjson.Get(g, "models.0").Exists() {
				return ""
			}
			return wiringOff("VS Code", models, func(k string) (string, bool) {
				r := gjson.Get(g, "models.0."+k)
				return r.String(), r.Exists()
			}, "url", vscodeURL(), "requestHeaders.Authorization", "Bearer "+gateway.Token)
		},
		Fields: []Field{{
			Key: "model", Label: "model", Get: model,
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !inGroup(get()) {
						stash(map[string]string{key + "model": get()})
					}
					if err := edit.SetJSONItem(models, vscodeGroup, vscodeGroupJSON()); err != nil {
						return err
					}
					return edit.SetJSON(path, edit.KV{Path: vscodeDefault, Value: ref})
				}
				// VS Code's own: its default, where magpie's group stays
				if v == "" {
					if inGroup(get()) {
						return restore()
					}
					return edit.DelJSON(path, vscodeDefault)
				}
				forget(key + "model")
				return edit.SetJSON(path, edit.KV{Path: vscodeDefault, Value: v})
			},
			Options: func(cur map[string]string) []Option {
				// auto: Copilot's Auto, for one signed in to it
				own := []Option{{Value: "auto", Label: "Auto"}}
				if v := cur["model"]; v != "" && v != "auto" && !usesMagpie(v) {
					own = append(own, Option{Value: v, Icon: modelIcon("", v)})
				}
				return append(group("VS Code", own), viaMagpie("vscode", magpieID+"/")...)
			},
		}},
	}, path, models, stashPath())
}

// vscodeURL is the gateway's Chat Completions URL: a Custom Endpoint model's
// url that ends in /chat/completions is asked as it is.
func vscodeURL() string { return gatewayV1() + "/chat/completions" }

// vscodeGroupJSON is magpie's group in chatLanguageModels.json: the catalog
// as VS Code's chat is shown it.
func vscodeGroupJSON() map[string]any {
	list := []any{}
	for _, m := range magpieModels("vscode") {
		context := m.Context
		if context == 0 {
			context = 128000 // what VS Code takes a model of unknown window for
		}
		// VS Code keeps the output budget out of the window it fills with
		// the prompt, so a model whose output limit is near its window
		// would leave the prompt next to no room
		output := maxTokens(m)
		if output <= 0 {
			output = 8192
		}
		output = min(output, context/4)
		entry := map[string]any{
			"id": m.ID, "name": m.Name, "url": vscodeURL(),
			"toolCalling": true, "vision": m.Images,
			"contextWindow": context, "maxOutputTokens": output,
			"requestHeaders": map[string]string{"Authorization": "Bearer " + gateway.Token},
		}
		if len(m.Efforts) > 0 {
			entry["supportsReasoningEffort"] = m.Efforts
			entry["reasoningEffortFormat"] = "chat-completions"
		}
		list = append(list, entry)
	}
	return map[string]any{"name": magpieID, "vendor": "customendpoint", "apiType": "chat-completions", "models": list}
}
