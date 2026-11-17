package agent

// Open Code Review (github.com/alibaba/open-code-review, npm
// @alibaba-group/open-code-review) is a Go CLI, `ocr`, that reviews a
// diff with a model; its plugins for Claude Code, Codex, Cursor, Kimi Code
// and OpenCode, and its skills, all run that same command. It keeps its
// settings in ~/.opencodereview/config.json (os.UserHomeDir, no variable
// moves it): `provider` names the entry in use, from `providers` (its
// presets: anthropic, openai, deepseek, …) or `custom_providers` (any
// other name), and `model` the model, the entry's own `model` winning
// (internal/llm/resolver.go tryProviderConfig). A custom entry needs a
// protocol, a url and a key of its own: no variable stands in for it.
//
// magpie adds the custom provider "magpie": the openai protocol, which
// posts Chat Completions to url + "/chat/completions", the gateway's /v1,
// a key naming Open Code Review, and the model picked, which goes to the
// gateway as it is, for it to resolve. provider is pointed at it; the
// provider and model the user had are stashed and put back when magpie
// steps out. Requests carry a User-Agent of open-code-review/<version>,
// which the gateway counts as this agent's. ocr reads the file on every
// run, so a change needs no restart.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

const ocrID = "open-code-review"

// ocrEntry is the key path of magpie's custom provider in config.json.
var ocrEntry = "custom_providers." + magpieID

func ocr(home string) *Agent { return ocrIn(here(home)) }

// ocrIn is Open Code Review at a place.
func ocrIn(at place) *Agent {
	dir := at.join(".opencodereview")
	path := filepath.Join(dir, "config.json")
	key := ocrID + ":" + path
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	// section is where OCR keeps the entry of a provider of the user's
	// own: "providers" or "custom_providers"; "" when it has none.
	section := func(p string) string {
		for _, s := range []string{"providers", "custom_providers"} {
			if r, ok := edit.GetJSONRaw(path, s+"."+gjson.Escape(p)); ok && strings.HasPrefix(strings.TrimSpace(r), "{") {
				return s
			}
		}
		return ""
	}
	// model is the model OCR sends for provider p: its entry's, which
	// wins, else the top-level one.
	model := func(p string) string {
		if s := section(p); s != "" {
			if m := get(s + "." + gjson.Escape(p) + ".model"); m != "" {
				return m
			}
		}
		return get("model")
	}
	cur := func() string {
		p := get("provider")
		if p == "" {
			return ""
		}
		m := model(p)
		if m == "" {
			return p
		}
		return p + "/" + m
	}
	writeMagpie := func(ref string) error {
		kvs := []edit.KV{
			{Path: ocrEntry + ".protocol", Value: "openai"},
			{Path: ocrEntry + ".url", Value: at.v1()},
			{Path: ocrEntry + ".api_key", Value: agentKeyAt(ocrID, at.gw())},
		}
		if ref != "" {
			kvs = append(kvs, edit.KV{Path: ocrEntry + ".model", Value: ref})
		}
		return edit.SetJSON(path, kvs...)
	}
	// setOwn makes provider/model of the user's own the one in use, as
	// `ocr config model` does: the top-level model and the entry's.
	setOwn := func(v string) error {
		p, m, _ := strings.Cut(v, "/")
		s := section(p)
		if p == magpieID || s == "" {
			return fmt.Errorf("Open Code Review has no provider %q set up; add it with `ocr config provider` first", p)
		}
		kvs := []edit.KV{{Path: "provider", Value: p}}
		if m != "" {
			kvs = append(kvs, edit.KV{Path: "model", Value: m}, edit.KV{Path: s + "." + gjson.Escape(p) + ".model", Value: m})
		}
		return edit.SetJSON(path, kvs...)
	}
	leave := func() error {
		forget(key+":provider", key+":model")
		if err := edit.DelJSON(path, ocrEntry); err != nil {
			return err
		}
		// custom_providers made for magpie's alone goes with it
		if r, ok := edit.GetJSONRaw(path, "custom_providers"); ok && strings.Join(strings.Fields(r), "") == "{}" {
			return edit.DelJSON(path, "custom_providers")
		}
		return nil
	}
	return atomic(&Agent{
		ID: ocrID, Name: "Open Code Review", Icon: ocrID, Spelled: prefixed, NoNames: true,
		UA: []string{ocrID},
		// `ocr` is other programs' name too: only one under a folder
		// named for it (npm's) is taken for it; one the install script put
		// in /usr/local/bin is known by its folder
		Bin: "ocr", binNot: ocrNotOurs,
		Dir: dir, Path: path,
		Sync: func() error {
			if _, ok := edit.GetJSONRaw(path, ocrEntry); !ok {
				return nil
			}
			return writeMagpie("")
		},
		Check: func() string {
			if get("provider") != magpieID {
				return ""
			}
			if _, ok := edit.GetJSONRaw(path, ocrEntry); !ok {
				return "Open Code Review's " + ocrEntry + " (config.json) is gone, so it no longer reaches magpie"
			}
			return wiringOff("Open Code Review", path, func(f string) (string, bool) { return edit.GetJSON(path, ocrEntry+"."+f) },
				"url", at.v1(), "api_key", agentKeyAt(ocrID, at.gw()))
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				if get("provider") != magpieID {
					return cur()
				}
				m := get(ocrEntry + ".model")
				if m == "" {
					m = get("model")
				}
				return magpieID + "/" + m
			},
			Set: func(v string) error {
				onMagpie := get("provider") == magpieID
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !onMagpie {
						stash(map[string]string{
							key + ":provider": get("provider"),
							key + ":model":    get("model"),
						})
					}
					if err := writeMagpie(ref); err != nil {
						return err
					}
					return edit.SetJSON(path, edit.KV{Path: "provider", Value: magpieID})
				}
				if v != "" {
					if err := setOwn(v); err != nil {
						return err
					}
					if onMagpie {
						return leave()
					}
					return nil
				}
				if !onMagpie {
					return edit.DelJSON(path, "provider", "model")
				}
				// magpie was in use: the provider and model the user had
				// come back, where OCR still has the provider
				saved := stashLoad()
				p, m := saved[key+":provider"], saved[key+":model"]
				if p != "" && p != magpieID && section(p) != "" {
					if err := edit.SetJSON(path, edit.KV{Path: "provider", Value: p}); err != nil {
						return err
					}
				} else if err := edit.DelJSON(path, "provider"); err != nil {
					return err
				}
				if m != "" {
					if err := edit.SetJSON(path, edit.KV{Path: "model", Value: m}); err != nil {
						return err
					}
				} else if err := edit.DelJSON(path, "model"); err != nil {
					return err
				}
				return leave()
			},
			Options: func(c map[string]string) []Option {
				return withVia(ocrOwnModels(path, c["model"]), ocrID, magpieID+"/")
			},
		}},
	})
}

// ocrNotOurs turns away an `ocr` command that isn't Open Code Review's:
// one under no folder named for it.
func ocrNotOurs(bin string) bool {
	for _, p := range []string{bin, evalLink(bin)} {
		s := strings.ToLower(filepath.ToSlash(p))
		if strings.Contains(s, "open-code-review") || strings.Contains(s, "opencodereview") {
			return false
		}
	}
	return true
}

// ocrOwnModels is the provider/model pairs OCR's config.json sets up of
// the user's own: each entry's model and models, and the one in use.
func ocrOwnModels(path, cur string) []Option {
	seen := map[string]bool{}
	var vals []string
	add := func(v string) {
		if v == "" || seen[v] || strings.HasPrefix(v, magpieID+"/") || v == magpieID {
			return
		}
		seen[v] = true
		vals = append(vals, v)
	}
	add(cur)
	if raw, err := edit.Read(path); err == nil {
		for _, s := range []string{"providers", "custom_providers"} {
			gjson.GetBytes(raw, gjson.Escape(s)).ForEach(func(k, e gjson.Result) bool {
				p := k.String()
				if p == magpieID || !e.IsObject() {
					return true
				}
				if m := e.Get("model").String(); m != "" {
					add(p + "/" + m)
				}
				for _, m := range e.Get("models").Array() {
					if m.String() != "" {
						add(p + "/" + m.String())
					}
				}
				return true
			})
		}
	}
	sort.Strings(vals)
	return static(vals...)
}
