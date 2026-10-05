package agent

// VS Code's own chat (GitHub Copilot Chat, built into VS Code) takes models
// of other vendors through its Custom Endpoint provider, in VS Code Stable
// since 1.122, with no GitHub sign-in or Copilot plan needed: groups of
// models in chatLanguageModels.json beside its settings.json (the default
// profile's, in its User folder, and each other profile's in its own:
// vscodeProfiles), a JSONC array VS Code watches and reads again when it
// changes. magpie adds a group of its own there,
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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/appdir"
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
		cfg = appdir.Getenv("APPDATA")
		if cfg == "" {
			cfg = filepath.Join(home, "AppData", "Roaming")
		}
	}
	return vscodeAt(filepath.Join(cfg, "Code", "User"))
}

func vscodeAt(dir string) *Agent {
	path := filepath.Join(dir, "settings.json")
	models := filepath.Join(dir, "chatLanguageModels.json")
	key := func(p string) string { return "vscode:" + p + ":model" }
	getAt := func(p string) string { v, _ := edit.GetJSON(p, vscodeDefault); return v }
	get := func() string { return getAt(path) }
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
	// restore puts back the chat.defaultModel a settings.json had before
	// magpie's
	restore := func(p string) error {
		if was := unstash(key(p)); was != "" {
			return edit.SetJSON(p, edit.KV{Path: vscodeDefault, Value: was})
		}
		return edit.DelJSON(p, vscodeDefault)
	}
	// profiles runs fn on the files of the profiles VS Code has now, put
	// back as they were if it fails, as atomic does the default's
	profiles := func(fn func(settings, models []string) error) error {
		s, m := vscodeProfiles(dir)
		return edit.Atomically(func() error { return fn(s, m) }, append(s, m...)...)
	}
	// setGroup writes magpie's group in the default's and each profile's
	setGroup := func(lms []string) error {
		v := vscodeGroupJSON()
		for _, lm := range append([]string{models}, lms...) {
			if cur, ok := edit.GetJSONItem(lm, vscodeGroup); ok && sameJSON(cur, v) {
				continue
			}
			if err := edit.SetJSONItem(lm, vscodeGroup, v); err != nil {
				return err
			}
		}
		return nil
	}
	return atomic(&Agent{
		ID: "vscode", Name: "VS Code", Icon: "vscode", Aliases: []string{"vs-code", "copilot-chat", "vscode-chat"}, Spelled: prefixed,
		Bin: "code", Dir: dir, Path: path,
		UA: []string{vscodeUA},
		detect: func() bool {
			if Taken(dir) {
				return false
			}
			if isDir(dir) {
				return true
			}
			bin, err := exec.LookPath("code")
			return err == nil && vscodeCodeBinary(bin)
		},
		Notice: func() string {
			if joined() {
				return "magpie's models are in VS Code's Chat model picker, under magpie, in each of its profiles (VS Code 1.122 or later). If they don't show, run Developer: Reload Window in VS Code."
			}
			return ""
		},
		// magpie's group joins the user's own models: picking one of VS
		// Code's in magpie keeps it, and only switching off takes it out
		Joined: joined,
		Unwire: func() error {
			return profiles(func(settings, lms []string) error {
				for _, p := range append([]string{path}, settings...) {
					if inGroup(getAt(p)) {
						if err := restore(p); err != nil {
							return err
						}
					}
					forget(key(p))
				}
				for _, lm := range append([]string{models}, lms...) {
					if err := edit.DelJSONItem(lm, vscodeGroup); err != nil {
						return err
					}
				}
				return nil
			})
		},
		// a profile made since is given the group too
		Sync: func() error {
			if !joined() {
				return nil
			}
			return profiles(func(_, lms []string) error { return setGroup(lms) })
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
				return profiles(func(settings, lms []string) error {
					if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
						if err := setGroup(lms); err != nil {
							return err
						}
						// a new chat in each profile starts on it
						for _, p := range append([]string{path}, settings...) {
							if !inGroup(getAt(p)) {
								stash(map[string]string{key(p): getAt(p)})
							}
							if err := edit.SetJSON(p, edit.KV{Path: vscodeDefault, Value: ref}); err != nil {
								return err
							}
						}
						return nil
					}
					// one of VS Code's own: the profiles go back to theirs
					for _, p := range settings {
						if inGroup(getAt(p)) {
							if err := restore(p); err != nil {
								return err
							}
						}
					}
					// VS Code's own: its default, where magpie's group stays
					if v == "" {
						if inGroup(get()) {
							return restore(path)
						}
						return edit.DelJSON(path, vscodeDefault)
					}
					forget(key(path))
					return edit.SetJSON(path, edit.KV{Path: vscodeDefault, Value: v})
				})
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

// Cursor can install its own launcher as code. Only a known product identity
// rules it out: wrappers without readable metadata keep the PATH fallback.
func vscodeCodeBinary(bin string) bool {
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return true
	}
	dir := filepath.Dir(resolved)
	if !strings.EqualFold(filepath.Base(dir), "bin") {
		return true
	}
	// Launchers live in app/bin or in bin beside resources/app. Do not
	// search arbitrary ancestors of an unknown wrapper.
	for _, path := range []string{
		filepath.Join(dir, "..", "product.json"),
		filepath.Join(dir, "..", "resources", "app", "product.json"),
	} {
		var product struct {
			ApplicationName string `json:"applicationName"`
			NameShort       string `json:"nameShort"`
		}
		b, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(b, &product) != nil {
			continue
		}
		if strings.EqualFold(product.ApplicationName, "cursor") || strings.EqualFold(product.NameShort, "Cursor") {
			return false
		}
	}
	return true
}

// vscodeProfiles are the settings.json and chatLanguageModels.json of the
// profiles made in VS Code besides its default (whose are in dir): a window
// opened on one reads that profile's own, so a group only in the default's
// is in the Agents window (which uses the default's) and not in the Chat of
// a window on another profile (TJHHHH on Discord). VS Code lists them in
// globalStorage/storage.json's userDataProfiles, each location a folder
// under dir/profiles; a profile set to use the default's settings or
// models (useDefaultFlags) has none of that kind, and one whose folder is
// gone none at all.
func vscodeProfiles(dir string) (settings, models []string) {
	b, err := os.ReadFile(filepath.Join(dir, "globalStorage", "storage.json"))
	if err != nil {
		return nil, nil
	}
	for _, p := range gjson.GetBytes(b, "userDataProfiles").Array() {
		loc := p.Get("location")
		at := loc.String()
		if loc.IsObject() {
			// a URI: its fsPath when kept, else its path (/c:/… on Windows)
			if at = loc.Get("fsPath").String(); at == "" {
				at = loc.Get("path").String()
				if len(at) > 2 && at[0] == '/' && at[2] == ':' {
					at = at[1:]
				}
				at = filepath.FromSlash(at)
			}
		} else if at != "" {
			at = filepath.Join(dir, "profiles", at)
		}
		if at == "" || !isDir(at) {
			continue
		}
		if !p.Get("useDefaultFlags.settings").Bool() {
			settings = append(settings, filepath.Join(at, "settings.json"))
		}
		if !p.Get("useDefaultFlags.languageModels").Bool() {
			models = append(models, filepath.Join(at, "chatLanguageModels.json"))
		}
	}
	return settings, models
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
