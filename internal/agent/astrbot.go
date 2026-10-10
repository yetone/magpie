package agent

// AstrBot (AstrBotDevs/AstrBot, a chatbot framework for QQ, Telegram,
// Discord, Lark and the rest) keeps every setting in one file,
// <root>/data/cmd_config.json, where the root is ASTRBOT_ROOT, else ~/.astrbot
// for the desktop app (AstrBot-desktop's runtime_paths.rs, and
// astrbot_path.get_astrbot_root for a packaged desktop runtime), else the
// folder it was started in: `astrbot init` marks one with a .astrbot file,
// and `astrbot run` there sets ASTRBOT_ROOT to it (cli/commands/cmd_run.py).
//
// Since 4.10 the file splits a chat provider into two lists (default.py,
// the dashboard's buildModelProviderConfig):
//
//	"provider_sources": [{"id":…,"provider":"openai",
//	  "type":"openai_chat_completion","provider_type":"chat_completion",
//	  "enable":true,"key":["…"],"api_base":"…/v1","timeout":120,
//	  "proxy":"","custom_headers":{}}],
//	"provider": [{"id":"<source>/<model>","enable":true,
//	  "provider_source_id":"<source>","model":"…",
//	  "modalities":["text","image","tool_use"],"custom_extra_body":{},
//	  "max_context_tokens":0}]
//
// — a source is an endpoint and its keys, each entry of provider one model
// of a source, and what AstrBot runs is the two merged, the model's keys
// winning (ProviderManager.get_merged_provider_config). The types are
// config_service.validate_config's, from default.py's schema: key a list of
// strings, timeout and max_context_tokens ints (0: AstrBot looks the window
// up itself), custom_headers and custom_extra_body objects, modalities a
// list of text/image/audio/tool_use.
//
// magpie adds a source "magpie", OpenAI Compatible at the gateway's /v1 with
// a key naming AstrBot (its requests carry no User-Agent of its own), and an
// entry "magpie/<provider>/<model>" for every model of magpie's, and makes
// the one picked AstrBot's default chat model: agent_runner.config.model.
// provider_id, which AstrBot reads when agent_runner.runner_type is "local"
// (4.28's ProviderManager._resolve_using_provider), or
// provider_settings.default_provider_id in a file from before agent_runner,
// which AstrBot's migration (migra_helper.py) carries over. The user's own
// sources and models are left where they are, byte for byte, and so is
// what they changed in magpie's own entries in AstrBot's WebUI (a source's
// timeout or proxy, a model switched off, its extra body). A model picked
// for one chat with /provider, which AstrBot keeps in its own database, is
// the user's and stands. The file is copied to cmd_config.json.before-magpie
// before magpie first writes it.
//
// AstrBot reads the file once, at start, and keeps it in memory: its WebUI
// writes the whole of it back on every save. So magpie's change needs
// AstrBot restarted, and a WebUI save made before that restart writes over
// it (Check then says so). AstrBot writes the file with a UTF-8 BOM
// (astrbot_config.py, encoding utf-8-sig), which magpie keeps.
//
// AstrBot in Docker is not one magpie wires: its data folder is a volume
// magpie can't know, and the gateway on this machine's loopback is not the
// container's.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

const (
	astrbotSources = "provider_sources"
	astrbotModels  = "provider"
	// astrbotRunner and astrbotLegacy are where AstrBot's default chat model
	// is named, from 4.28 and before it
	astrbotRunner = "agent_runner.config.model.provider_id"
	astrbotLegacy = "provider_settings.default_provider_id"
)

// astrbotBOM is the byte order mark AstrBot writes its config with.
var astrbotBOM = []byte("\xef\xbb\xbf")

// astrbotRoot is the folder AstrBot keeps data/ in: ASTRBOT_ROOT, else the
// desktop app's ~/.astrbot, else a folder `astrbot init` was run in that
// magpie can know of — the home itself (a .astrbot file there), or ~/astrbot
// or ~/AstrBot, where its README and a clone of its source put it. A root
// none of these is reaches magpie through ASTRBOT_ROOT. With none found it is
// the desktop app's, which then isn't there.
func astrbotRoot(at place) string {
	if d := strings.TrimSpace(at.getenv("ASTRBOT_ROOT")); d != "" {
		return d
	}
	desktop := filepath.Join(at.home, ".astrbot")
	if isFile(filepath.Join(desktop, "data", "cmd_config.json")) {
		return desktop
	}
	if isFile(desktop) {
		return at.home
	}
	for _, d := range []string{"astrbot", "AstrBot"} {
		root := filepath.Join(at.home, d)
		if isFile(filepath.Join(root, "data", "cmd_config.json")) || isFile(filepath.Join(root, ".astrbot")) {
			return root
		}
	}
	return desktop
}

func astrbot(home string) *Agent { return astrbotAt(here(home)) }

func astrbotAt(at place) *Agent {
	root := astrbotRoot(at)
	data := filepath.Join(root, "data")
	path := filepath.Join(data, "cmd_config.json")
	backup := path + ".before-magpie"
	// keyPick is the default chat model AstrBot was on before magpie's
	keyPick := at.key("astrbot.provider_id")
	key := func() string { return agentKeyAt("astrbot", at.gw()) }
	// read is the file without its BOM, and whether it had one
	read := func() ([]byte, bool, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, false, err
		}
		if b, ok := bytes.CutPrefix(raw, astrbotBOM); ok {
			return b, true, nil
		}
		return raw, false, nil
	}
	write := func(raw []byte, bom bool) error {
		if bom {
			raw = append(append([]byte(nil), astrbotBOM...), raw...)
		}
		return edit.WriteAtomic(path, raw)
	}
	get := func(k string) (gjson.Result, bool) {
		raw, _, err := read()
		if err != nil || !json.Valid(raw) {
			return gjson.Result{}, false
		}
		return gjson.GetBytes(raw, k), true
	}
	// pickKey is where this file names AstrBot's default chat model
	pickKey := func() string {
		if r, _ := get("agent_runner"); r.Exists() {
			return astrbotRunner
		}
		return astrbotLegacy
	}
	pick := func() string { r, _ := get(pickKey()); return r.String() }
	// source is magpie's entry in provider_sources
	source := func() (gjson.Result, bool) {
		r, _ := get(astrbotSources)
		for _, e := range r.Array() {
			if e.Get("id").String() == magpieID {
				return e, true
			}
		}
		return gjson.Result{}, false
	}
	wired := func() bool { _, ok := source(); return ok }
	ours := func() bool { return prefixed(pick()) }
	// setPick names AstrBot's default chat model
	setPick := func(v string) error {
		raw, bom, err := read()
		if err != nil {
			return err
		}
		out, err := edit.PatchJSON(raw, []edit.KV{{Path: pickKey(), Value: v}}, nil)
		if err != nil {
			return err
		}
		if bytes.Equal(out, raw) {
			return nil
		}
		return write(out, bom)
	}
	// lists rewrites provider_sources and provider: the user's entries as
	// they are, magpie's — when on — after them, as astrbotWired makes them
	// from what is there
	lists := func(on bool) error {
		raw, bom, err := read()
		if err != nil {
			return err
		}
		out, err := astrbotWired(raw, on, at.v1(), key())
		if err != nil {
			return fmt.Errorf("AstrBot's %s: %w", path, err)
		}
		if bytes.Equal(out, raw) {
			return nil
		}
		return write(out, bom)
	}
	// unwire takes magpie's source and models out, and AstrBot back to the
	// default chat model it was on — when it is still on one of magpie's: a
	// pick the user made in AstrBot since is theirs and stands
	unwire := func() error {
		if !isFile(path) {
			forget(keyPick)
			return nil
		}
		if ours() {
			if err := setPick(unstash(keyPick)); err != nil {
				return err
			}
		} else {
			forget(keyPick)
		}
		return lists(false)
	}
	// put is magpie's source and models in, and AstrBot's default chat model
	// the one picked (magpie/<provider>/<model>). What AstrBot was on is
	// saved before it is written over, and only then.
	put := func(v string) error {
		raw, _, err := read()
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("AstrBot has no %s yet: start AstrBot once so it writes its settings (or start magpie with ASTRBOT_ROOT set to the folder AstrBot runs in), then connect it", path)
		}
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return fmt.Errorf("AstrBot's %s isn't JSON magpie can read; it was left as it is", path)
		}
		if !gjson.GetBytes(raw, astrbotSources).Exists() {
			return fmt.Errorf("this AstrBot keeps no provider_sources in %s: update AstrBot to 4.10 or later, start it once, then connect it", path)
		}
		if !wired() {
			b, _ := os.ReadFile(path)
			if err := os.WriteFile(backup, b, 0o600); err != nil {
				return err
			}
		}
		if !ours() {
			stash(map[string]string{keyPick: pick()})
		}
		if err := lists(true); err != nil {
			return err
		}
		return setPick(v)
	}
	return atomic(&Agent{
		ID: "astrbot", Name: "AstrBot", Icon: "astrbot", Spelled: prefixed,
		Bin: "astrbot", Dir: data, Path: path,
		Notice: func() string {
			return noticeAstrbot.String()
		},
		Joined: wired,
		Unwire: func() error {
			if !wired() {
				return nil
			}
			return unwire()
		},
		Check: func() string {
			if !ours() {
				return ""
			}
			if r, _ := get("agent_runner.runner_type"); pickKey() == astrbotRunner && r.String() != "local" {
				return "AstrBot's agent runner (cmd_config.json) is " + orDefault(r.String()) + ", which doesn't ask its chat model, so it no longer asks magpie"
			}
			e, ok := source()
			if !ok {
				return "magpie's provider source is gone from AstrBot's " + path + " (a save in AstrBot's WebUI writes its settings back whole) — connect it again"
			}
			return wiringOff("AstrBot", path, func(k string) (string, bool) {
				r := e.Get(k)
				return r.String(), r.Exists()
			}, "api_base", at.v1(), "key.0", key())
		},
		Sync: func() error {
			if !wired() {
				return nil
			}
			return lists(true)
		},
		Fields: []Field{{
			// AstrBot's default chat model, by its provider id: one of
			// magpie's is magpie/<provider>/<model>, the id magpie gives
			// its entry in provider, which is "<source>/<model>" as
			// AstrBot's own WebUI names them
			Key: "model", Label: "model",
			Get: pick,
			Set: func(v string) error {
				if v == "" {
					if ours() {
						return unwire()
					}
					return setPick("")
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					return put(v)
				}
				// one of the user's own: AstrBot goes onto it, magpie's
				// models left beside it in its list until Disconnect
				if ours() {
					forget(keyPick)
				}
				return setPick(v)
			},
			Options: func(cur map[string]string) []Option {
				return append(astrbotOwn(path, cur["model"]), viaMagpie("astrbot", magpieID+"/")...)
			},
		}},
	}, path)
}

// astrbotOwn is the user's own chat models in AstrBot's file, by the
// provider id AstrBot names them with, for the picker.
func astrbotOwn(path, cur string) []Option {
	var out []Option
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	raw = bytes.TrimPrefix(raw, astrbotBOM)
	seen := map[string]bool{}
	for _, e := range gjson.GetBytes(raw, astrbotModels).Array() {
		id := e.Get("id").String()
		if id == "" || prefixed(id) || seen[id] {
			continue
		}
		if t := e.Get("provider_type").String(); t != "" && t != "chat_completion" {
			continue
		}
		seen[id] = true
		out = append(out, Option{Value: id, Label: id, Note: "AstrBot's own"})
	}
	if cur != "" && !prefixed(cur) && !seen[cur] {
		out = append(out, Option{Value: cur, Label: cur, Note: "AstrBot's own"})
	}
	return out
}

// astrbotWired is raw, AstrBot's config, with provider_sources and provider
// holding the user's entries as they are and, when on, magpie's after them:
// its source at gw (the gateway's /v1) with key, and one entry for each of
// magpie's models. What the user changed in magpie's entries is kept
// (astrbotKeep).
func astrbotWired(raw []byte, on bool, gw, key string) ([]byte, error) {
	var wantSource map[string]any
	var wantModels []map[string]any
	if on {
		wantSource = astrbotSourceJSON(gw, key)
		for _, m := range magpieModels("astrbot") {
			wantModels = append(wantModels, astrbotModelJSON(m))
		}
	}
	var err error
	raw, err = astrbotList(raw, astrbotSources, func(e gjson.Result) bool { return e.Get("id").String() == magpieID },
		astrbotKeep(raw, astrbotSources, []map[string]any{wantSource}, astrbotSourceOwned))
	if err != nil {
		return nil, err
	}
	return astrbotList(raw, astrbotModels, func(e gjson.Result) bool { return e.Get("provider_source_id").String() == magpieID },
		astrbotKeep(raw, astrbotModels, wantModels, astrbotModelOwned))
}

// astrbotSourceOwned and astrbotModelOwned are the keys of magpie's entries
// that are magpie's to say; every other key of one is the user's to change
// in AstrBot's WebUI, and stays as they left it.
var (
	astrbotSourceOwned = []string{"id", "provider", "type", "provider_type", "key", "api_base"}
	astrbotModelOwned  = []string{"id", "provider_source_id", "model", "modalities", "max_context_tokens"}
)

// astrbotKeep is want, magpie's entries for the list at k, each with the
// keys of the entry of the same id already in raw that aren't magpie's to
// say (owned) kept as they are.
func astrbotKeep(raw []byte, k string, want []map[string]any, owned []string) []map[string]any {
	have := map[string]map[string]json.RawMessage{}
	for _, e := range gjson.GetBytes(raw, k).Array() {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(e.Raw), &m) == nil {
			have[e.Get("id").String()] = m
		}
	}
	var out []map[string]any
	for _, w := range want {
		if w == nil {
			continue
		}
		id, _ := w["id"].(string)
		for hk, hv := range have[id] {
			if !slices.Contains(owned, hk) {
				w[hk] = hv
			}
		}
		out = append(out, w)
	}
	return out
}

// astrbotList is raw with the top-level list at k rewritten: its entries
// that aren't magpie's (mine) as they are, byte for byte, then add. Nothing
// else of the file changes.
func astrbotList(raw []byte, k string, mine func(gjson.Result) bool, add []map[string]any) ([]byte, error) {
	r := gjson.GetBytes(raw, k)
	if !r.Exists() {
		if len(add) == 0 {
			return raw, nil
		}
		return nil, fmt.Errorf("no %s list", k)
	}
	if !r.IsArray() {
		return nil, fmt.Errorf("%s is not a list", k)
	}
	base := ""
	if start := bytes.LastIndexByte(raw[:r.Index], '\n') + 1; start < r.Index {
		for i := start; i < r.Index && (raw[i] == ' ' || raw[i] == '\t'); i++ {
			base += string(raw[i])
		}
	}
	in := base + "  "
	var items []string
	for _, e := range r.Array() {
		if !mine(e) {
			items = append(items, e.Raw)
		}
	}
	for _, a := range add {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		// AstrBot writes with ensure_ascii off: a model's name stays as it is
		enc.SetEscapeHTML(false)
		enc.SetIndent(in, "  ")
		if err := enc.Encode(a); err != nil {
			return nil, err
		}
		items = append(items, strings.TrimRight(b.String(), "\n"))
	}
	list := "[]"
	if len(items) > 0 {
		list = "[\n" + in + strings.Join(items, ",\n"+in) + "\n" + base + "]"
	}
	// the same entries, though AstrBot wrote them in another order of
	// keys, are no change: a file that says the same isn't written again
	var was, now any
	if list == r.Raw || json.Unmarshal([]byte(r.Raw), &was) == nil && json.Unmarshal([]byte(list), &now) == nil && reflect.DeepEqual(was, now) {
		return raw, nil
	}
	out := make([]byte, 0, len(raw)+len(list))
	out = append(out, raw[:r.Index]...)
	out = append(out, list...)
	return append(out, raw[r.Index+len(r.Raw):]...), nil
}

// astrbotSourceJSON is magpie's provider source: AstrBot's OpenAI Compatible
// template (default.py's "OpenAI" in CONFIG_METADATA_2's provider templates)
// at the gateway's /v1, with a key naming AstrBot.
func astrbotSourceJSON(gw, key string) map[string]any {
	return map[string]any{
		"id":             magpieID,
		"provider":       "openai",
		"type":           "openai_chat_completion",
		"provider_type":  "chat_completion",
		"enable":         true,
		"key":            []string{key},
		"api_base":       gw,
		"timeout":        120,
		"proxy":          "",
		"custom_headers": map[string]any{},
	}
}

// astrbotModelJSON is one of magpie's models as an entry of AstrBot's
// provider list. Every one is said to take tools: AstrBot drops a model's
// tools when tool_use isn't in its modalities (modalities.py), and the
// gateway hands a tool call on whatever the model. Images are what magpie
// knows the model sees, or describes to it. A window magpie doesn't know is
// 0, which has AstrBot look it up itself.
func astrbotModelJSON(m catalog.Model) map[string]any {
	mods := []string{"text"}
	if m.Images {
		mods = append(mods, "image")
	}
	mods = append(mods, "tool_use")
	return map[string]any{
		"id":                 magpieID + "/" + m.ID,
		"enable":             true,
		"provider_source_id": magpieID,
		"model":              m.ID,
		"modalities":         mods,
		"custom_extra_body":  map[string]any{},
		"max_context_tokens": m.Context,
	}
}

// what astrbot says after a change (notice.go)
var noticeAstrbot = newNotice("AstrBot reads cmd_config.json at start-up and writes it back whole when its WebUI saves settings — restart AstrBot to use this, before saving anything in its WebUI.")
