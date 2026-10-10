package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	home := at.getenv("OPENCLAW_HOME")
	if home == "" {
		home = at.home
	}
	dir := at.getenv("OPENCLAW_STATE_DIR")
	if dir == "" {
		dir = filepath.Join(home, ".openclaw")
	}
	path := at.getenv("OPENCLAW_CONFIG_PATH")
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
		v := get(openclawModel + ".primary")
		if v == "" {
			v = get(openclawModel)
			if strings.HasPrefix(v, "{") {
				return ""
			}
		}
		return v
	}
	writable := func() error {
		if at.spell == nil && (os.Getenv("OPENCLAW_CONFIG_READONLY") == "1" || os.Getenv("OPENCLAW_NIX_MODE") == "1") {
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
	return &Agent{
		ID: "openclaw", Name: "OpenClaw", Icon: "openclaw-color", Bin: "openclaw", Dir: dir, Path: path,
		Spelled: prefixed, UA: []string{"openclaw"}, Unwire: unwire,
		Check: func() string {
			if !prefixed(model()) {
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
				return openclawRestore(path, key, v)
			},
			Options: func(cur map[string]string) []Option {
				return append(openclawOwn(path, cur["model"]), viaMagpie("openclaw", magpieID+"/")...)
			},
		}},
	}
}

// openclawRead validates before editing. JSONC keeps its layout; other JSON5
// syntax (bare keys, single quotes) is normalized to JSON, also read by OpenClaw.
// Includes are not flattened: their source files belong to the user.
func openclawRead(path string) (raw, data []byte, err error) {
	raw, err = edit.Read(path)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}\n")
	}
	dec := json5.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err = dec.Decode(&doc); err != nil || doc == nil {
		return nil, nil, fmt.Errorf("OpenClaw: cannot parse %s: %v", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, nil, fmt.Errorf("OpenClaw: invalid trailing data in %s", path)
	}
	if openclawIncludes(doc) {
		return nil, nil, fmt.Errorf("OpenClaw uses $include; configure its models in OpenClaw instead")
	}
	if err := openclawNumbers(doc); err != nil {
		return nil, nil, err
	}
	data, err = json.Marshal(doc)
	if err != nil {
		return nil, nil, err
	}
	if !json.Valid(jsonc.ToJSON(append([]byte(nil), raw...))) {
		raw, err = json.MarshalIndent(doc, "", "  ")
		raw = append(raw, '\n')
	}
	return raw, data, err
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
		if !slices.Contains(allow, "magpie/*") {
			remember(policy, append(allow, "magpie/*"))
		}
	}
	sort.Strings(del)
	out, err := edit.PatchJSON(raw, set, del)
	if err != nil {
		return err
	}
	if !bytes.Equal(out, raw) {
		if err := edit.WriteAtomic(path, out); err != nil {
			return err
		}
	}
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
	if e, ok := rec.Edits[openclawModel+".primary"]; ok && (sameJSON(curModel.Get("primary").Raw, e.After) || prefixed(curModel.Get("primary").String())) &&
		curModel.IsObject() && len(curModel.Map()) == 1 && (len(rec.Model) == 0 || gjson.ParseBytes(rec.Model).Type == gjson.String) {
		set = slices.DeleteFunc(set, func(kv edit.KV) bool { return kv.Path == openclawModel+".primary" })
		del = slices.DeleteFunc(del, func(k string) bool { return k == openclawModel+".primary" })
		if len(rec.Model) > 0 {
			set = append(set, edit.KV{Path: openclawModel, Value: rec.Model})
		} else {
			del = append(del, openclawModel)
		}
	}
	if model != "" {
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
	sort.Slice(set, func(i, j int) bool { return set[i].Path < set[j].Path })
	sort.Strings(del)
	out, err := edit.PatchJSON(raw, set, del)
	if err != nil {
		return err
	}
	if !bytes.Equal(out, raw) {
		if err := edit.WriteAtomic(path, out); err != nil {
			return err
		}
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
