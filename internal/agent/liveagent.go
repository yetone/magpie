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
//	 "providers":{…,
//	   "magpie":{"name":"magpie","type":"codex",
//	     "baseUrl":"http://127.0.0.1:3425/v1","api":"openai-completions",
//	     "apiKey":"magpie-liveagent","activeModels":null,
//	     "modelOrder":["<model>",…],"requestFormat":"openai-completions",
//	     "models":[{"id":"<model>","displayName":"…","input":["text","image"],
//	       "contextWindow":200000,"maxTokens":64000},…]},
//	   "magpie-responses":{…"type":"codex","api":"openai-responses"…},
//	   "magpie-anthropic":{…"type":"claude_code",
//	     "baseUrl":"http://127.0.0.1:3425","api":"anthropic-messages"…}}}
//
// magpie's models go on three providers by the API each is served on
// natively (liveAgentKinds). type codex with a requestFormat is what
// LiveAgent's settings write for an OpenAI-compatible provider, whose
// request format they let the user switch; claude_code with
// anthropic-messages an Anthropic-compatible one (providerSettings.ts).
// K-brain appends /v1 to an Anthropic baseUrl that doesn't end in a
// version and posts to /messages with X-Api-Key, so that one is the
// gateway's root; an OpenAI one gets only /chat/completions or /responses
// appended, so it is the gateway's /v1. activeModels null is every model
// active (Provider.ModelActive). K-brain decodes the file with unknown
// fields refused, so magpie writes only fields it knows. A file K-brain
// wouldn't read as providers (a top-level models, or one provider written
// bare at the top) is left alone.
//
// The backend makes the file as {} when there is none, reads it once, when
// it starts (storage.OpenLocal), and writes the whole of it back from what
// it holds whenever a setting changes (Config.SaveFile), so a
// change magpie makes while it runs is lost: magpie refuses one then,
// where it can tell (Running can't on Windows, where the notice says so).
// The model is picked in LiveAgent's model picker, so the field is whether
// magpie's models are in it (ListsModels). The user's name for each of
// magpie's providers, the request format they picked, the models they
// turned off, their order of them and the fields K-brain adds (metadata)
// stay through a sync, and a sync with nothing to change doesn't write.

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
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
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
		return liveAgentWire(path, liveAgentFor(gateway.URL()))
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

// liveAgentEntry is one of magpie's providers, in the order K-brain
// writes one.
type liveAgentEntry struct {
	Name          string           `json:"name"`
	Type          string           `json:"type"`
	BaseURL       string           `json:"baseUrl"`
	API           string           `json:"api"`
	APIKey        string           `json:"apiKey"`
	ActiveModels  []string         `json:"activeModels"`
	ModelOrder    []string         `json:"modelOrder,omitempty"`
	RequestFormat string           `json:"requestFormat,omitempty"`
	Models        []liveAgentModel `json:"models"`
}

// liveAgentKind is one of magpie's providers in config.json: its key, the
// API it speaks, LiveAgent's type for it and its name.
type liveAgentKind struct {
	key, api, typ, name string
}

// liveAgentKinds are magpie's providers, one per API a model is best asked
// on, as dsh's routes are (dshKinds). K-brain asks a provider's every model
// on the provider's one API, so with Anthropic Messages for all, a GPT
// served on Responses was translated from Messages each turn, and the
// format couldn't be changed: LiveAgent's settings lock an Anthropic
// provider's API and offer a request format only for an OpenAI one
// (ProviderModalView.tsx; Discord, xiaoxiaofei: magpie 默认导入的是 message
// 格式的，要可以自定义请求格式). A model goes on the provider of the API its
// provider serves it on natively (catalog.Model.APIs), every other one — a
// group, a Chat provider's — on "magpie", on Chat Completions. The two
// OpenAI ones are type codex with a requestFormat, which LiveAgent's
// settings let the user switch between Chat Completions and Responses, and
// a sync keeps the one they picked.
var liveAgentKinds = []liveAgentKind{
	{"magpie", "openai-completions", "codex", "magpie"},
	{"magpie-responses", "openai-responses", "codex", "magpie · Responses"},
	{"magpie-anthropic", "anthropic-messages", "claude_code", "magpie · Anthropic"},
}

// liveAgentOpenAI are the request formats LiveAgent offers an OpenAI
// provider (CODEX_REQUEST_FORMAT_LABELS; K-brain's settings_provider.go).
var liveAgentOpenAI = []string{"openai-completions", "openai-responses"}

// isLiveAgentKey says a provider key in config.json is one of magpie's.
func isLiveAgentKey(key string) bool {
	return slices.ContainsFunc(liveAgentKinds, func(k liveAgentKind) bool { return k.key == key })
}

// liveAgentKindOf is the provider of magpie's a model goes on.
func liveAgentKindOf(m catalog.Model) liveAgentKind {
	switch {
	case slices.Contains(m.APIs, string(provider.Responses)):
		return liveAgentKinds[1]
	case slices.Contains(m.APIs, string(provider.Anthropic)):
		return liveAgentKinds[2]
	}
	return liveAgentKinds[0]
}

// liveAgentBaseURL is the gateway as K-brain asks it on api: Messages at
// the root (K-brain appends /v1), OpenAI's under /v1 (it appends only
// /chat/completions or /responses).
func liveAgentBaseURL(api, gw string) string {
	if api == "anthropic-messages" {
		return gw
	}
	return gw + "/v1"
}

// liveAgentWant is what magpie writes for a gateway: the key that names
// LiveAgent, each provider of magpie's that has a model, every model in
// magpie's order, and the provider each one is on.
type liveAgentWant struct {
	Key     string
	Entries map[string]liveAgentEntry
	Order   []string
	On      map[string]string
}

// liveAgentFor is magpie's providers for a gateway at gw, each with the
// models picked for it that go on it, each once, sorted by id as K-brain
// writes them, and in magpie's order as modelOrder. With no model at all
// it is "magpie" alone, empty.
func liveAgentFor(gw string) liveAgentWant {
	w := liveAgentWant{Key: agentKeyAt("liveagent", gw), Entries: map[string]liveAgentEntry{}, On: map[string]string{}}
	entry := func(k liveAgentKind) liveAgentEntry {
		e, ok := w.Entries[k.key]
		if !ok {
			e = liveAgentEntry{Name: k.name, Type: k.typ, BaseURL: liveAgentBaseURL(k.api, gw), API: k.api,
				APIKey: w.Key, Models: []liveAgentModel{}}
			if k.typ == "codex" {
				e.RequestFormat = k.api
			}
		}
		return e
	}
	for _, m := range sharedModels("liveagent") {
		if strings.TrimSpace(m.ID) == "" || w.On[m.ID] != "" {
			continue
		}
		k := liveAgentKindOf(m)
		e := entry(k)
		lm := liveAgentModel{ID: m.ID, ContextWindow: max(m.Context, 0), MaxTokens: max(m.Output, 0)}
		if m.Name != "" && m.Name != m.ID {
			lm.DisplayName = m.Name
		}
		if m.Images {
			lm.Input = []string{"text", "image"}
		}
		e.Models = append(e.Models, lm)
		e.ModelOrder = append(e.ModelOrder, m.ID)
		w.Entries[k.key] = e
		w.On[m.ID] = k.key
		w.Order = append(w.Order, m.ID)
	}
	if len(w.Entries) == 0 {
		w.Entries[liveAgentKinds[0].key] = entry(liveAgentKinds[0])
	}
	for k, e := range w.Entries {
		sort.SliceStable(e.Models, func(i, j int) bool { return e.Models[i].ID < e.Models[j].ID })
		w.Entries[k] = e
	}
	return w
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

// liveAgentWired says config.json has a provider of magpie's.
func liveAgentWired(path string) bool {
	b, _, err := liveAgentRead(path)
	if err != nil {
		return false
	}
	for _, k := range liveAgentKinds {
		if gjson.GetBytes(b, "providers."+k.key).IsObject() {
			return true
		}
	}
	return false
}

// liveAgentPicks are config.json's picks of a model, each with the key of
// its provider.
var liveAgentPicks = [][2]string{{"defaultModel", "defaultProvider"}, {"compactModel", "compactProvider"}, {"taskModel", "taskProvider"}}

// liveAgentWire puts magpie's providers into config.json, the file made
// when there is none, and takes out one left with no model. One there
// already keeps its name, the fields K-brain adds, the request format the
// user picked, each model's other fields, the models the user turned off
// and their order — a model that moved from another provider of magpie's
// brings its own; a model new to magpie's list is turned on and goes last.
// A pick of magpie's model follows it to its provider. A default model
// still K-brain's own (model1 of its empty demo provider, which has no
// endpoint to call) becomes magpie's first.
func liveAgentWire(path string, want liveAgentWant) error {
	b, _, err := liveAgentRead(path)
	if err != nil {
		return err
	}
	olds := map[string]gjson.Result{}
	for _, k := range liveAgentKinds {
		if o := gjson.GetBytes(b, "providers."+k.key); o.IsObject() {
			olds[k.key] = o
		}
	}
	kept := liveAgentKeptLoad(path)
	pids := liveAgentPIDs()
	want, untouched := kept.take(olds, want, pids)
	prior := liveAgentPriorOf(olds)
	var set []edit.KV
	var del []string
	for _, k := range liveAgentKinds {
		w, ok := want.Entries[k.key]
		old, had := olds[k.key]
		if !ok {
			if had && !untouched[k.key] {
				del = append(del, "providers."+k.key)
			}
			continue
		}
		var o []byte
		if had {
			o = []byte(old.Raw)
		}
		entry, err := liveAgentMerge(o, w, prior)
		if err != nil {
			return err
		}
		if had && jsonEqual([]byte(old.Raw), entry) {
			continue
		}
		set = append(set, edit.KV{Path: "providers." + k.key, Value: json.RawMessage(entry)})
	}
	for _, pick := range liveAgentPicks {
		p := gjson.GetBytes(b, pick[1]).String()
		if on := want.On[gjson.GetBytes(b, pick[0]).String()]; isLiveAgentKey(p) && on != "" && on != p {
			set = append(set, edit.KV{Path: pick[1], Value: on})
		}
	}
	if len(want.Order) > 0 {
		first, on := want.Order[0], want.On[want.Order[0]]
		if liveAgentDefaultUnset(b, "defaultModel", "defaultProvider") {
			set = append(set, edit.KV{Path: "defaultModel", Value: first}, edit.KV{Path: "defaultProvider", Value: on})
		}
		if b != nil && liveAgentDefaultUnset(b, "compactModel", "compactProvider") && gjson.GetBytes(b, "compactModel").Exists() {
			set = append(set, edit.KV{Path: "compactModel", Value: first}, edit.KV{Path: "compactProvider", Value: on})
		}
	}
	if len(set) == 0 && len(del) == 0 {
		kept.wrote(olds, want, pids)
		liveAgentKeptSave(path, kept)
		return nil
	}
	if len(del) == 0 {
		err = edit.SetJSON(path, set...)
	} else {
		var raw, out []byte
		if raw, err = os.ReadFile(path); err == nil {
			if out, err = edit.PatchJSON(raw, set, del); err == nil {
				err = edit.WriteAtomic(path, out)
			}
		}
	}
	if err == nil {
		kept.wrote(olds, want, pids)
		liveAgentKeptSave(path, kept)
	}
	return err
}

// LiveAgent's settings edit magpie's providers as any other: a model
// deleted there, or a whole provider of magpie's, is the user's choice,
// and a sync that wrote every model of magpie's back undid it each time
// (zero2sin on Discord: 每次重启 LiveAgent 配置都被重置). So magpie
// remembers, per config.json, which of its models and providers LiveAgent
// has had, and one of them gone from the file since is taken out by the
// user: it stays out until it is in the file again (put back in LiveAgent)
// or magpie is turned off and on for LiveAgent. A model new to magpie's
// list is still added.
//
// LiveAgent's backend writes back what it holds when a setting changes, so
// a model magpie added while it ran, which it never read, is gone after its
// next save without the user taking it out. One added while LiveAgent runs
// is held as pending, with the processes that ran then, and written again
// while it is missing under them. Once other processes run (LiveAgent
// started again and read the file) it counts as had; once none do, as had
// if it is still there, else it is written again. A LiveAgent that saved
// over one and was started again before the next sync reads as the user
// taking it out. Windows can't be asked what runs, so there each one
// counts as had at once.

// liveAgentKept is what magpie remembers of a config.json: items are
// "m:<model id>" and "k:<provider key>".
type liveAgentKept struct {
	// in the file, as LiveAgent has read it
	Had []string `json:"had,omitempty"`
	// written while LiveAgent ran: the processes then
	Pending map[string]string `json:"pending,omitempty"`
	// taken out in LiveAgent
	Gone []string `json:"gone,omitempty"`
}

func liveAgentKeptKey(path string) string { return "liveagent:" + path + ":kept" }

func liveAgentKeptLoad(path string) liveAgentKept {
	var k liveAgentKept
	if v := stashShared()[liveAgentKeptKey(path)]; v != "" {
		json.Unmarshal([]byte(v), &k)
	}
	return k
}

func liveAgentKeptSave(path string, k liveAgentKept) {
	v := ""
	if len(k.Had) > 0 || len(k.Pending) > 0 || len(k.Gone) > 0 {
		b, _ := json.Marshal(k)
		v = string(b)
	}
	if v != stashShared()[liveAgentKeptKey(path)] {
		stash(map[string]string{liveAgentKeptKey(path): v})
	}
}

// liveAgentPIDs is the ids of LiveAgent's processes, "" for none, or where
// magpie can't tell (Windows). A var so tests can say what runs.
var liveAgentPIDs = func() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	var ids []string
	for _, pat := range liveAgentProc {
		out, _ := proc.Command("pgrep", "-f", pat).Output()
		for _, f := range strings.Fields(string(out)) {
			if !slices.Contains(ids, f) {
				ids = append(ids, f)
			}
		}
	}
	slices.Sort(ids)
	return strings.Join(ids, ",")
}

// liveAgentHere is the items of magpie's providers in config.json now.
func liveAgentHere(olds map[string]gjson.Result) map[string]bool {
	here := map[string]bool{}
	for key, o := range olds {
		here["k:"+key] = true
		for _, m := range o.Get("models").Array() {
			if id := m.Get("id").String(); id != "" {
				here["m:"+id] = true
			}
		}
	}
	return here
}

// take is want without what the user took out in LiveAgent, with k brought
// up to the file (olds, magpie's providers in it) and LiveAgent's processes
// now (pids). untouched are magpie's providers left in the file with
// nothing magpie would put in them: kept as the user has them.
func (k *liveAgentKept) take(olds map[string]gjson.Result, want liveAgentWant, pids string) (liveAgentWant, map[string]bool) {
	here := liveAgentHere(olds)
	// a LiveAgent started since read what magpie added; one that quit
	// read it only if it is still there (its save drops one it never
	// read)
	for item, was := range k.Pending {
		if was == pids {
			continue
		}
		delete(k.Pending, item)
		if (pids != "" || here[item]) && !slices.Contains(k.Had, item) {
			k.Had = append(k.Had, item)
		}
	}
	// with none of magpie's providers left nothing is judged: the sync
	// writes nothing then, and on starts over
	if len(olds) > 0 {
		for _, item := range k.Had {
			if !here[item] && !slices.Contains(k.Gone, item) {
				k.Gone = append(k.Gone, item)
			}
		}
	}
	k.Gone = slices.DeleteFunc(k.Gone, func(item string) bool { return here[item] })
	if len(k.Gone) == 0 {
		return want, nil
	}
	out := liveAgentWant{Key: want.Key, Entries: map[string]liveAgentEntry{}, On: map[string]string{}}
	untouched := map[string]bool{}
	for key, e := range want.Entries {
		if slices.Contains(k.Gone, "k:"+key) {
			continue
		}
		had := len(e.Models) > 0
		e.Models = slices.DeleteFunc(slices.Clone(e.Models), func(m liveAgentModel) bool { return slices.Contains(k.Gone, "m:"+m.ID) })
		e.ModelOrder = slices.DeleteFunc(slices.Clone(e.ModelOrder), func(id string) bool { return slices.Contains(k.Gone, "m:"+id) })
		if had && len(e.Models) == 0 {
			untouched[key] = true
			continue
		}
		out.Entries[key] = e
	}
	for _, id := range want.Order {
		if on := want.On[id]; on != "" {
			if _, ok := out.Entries[on]; ok && !slices.Contains(k.Gone, "m:"+id) {
				out.Order = append(out.Order, id)
				out.On[id] = on
			}
		}
	}
	return out, untouched
}

// wrote brings k up to what the sync left in the file: what was there
// before (olds) LiveAgent has had, what magpie added now it has had too
// unless LiveAgent ran (pids), and what left magpie's list is forgotten,
// except what the user took out.
func (k *liveAgentKept) wrote(olds map[string]gjson.Result, want liveAgentWant, pids string) {
	here := liveAgentHere(olds)
	now := map[string]bool{}
	for key, e := range want.Entries {
		now["k:"+key] = true
		for _, m := range e.Models {
			now["m:"+m.ID] = true
		}
	}
	for key := range olds {
		// a provider kept as the user has it, its models too
		if _, ok := want.Entries[key]; !ok {
			for item := range liveAgentHere(map[string]gjson.Result{key: olds[key]}) {
				now[item] = true
			}
		}
	}
	k.Had = slices.DeleteFunc(k.Had, func(item string) bool { return !now[item] })
	for item := range k.Pending {
		if !now[item] {
			delete(k.Pending, item)
		}
	}
	items := make([]string, 0, len(now))
	for item := range now {
		items = append(items, item)
	}
	sort.Strings(items)
	for _, item := range items {
		if slices.Contains(k.Had, item) || k.Pending[item] != "" {
			continue
		}
		if here[item] || pids == "" {
			k.Had = append(k.Had, item)
			continue
		}
		if k.Pending == nil {
			k.Pending = map[string]string{}
		}
		k.Pending[item] = pids
	}
	sort.Strings(k.Had)
}

// liveAgentPrior is what config.json's providers of magpie's hold of each
// model: its record, and whether it is turned off in its provider.
type liveAgentPrior struct {
	rec map[string][]byte
	off map[string]bool
}

func liveAgentPriorOf(olds map[string]gjson.Result) liveAgentPrior {
	p := liveAgentPrior{rec: map[string][]byte{}, off: map[string]bool{}}
	for _, k := range liveAgentKinds {
		old, ok := olds[k.key]
		if !ok {
			continue
		}
		a := old.Get("activeModels")
		for _, m := range old.Get("models").Array() {
			id := m.Get("id").String()
			if id == "" {
				continue
			}
			if _, seen := p.rec[id]; !seen {
				p.rec[id] = []byte(m.Raw)
			}
			if a.IsArray() && !slices.ContainsFunc(a.Array(), func(x gjson.Result) bool { return x.String() == id }) {
				p.off[id] = true
			}
		}
	}
	return p
}

// liveAgentMerge is magpie's entry want laid over the one config.json has
// under its key, old (nil for none), with prior the models of magpie's
// other providers.
func liveAgentMerge(old []byte, want liveAgentEntry, prior liveAgentPrior) ([]byte, error) {
	fresh := old == nil
	if fresh {
		var err error
		if old, err = json.Marshal(want); err != nil {
			return nil, err
		}
	}
	own := map[string][]byte{}
	for _, m := range gjson.GetBytes(old, "models").Array() {
		if id := m.Get("id").String(); id != "" && !fresh {
			own[id] = []byte(m.Raw)
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
		o, had := own[m.ID]
		if !had {
			o, had = prior.rec[m.ID]
		}
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
	// an OpenAI provider keeps the request format the user picked
	api := want.API
	if want.Type == "codex" && !fresh && gjson.GetBytes(old, "type").String() == "codex" {
		cur := gjson.GetBytes(old, "requestFormat").String()
		if cur == "" {
			cur = gjson.GetBytes(old, "api").String()
		}
		if slices.Contains(liveAgentOpenAI, cur) {
			api = cur
		}
	}
	set := []jsonKV{
		{"type", rawJSON(want.Type)}, {"baseUrl", rawJSON(want.BaseURL)}, {"api", rawJSON(api)},
		{"apiKey", rawJSON(want.APIKey)},
	}
	if want.Type == "codex" {
		set = append(set, jsonKV{"requestFormat", rawJSON(api)})
	}
	set = append(set, jsonKV{"models", joinRaw(models)})
	// the models the user turned off stay off, a new one is on
	if a := gjson.GetBytes(old, "activeModels"); a.IsArray() || slices.ContainsFunc(want.Models, func(m liveAgentModel) bool { return prior.off[m.ID] }) {
		var active []string
		for _, id := range a.Array() {
			if ids[id.String()] && !prior.off[id.String()] && !slices.Contains(active, id.String()) {
				active = append(active, id.String())
			}
		}
		for _, m := range want.Models {
			if !prior.off[m.ID] && !slices.Contains(active, m.ID) {
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
	if o := gjson.GetBytes(old, "modelOrder"); o.IsArray() && !fresh {
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

// liveAgentUnwire takes magpie's providers out of config.json and every
// pick of it back to K-brain's default; the file goes when only what
// magpie made is left. A file left with no provider gets K-brain's own
// demo one back, since K-brain reads one with none as written over and
// restores its config.json.bak, which can still hold magpie's.
func liveAgentUnwire(path string) error {
	b, root, err := liveAgentRead(path)
	if err != nil {
		return err
	}
	var magpieOnly = map[string]bool{}
	var del []string
	for _, k := range liveAgentKinds {
		key := "providers." + k.key
		if !gjson.GetBytes(b, key).Exists() {
			continue
		}
		del = append(del, key)
		for _, m := range gjson.GetBytes(b, key+".models").Array() {
			magpieOnly[m.Get("id").String()] = true
		}
	}
	liveAgentKeptSave(path, liveAgentKept{})
	if del == nil {
		return nil
	}
	left := 0
	// K-brain's demo provider still there, with its model1
	demo := gjson.GetBytes(b, "providers."+liveAgentDemo+".models.#(id=="+liveAgentDemoModel+")").Exists()
	gjson.GetBytes(b, "providers").ForEach(func(k, v gjson.Result) bool {
		if isLiveAgentKey(k.String()) {
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
	var set []edit.KV
	for _, pick := range liveAgentPicks {
		m, p := gjson.GetBytes(b, pick[0]).String(), gjson.GetBytes(b, pick[1]).String()
		if !isLiveAgentKey(p) && (p != "" || !magpieOnly[m]) {
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
