package agent

// LiveAgent (Stack-Cairn/LiveAgent, a Tauri app; xiaoxiaofei. on Discord)
// is a desktop agent whose v2 runs its model calls through K-brain, a Go
// backend it starts as `k-brain backend -data-dir <root>`. K-brain keeps
// every provider in one config.json in that root (LiveAgent's
// kbrain_backend.rs ensure_started, data_home.rs; K-brain v0.107.12's
// internal/config): $LIVEAGENT_HOME, else $K_BRAIN_HOME, else the folder
// ~/.liveagent/data-dir.json points to ({"path": <absolute folder>}, set
// when the user moves LiveAgent's data in its settings), else ~/.liveagent,
// on every system. The file is JSONC, Pi's provider shape:
//
//	// k-brain configuration — JSONC: comments and trailing commas are allowed.
//	{"defaultModel":"…", "defaultProvider":"…", …,
//	 "providers":{…, "magpie":{"name":"magpie","type":"claude_code",
//	   "baseUrl":"http://127.0.0.1:3425","api":"anthropic-messages",
//	   "apiKey":"magpie-liveagent","activeModels":null,
//	   "modelOrder":["<model>",…],
//	   "models":[{"id":"<model>","displayName":"…","input":["text","image"],
//	     "contextWindow":200000,"maxTokens":64000},…]}}}
//
// type claude_code with api anthropic-messages is what LiveAgent's own
// settings write for an Anthropic-compatible provider
// (providerSettings.ts); K-brain appends /v1 to a baseUrl that doesn't end
// in a version and posts to /messages with X-Api-Key, so baseUrl is the
// gateway's root. activeModels null is every model active
// (Provider.ModelActive). K-brain decodes the file with unknown fields
// refused, so magpie writes only fields it knows. A file K-brain wouldn't
// read as providers (a top-level models, or one provider written bare at
// the top) is left alone.
//
// The backend makes the file as {} when there is none, reads it once, when
// it starts (storage.OpenLocal), and writes the whole of it back from what
// it holds whenever a setting changes (Config.SaveFile), so a
// change magpie makes while it runs is lost: magpie refuses one then,
// where it can tell (Running can't on Windows, where the notice says so).
// The model is picked in LiveAgent's model picker, so the field is whether
// magpie's models are in it (ListsModels). The user's name for magpie's
// entry, the models they turned off in it, their order of them and the
// fields K-brain adds (metadata) stay through a sync, and a sync with
// nothing to change doesn't write.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// liveAgentApp is LiveAgent's macOS bundle (tauri.conf.json's identifier).
var liveAgentApp = desktopApp{id: "com.xiaofei.liveagent", names: []string{"LiveAgent.app"}}

// liveAgentProc is LiveAgent's processes: the app (CFBundleExecutable
// liveagent, the Linux and Windows binary's name too) and the k-brain
// backend it starts, which holds the settings. The program itself, the
// first word of its command line, or an executable in the .app wherever
// the bundle is.
var liveAgentProc = []string{`^([^ ]*/)?(liveagent|k-brain)( |$)`, `LiveAgent\.app/Contents/MacOS/(liveagent|k-brain)( |$)`}

var noticeLiveAgent = newNotice("LiveAgent keeps its settings in memory while it runs and writes them all back when one changes — quit LiveAgent before changing this, then open it and pick a magpie model in its model picker.")

// errLiveAgentRunning is a change refused while LiveAgent runs.
var errLiveAgentRunning = errors.New("LiveAgent writes its settings back over what magpie writes now: quit LiveAgent, then try again")

// liveAgentDemo is the provider K-brain's CLI writes in a config it makes
// (config.Default), with no endpoint, and its models.
const liveAgentDemo = "demo"

const liveAgentDemoModel = "model1"

func liveAgent(home string) *Agent {
	dir := liveAgentDir(home)
	path := filepath.Join(dir, "config.json")
	write := func(on bool) error {
		if !on {
			return liveAgentUnwire(path)
		}
		return liveAgentWire(path, liveAgentEntryFor(gateway.URL()))
	}
	return atomic(&Agent{
		ID: "liveagent", Name: "LiveAgent", Icon: "liveagent-color", Aliases: []string{"live-agent", "k-brain", "kbrain"},
		Dir: dir, Path: path,
		detect: func() bool {
			return isDir(dir) || isDir(filepath.Join(home, ".liveagent")) || macBundle(liveAgentApp) || liveAgentWindowsApp()
		},
		Notice: func() string {
			if liveAgentWired(path) && Running(liveAgentProc...) {
				return noticeLiveAgent.String()
			}
			return ""
		},
		// a sync while it runs is written as well: its next save puts back
		// what it holds, and the next sync magpie's models
		Sync: func() error {
			if !liveAgentWired(path) {
				return nil
			}
			return write(true)
		},
		ListsModels: true,
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if liveAgentWired(path) {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				if liveAgentRunning() {
					return errLiveAgentRunning
				}
				return write(v != "")
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in LiveAgent's model picker"}}
			},
		}},
	}, path)
}

// liveAgentRunning says LiveAgent is open where magpie can tell; on
// Windows, where Running can't, no, and the notice asks to quit it first.
var liveAgentRunning = func() bool {
	return runtime.GOOS != "windows" && Running(liveAgentProc...)
}

// liveAgentDir is the folder of K-brain's config.json as LiveAgent hands it
// over: $LIVEAGENT_HOME, else $K_BRAIN_HOME (each only an absolute path,
// here), else the folder ~/.liveagent/data-dir.json points to, else
// ~/.liveagent.
func liveAgentDir(home string) string {
	for _, v := range []string{"LIVEAGENT_HOME", "K_BRAIN_HOME"} {
		if d := strings.TrimSpace(appdir.Getenv(v)); d != "" && filepath.IsAbs(d) {
			return d
		}
	}
	def := filepath.Join(home, ".liveagent")
	if b, err := os.ReadFile(filepath.Join(def, "data-dir.json")); err == nil {
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(b, &p) == nil && filepath.IsAbs(p.Path) && filepath.Clean(p.Path) != filepath.Clean(def) {
			return p.Path
		}
	}
	return def
}

// liveAgentWindowsApp says LiveAgent's installer has put it where it
// installs it: per user under %LOCALAPPDATA%\LiveAgent, or for everyone
// under %ProgramFiles%\LiveAgent.
func liveAgentWindowsApp() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	for _, v := range []string{"LOCALAPPDATA", "ProgramW6432", "ProgramFiles"} {
		if d := appdir.Getenv(v); d != "" && isFile(filepath.Join(d, "LiveAgent", "liveagent.exe")) {
			return true
		}
	}
	return false
}

// liveAgentModel is one of magpie's models as K-brain writes one (PiModel).
type liveAgentModel struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"displayName,omitempty"`
	Input         []string `json:"input,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	MaxTokens     int      `json:"maxTokens,omitempty"`
}

// liveAgentEntry is magpie's provider, in the order K-brain writes one.
type liveAgentEntry struct {
	Name         string           `json:"name"`
	Type         string           `json:"type"`
	BaseURL      string           `json:"baseUrl"`
	API          string           `json:"api"`
	APIKey       string           `json:"apiKey"`
	ActiveModels []string         `json:"activeModels"`
	ModelOrder   []string         `json:"modelOrder,omitempty"`
	Models       []liveAgentModel `json:"models"`
}

// liveAgentEntryFor is magpie's provider for a gateway at gw: its
// Anthropic Messages at the root, the key that names LiveAgent, and the
// models picked for it, each once, sorted by id as K-brain writes them,
// and in magpie's order as modelOrder.
func liveAgentEntryFor(gw string) liveAgentEntry {
	e := liveAgentEntry{Name: "magpie", Type: "claude_code", BaseURL: gw, API: "anthropic-messages",
		APIKey: agentKeyAt("liveagent", gw), Models: []liveAgentModel{}}
	seen := map[string]bool{}
	for _, m := range sharedModels("liveagent") {
		if strings.TrimSpace(m.ID) == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		lm := liveAgentModel{ID: m.ID, ContextWindow: max(m.Context, 0), MaxTokens: max(m.Output, 0)}
		if m.Name != "" && m.Name != m.ID {
			lm.DisplayName = m.Name
		}
		if m.Images {
			lm.Input = []string{"text", "image"}
		}
		e.Models = append(e.Models, lm)
		e.ModelOrder = append(e.ModelOrder, m.ID)
	}
	sort.SliceStable(e.Models, func(i, j int) bool { return e.Models[i].ID < e.Models[j].ID })
	return e
}

// liveAgentRead is config.json without its comments and trailing commas,
// the object it holds, and an error for one K-brain wouldn't read as its
// providers: not an object, providers not an object, a top-level models,
// or one provider written bare. A file that isn't there, or holds nothing,
// is nil.
func liveAgentRead(path string) ([]byte, map[string]json.RawMessage, error) {
	b, err := edit.Read(path)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil, nil
	}
	b = jsonc.ToJSON(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
	var root map[string]json.RawMessage
	if !json.Valid(b) || json.Unmarshal(b, &root) != nil || root == nil {
		return nil, nil, fmt.Errorf("%s isn't a JSON object K-brain reads, so magpie leaves it alone", path)
	}
	if _, ok := root["models"]; ok {
		return nil, nil, fmt.Errorf("%s has a top-level models, which K-brain refuses, so magpie leaves it alone", path)
	}
	p, ok := root["providers"]
	if !ok {
		for _, k := range []string{"name", "api", "baseUrl", "apiKey"} {
			if _, bare := root[k]; bare {
				return nil, nil, fmt.Errorf("%s is one provider written bare, so magpie leaves it alone", path)
			}
		}
	} else if r := gjson.ParseBytes(p); r.Type != gjson.Null && !r.IsObject() {
		return nil, nil, fmt.Errorf("%s: providers is not an object, so magpie leaves it alone", path)
	}
	return b, root, nil
}

// liveAgentWired says config.json has magpie's provider.
func liveAgentWired(path string) bool {
	b, _, err := liveAgentRead(path)
	return err == nil && gjson.GetBytes(b, "providers.magpie").IsObject()
}

// liveAgentWire puts magpie's provider into config.json, the file made
// when there is none. One there already keeps its name, the fields K-brain
// adds, each model's other fields, the models the user turned off and
// their order; a model new to magpie's list is turned on and goes last.
// A default model still K-brain's own (model1 of its empty demo provider,
// which has no endpoint to call) becomes magpie's first.
func liveAgentWire(path string, want liveAgentEntry) error {
	b, _, err := liveAgentRead(path)
	if err != nil {
		return err
	}
	old := gjson.GetBytes(b, "providers.magpie")
	var entry []byte
	if !old.IsObject() {
		entry, err = json.Marshal(want)
		if err != nil {
			return err
		}
	} else {
		entry, err = liveAgentMerge([]byte(old.Raw), want)
		if err != nil {
			return err
		}
		if jsonEqual([]byte(old.Raw), entry) {
			entry = nil
		}
	}
	var set []edit.KV
	if entry != nil {
		set = append(set, edit.KV{Path: "providers.magpie", Value: json.RawMessage(entry)})
	}
	if len(want.ModelOrder) > 0 && liveAgentDefaultUnset(b, "defaultModel", "defaultProvider") {
		set = append(set, edit.KV{Path: "defaultModel", Value: want.ModelOrder[0]}, edit.KV{Path: "defaultProvider", Value: "magpie"})
	}
	if len(want.ModelOrder) > 0 && b != nil && liveAgentDefaultUnset(b, "compactModel", "compactProvider") &&
		gjson.GetBytes(b, "compactModel").Exists() {
		set = append(set, edit.KV{Path: "compactModel", Value: want.ModelOrder[0]}, edit.KV{Path: "compactProvider", Value: "magpie"})
	}
	if len(set) == 0 {
		return nil
	}
	return edit.SetJSON(path, set...)
}

// liveAgentMerge is magpie's entry want laid over the one config.json has.
func liveAgentMerge(old []byte, want liveAgentEntry) ([]byte, error) {
	oldModels := map[string][]byte{}
	var known []string
	for _, m := range gjson.GetBytes(old, "models").Array() {
		if id := m.Get("id").String(); id != "" {
			oldModels[id] = []byte(m.Raw)
			known = append(known, id)
		}
	}
	ids := map[string]bool{}
	var models []json.RawMessage
	for _, m := range want.Models {
		ids[m.ID] = true
		w, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		o, had := oldModels[m.ID]
		if !had {
			models = append(models, w)
			continue
		}
		var set []jsonKV
		gjson.ParseBytes(w).ForEach(func(k, v gjson.Result) bool {
			// a name the user gave it in LiveAgent stays
			if k.String() == "displayName" && gjson.GetBytes(o, "displayName").Exists() {
				return true
			}
			set = append(set, jsonKV{k.String(), []byte(v.Raw)})
			return true
		})
		models = append(models, jsonSetKeys(o, set))
	}
	if models == nil {
		models = []json.RawMessage{}
	}
	set := []jsonKV{
		{"type", rawJSON(want.Type)}, {"baseUrl", rawJSON(want.BaseURL)}, {"api", rawJSON(want.API)},
		{"apiKey", rawJSON(want.APIKey)}, {"models", joinRaw(models)},
	}
	// the models the user turned off stay off, a new one is on
	if a := gjson.GetBytes(old, "activeModels"); a.IsArray() {
		var active []string
		for _, id := range a.Array() {
			if ids[id.String()] {
				active = append(active, id.String())
			}
		}
		for _, m := range want.Models {
			if !slices.Contains(known, m.ID) && !slices.Contains(active, m.ID) {
				active = append(active, m.ID)
			}
		}
		if active == nil {
			active = []string{}
		}
		set = append(set, jsonKV{"activeModels", rawJSON(active)})
	} else {
		set = append(set, jsonKV{"activeModels", []byte("null")})
	}
	// the user's order stays, a new model goes last
	order := want.ModelOrder
	if o := gjson.GetBytes(old, "modelOrder"); o.IsArray() {
		var kept []string
		for _, id := range o.Array() {
			if ids[id.String()] && !slices.Contains(kept, id.String()) {
				kept = append(kept, id.String())
			}
		}
		for _, id := range want.ModelOrder {
			if !slices.Contains(kept, id) {
				kept = append(kept, id)
			}
		}
		order = kept
	}
	if len(order) > 0 {
		set = append(set, jsonKV{"modelOrder", rawJSON(order)})
	}
	if !gjson.GetBytes(old, "name").Exists() {
		set = append(set, jsonKV{"name", rawJSON(want.Name)})
	}
	return jsonSetKeys(old, set), nil
}

// liveAgentDefaultUnset says the pick at the keys model and prov is none,
// or still K-brain's own: model1 of its demo provider, which has no
// endpoint, with no other provider listing a model1.
func liveAgentDefaultUnset(b []byte, model, prov string) bool {
	if b == nil {
		return true
	}
	m, p := gjson.GetBytes(b, model).String(), gjson.GetBytes(b, prov).String()
	if p != "" && p != liveAgentDemo {
		return false
	}
	if m == "" {
		return p == ""
	}
	if m != liveAgentDemoModel {
		return false
	}
	ok := true
	gjson.GetBytes(b, "providers").ForEach(func(k, v gjson.Result) bool {
		if k.String() == liveAgentDemo {
			if strings.TrimSpace(v.Get("baseUrl").String()) != "" {
				ok = false
			}
			return true
		}
		for _, x := range v.Get("models").Array() {
			if x.Get("id").String() == liveAgentDemoModel {
				ok = false
			}
		}
		return true
	})
	return ok
}

// liveAgentUnwire takes magpie's provider out of config.json and every
// pick of it back to K-brain's default; the file goes when only what
// magpie made is left. A file left with no provider gets K-brain's own
// demo one back, since K-brain reads one with none as written over and
// restores its config.json.bak, which can still hold magpie's.
func liveAgentUnwire(path string) error {
	b, root, err := liveAgentRead(path)
	if err != nil || !gjson.GetBytes(b, "providers.magpie").Exists() {
		return err
	}
	var magpieOnly = map[string]bool{}
	for _, m := range gjson.GetBytes(b, "providers.magpie.models").Array() {
		magpieOnly[m.Get("id").String()] = true
	}
	left := 0
	// K-brain's demo provider still there, with its model1
	demo := gjson.GetBytes(b, "providers."+liveAgentDemo+".models.#(id=="+liveAgentDemoModel+")").Exists()
	gjson.GetBytes(b, "providers").ForEach(func(k, v gjson.Result) bool {
		if k.String() == "magpie" {
			return true
		}
		left++
		for _, m := range v.Get("models").Array() {
			delete(magpieOnly, m.Get("id").String())
		}
		return true
	})
	// the file magpie made: its provider and its picks
	rest := 0
	for k := range root {
		switch k {
		case "providers", "defaultModel", "defaultProvider":
		default:
			rest++
		}
	}
	if left == 0 && rest == 0 {
		return os.Remove(path)
	}
	del := []string{"providers.magpie"}
	var set []edit.KV
	for _, pick := range [][2]string{{"defaultModel", "defaultProvider"}, {"compactModel", "compactProvider"}, {"taskModel", "taskProvider"}} {
		m, p := gjson.GetBytes(b, pick[0]).String(), gjson.GetBytes(b, pick[1]).String()
		if p != "magpie" && (p != "" || !magpieOnly[m]) {
			continue
		}
		if p != "" {
			del = append(del, pick[1])
		}
		// K-brain's own default and compact model are its demo's model1
		switch {
		case pick[0] != "taskModel" && (left == 0 || demo):
			set = append(set, edit.KV{Path: pick[0], Value: liveAgentDemoModel})
		case pick[0] == "defaultModel":
			// K-brain picks the first model of its providers
			set = append(set, edit.KV{Path: pick[0], Value: ""})
		default:
			del = append(del, pick[0])
		}
	}
	if left == 0 {
		set = append(set, edit.KV{Path: "providers." + liveAgentDemo, Value: json.RawMessage(liveAgentDemoProvider)})
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := edit.PatchJSON(raw, set, del)
	if err != nil {
		return err
	}
	return edit.WriteAtomic(path, out)
}

// liveAgentDemoProvider is K-brain's demo provider as its Save writes
// config.Default's.
const liveAgentDemoProvider = `{"name":"demo","baseUrl":"","api":"openai-completions","apiKey":"","activeModels":null,"models":[{"id":"model1","contextWindow":128000,"maxTokens":8192},{"id":"model2","contextWindow":128000,"maxTokens":8192}]}`

func rawJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// jsonEqual says two JSON texts hold the same value.
func jsonEqual(a, b []byte) bool {
	var y any
	return json.Unmarshal(b, &y) == nil && sameJSON(string(a), y)
}
