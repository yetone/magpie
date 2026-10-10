package agent

// Empryo keeps its settings in ~/.empryo/config.json (a project's
// .empryo/config.json goes over it). Its own providers are a "providers"
// array of {id, name, baseURL, envVar, models, modelsAPI}, OpenAI-compatible
// endpoints it lists models from (modelsAPI, else baseURL's /models) and
// asks /chat/completions of; with no envVar it sends no key of the user's.
// The model sessions start on is defaultModel, "<provider id>/<model id>",
// split at the first "/".
//
// magpie adds a provider of its own, id "magpie", at the gateway, and sets
// defaultModel to magpie/<catalog id>; the defaultModel the user had is
// stashed and put back when magpie steps out, and the "magpie" provider
// goes again then. The user's providers and other keys are left as they
// are. With no envVar Empryo only lists a provider whose baseURL answers
// (2xx, 401 or 403), which the gateway's GET /v1 does.

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

const (
	empryoProviders = "providers"
	empryoModel     = "defaultModel"
)

func empryo(home string) *Agent { return empryoAt(here(home)) }

// empryoIn is Empryo in a WSL distro (see wsl.go).
func empryoIn(at place) *Agent { return empryoAt(at) }

// empryoAt is Empryo with its home at at.home, reaching the gateway as at
// does.
func empryoAt(at place) *Agent {
	gatewayV1 := at.v1
	dir := filepath.Join(at.home, ".empryo")
	path := filepath.Join(dir, "config.json")
	keyModel := "empryo:" + path + ":" + empryoModel
	// keyNew marks a file magpie made, which goes again once magpie's
	// provider and model are taken out of it and nothing is left
	keyNew := "empryo:" + path + ":new"
	model := func() string { v, _ := edit.GetJSON(path, empryoModel); return v }
	// ours is magpie's provider entry, "" where there is none
	ours := func() string {
		raw, err := edit.Read(path)
		if err != nil {
			return ""
		}
		for _, p := range gjson.GetBytes(raw, empryoProviders).Array() {
			if p.Get("id").String() == magpieID {
				return p.Raw
			}
		}
		return ""
	}
	// on: magpie's — the model is one of magpie's provider
	on := func() bool {
		ref, ok := strings.CutPrefix(model(), magpieID+"/")
		return ok && isMagpie(ref)
	}
	// providers sets the providers array to the user's ones plus, with
	// add, magpie's; with neither left the key goes
	providers := func(add bool) error {
		raw, err := edit.Read(path)
		if err != nil && isFile(path) {
			return err
		}
		var list []json.RawMessage
		for _, p := range gjson.GetBytes(raw, empryoProviders).Array() {
			if p.Get("id").String() != magpieID {
				list = append(list, json.RawMessage(p.Raw))
			}
		}
		if add {
			mine, _ := json.Marshal(map[string]any{
				"id": magpieID, "name": "Magpie",
				"baseURL": gatewayV1(), "modelsAPI": gatewayV1() + "/models",
			})
			list = append(list, mine)
		}
		if len(list) == 0 {
			if !isFile(path) {
				return nil
			}
			return edit.DelJSON(path, empryoProviders)
		}
		return edit.SetJSON(path, edit.KV{Path: empryoProviders, Value: list})
	}
	put := func(v string) error {
		if v == "" {
			if !isFile(path) {
				return nil
			}
			return edit.DelJSON(path, empryoModel)
		}
		return edit.SetJSON(path, edit.KV{Path: empryoModel, Value: v})
	}
	return atomic(&Agent{
		ID: "empryo", Name: "Empryo", Icon: "empryo-color", Aliases: []string{"soulforge"}, Spelled: prefixed,
		Bin: "empryo", Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)empryo( |$)`) {
				return noticeEmpryo.String()
			}
			return ""
		},
		Check: func() string {
			if !on() {
				return ""
			}
			raw := ours()
			if raw == "" {
				return "Empryo's magpie provider (config.json) is gone, so it no longer reaches magpie"
			}
			return wiringOff("Empryo", path, func(k string) (string, bool) {
				r := gjson.Get(raw, k)
				return r.String(), r.Exists()
			}, "baseURL", gatewayV1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: model,
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					// what the user had, unless magpie stashed it already
					if !on() {
						forget(keyModel, keyNew)
						stash(map[string]string{keyModel: model()})
						if !isFile(path) {
							stash(map[string]string{keyNew: "1"})
						}
					}
					if err := providers(true); err != nil {
						return err
					}
					return put(v)
				}
				made := false
				if on() || ours() != "" {
					made = unstash(keyNew) != ""
					// back to what the user had
					if v == "" {
						v = unstash(keyModel)
					} else {
						forget(keyModel)
					}
					if err := providers(false); err != nil {
						return err
					}
				}
				if err := put(v); err != nil {
					return err
				}
				if made {
					if raw, err := edit.Read(path); err == nil && len(gjson.ParseBytes(raw).Map()) == 0 {
						return edit.Remove(path)
					}
				}
				return nil
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					_, id, _ := strings.Cut(c, "/")
					own = append(own, Option{Value: c, Icon: modelIcon("", id)})
				}
				return append(group("Empryo", own), viaMagpie("empryo", magpieID+"/")...)
			},
		}},
	}, path)
}

// what empryo says after a change (notice.go)
var (
	noticeEmpryo = newNotice("Empryo reads its config at start-up — restart open empryo sessions to use this.")
)
