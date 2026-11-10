package agent

// An agent a plugin adds (internal/agentplug) is wired as a built-in one
// that keeps its model in a file is: its model field picks among the
// agent's own models and magpie's, and a model through magpie writes the
// model, as the plugin spells it, and the keys the plugin's connect
// returns (where the gateway is, its key). What the user had under every
// key magpie writes is stashed first and put back when magpie steps out,
// so a plugin needs no code for that. The plugin says only where things
// go; magpie does every write, to the one file the plugin names, and to
// the file of its startup model when it keeps that apart (PiG's
// settings.json beside its models.json, agentplug.Startup).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/agentplug"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/update"
	"github.com/yetone/magpie/internal/usage"
)

func init() {
	// the agents plugins add come and go, unlike magpie's own; one whose
	// id is a built-in's is the built-in's, which the gateway asks first
	usage.PluginAgents = func() []usage.Known {
		list, _ := agentplug.List()
		out := make([]usage.Known, 0, len(list))
		for _, p := range list {
			out = append(out, usage.Known{ID: p.Desc.ID, Names: []string{p.Desc.ID}, UA: p.Desc.UA})
		}
		return out
	}
}

// pluggedStash is what magpie keeps of an agent plugin's file while it is
// wired: what was under each key it wrote (absent where there was nothing,
// as the file spells it), the model it wrote, and the key whose value is
// the gateway's key, which says the wiring is still magpie's.
type pluggedStash struct {
	Had   map[string]string `json:"had"`
	Ref   string            `json:"ref"`
	Mark  string            `json:"mark,omitempty"`
	Value string            `json:"value,omitempty"`
	// Wrote are the strings magpie wrote, by key, for Check to tell
	// one changed since
	Wrote map[string]string `json:"wrote,omitempty"`
	// Spec and Format are the plugin's and its file's, so what the user
	// had can be put back with the plugin gone (#3810); a stash written
	// before them has neither, and its file's name says its format
	Spec   string `json:"spec,omitempty"`
	Format string `json:"format,omitempty"`
	// Made are the objects a merge key sits in (and the key itself) that
	// the file didn't have before magpie's first write, shallowest
	// first: each is taken out again when it is left empty
	Made []string `json:"made,omitempty"`
}

// PluginErrors is why an agent a plugin adds isn't listed, by plugin
// spec: its module didn't load, or its id is a built-in agent's.
func PluginErrors() map[string]string {
	out := map[string]string{}
	home, cfg := homes()
	pluggedAgents(home, builtins(home, cfg), out)
	return out
}

// PluginAgentOf is the id of the agent the plugin spec adds, "" for none.
func PluginAgentOf(spec string) string {
	list, _ := agentplug.List()
	for _, a := range list {
		if a.Spec == spec {
			return a.Desc.ID
		}
	}
	return ""
}

// pluggedAgents are the agents plugins add, but one whose id or alias is
// one of builtin's, which errs (when not nil) says.
func pluggedAgents(home string, builtin []*Agent, errs map[string]string) []*Agent {
	list, lerrs := agentplug.List()
	for k, v := range lerrs {
		if errs != nil {
			errs[k] = v
		}
	}
	if len(list) == 0 {
		return nil
	}
	taken := map[string]bool{magpieID: true}
	for _, a := range append(builtin, others...) {
		taken[a.ID] = true
		for _, al := range a.Aliases {
			taken[al] = true
		}
	}
	var out []*Agent
	for _, p := range list {
		if taken[p.Desc.ID] {
			if errs != nil {
				errs[p.Spec] = fmt.Sprintf("agent id %q is magpie's own agent's", p.Desc.ID)
			}
			continue
		}
		out = append(out, plugged(here(home), p))
	}
	return out
}

// PluginPath is a path an agent plugin gives, as it reads its config's:
// ~ and $VAR expanded against this user's home.
func PluginPath(p string) string {
	home, _ := homes()
	return pluggedPath(home, p)
}

// pluggedPath is the file a plugin names, with ~ and $VAR as a shell
// reads them.
func pluggedPath(home, p string) string { return pluggedPathEnv(home, p, os.Getenv) }

// pluggedPathEnv is pluggedPath with the variables getenv gives: none in
// a WSL distro, whose variables magpie can't read.
func pluggedPathEnv(home, p string, getenv func(string) string) string {
	p = os.Expand(p, func(k string) string {
		if k == "HOME" {
			return home
		}
		return getenv(k)
	})
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[1:])
	}
	return filepath.Clean(p)
}

// pluggedFile is an agent plugin's file, read and written as its format.
type pluggedFile struct {
	path, format string
}

// get is the value at key as a string, for comparing.
func (f pluggedFile) get(key string) (string, bool) {
	switch f.format {
	case "json":
		return edit.GetJSON(f.path, key)
	case "yaml":
		return edit.GetYAML(f.path, key)
	}
	return edit.GetEnvFile(f.path, key)
}

// raw is the value at key as the file spells it, to be put back so.
func (f pluggedFile) raw(key string) (string, bool) {
	switch f.format {
	case "json":
		b, err := edit.Read(f.path)
		if err != nil || len(b) == 0 {
			return "", false
		}
		r := gjson.GetBytes(jsonc.ToJSONInPlace(b), key)
		return r.Raw, r.Exists()
	case "yaml":
		return edit.GetYAMLText(f.path, key)
	}
	return edit.GetEnvFile(f.path, key)
}

// back is a value raw read, to be written as it was.
func (f pluggedFile) back(v string) any {
	switch f.format {
	case "json":
		return json.RawMessage(v)
	case "yaml":
		return edit.YAMLText(v)
	}
	return v
}

func (f pluggedFile) set(kvs []edit.KV) error {
	if len(kvs) == 0 {
		return nil
	}
	switch f.format {
	case "json":
		return edit.SetJSON(f.path, kvs...)
	case "yaml":
		return edit.SetYAML(f.path, kvs...)
	}
	for i, kv := range kvs {
		switch v := kv.Value.(type) {
		case string:
		case nil:
			kvs[i].Value = ""
		default:
			b, _ := json.Marshal(v)
			kvs[i].Value = string(b)
		}
	}
	return edit.SetEnvFile(f.path, kvs...)
}

func (f pluggedFile) del(keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	switch f.format {
	case "json":
		return edit.DelJSON(f.path, keys...)
	case "yaml":
		return edit.DelYAML(f.path, keys...)
	}
	return edit.DelEnvFile(f.path, keys...)
}

// put writes kvs into f, keeping in st.Had what each key held before
// magpie's first write, and puts back what the user had under a key
// magpie wrote before and kvs no longer has.
func (f pluggedFile) put(st *pluggedStash, kvs []edit.KV) error {
	wrote := map[string]bool{}
	for _, kv := range kvs {
		wrote[kv.Path] = true
		if _, ok := st.Had[kv.Path]; ok {
			continue
		}
		if v, ok := f.raw(kv.Path); ok {
			st.Had[kv.Path] = v
		} else {
			st.Had[kv.Path] = "\x00"
		}
	}
	var del []string
	for _, k := range sortedKeys(st.Had) {
		if wrote[k] {
			continue
		}
		if v := st.Had[k]; v == "\x00" {
			del = append(del, k)
		} else {
			kvs = append(kvs, edit.KV{Path: k, Value: f.back(v)})
		}
		delete(st.Had, k)
	}
	if err := f.del(del); err != nil {
		return err
	}
	return f.set(kvs)
}

// wired says the agent id's file is still as magpie wired it, by st: the
// gateway's key magpie wrote is there.
func (f pluggedFile) wired(id string, st pluggedStash) bool {
	if st.Mark == "" {
		return true
	}
	v, _ := f.get(st.Mark)
	return v == st.Value || ourKey(v) || v == gateway.TokenFor(id)
}

// restore puts back what the user had under every key magpie wrote, as st
// stashed it under key, and forgets it.
func (f pluggedFile) restore(key string, st pluggedStash) error {
	var del []string
	var kvs []edit.KV
	for _, k := range sortedKeys(st.Had) {
		if v := st.Had[k]; v == "\x00" {
			del = append(del, k)
		} else {
			kvs = append(kvs, edit.KV{Path: k, Value: f.back(v)})
		}
	}
	if err := f.del(del); err != nil {
		return err
	}
	if err := f.set(kvs); err != nil {
		return err
	}
	for i := len(st.Made) - 1; i >= 0; i-- {
		if v, ok := f.raw(st.Made[i]); ok && emptyObject(v) {
			if err := f.del(st.Made[i : i+1]); err != nil {
				return err
			}
		}
	}
	forget(key)
	return nil
}

// emptyObject says raw JSON v is {}.
func emptyObject(v string) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal([]byte(v), &m) == nil && m != nil && len(m) == 0
}

// merged writes each object out has under one of merge's keys member by
// member, beside what the user has there; the rest of out as it is.
func merged(out map[string]any, merge []string) []edit.KV {
	var kvs []edit.KV
	for _, m := range merge {
		// connect's objects come as their JSON text (agentplug.keys)
		var obj map[string]json.RawMessage
		switch v := out[m].(type) {
		case json.RawMessage:
			if json.Unmarshal(v, &obj) != nil || obj == nil {
				continue
			}
		case map[string]any:
			obj = map[string]json.RawMessage{}
			for k, x := range v {
				b, _ := json.Marshal(x)
				obj[k] = b
			}
		default:
			continue
		}
		delete(out, m)
		for _, k := range sortedKeys(obj) {
			kvs = append(kvs, edit.KV{Path: m + "." + gjson.Escape(k), Value: obj[k]})
		}
	}
	return kvs
}

// made are the objects of merge's keys, and those they sit in, that f
// doesn't have, shallowest first.
func (f pluggedFile) made(merge []string) []string {
	var out []string
	for _, m := range merge {
		parts := strings.Split(m, ".")
		for i := range parts {
			k := strings.Join(parts[:i+1], ".")
			if _, ok := f.raw(k); !ok && !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// plugged is the Agent of a plugin's agent at a place: this machine's
// home, or a WSL distro's for one that says wsl.
func plugged(at place, p *agentplug.Agent) *Agent {
	d := p.Desc
	id := d.ID
	home := at.home
	distro := at.sys != nil
	ppath := func(s string) string { return pluggedPath(home, s) }
	if distro {
		ppath = func(s string) string { return at.open(pluggedPathEnv(home, s, func(string) string { return "" })) }
	}
	f := pluggedFile{ppath(d.Config), d.Format}
	key := "plugin:" + id + ":" + f.path
	// bf: the agent's own settings file, where config is magpie's alone
	// (agentplug.Desc.Base): the user's model is there off magpie
	var bf *pluggedFile
	if d.Base != "" {
		bf = &pluggedFile{ppath(d.Base), d.Format}
	}
	saved := func() (pluggedStash, bool) {
		var st pluggedStash
		v := stashShared()[key]
		if v == "" {
			return handedOver(id, at, f, p.Spec, key)
		}
		return st, json.Unmarshal([]byte(v), &st) == nil
	}
	save := func(st pluggedStash) {
		b, _ := json.Marshal(st)
		stash(map[string]string{key: string(b)})
	}
	// onMagpie: magpie wrote the file, and the gateway's key it wrote is
	// still there
	onMagpie := func() (pluggedStash, bool) {
		st, ok := saved()
		return st, ok && f.wired(id, st)
	}
	// restore puts back what the user had; magpie's own file (with a
	// base) goes as a whole
	restore := func(st pluggedStash) error {
		if err := f.restore(key, st); err != nil || bf == nil {
			return err
		}
		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// l: one that takes the gateway only from how it is started, config
	// as the agent names it
	l := newLaunch(d, at.native(f.path))
	gkey := func() string { return agentKeyAt(id, at.gw()) }
	// only: the agent picks its model itself among those connect writes
	// (#2374), so magpie writes no model, and the switch is its one field;
	// or it keeps its startup model in a file apart (sf), which its model
	// field writes, and config holds connect's keys alone
	only := d.Model == ""
	var sf *pluggedFile
	var skey string
	if d.Startup != nil {
		sf = &pluggedFile{ppath(d.Startup.Config), d.Startup.Format}
		skey = "plugin:" + id + ":" + sf.path
	}
	// startSaved is what magpie keeps of the startup file
	startSaved := func() (pluggedStash, bool) {
		var st pluggedStash
		v := stashShared()[skey]
		return st, sf != nil && v != "" && json.Unmarshal([]byte(v), &st) == nil
	}
	// startOn: the startup file starts on magpie's provider still, as
	// startup's first key of a string says (PiG's defaultProvider), else
	// on the model magpie wrote. A model picked in the agent since among
	// magpie's (PiG's /model, saved) is still magpie's.
	startOn := func() (pluggedStash, bool) {
		st, ok := startSaved()
		return st, ok && sf.wired(id, st)
	}
	// startWire writes the startup model ref and startup's keys beside it
	startWire := func(ref string) error {
		kvs := []edit.KV{{Path: d.Startup.Model, Value: d.Prefix + ref}}
		mark, value := d.Startup.Model, d.Prefix+ref
		first := true
		for _, k := range sortedKeys(d.Startup.Keys) {
			v := d.Startup.Keys[k]
			kvs = append(kvs, edit.KV{Path: k, Value: v})
			if s, ok := v.(string); ok && first {
				mark, value, first = k, s, false
			}
		}
		st, was := startOn()
		if !was {
			st = pluggedStash{Had: map[string]string{}}
		}
		if err := sf.put(&st, kvs); err != nil {
			return err
		}
		st.Ref, st.Mark, st.Value, st.Wrote = ref, mark, value, nil
		st.Spec, st.Format = p.Spec, sf.format
		b, _ := json.Marshal(st)
		stash(map[string]string{skey: string(b)})
		return nil
	}
	// startModel is the startup model as the field shows it: magpie's
	// when the file starts on magpie still and the model is one of its
	startModel := func() string {
		v, _ := sf.get(d.Startup.Model)
		if _, ok := onMagpie(); ok {
			if st, ok := startOn(); ok {
				if r, has := strings.CutPrefix(v, d.Prefix); has && (r == st.Ref || isMagpie(r)) {
					return magpieID + "/" + r
				}
			}
		}
		return v
	}
	// startRestore puts back what the user had in the startup file, where
	// it starts on magpie still; a model of their own picked in the agent
	// since is theirs, and the stash is dropped
	startRestore := func() error {
		if sf == nil {
			return nil
		}
		if st, ok := startOn(); ok {
			return sf.restore(skey, st)
		}
		forget(skey)
		return nil
	}
	// wire writes ref and connect's keys, stashing what each key had; a
	// connect-only agent's ref is ""
	wire := func(ref string) error {
		var model map[string]any
		var models []map[string]any
		for _, m := range sharedModels(id) {
			j := map[string]any{"id": m.ID, "name": m.Name, "context": m.Context, "output": m.Output, "images": m.Images, "efforts": m.Efforts}
			if j["efforts"] == nil {
				j["efforts"] = []string{}
			}
			// what /v1/models says of it too, by the same names, for an
			// agent built on the gateway (cleary5779 on Discord): the APIs
			// a request for it goes on untranslated, and what a token
			// costs; each left out when not known, never empty or 0
			if len(m.Endpoints) > 0 {
				j["native_endpoints"] = m.Endpoints
			}
			if m.Price != nil {
				j["pricing"] = catalog.OpenRouterPricing(*m.Price)
			}
			if m.ID == ref {
				model = j
			}
			models = append(models, j)
		}
		if model == nil {
			model = map[string]any{"id": ref, "name": ref, "context": 0, "output": 0, "images": false, "efforts": []string{}}
		}
		gk := gkey()
		in := map[string]any{
			"gateway": map[string]any{"url": at.gw(), "v1": at.v1(), "key": gk},
			"models":  models,
			"agent":   map[string]any{"id": id, "config": f.path},
		}
		if bf != nil {
			in["agent"].(map[string]any)["base"] = bf.path
			in["base"] = bf.parsed()
		}
		if !only || ref != "" {
			in["model"] = model
		}
		out, err := p.Connect(in)
		if err != nil {
			return fmt.Errorf("%s: %w", plugin.Name(p.Spec), err)
		}
		kvs := merged(out, d.Merge)
		if only {
			// nothing written would read as connected all the same
			if len(out) == 0 {
				return fmt.Errorf("%s: connect returned no keys to write", plugin.Name(p.Spec))
			}
		} else {
			delete(out, d.Model)
			kvs = append(kvs, edit.KV{Path: d.Model, Value: d.Prefix + ref})
		}
		mark, strs := "", map[string]string{}
		for _, k := range sortedKeys(out) {
			kvs = append(kvs, edit.KV{Path: k, Value: out[k]})
			if s, ok := out[k].(string); ok {
				strs[k] = s
				if s == gk && mark == "" {
					mark = k
				}
			}
		}
		st, was := onMagpie()
		if !was {
			st = pluggedStash{Had: map[string]string{}, Made: f.made(d.Merge)}
		}
		// a key magpie wrote before that connect no longer returns gets
		// what the user had back
		if err := f.put(&st, kvs); err != nil {
			return err
		}
		st.Ref, st.Mark, st.Value, st.Wrote = ref, mark, gk, strs
		if mark == "" && l != nil && ref != "" {
			// nothing written is the key, which goes in the environment:
			// the model says it is magpie's file still (holds)
			st.Value = d.Prefix + ref
		}
		st.Spec, st.Format = p.Spec, f.format
		save(st)
		if sf != nil && ref != "" {
			return startWire(ref)
		}
		return nil
	}
	get := func() string {
		if sf != nil {
			return startModel()
		}
		st, ok := onMagpie()
		if !ok && bf != nil {
			v, _ := bf.get(d.Model)
			return v
		}
		v, _ := f.get(d.Model)
		if ok {
			// one of magpie's models picked in the agent since is magpie's
			// still, for one started on it (its picker lists them all)
			if r, has := strings.CutPrefix(v, d.Prefix); has && (r == st.Ref || l != nil && isMagpie(r)) {
				return magpieID + "/" + r
			}
		}
		return v
	}
	// magpieModel is the model as the agent names it while it is one of
	// magpie's, "" otherwise
	magpieModel := func() string {
		if r, ok := strings.CutPrefix(get(), magpieID+"/"); ok {
			return d.Prefix + r
		}
		return ""
	}
	var lsh launchShell
	if l != nil {
		read := modelSed(d.Format, d.Model, d.Prefix)
		file := at.native(f.path)
		lsh = launchShell{l: l, at: at, distro: distro, fn: func(kind string) string {
			held := ""
			if read == "" {
				held = magpieModel()
			}
			return l.distroFunction(kind, l.vars(at.gw(), gkey()), read, file, held)
		}}
	}
	// shell gives the shells the function while the agent is on magpie's
	// models, and takes it out after; never at a stopped distro
	shell := func() {
		if l != nil && !at.cold {
			lsh.set(magpieModel() != "")
		}
	}
	icon := pluggedIcon(p)
	a := &Agent{
		ID: id, Name: d.Name, Icon: icon, Spelled: prefixed,
		UA:  d.UA,
		Bin: d.Bin, Path: f.path,
		Plugin:  p.Spec,
		Aliases: slices.Clone(d.Aliases),
		Notice: func() string {
			if l == nil {
				return d.Notice
			}
			m := magpieModel()
			if m == "" {
				return d.Notice
			}
			cmd := l.command(at.gw(), gkey(), m, !distro && runtime.GOOS == "windows")
			if in := lsh.in(); len(in) > 0 {
				for i := range in {
					if distro {
						in[i] = at.native(in[i])
					} else {
						in[i] = update.TildeHome(in[i])
					}
				}
				return noticeLaunchTyped.say("bin", l.bin, "profiles", strings.Join(in, ", "), "agent", d.Name, "command", cmd)
			}
			return noticeLaunch.say("agent", d.Name, "command", cmd)
		},
		Check: func() string {
			st, ok := onMagpie()
			if !ok {
				return ""
			}
			if v, _ := f.get(d.Model); !only && v != d.Prefix+st.Ref {
				return ""
			}
			if _, ok := startOn(); sf != nil && !ok {
				return ""
			}
			var kvs []string
			for _, k := range sortedKeys(st.Wrote) {
				kvs = append(kvs, k, st.Wrote[k])
			}
			if w := wiringOff(d.Name, f.path, f.get, kvs...); w != "" || l == nil {
				return w
			}
			// the model's entry under a merge key, which it is picked from
			m := magpieModel()
			for _, mk := range d.Merge {
				k := mk + "." + gjson.Escape(m)
				if _, wrote := st.Had[k]; !wrote {
					continue
				}
				if _, ok := f.raw(k); !ok {
					return fmt.Sprintf("%s's %s (%s) is gone, so it no longer reaches magpie", d.Name, m, filepath.Base(f.path))
				}
			}
			if p := lsh.stale(); p != "" {
				return fmt.Sprintf("%s's function %s in %s names magpie at an address it no longer has", d.Name, l.bin, at.native(p))
			}
			return ""
		},
		ListsModels: only && sf == nil,
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if err := wire(ref); err != nil {
						return err
					}
					shell()
					return nil
				}
				if err := startRestore(); err != nil {
					return err
				}
				if st, ok := onMagpie(); ok {
					if err := restore(st); err != nil {
						return err
					}
				}
				defer shell()
				mf, key := f, d.Model
				if sf != nil {
					mf, key = *sf, d.Startup.Model
				} else if bf != nil {
					mf = *bf
				}
				if v == "" {
					return mf.del([]string{key})
				}
				return mf.set([]edit.KV{{Path: key, Value: v}})
			},
			Options: func(cur map[string]string) []Option {
				if !p.HasOwn() {
					return withVia(ownOptions("", cur["model"]), id, magpieID+"/")
				}
				return withVia(group(d.Name, pluggedOwn(p, f, bf, cur["model"])), id, magpieID+"/")
			},
		}},
		// the catalog changed: what connect writes for the model may too
		Sync: func() error {
			defer shell()
			if st, ok := onMagpie(); ok && sf != nil {
				// the startup model too while it is magpie's, as it is
				// now; else connect's list alone, for the agent's picker
				if r, ok := strings.CutPrefix(startModel(), magpieID+"/"); ok {
					return wire(r)
				}
				return wire("")
			} else if ok {
				if v, _ := f.get(d.Model); only || v == d.Prefix+st.Ref {
					return wire(st.Ref)
				} else if r, ok := strings.CutPrefix(get(), magpieID+"/"); ok && l != nil {
					// another of magpie's, picked in the agent
					return wire(r)
				}
			}
			return nil
		},
		Unwire: func() error {
			defer shell()
			if err := startRestore(); err != nil {
				return err
			}
			if st, ok := onMagpie(); ok {
				return restore(st)
			}
			return nil
		},
	}
	paths := []string{f.path}
	if bf != nil {
		paths = append(paths, bf.path)
	}
	if sf != nil {
		// a model of the agent's own picked in it, magpie's provider still
		// in its list: connected, as joined, not drift (#2424's Beside)
		a.Beside = func() bool {
			_, ok := onMagpie()
			return ok
		}
		paths = append(paths, sf.path)
	}
	if only && sf == nil {
		// magpie as its provider, on or off: the models are the agent's
		// to pick, in the list connect writes
		a.Fields = []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if _, ok := onMagpie(); ok {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				if v == magpieID {
					return wire("")
				}
				if st, ok := onMagpie(); ok {
					return restore(st)
				}
				return nil
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie"}}
			},
		}}
	}
	if d.Dir != "" {
		a.Dir = ppath(d.Dir)
	}
	if l != nil {
		a.Launch = func() string {
			m := magpieModel()
			if m == "" {
				return ""
			}
			return l.command(at.gw(), gkey(), m, !distro && runtime.GOOS == "windows")
		}
		a.Typed = func() string {
			if magpieModel() == "" || len(lsh.in()) == 0 {
				return ""
			}
			return l.bin
		}
		a.Start = func() Starting {
			s := Starting{Bin: l.bin, Flag: l.flag, Plain: slices.Clone(l.plain)}
			if m := magpieModel(); m != "" {
				s.Env, s.Model, s.Args = l.vars(at.gw(), gkey()), m, slices.Clone(l.args)
			}
			return s
		}
	}
	if d.Effort {
		a.Fields = append(a.Fields, effortField(id, func() bool { return !at.cold && strings.HasPrefix(get(), magpieID+"/") }))
	}
	return atomic(a, paths...)
}

// parsed is f as a hook is given it: its JSON parsed, {} for none or
// another format.
func (f pluggedFile) parsed() any {
	var cfg any = map[string]any{}
	if f.format == "json" {
		if b, err := edit.Read(f.path); err == nil && len(b) > 0 {
			var v map[string]any
			if json.Unmarshal(jsonc.ToJSON(b), &v) == nil && v != nil {
				cfg = v
			}
		}
	}
	return cfg
}

// pluggedOwn are the agent's own models its plugin's own lists, read from
// its file as it is (and its base, bf, nil for none), cur among them;
// each says the vendor it goes to.
func pluggedOwn(p *agentplug.Agent, f pluggedFile, bf *pluggedFile, cur string) []Option {
	var base any
	if bf != nil {
		base = bf.parsed()
	}
	if strings.HasPrefix(cur, magpieID+"/") {
		cur = ""
	}
	own, _ := p.OwnModels(f.parsed(), base, cur)
	out := make([]Option, 0, len(own))
	for _, o := range own {
		m := o.Model
		if m == "" {
			m = o.Value
		}
		out = append(out, Option{Value: o.Value, Icon: modelIcon("", m), Direct: o.Direct})
	}
	return out
}

// pluggedIcon is the picture the plugin gives its agent, else the one the
// plugin market gives the plugin, else magpie's generic one.
func pluggedIcon(p *agentplug.Agent) string {
	if ic := provider.PluginOwnIcon(p.Desc.Icon); ic != "" {
		return ic
	}
	if ic := provider.MarketIcon(plugin.Icon(p.Spec, p.Desc.ID)); ic != "" {
		return ic
	}
	// magpie's glyph for one it has no picture of, not an empty square
	return "generic"
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
