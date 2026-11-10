package agent

// Craft Agents (lukilabs/craft-agents-oss, thecraftagents.com; springg1998
// on X) is an Electron app that runs Claude and Pi sessions. Its model
// connections are config.json's llmConnections in its config folder,
// $CRAFT_CONFIG_DIR, else ~/.craft-agent (packages/shared/src/config/
// paths.ts; read at 0.15.8). The connection new sessions start on is the
// one defaultLlmConnection names by slug; a workspace's own default goes
// over it, and a session keeps the connection it started on.
//
// A custom endpoint is a connection of providerType pi_compat:
//
//	{"slug":"magpie","name":"magpie","providerType":"pi_compat",
//	 "authType":"none","baseUrl":"http://127.0.0.1:3425/agent/craft-agents/v1",
//	 "customEndpoint":{"api":"openai-completions"},
//	 "models":[{"id":"<catalog id>","name":…},…],"defaultModel":"<catalog id>",
//	 "createdAt":<ms>}
//
// Keys live apart from config.json, sealed in credentials.enc, which magpie
// doesn't open. authType none is what Craft itself saves for an endpoint
// on this computer with no key (resolveCustomEndpointSetup: "Local Model"),
// and its Pi server then sends the placeholder key "not-needed"
// (resolveCustomEndpointApiKey), which the gateway takes from this
// computer. Craft's Pi sends no User-Agent of Craft's own either, so the
// address names it: the gateway reads /agent/craft-agents off it
// (gateway.AgentPath). Craft resets a defaultModel that isn't in models to
// the first one there (backfillAllConnectionModels), so the model picked
// is always in the list.
//
// magpie adds that connection and makes it the default; the default the
// user had is stashed and put back when magpie steps out, and magpie's
// connection goes then. The user's connections and every other key stay as
// written. Craft reads config.json afresh on every lookup and watches it
// (watcher.ts), so a running app takes the change at once, for its new
// sessions. Craft won't load a config.json without its workspaces
// (loadStoredConfig), so magpie never makes one: Craft is opened once
// first.

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

const (
	craftConnections = "llmConnections"
	craftDefault     = "defaultLlmConnection"
)

// craftProc is Craft Agents' process: the .app's and the .exe's Craft
// Agents, the AppImage's craft-agents.
var craftProc = []string{`(^|/)Craft Agents(\.exe)?( |$)`, `(^|/)craft-agents( |$)`}

var noticeCraft = newNotice("Craft Agents starts its new sessions on this; a session already open stays on the connection it started on.")

// craftDir is Craft Agents' config folder (paths.ts: CRAFT_CONFIG_DIR, else
// ~/.craft-agent).
func craftDir(at place) string {
	if d := at.getenv("CRAFT_CONFIG_DIR"); d != "" {
		return d
	}
	return at.join(".craft-agent")
}

// craftModel is a model of magpie's connection, as Craft's ModelDefinition
// spells one.
type craftModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ShortName     string `json:"shortName"`
	Description   string `json:"description"`
	Provider      string `json:"provider"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	Images        bool   `json:"supportsImages,omitempty"`
}

// craftModels are the models picked for Craft Agents, with ref among them
// where it isn't ("" for none).
func craftModels(ref string) []craftModel {
	ms := []craftModel{}
	seen := map[string]bool{}
	add := func(id, name string, ctx int, images bool) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		if name == "" {
			name = id
		}
		ms = append(ms, craftModel{ID: id, Name: name, ShortName: name, Provider: "pi", ContextWindow: ctx, Images: images})
	}
	for _, m := range sharedModels("craft-agents") {
		add(m.ID, m.Name, m.Context, m.Images)
	}
	add(ref, "", 0, false)
	return ms
}

func craftAgents(home string) *Agent { return craftAgentsAt(here(home)) }

// craftAgentsAt is Craft Agents with its home at at.home.
func craftAgentsAt(at place) *Agent {
	dir := craftDir(at)
	path := filepath.Join(dir, "config.json")
	// keyDefault is the defaultLlmConnection the user had, as JSON ("" where
	// there was none)
	keyDefault := "craft-agents:" + path + ":" + craftDefault
	base := func() string { return gateway.AgentURL(at.gw(), "craft-agents") + "/v1" }
	read := func() gjson.Result {
		raw, err := edit.Read(path)
		if err != nil {
			return gjson.Result{}
		}
		return gjson.ParseBytes(raw)
	}
	// ours is magpie's connection, its slug magpie's
	ours := func(c gjson.Result) gjson.Result {
		for _, x := range c.Get(craftConnections).Array() {
			if x.Get("slug").String() == magpieID && x.Get("providerType").String() == "pi_compat" {
				return x
			}
		}
		return gjson.Result{}
	}
	// theirs is the user's connection new sessions start on: the default,
	// else the first, as Craft's getDefaultLlmConnection picks one
	theirs := func(c gjson.Result) gjson.Result {
		var first gjson.Result
		def := c.Get(craftDefault).String()
		for _, x := range c.Get(craftConnections).Array() {
			s := x.Get("slug").String()
			if s == magpieID && x.Get("providerType").String() == "pi_compat" {
				continue
			}
			if s == def {
				return x
			}
			if !first.Exists() {
				first = x
			}
		}
		return first
	}
	// on: new sessions start on magpie's connection
	on := func(c gjson.Result) bool {
		return c.Get(craftDefault).String() == magpieID && ours(c).Exists()
	}
	modelOf := func(x gjson.Result) string {
		if m := x.Get("defaultModel").String(); m != "" {
			return m
		}
		m := x.Get("models.0")
		if m.IsObject() {
			return m.Get("id").String()
		}
		return m.String()
	}
	// usable is config.json as Craft loads one, or why magpie won't write it
	usable := func() (gjson.Result, error) {
		raw, err := edit.Read(path)
		if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
			return gjson.Result{}, errors.New("open Craft Agents once first: it has no " + path + " yet")
		}
		c := gjson.ParseBytes(raw)
		if !json.Valid(raw) || !c.IsObject() || !c.Get("workspaces").IsArray() {
			return gjson.Result{}, errors.New(path + " isn't a config Craft Agents loads, so magpie leaves it alone")
		}
		return c, nil
	}
	// conns are the connections as written, magpie's left out
	conns := func(c gjson.Result) []json.RawMessage {
		var list []json.RawMessage
		for _, x := range c.Get(craftConnections).Array() {
			if x.Get("slug").String() == magpieID && x.Get("providerType").String() == "pi_compat" {
				continue
			}
			list = append(list, json.RawMessage(x.Raw))
		}
		return list
	}
	// entry is magpie's connection with ref as its model, keeping the name
	// and other fields it was given in Craft
	entry := func(c gjson.Result, ref string) json.RawMessage {
		m := map[string]any{}
		if o := ours(c); o.Exists() {
			json.Unmarshal([]byte(o.Raw), &m)
		}
		if n, _ := m["name"].(string); n == "" {
			m["name"] = "magpie"
		}
		if _, ok := m["createdAt"].(float64); !ok {
			m["createdAt"] = time.Now().UnixMilli()
		}
		ce, _ := m["customEndpoint"].(map[string]any)
		if ce == nil {
			ce = map[string]any{}
		}
		ce["api"] = "openai-completions"
		m["customEndpoint"] = ce
		m["slug"] = magpieID
		m["providerType"] = "pi_compat"
		m["authType"] = "none"
		m["baseUrl"] = base()
		m["models"] = craftModels(ref)
		m["defaultModel"] = ref
		m["modelSelectionMode"] = "userDefined3Tier"
		delete(m, "piAuthProvider")
		b, _ := json.Marshal(m)
		return b
	}
	// wire writes magpie's connection with ref as its model, and the
	// default as def ("" leaves it as it is)
	wire := func(c gjson.Result, ref, def string) error {
		list := append(conns(c), entry(c, ref))
		kvs := []edit.KV{{Path: craftConnections, Value: list}}
		if def != "" {
			kvs = append(kvs, edit.KV{Path: craftDefault, Value: def})
		}
		return edit.SetJSON(path, kvs...)
	}
	// unwire takes magpie's connection out and puts back the default the
	// user had, where that connection is still there
	unwire := func(c gjson.Result) error {
		var was string
		if s := unstash(keyDefault); s != "" {
			json.Unmarshal([]byte(s), &was)
		}
		list := conns(c)
		kvs := []edit.KV{{Path: craftConnections, Value: list}}
		if len(list) == 0 {
			kvs[0].Value = []json.RawMessage{}
		}
		if err := edit.SetJSON(path, kvs...); err != nil {
			return err
		}
		if c.Get(craftDefault).String() != magpieID {
			return nil
		}
		for _, x := range list {
			if was != "" && gjson.GetBytes(x, "slug").String() == was {
				return edit.SetJSON(path, edit.KV{Path: craftDefault, Value: was})
			}
		}
		// none of theirs to go back to: Craft picks its first
		return edit.DelJSON(path, craftDefault)
	}
	return atomic(&Agent{
		ID: "craft-agents", Name: "Craft Agents", Icon: "craft-agents-color", Aliases: []string{"craft", "craftagents", "craft-agent"},
		Spelled: prefixed, NoNames: true,
		Dir: dir, Path: path,
		// its config folder once it has run, else its app
		detect: func() bool {
			return isDir(dir) || (&Agent{ID: "craft-agents"}).HasApp()
		},
		Notice: func() string {
			if on(read()) && Running(craftProc...) {
				return noticeCraft.String()
			}
			return ""
		},
		Check: func() string {
			c := read()
			if c.Get(craftDefault).String() != magpieID {
				return ""
			}
			o := ours(c)
			if !o.Exists() {
				return "Craft Agents' magpie connection (config.json) is gone, so it no longer reaches magpie"
			}
			return wiringOff("Craft Agents", path, func(k string) (string, bool) {
				r := o.Get(k)
				return r.String(), r.Exists()
			}, "baseUrl", base())
		},
		Sync: func() error {
			c := read()
			o := ours(c)
			if !o.Exists() {
				return nil
			}
			ref := o.Get("defaultModel").String()
			want := entry(c, ref)
			if sameJSON(o.Raw, json.RawMessage(want)) {
				return nil
			}
			return wire(c, ref, "")
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				c := read()
				if on(c) {
					return magpieID + "/" + ours(c).Get("defaultModel").String()
				}
				return modelOf(theirs(c))
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					c, err := usable()
					if err != nil {
						return err
					}
					if !on(c) {
						// what the user had, unless magpie stashed it already
						forget(keyDefault)
						was, _ := json.Marshal(c.Get(craftDefault).String())
						stash(map[string]string{keyDefault: string(was)})
					}
					return wire(c, ref, magpieID)
				}
				c := read()
				if ours(c).Exists() {
					if err := unwire(c); err != nil {
						return err
					}
					c = read()
				} else {
					forget(keyDefault)
				}
				if v == "" {
					return nil
				}
				t := theirs(c)
				if !t.Exists() {
					return errors.New("Craft Agents has no connection of its own to use " + v + " with: add one in its Settings")
				}
				if modelOf(t) == v {
					return nil
				}
				list := conns(c)
				slug := t.Get("slug").String()
				for i, x := range list {
					if gjson.GetBytes(x, "slug").String() == slug {
						var m map[string]any
						if json.Unmarshal(x, &m) != nil {
							return errors.New(path + ": its connection " + slug + " isn't JSON magpie reads")
						}
						m["defaultModel"] = v
						list[i], _ = json.Marshal(m)
						break
					}
				}
				return edit.SetJSON(path, edit.KV{Path: craftConnections, Value: list})
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c)})
				}
				return withVia(group("Craft Agents", own), "craft-agents", magpieID+"/")
			},
		}},
	}, path)
}
