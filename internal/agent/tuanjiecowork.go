package agent

// Tuanjie Cowork (codely.tuanjie.cn, Unity China's coworker app;
// Charles24209792 on X) is a Tauri app, dev.codelycowork.desktop, whose core
// runs Codely sessions. Its models of one's own are its Settings' Custom
// Models, kept in ~/.codely/settings.json (the core's settings path:
// homedir/.codely; read at 2.1.6-stable). The file is read afresh on every
// lookup and written whole, JSON with two-space indent, other keys kept:
//
//	"customModelProviders": [{"id":"custom-provider","name":…,
//	  "baseUrl":…,"apiKey":…,"authType":"openai",
//	  "flashModelId"?,"multimodalModelId"?}, …],
//	"customModels": [{"id":…,"model":…,"providerId":…,"displayName":…,
//	  "roles":["model"],"wireApi":"chat"|"responses"|"messages",
//	  "supportsMultimodal":…}, …]
//
// A model's id is unique across providers. A session on one is started with
// its provider's baseUrl and key as OPENAI_BASE_URL and OPENAI_API_KEY
// (ANTHROPIC_* for wireApi messages), and Cowork lists a provider's models
// at baseUrl + /models when baseUrl ends in /v1.
//
// So magpie adds a provider and a model of its own, both with id "magpie":
// the gateway's /v1 with magpie's key for Cowork (the key names it), and
// the model picked, on chat completions. The user's providers and models
// stay as they are. Which model a chat is on is Cowork's own pick in its
// model menu (per workspace, by model id), which magpie doesn't write: the
// user picks "<model> (magpie)" there once, and since the id stays
// "magpie", a model changed in magpie later is the one Cowork's pick then
// sends. Cowork's startup migrations (migratedToMultiProvider) only move
// models with no providerId, so magpie's entries are left alone.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

const (
	coworkProviders = "customModelProviders"
	coworkModels    = "customModels"
)

// coworkProc is Tuanjie Cowork's app process: the .app's cowork, the
// Windows build's cowork.exe.
var coworkProc = []string{`Tuanjie Cowork\.app/Contents/MacOS/cowork`, `(^|[/\\])cowork\.exe`}

var noticeCowork = newNotice("Pick magpie's model in Tuanjie Cowork's model menu; if it isn't there yet, restart Tuanjie Cowork.")

func tuanjieCowork(home string) *Agent { return tuanjieCoworkAt(here(home)) }

// tuanjieCoworkAt is Tuanjie Cowork with its home at at.home.
func tuanjieCoworkAt(at place) *Agent {
	const id = "tuanjie-cowork"
	dir := at.join(".codely")
	path := filepath.Join(dir, "settings.json")
	// keyNew marks a settings.json magpie made
	keyNew := id + ":" + path + ":new"
	key := func() string { return agentKeyAt(id, at.gw()) }
	read := func() gjson.Result {
		raw, err := edit.Read(path)
		if err != nil {
			return gjson.Result{}
		}
		return gjson.ParseBytes(raw)
	}
	find := func(c gjson.Result, list, k, v string) gjson.Result {
		for _, x := range c.Get(list).Array() {
			if x.Get(k).String() == v {
				return x
			}
		}
		return gjson.Result{}
	}
	// prov and model are magpie's provider and model, where they are
	prov := func(c gjson.Result) gjson.Result { return find(c, coworkProviders, "id", magpieID) }
	model := func(c gjson.Result) gjson.Result {
		m := find(c, coworkModels, "id", magpieID)
		if m.Get("providerId").String() != magpieID {
			return gjson.Result{}
		}
		return m
	}
	// mine: the provider with magpie's id is magpie's, known by magpie's
	// key for Cowork, which stays where the gateway's port moved (Check
	// tells the address apart), or by the gateway's own address
	mine := func(p gjson.Result) bool {
		return p.Exists() && (keyHolds(p.Get("apiKey").String(), key()) ||
			sameGateway(p.Get("baseUrl").String(), at.v1()))
	}
	on := func(c gjson.Result) bool { return mine(prov(c)) && model(c).Exists() }
	// rest is a list as written with magpie's entries left out
	rest := func(c gjson.Result, list, owner string) []json.RawMessage {
		out := []json.RawMessage{}
		for _, x := range c.Get(list).Array() {
			if x.Get(owner).String() == magpieID {
				continue
			}
			out = append(out, json.RawMessage(x.Raw))
		}
		return out
	}
	// merged is an entry as written with v laid over it
	merged := func(x gjson.Result, v map[string]any) json.RawMessage {
		m := map[string]any{}
		if x.Exists() {
			json.Unmarshal([]byte(x.Raw), &m)
		}
		for k, val := range v {
			m[k] = val
		}
		b, _ := json.Marshal(m)
		return b
	}
	images := func(ref string) bool {
		for _, m := range sharedModels(id) {
			if m.ID == ref {
				return m.Images
			}
		}
		return false
	}
	// wire writes magpie's provider and model, ref the model, keeping a
	// name, roles or other fields given them in Cowork
	wire := func(c gjson.Result, ref string) error {
		p := prov(c)
		pv := map[string]any{"id": magpieID, "baseUrl": at.v1(), "apiKey": key(), "authType": "openai"}
		if p.Get("name").String() == "" {
			pv["name"] = magpieID
		}
		m := model(c)
		mv := map[string]any{
			"id": magpieID, "providerId": magpieID, "model": ref, "displayName": ref,
			"wireApi": "chat", "supportsMultimodal": images(ref),
		}
		if !m.Get("roles").IsArray() || len(m.Get("roles").Array()) == 0 {
			mv["roles"] = []string{"model"}
		}
		ps := append(rest(c, coworkProviders, "id"), merged(p, pv))
		ms := append(rest(c, coworkModels, "providerId"), merged(m, mv))
		if !isFile(path) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			// it holds keys, as Cowork's own does
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
				f.WriteString("{}\n")
				f.Close()
			}
		}
		return edit.SetJSON(path, edit.KV{Path: coworkProviders, Value: ps}, edit.KV{Path: coworkModels, Value: ms})
	}
	// unwire takes magpie's provider and models out, and the file where
	// magpie made it and nothing else is left
	unwire := func(c gjson.Result) error {
		made := unstash(keyNew) != ""
		if !prov(c).Exists() && !find(c, coworkModels, "providerId", magpieID).Exists() {
			return nil
		}
		ps, ms := rest(c, coworkProviders, "id"), rest(c, coworkModels, "providerId")
		if err := edit.SetJSON(path, edit.KV{Path: coworkProviders, Value: ps}, edit.KV{Path: coworkModels, Value: ms}); err != nil {
			return err
		}
		if !made || len(ps) > 0 || len(ms) > 0 {
			return nil
		}
		left := read().Map()
		delete(left, coworkProviders)
		delete(left, coworkModels)
		if len(left) == 0 {
			return edit.Remove(path)
		}
		return nil
	}
	return atomic(&Agent{
		ID: id, Name: "Tuanjie Cowork", Icon: "tuanjie-cowork-color",
		Aliases: []string{"tuanjie", "tjcowork", "codely-cowork", "tuanjiecowork"},
		Spelled: prefixed, NoNames: true,
		Dir: dir, Path: path,
		// its folder once it has run, else its app
		detect: func() bool { return isDir(dir) || (&Agent{ID: id}).HasApp() },
		Notice: func() string {
			if on(read()) && Running(coworkProc...) {
				return noticeCowork.String()
			}
			return ""
		},
		Check: func() string {
			c := read()
			p := prov(c)
			if !mine(p) {
				return ""
			}
			if !model(c).Exists() {
				return "Tuanjie Cowork's magpie model (settings.json) is gone, so it no longer reaches magpie"
			}
			return wiringOff("Tuanjie Cowork", path, func(k string) (string, bool) {
				r := p.Get(k)
				return r.String(), r.Exists()
			}, "baseUrl", at.v1(), "apiKey", key())
		},
		Sync: func() error {
			c := read()
			if !on(c) {
				return nil
			}
			p := prov(c)
			if p.Get("baseUrl").String() == at.v1() && p.Get("apiKey").String() == key() && p.Get("authType").String() == "openai" {
				return nil
			}
			return wire(c, model(c).Get("model").String())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				c := read()
				if on(c) {
					return magpieID + "/" + model(c).Get("model").String()
				}
				return ""
			},
			Set: func(v string) error {
				c := read()
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if raw, err := edit.Read(path); err == nil && len(strings.TrimSpace(string(raw))) > 0 && !c.IsObject() {
						return errors.New(path + " isn't settings Tuanjie Cowork reads, so magpie leaves it alone")
					}
					if p := prov(c); p.Exists() && !mine(p) {
						return errors.New("Tuanjie Cowork has a model provider of its own with the id magpie (" + path + "), so magpie leaves it alone")
					}
					if m := find(c, coworkModels, "id", magpieID); m.Exists() && m.Get("providerId").String() != magpieID {
						return errors.New("Tuanjie Cowork has a model of its own with the id magpie (" + path + "), so magpie leaves it alone")
					}
					if !prov(c).Exists() {
						forget(keyNew)
						if !isFile(path) {
							stash(map[string]string{keyNew: "1"})
						}
					}
					return wire(c, ref)
				}
				if mine(prov(c)) {
					if err := unwire(c); err != nil {
						return err
					}
				} else {
					forget(keyNew)
				}
				if v == "" {
					return nil
				}
				return errors.New("Tuanjie Cowork picks its own models in its model menu, so magpie can't set " + v + " there")
			},
			Options: func(cur map[string]string) []Option {
				return withVia(group("Tuanjie Cowork", nil), id, magpieID+"/")
			},
		}},
	}, path)
}
