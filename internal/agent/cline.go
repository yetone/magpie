package agent

// Cline's CLI (3.x) keeps its providers in $CLINE_DIR/data/settings/
// providers.json, ~/.cline by default:
//
//	{"version":1,"lastUsedProvider":"<id>","modes":{},"providers":{"<id>":{
//	  "settings":{"provider":"<id>","apiKey":…,"model":…,"baseUrl":…,
//	    "headers":{…},"reasoning":{"effort":…}},
//	  "updatedAt":…,"tokenSource":"manual"}}}
//
// A session runs the model of lastUsedProvider, asking for its
// reasoning.effort. A provider of one's own, added to models.json, is
// refused when a session starts ("Unknown or disabled provider",
// cline/cline#14180), so magpie takes Cline's built-in openai-compatible
// provider, which reads the models from the gateway's /v1/models, and makes
// it the one in use. That provider lists only gpt-4o, so magpie's models
// are put in models.json beside it, as the entry Cline's own migration writes
// for it ({"provider":{"name","baseUrl","defaultModelId"},"models":{…}}),
// which takes the place of the built-in list. What was there, and the
// provider in use, are stashed and put back when magpie steps out. Cline's
// requests name only the AI SDK, so the header says they are Cline's.
//
// reasoning is {"enabled":false} for no thinking, as Cline's own pickers
// write it; an effort beside a false enabled is dropped, so magpie writes
// {"enabled":true,"effort":…} for an effort.
//
// Cline's VS Code extension (4.x) runs on the same files, but takes the
// provider, model, base URL and key from its own state beside them first —
// $CLINE_DATA_DIR (else $CLINE_DIR/data) globalState.json's
// act/planModeApiProvider ("openai" for openai-compatible),
// act/planModeOpenAiModelId and openAiBaseUrl, secrets.json's openAiApiKey
// — and providers.json's lastUsedProvider only when no mode has one. Where
// that state is, magpie sets those too, stashing what they were. The
// extension reads the files once, at start-up.
//
// Cline's desktop app (0.0.x) keeps the provider, model and effort picked
// in its composer in its web view's localStorage, per provider, and never
// reads them from these files: magpie's models are in its picker, under
// magpie, but which one it runs is picked there.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/edit"
)

// clineSlot is the provider magpie takes in Cline.
const clineSlot = "openai-compatible"

func cline(home string) *Agent { return clineIn(here(home)) }

// clineIn is Cline at a place: this machine's home, or a WSL distro's (see
// wsl.go), where CLINE_DIR and CLINE_DATA_DIR aren't read and its provider
// names the gateway as the distro reaches it, with the key it takes from
// there.
func clineIn(at place) *Agent {
	dir := at.getenv("CLINE_DIR")
	if dir == "" {
		dir = filepath.Join(at.home, ".cline")
	}
	data := at.getenv("CLINE_DATA_DIR")
	if data == "" {
		data = filepath.Join(dir, "data")
	}
	path := filepath.Join(data, "settings", "providers.json")
	models := filepath.Join(data, "settings", "models.json")
	state, secrets := filepath.Join(data, "globalState.json"), filepath.Join(data, "secrets.json")
	key := "cline:" + path + ":"
	slot := "providers." + clineSlot
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	inUse := func() string { return get("lastUsedProvider") }
	onMagpie := func() bool {
		return inUse() == clineSlot && ourKey(get(slot+".settings.apiKey"))
	}
	// stateOnMagpie: the extension's state is magpie's
	stateOnMagpie := func() bool {
		k, _ := edit.GetJSON(secrets, "openAiApiKey")
		u, _ := edit.GetJSON(state, "openAiBaseUrl")
		return ourKey(k) && u == at.v1()
	}
	// wireState points the extension's state, when there is one, at model
	// on magpie's openai-compatible
	wireState := func(model string) error {
		if _, err := os.Stat(state); err != nil {
			return nil
		}
		if !stateOnMagpie() {
			k, _ := edit.GetJSON(secrets, "openAiApiKey")
			stash(map[string]string{key + "state": clineSnapshot(state), key + "secret": k, key + "stated": "1"})
		}
		// the model info a pick leaves is the old model's; magpie's is in
		// models.json
		if err := edit.DelJSON(state, "actModeOpenAiModelInfo", "planModeOpenAiModelInfo"); err != nil {
			return err
		}
		if err := edit.SetJSON(state,
			edit.KV{Path: "actModeApiProvider", Value: "openai"}, edit.KV{Path: "planModeApiProvider", Value: "openai"},
			edit.KV{Path: "actModeOpenAiModelId", Value: model}, edit.KV{Path: "planModeOpenAiModelId", Value: model},
			edit.KV{Path: "openAiBaseUrl", Value: at.v1()},
			edit.KV{Path: "openAiHeaders", Value: map[string]string{"User-Agent": "cline"}}); err != nil {
			return err
		}
		return clineWrite(secrets, "{}", edit.KV{Path: "openAiApiKey", Value: at.gwKey()})
	}
	// restoreState puts back the extension's state from before magpie;
	// magpie's keys go when nothing was stashed
	restoreState := func() error {
		saved, secret, stated := unstash(key+"state"), unstash(key+"secret"), unstash(key+"stated")
		if stated == "" && !stateOnMagpie() {
			return nil
		}
		if _, err := os.Stat(state); err == nil {
			if err := clineRestore(state, saved); err != nil {
				return err
			}
		}
		if _, err := os.Stat(secrets); err != nil {
			return nil
		}
		if secret != "" {
			return edit.SetJSON(secrets, edit.KV{Path: "openAiApiKey", Value: secret})
		}
		return edit.DelJSON(secrets, "openAiApiKey")
	}
	// restore puts back the openai-compatible provider and the provider in
	// use the user had before magpie
	restore := func() error {
		if err := restoreState(); err != nil {
			return err
		}
		entry, last := unstash(key+"entry"), unstash(key+"lastUsedProvider")
		if list := unstash(key + "models"); list != "" {
			if err := edit.SetJSON(models, edit.KV{Path: "providers." + clineSlot, Value: json.RawMessage(list)}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(models, "providers."+clineSlot); err != nil {
			return err
		}
		if entry != "" {
			if err := edit.SetJSON(path, edit.KV{Path: slot, Value: json.RawMessage(entry)}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, slot); err != nil {
			return err
		}
		if last != "" {
			return edit.SetJSON(path, edit.KV{Path: "lastUsedProvider", Value: last})
		}
		return edit.DelJSON(path, "lastUsedProvider")
	}
	// own is the provider a model or effort of Cline's own is set on: the
	// one in use, else Cline's default
	own := func() string {
		if p := inUse(); p != "" {
			return p
		}
		return "cline"
	}
	return &Agent{
		ID: "cline", Name: "Cline", Icon: "cline", Aliases: []string{"cline-cli"}, Spelled: prefixed,
		UA:  []string{"cline"},
		Bin: "cline", Dir: dir, Path: path,
		Sync: func() error {
			if !onMagpie() {
				return nil
			}
			if err := syncJSON(path, slot+".settings.baseUrl", func() any { return at.v1() }); err != nil {
				return err
			}
			if k, _ := edit.GetJSON(secrets, "openAiApiKey"); ourKey(k) {
				if err := syncJSON(state, "openAiBaseUrl", func() any { return at.v1() }); err != nil {
					return err
				}
			}
			return syncJSON(models, "providers."+clineSlot, func() any { return clineModelsAt(get(slot+".settings.model"), at.v1()) })
		},
		Notice: func() string {
			if runtime.GOOS == "windows" {
				// Running can't tell the desktop app from the CLI there
				return "Cline reads its provider as a session starts — open sessions keep the model they have; new ones use this. Reload VS Code's window for its extension; Cline's desktop app keeps the model picked in its own composer — pick magpie's there."
			}
			if Running(`Cline\.app/`, `(^|/)cline-app( |$)`) {
				return "Cline's desktop app keeps the model and effort picked in its own composer — pick magpie's there; this sets Cline's CLI and VS Code extension."
			}
			if Running(`(^|/)cline( |$)`) {
				return "Cline reads its provider as a session starts — open sessions keep the model they have; new ones use this. Reload VS Code's window for its extension."
			}
			return ""
		},
		Check: func() string {
			if !onMagpie() {
				return ""
			}
			return wiringOff("Cline", path, func(k string) (string, bool) { return edit.GetJSON(path, slot+".settings."+k) },
				"baseUrl", at.v1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				p := inUse()
				if p == "" {
					return ""
				}
				v := get("providers." + p + ".settings.model")
				if v != "" && onMagpie() {
					return magpieID + "/" + v
				}
				return v
			},
			Set: func(v string) error {
				if v == "" {
					if onMagpie() {
						return restore()
					}
					if p := inUse(); p != "" {
						return edit.DelJSON(path, "providers."+p+".settings.model")
					}
					return nil
				}
				if ref, ok := cutMagpie(v); ok {
					effort := clineEffort(get("providers." + own() + ".settings.reasoning"))
					if !onMagpie() {
						list, _ := edit.GetJSON(models, "providers."+clineSlot)
						stash(map[string]string{
							key + "entry":            get(slot),
							key + "lastUsedProvider": inUse(),
							key + "models":           list,
						})
					}
					if err := clineWrite(models, `{"version":1,"providers":{}}`,
						edit.KV{Path: "providers." + clineSlot, Value: clineModelsAt(ref, at.v1())}); err != nil {
						return err
					}
					if err := wireState(ref); err != nil {
						return err
					}
					return clineWrite(path, providersEmpty,
						edit.KV{Path: slot, Value: clineProvider(ref, effort, at)},
						edit.KV{Path: "lastUsedProvider", Value: clineSlot})
				}
				if onMagpie() {
					if err := restore(); err != nil {
						return err
					}
				}
				p := own()
				return clineWrite(path, providersEmpty,
					edit.KV{Path: "providers." + p + ".settings.provider", Value: p},
					edit.KV{Path: "providers." + p + ".settings.model", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				return append(ownOptions("", cur["model"]), viaMagpie("cline", magpieID+"/")...)
			},
		}, {
			// the reasoning of the provider in use, what Cline's --thinking
			// (and its extension) asks for when it is not given
			Key: "effort", Label: "effort",
			Get: func() string {
				p := inUse()
				if p == "" {
					return ""
				}
				return clineEffort(get("providers." + p + ".settings.reasoning"))
			},
			Set: func(v string) error {
				k := "providers." + own() + ".settings.reasoning"
				if v == "" {
					return edit.DelJSON(path, k)
				}
				return clineWrite(path, providersEmpty, edit.KV{Path: k, Value: clineReasoning(v)})
			},
			Options: func(map[string]string) []Option {
				return static("none", "low", "medium", "high", "xhigh")
			},
		}},
	}
}

// cutMagpie is the model a magpie/<ref> value names, when it is magpie's.
func cutMagpie(v string) (string, bool) {
	ref, ok := strings.CutPrefix(v, magpieID+"/")
	return ref, ok && isMagpie(ref)
}

// clineProvider is magpie's openai-compatible entry, asking model with
// effort (none unset), for the Cline at a place.
func clineProvider(model, effort string, at place) map[string]any {
	s := map[string]any{
		"provider": clineSlot, "apiKey": at.gwKey(), "model": model, "baseUrl": at.v1(),
		"headers": map[string]string{"User-Agent": "cline"},
	}
	if effort != "" {
		s["reasoning"] = clineReasoning(effort)
	}
	return map[string]any{"settings": s, "updatedAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "tokenSource": "manual"}
}

// clineReasoning is a provider's reasoning asking for effort, none for no
// thinking.
func clineReasoning(effort string) map[string]any {
	if effort == "none" {
		return map[string]any{"enabled": false}
	}
	return map[string]any{"enabled": true, "effort": effort}
}

// clineEffort is the effort a provider's reasoning asks for: none when it
// is off, whatever effort is left beside that.
func clineEffort(raw string) string {
	var r struct {
		Enabled *bool  `json:"enabled"`
		Effort  string `json:"effort"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil {
		return ""
	}
	if r.Enabled != nil && !*r.Enabled {
		return "none"
	}
	return r.Effort
}

// clineStateKeys are the keys of the extension's globalState.json magpie
// sets.
var clineStateKeys = []string{"actModeApiProvider", "planModeApiProvider", "actModeOpenAiModelId", "planModeOpenAiModelId",
	"actModeOpenAiModelInfo", "planModeOpenAiModelInfo", "openAiBaseUrl", "openAiHeaders"}

// clineSnapshot is what path has of clineStateKeys, as a JSON object.
func clineSnapshot(path string) string {
	var all map[string]json.RawMessage
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &all)
	m := map[string]json.RawMessage{}
	for _, k := range clineStateKeys {
		if v, ok := all[k]; ok {
			m[k] = v
		}
	}
	b, _ = json.Marshal(m)
	return string(b)
}

// clineRestore sets path's clineStateKeys back to a snapshot, removing
// those it has not.
func clineRestore(path, snapshot string) error {
	var m map[string]json.RawMessage
	json.Unmarshal([]byte(snapshot), &m)
	var kvs []edit.KV
	var gone []string
	for _, k := range clineStateKeys {
		if v, ok := m[k]; ok {
			kvs = append(kvs, edit.KV{Path: k, Value: v})
		} else {
			gone = append(gone, k)
		}
	}
	if err := edit.DelJSON(path, gone...); err != nil {
		return err
	}
	if len(kvs) == 0 {
		return nil
	}
	return edit.SetJSON(path, kvs...)
}

// clineModels is magpie's entry in models.json: every magpie model, model
// the one the provider starts on.
func clineModels(model string) map[string]any { return clineModelsAt(model, gatewayV1()) }

// clineModelsAt is clineModels for a Cline reaching the gateway's /v1 at v1.
func clineModelsAt(model, v1 string) map[string]any {
	ms := map[string]any{}
	for _, m := range magpieModels("cline") {
		caps := []string{"streaming", "tools"}
		if m.Images {
			caps = append(caps, "images")
		}
		if len(m.Efforts) > 0 {
			caps = append(caps, "reasoning")
		}
		e := map[string]any{"id": m.ID, "name": m.Name, "capabilities": caps}
		if m.Context > 0 {
			e["contextWindow"] = m.Context
		}
		if m.Output > 0 {
			e["maxTokens"] = maxTokens(m)
		}
		ms[m.ID] = e
	}
	return map[string]any{
		"provider": map[string]any{"name": "magpie", "baseUrl": v1, "defaultModelId": model},
		"models":   ms,
	}
}

// providersEmpty is a providers.json with nothing in it.
const providersEmpty = `{"version":1,"modes":{},"providers":{}}`

// clineWrite sets kvs in a file of Cline's settings, which it keeps
// private, made from empty when there is none.
func clineWrite(path, empty string, kvs ...edit.KV) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(empty+"\n"), 0o600); err != nil {
			return err
		}
	}
	return edit.SetJSON(path, kvs...)
}
