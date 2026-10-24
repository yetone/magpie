package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/titanous/json5"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

const openclawModel = "agents.defaults.model"
const openclawProvider = "models.providers.magpie"

var openclawMu sync.Mutex

// openclawEdit remembers individual values, not the whole config: disconnect
// restores only values that still say what magpie wrote.
type openclawEdit struct {
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after"`
}

type openclawRecord struct {
	Edits map[string]openclawEdit `json:"edits"`
	Model json.RawMessage         `json:"model,omitempty"`
}

func openclaw(home string) *Agent { return openclawIn(here(home)) }

func openclawIn(at place) *Agent {
	home := openclawPath(at.home, at.getenv("OPENCLAW_HOME"))
	if home == "" {
		home = at.home
	}
	dir := openclawPath(home, at.getenv("OPENCLAW_STATE_DIR"))
	if dir == "" {
		dir = filepath.Join(home, ".openclaw")
	}
	path := openclawPath(home, at.getenv("OPENCLAW_CONFIG_PATH"))
	if path == "" {
		path = filepath.Join(dir, "openclaw.json")
	}
	// A relative key also works on the copied HOME used by disconnect preview.
	rel, err := filepath.Rel(at.home, path)
	if err != nil {
		rel = path // another volume on Windows
	}
	key := at.key("openclaw.config:") + filepath.ToSlash(rel)
	get := func(k string) string {
		_, data, err := openclawRead(path)
		if err != nil {
			return ""
		}
		return gjson.GetBytes(data, k).String()
	}
	model := func() string {
		c, _ := openclawChain(path)
		return c.Primary
	}
	writable := func() error {
		if at.getenv("OPENCLAW_CONFIG_READONLY") == "1" || at.getenv("OPENCLAW_NIX_MODE") == "1" {
			return fmt.Errorf("OpenClaw config is read-only")
		}
		return nil
	}
	unwire := func() error {
		if err := writable(); err != nil {
			return err
		}
		return openclawRestore(path, key, "")
	}
	readChain := func() (ModelChain, error) { return openclawChain(path) }
	return &Agent{
		ID: "openclaw", Name: "OpenClaw", Icon: "openclaw-color", Bin: "openclaw", Dir: dir, Path: path,
		Spelled: prefixed, UA: []string{"openclaw"}, Unwire: unwire,
		ReadModelChain: readChain,
		SetModelChain: func(c ModelChain) error {
			if err := writable(); err != nil {
				return err
			}
			if err := validateOpenClawChain(c); err != nil {
				return err
			}
			if c.Fallbacks == nil {
				c.Fallbacks = []string{}
			}
			return openclawWriteChain(path, key, at, c.Primary, &c)
		},
		// A chain can start on an own model and reach magpie only on fallback.
		Joined: func() bool { return ourKey(get(openclawProvider + ".apiKey")) },
		Check: func() string {
			if !prefixed(model()) && !ourKey(get(openclawProvider+".apiKey")) {
				return ""
			}
			return wiringOff("OpenClaw", path, func(k string) (string, bool) {
				v := get(openclawProvider + "." + k)
				return v, v != ""
			}, "baseUrl", at.v1(), "apiKey", at.gwKey())
		},
		Sync: func() error {
			if err := writable(); err != nil {
				return err
			}
			// A provider the user changed to their own key is no longer ours.
			if !ourKey(get(openclawProvider + ".apiKey")) {
				return nil
			}
			return openclawWrite(path, key, at, "")
		},
		Fields: []Field{{
			Key: "model", Label: "model", Get: model,
			Set: func(v string) error {
				if err := writable(); err != nil {
					return err
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					return openclawWrite(path, key, at, v)
				}
				if v != "" {
					c, err := readChain()
					if err != nil {
						return err
					}
					if len(c.Fallbacks) > 0 {
						return openclawWrite(path, key, at, v)
					}
				}
				return openclawRestore(path, key, v)
			},
			Options: func(cur map[string]string) []Option {
				return append(openclawOwn(path, cur["model"]), viaMagpie("openclaw", magpieID+"/")...)
			},
		}},
	}
}

// OpenClaw expands path overrides against OPENCLAW_HOME, whose own tilde
// is relative to the OS home (src/infra/home-dir.ts).
func openclawPath(home, path string) string {
	if rest, ok := strings.CutPrefix(path, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
		return filepath.Clean(home + rest)
	}
	return path
}

// openclawRead validates before editing. JSONC keeps its layout. Other JSON5
// syntax (bare keys, single quotes) is parsed here but left on disk: the first
// edit that needs strict JSON copies the original aside and writes JSON, and
// Disconnect puts that copy back when nothing else has changed. Includes are
// not flattened: their source files belong to the user.
func openclawRead(path string) (raw, data []byte, err error) {
	raw, err = edit.Read(path)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}\n")
	}
	doc, err := openclawDoc(path, raw)
	if err != nil {
		return nil, nil, err
	}
	data, err = json.Marshal(doc)
	return raw, data, err
}

func openclawDoc(path string, raw []byte) (map[string]any, error) {
	dec := json5.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, fmt.Errorf("OpenClaw: cannot parse %s: %v", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("OpenClaw: invalid trailing data in %s", path)
	}
	if openclawIncludes(doc) {
		return nil, fmt.Errorf("OpenClaw uses $include; configure its models in OpenClaw instead")
	}
	if err := openclawNumbers(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// openclawJSONC reports whether raw is JSON or JSONC, whose comments and
// layout PatchJSON can keep. Other JSON5 is edited as normalized JSON.
func openclawJSONC(raw []byte) bool {
	return json.Valid(jsonc.ToJSON(append([]byte(nil), raw...)))
}

func openclawOriginal(path string) string { return path + ".before-magpie" }

// openclawNormalize copies a non-JSONC file aside once, then returns the JSON
// the editor can patch. A later disconnect restores that copy only when the
// JSON still says what magpie left.
func openclawNormalize(path string, raw []byte) ([]byte, error) {
	if openclawJSONC(raw) {
		return raw, nil
	}
	doc, err := openclawDoc(path, raw)
	if err != nil {
		return nil, err
	}
	backup := openclawOriginal(path)
	if _, err := os.Stat(backup); errors.Is(err, fs.ErrNotExist) {
		if err := edit.WriteAtomic(backup, raw); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// openclawRestoreOriginal puts a normalized JSON5 file back when disconnect
// left the same values. Otherwise it keeps the user's changes and discards
// the old copy, so the next connection backs up the current JSON5.
func openclawRestoreOriginal(path string, left []byte) error {
	backup := openclawOriginal(path)
	was, err := os.ReadFile(backup)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(cur, left) {
		return os.Remove(backup)
	}
	then, err := openclawDoc(path, was)
	if err != nil {
		return os.Remove(backup)
	}
	now, err := openclawDoc(path, cur)
	if err != nil {
		return err
	}
	x, err1 := json.Marshal(now)
	y, err2 := json.Marshal(then)
	if err1 != nil {
		return err1
	}
	if err2 != nil {
		return err2
	}
	if !bytes.Equal(x, y) {
		return os.Remove(backup)
	}
	if err := edit.WriteAtomic(path, was); err != nil {
		return err
	}
	return os.Remove(backup)
}

// json5.Number is a string type, unlike json.Number: convert it before
// marshaling or numeric settings such as gateway.port would become strings.
func openclawNumbers(v any) error {
	convert := func(v any) (any, error) {
		if n, ok := v.(json5.Number); ok {
			if json.Valid([]byte(n)) {
				return json.Number(n), nil
			}
			f, err := n.Float64()
			if err != nil {
				return nil, err
			}
			s := strconv.FormatFloat(f, 'g', -1, 64)
			if !json.Valid([]byte(s)) {
				return nil, fmt.Errorf("OpenClaw: non-finite config number %s", n)
			}
			return json.Number(s), nil
		}
		return v, openclawNumbers(v)
	}
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			n, err := convert(child)
			if err != nil {
				return err
			}
			x[k] = n
		}
	case []any:
		for i, child := range x {
			n, err := convert(child)
			if err != nil {
				return err
			}
			x[i] = n
		}
	}
	return nil
}

func openclawIncludes(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["$include"]; ok {
			return true
		}
		for _, child := range x {
			if openclawIncludes(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if openclawIncludes(child) {
				return true
			}
		}
	}
	return false
}

func openclawSaved(key string) (openclawRecord, error) {
	var rec openclawRecord
	if raw := stashLoad()[key]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return rec, fmt.Errorf("OpenClaw: cannot read saved configuration: %w", err)
		}
	}
	if rec.Edits == nil {
		rec.Edits = map[string]openclawEdit{}
	}
	return rec, nil
}

// openclawWrite changes the provider, model and model menu together in one
// atomic write. A restricted model menu gets magpie's models beside its own.
func openclawWrite(path, key string, at place, model string) error {
	return openclawWriteChain(path, key, at, model, nil)
}

func openclawWriteChain(path, key string, at place, model string, chain *ModelChain) error {
	openclawMu.Lock()
	defer openclawMu.Unlock()
	raw, data, err := openclawRead(path)
	if err != nil {
		return err
	}
	rec, err := openclawSaved(key)
	if err != nil {
		return err
	}
	var set []edit.KV
	var del []string
	remember := func(k string, value any) {
		b, _ := json.Marshal(value)
		e, ok := rec.Edits[k]
		if !ok {
			e.Before = json.RawMessage(gjson.GetBytes(data, k).Raw)
		}
		e.After = b
		rec.Edits[k] = e
		set = append(set, edit.KV{Path: k, Value: value})
	}
	if model != "" {
		if _, ok := rec.Edits[openclawModel+".primary"]; !ok {
			rec.Model = json.RawMessage(gjson.GetBytes(data, openclawModel).Raw)
		}
		before := gjson.GetBytes(data, openclawModel)
		remember(openclawModel+".primary", model)
		if before.Type == gjson.String && len(rec.Edits[openclawModel+".primary"].Before) == 0 {
			e := rec.Edits[openclawModel+".primary"]
			e.Before = json.RawMessage(before.Raw)
			rec.Edits[openclawModel+".primary"] = e
		}
	}
	if chain != nil {
		remember(openclawModel+".fallbacks", chain.Fallbacks)
	}
	// The selected agent's model overrides defaults. Keep that override in sync
	// without changing models belonging to other agents.
	if target := openclawAgentModel(data); target != "" && (model != "" || chain != nil) {
		v := gjson.GetBytes(data, target)
		if v.Exists() {
			value := map[string]any{}
			if v.IsObject() {
				if err := json.Unmarshal([]byte(v.Raw), &value); err != nil {
					return err
				}
			} else if v.Type == gjson.String {
				value["primary"] = v.String()
			}
			if model != "" {
				value["primary"] = model
			}
			if chain != nil {
				value["fallbacks"] = chain.Fallbacks
			}
			remember(target, value)
		}
	}
	// A restricted menu must admit the sequence the user selected as well.
	selected := []string{model}
	if chain != nil {
		selected = append([]string{chain.Primary}, chain.Fallbacks...)
	} else {
		if c, err := openclawChain(path); err == nil {
			selected = append([]string{c.Primary}, c.Fallbacks...)
		}
		if model != "" {
			selected[0] = model
		}
	}
	remember(openclawProvider, openclawProviderAt(at.gw()))
	if menu := gjson.GetBytes(data, "agents.defaults.models"); menu.IsObject() && len(menu.Map()) > 0 {
		listed := map[string]bool{}
		for _, m := range magpieModels("openclaw") {
			k := "agents.defaults.models." + strings.ReplaceAll(magpieID+"/"+m.ID, ".", `\.`)
			listed[k] = true
			if !gjson.GetBytes(data, k).Exists() {
				remember(k, map[string]any{})
			}
		}
		for _, ref := range selected {
			if ref == "" {
				continue
			}
			k := "agents.defaults.models." + strings.ReplaceAll(ref, ".", `\.`)
			listed[k] = true
			if !gjson.GetBytes(data, k).Exists() {
				remember(k, map[string]any{})
			}
		}
		for k, e := range rec.Edits {
			if strings.HasPrefix(k, "agents.defaults.models.") && !listed[k] && len(e.Before) == 0 && sameJSON(gjson.GetBytes(data, k).Raw, e.After) {
				del = append(del, k)
				delete(rec.Edits, k)
			}
		}
	}
	// Newer OpenClaw versions have an explicit policy independent of models.
	policy := "agents.defaults.modelPolicy.allow"
	if p := gjson.GetBytes(data, policy); p.IsArray() {
		var allow []string
		if err := json.Unmarshal([]byte(p.Raw), &allow); err != nil {
			return err
		}
		before := slices.Clone(allow)
		for _, ref := range append([]string{"magpie/*"}, selected...) {
			if ref != "" && !slices.Contains(allow, ref) {
				allow = append(allow, ref)
			}
		}
		if !slices.Equal(before, allow) {
			remember(policy, allow)
		}
	}
	sort.Strings(del)
	base, err := openclawNormalize(path, raw)
	if err != nil {
		return err
	}
	out, err := edit.PatchJSON(base, set, del)
	if err != nil {
		return err
	}
	if !bytes.Equal(out, raw) {
		if err := edit.WriteAtomic(path, out); err != nil {
			return err
		}
	}
	// Sync's write filter can keep a remote gateway or reverse proxy. Save
	// the provider actually written, so Disconnect compares against that.
	_, written, err := openclawRead(path)
	if err != nil {
		return err
	}
	e := rec.Edits[openclawProvider]
	e.After = json.RawMessage(gjson.GetBytes(written, openclawProvider).Raw)
	rec.Edits[openclawProvider] = e
	b, _ := json.Marshal(rec)
	stash(map[string]string{key: string(b)})
	return nil
}

func openclawRestore(path, key, model string) error {
	openclawMu.Lock()
	defer openclawMu.Unlock()
	raw, data, err := openclawRead(path)
	if err != nil {
		return err
	}
	rec, err := openclawSaved(key)
	if err != nil {
		return err
	}
	var set []edit.KV
	var del []string
	for k, e := range rec.Edits {
		cur := gjson.GetBytes(data, k)
		if !sameJSON(cur.Raw, e.After) && !(k == openclawModel+".primary" && prefixed(cur.String())) {
			// Keep edits the user made after connecting, including policy entries.
			if k == "agents.defaults.modelPolicy.allow" && cur.IsArray() {
				var allow, before []string
				json.Unmarshal([]byte(cur.Raw), &allow)
				json.Unmarshal(e.Before, &before)
				if !slices.Contains(before, "magpie/*") {
					allow = slices.DeleteFunc(allow, func(v string) bool { return v == "magpie/*" })
					set = append(set, edit.KV{Path: k, Value: allow})
				}
			}
			continue
		}
		if len(e.Before) == 0 {
			del = append(del, k)
		} else {
			set = append(set, edit.KV{Path: k, Value: e.Before})
		}
	}
	// Put a string shorthand back only if no fallback or other user field
	// was added to the object while connected.
	curModel := gjson.GetBytes(data, openclawModel)
	modelFields := len(curModel.Map())
	if slices.Contains(del, openclawModel+".fallbacks") {
		modelFields--
	}
	if e, ok := rec.Edits[openclawModel+".primary"]; ok && (sameJSON(curModel.Get("primary").Raw, e.After) || prefixed(curModel.Get("primary").String())) &&
		curModel.IsObject() && modelFields == 1 && (len(rec.Model) == 0 || gjson.ParseBytes(rec.Model).Type == gjson.String) {
		set = slices.DeleteFunc(set, func(kv edit.KV) bool { return strings.HasPrefix(kv.Path, openclawModel+".") })
		del = slices.DeleteFunc(del, func(k string) bool { return strings.HasPrefix(k, openclawModel+".") })
		if len(rec.Model) > 0 {
			set = append(set, edit.KV{Path: openclawModel, Value: rec.Model})
		} else {
			del = append(del, openclawModel)
		}
	}
	if model != "" {
		if target := openclawAgentModel(data); target != "" && gjson.GetBytes(data, target).Exists() {
			set = slices.DeleteFunc(set, func(kv edit.KV) bool { return kv.Path == target })
			del = slices.DeleteFunc(del, func(k string) bool { return k == target })
			v := gjson.GetBytes(data, target)
			if v.Type == gjson.String {
				set = append(set, edit.KV{Path: target, Value: model})
			} else {
				set = append(set, edit.KV{Path: target + ".primary", Value: model})
			}
		}
		set = slices.DeleteFunc(set, func(kv edit.KV) bool {
			return kv.Path == openclawModel || strings.HasPrefix(kv.Path, openclawModel+".")
		})
		del = slices.DeleteFunc(del, func(k string) bool { return k == openclawModel || strings.HasPrefix(k, openclawModel+".") })
		if curModel.Type == gjson.String {
			set = append(set, edit.KV{Path: openclawModel, Value: model})
		} else {
			set = append(set, edit.KV{Path: openclawModel + ".primary", Value: model})
		}
	}
	// A provider magpie created can leave an empty models object behind.
	// That object was not in the user's file, so it goes with the provider.
	if slices.Contains(del, openclawProvider) && len(gjson.GetBytes(data, "models").Map()) == 1 {
		del = append(del, "models")
	}
	sort.Slice(set, func(i, j int) bool { return set[i].Path < set[j].Path })
	sort.Strings(del)
	base, err := openclawNormalize(path, raw)
	if err != nil {
		return err
	}
	out, err := edit.PatchJSON(base, set, del)
	if err != nil {
		return err
	}
	if !bytes.Equal(out, raw) {
		if err := edit.WriteAtomic(path, out); err != nil {
			return err
		}
	}
	if err := openclawRestoreOriginal(path, out); err != nil {
		return err
	}
	forget(key)
	return nil
}

func openclawOwn(path, current string) []Option {
	_, data, err := openclawRead(path)
	if err != nil {
		return nil
	}
	refs := map[string]bool{}
	gjson.GetBytes(data, "agents.defaults.models").ForEach(func(k, _ gjson.Result) bool {
		refs[k.String()] = true
		return true
	})
	gjson.GetBytes(data, "models.providers").ForEach(func(p, v gjson.Result) bool {
		v.Get("models").ForEach(func(_, m gjson.Result) bool {
			refs[p.String()+"/"+m.Get("id").String()] = true
			return true
		})
		return true
	})
	if current != "" {
		refs[current] = true
	}
	gjson.GetBytes(data, openclawModel+".fallbacks").ForEach(func(_, v gjson.Result) bool {
		refs[v.String()] = true
		return true
	})
	var ids []string
	for ref := range refs {
		if !prefixed(ref) {
			ids = append(ids, ref)
		}
	}
	sort.Strings(ids)
	return group("OpenClaw", static(ids...))
}

func openclawProviderAt(gw string) any {
	ms := []map[string]any{}
	for _, m := range magpieModels("openclaw") {
		e := openclawModelJSON(m)
		switch {
		case slices.Contains(m.APIs, string(provider.Responses)):
			e["api"] = "openai-responses"
		case slices.Contains(m.APIs, string(provider.Anthropic)):
			e["api"], e["baseUrl"] = "anthropic-messages", gw
		}
		ms = append(ms, e)
	}
	return map[string]any{"baseUrl": gw + "/v1", "apiKey": keyAt(gw), "api": "openai-completions",
		"headers": map[string]string{"User-Agent": "openclaw"}, "models": ms}
}

func openclawModelJSON(m catalog.Model) map[string]any {
	e := map[string]any{"id": m.ID, "name": m.Name, "reasoning": len(m.Efforts) > 0, "input": []string{"text"}}
	if m.Images {
		e["input"] = []string{"text", "image"}
	}
	if m.Context > 0 {
		e["contextWindow"] = m.Context
	}
	if n := maxTokens(m); n > 0 {
		e["maxTokens"] = n
	}
	if m.Price != nil {
		e["cost"] = map[string]any{"input": m.Price.Input, "output": m.Price.Output, "cacheRead": m.Price.CacheRead, "cacheWrite": m.Price.CacheWrite}
	}
	return e
}

func openclawChain(path string) (ModelChain, error) {
	_, data, err := openclawRead(path)
	if err != nil {
		return ModelChain{}, err
	}
	v := gjson.GetBytes(data, openclawModel)
	c, err := openclawParseChain(v)
	if err != nil {
		return c, err
	}
	if target := openclawAgentModel(data); target != "" {
		v := gjson.GetBytes(data, target)
		own, err := openclawParseChain(v)
		if err != nil {
			return c, err
		}
		if own.Primary != "" {
			c.Primary = own.Primary
			c.Fallbacks = own.Fallbacks
		}
		if v.Get("fallbacks").Exists() {
			c.Fallbacks = own.Fallbacks
		}
	}
	return c, nil
}

// Resolve only a configured default or sole agent. An explicit fleet without
// an owner continues to edit global defaults rather than guessing an agent.
func openclawAgentModel(data []byte) string {
	type entry struct {
		id, path string
		marked   bool
	}
	var entries []entry
	roster := gjson.GetBytes(data, "agents.entries")
	if roster.Exists() {
		roster.ForEach(func(k, v gjson.Result) bool {
			if v.IsObject() {
				entries = append(entries, entry{k.String(), "agents.entries." + strings.ReplaceAll(k.String(), ".", `\.`) + ".model", v.Get("default").Bool()})
			}
			return true
		})
	} else {
		for i, v := range gjson.GetBytes(data, "agents.list").Array() {
			if v.IsObject() {
				entries = append(entries, entry{v.Get("id").String(), fmt.Sprintf("agents.list.%d.model", i), v.Get("default").Bool()})
			}
		}
	}
	owner := gjson.GetBytes(data, "agents.defaults.systemAgent.agentId").String()
	for _, e := range entries {
		if owner != "" && e.id == owner {
			return e.path
		}
	}
	if gjson.GetBytes(data, "agents.ownership").String() != "explicit" {
		var marked []entry
		for _, e := range entries {
			if e.marked {
				marked = append(marked, e)
			}
		}
		if len(marked) == 1 {
			return marked[0].path
		}
	}
	if len(entries) == 1 {
		return entries[0].path
	}
	return ""
}

func openclawParseChain(v gjson.Result) (ModelChain, error) {
	c := ModelChain{Fallbacks: []string{}}
	if v.Type == gjson.String {
		c.Primary = v.String()
		return c, nil
	}
	if v.Exists() && !v.IsObject() {
		return c, fmt.Errorf("OpenClaw model must be a string or an object")
	}
	c.Primary = v.Get("primary").String()
	if f := v.Get("fallbacks"); f.Exists() {
		if !f.IsArray() {
			return c, fmt.Errorf("OpenClaw fallbacks must be an array of model IDs")
		}
		if err := json.Unmarshal([]byte(f.Raw), &c.Fallbacks); err != nil {
			return c, err
		}
	}
	return c, nil
}

func validateOpenClawChain(c ModelChain) error {
	seen := map[string]bool{}
	for _, id := range append([]string{c.Primary}, c.Fallbacks...) {
		provider, model, ok := strings.Cut(id, "/")
		if !ok || provider == "" || model == "" || strings.ContainsAny(id, " \t\r\n") {
			return fmt.Errorf("OpenClaw model %q must be provider/model", id)
		}
		if seen[id] {
			return fmt.Errorf("OpenClaw model %q is repeated", id)
		}
		seen[id] = true
	}
	return nil
}
