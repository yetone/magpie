package library

// LiveAgent's MCP servers and skills, as its engine K-brain keeps them in
// its data folder (agent.liveAgentDir: $LIVEAGENT_HOME, $K_BRAIN_HOME, the
// folder ~/.liveagent/data-dir.json names, else ~/.liveagent). Read in
// K-brain 507f560 and LiveAgent af89ea7, which pins it.
//
// MCP: config.json.live-mcp.json, beside config.json (cmd/kn/backend.go
// OpenLiveManager(configFilename+".live-mcp.json", …)):
//
//	{"servers": [{"id": "fs", "enabled": true, "transport": "stdio",
//	  "command": "npx", "args": […], "env": {…}}, …], "selected": […]}
//
// a list of servers by id, each stdio or streamable HTTP ("http"; there is
// no SSE, internal/mcp/manager.go defaultTransport), plus the user's own
// description, docsUrl, cwd, timeoutMs, workspaceRoots, policy and auth,
// kept as they are. A non-empty selected is the only servers whose tools
// are used (LiveManager.ToolsFor). Until that file is there K-brain reads
// config.json's "mcp" object instead (settingsFromConfigs), so magpie's
// first write carries those servers over, as K-brain's own first save
// does. The app holds the list in memory once started and saves it whole
// (src/lib/settings/storage.ts persistSettings), so a server written while
// it runs is seen at its next start.
//
// Skills: K-brain's own folder is skills there (internal/skills/
// managed.go NewManager), and a skill in it is used only when its name is
// picked in skills-settings.json's global.selected (Manager.Effective).
// K-brain refuses a link anywhere in a skill's path (safePath) and walks
// past one (ScanDetailed's e.IsDir), so LiveAgent gets copies (Copy), and
// each copy magpie makes is picked for it once (syncLiveAgentPicks).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
)

// liveAgentMCP is the file K-brain keeps its MCP servers in, in its folder.
const liveAgentMCP = "config.json.live-mcp.json"

// liveAgentPicked is K-brain's file of the skills picked in its folder.
const liveAgentPicked = "skills-settings.json"

// liveAgentBuiltins are the skills K-brain makes in its folder itself and
// picks always (ensureBuiltins, builtinsSelected): never one the user
// brings into the library.
var liveAgentBuiltins = []string{"skills-creator", "skills-installer"}

// liveAgentSkillName is a name K-brain takes for a skill in its folder:
// lowercase letters, digits and single hyphens inside, at most 64
// (validManagedName); SaveSettings refuses a selection with another.
var liveAgentSkillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func liveAgentNameOK(name string) bool {
	return len(name) <= 64 && liveAgentSkillName.MatchString(name)
}

// liveAgentDoc is live-mcp.json: its servers in their order, each as
// written, and the rest of the file.
type liveAgentDoc struct {
	servers []json.RawMessage
	rest    map[string]json.RawMessage
	// there: the file is there; a doc made from config.json's servers
	// isn't
	there bool
}

func (d *liveAgentDoc) ids() []string {
	out := make([]string, len(d.servers))
	for i, raw := range d.servers {
		var v struct {
			ID string `json:"id"`
		}
		json.Unmarshal(raw, &v)
		out[i] = strings.TrimSpace(v.ID)
	}
	return out
}

func (d *liveAgentDoc) selected() []string {
	var out []string
	json.Unmarshal(d.rest["selected"], &out)
	return out
}

// liveAgentRead is the file at path, or, while there is none, what K-brain
// reads in its place: config.json's servers.
func liveAgentRead(path string) (*liveAgentDoc, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return liveAgentSeed(filepath.Join(filepath.Dir(path), "config.json"))
	}
	if err != nil {
		return nil, err
	}
	d := &liveAgentDoc{rest: map[string]json.RawMessage{}, there: true}
	raw = edit.TrimBOM(raw)
	if len(bytes.TrimSpace(raw)) == 0 {
		return d, nil
	}
	// K-brain reads it as plain JSON (loadLiveSettings): a file it can't
	// read isn't one magpie writes over
	if err := json.Unmarshal(raw, &d.rest); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s := d.rest["servers"]; len(s) > 0 && string(s) != "null" {
		if err := json.Unmarshal(s, &d.servers); err != nil {
			return nil, fmt.Errorf("%s: servers: %w", path, err)
		}
	}
	delete(d.rest, "servers")
	return d, nil
}

// liveAgentSeed is config.json's "mcp" servers as K-brain takes them when
// it has no live-mcp.json (mcp.settingsFromConfigs): a url is http, else
// the command's first word and its args; enabled unless it says false;
// startupTimeout's seconds as timeoutMs.
func liveAgentSeed(config string) (*liveAgentDoc, error) {
	d := &liveAgentDoc{rest: map[string]json.RawMessage{}}
	raw, err := os.ReadFile(config)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	var c struct {
		MCP map[string]struct {
			Command        []string          `json:"command"`
			Env            map[string]string `json:"env"`
			Cwd            string            `json:"cwd"`
			URL            string            `json:"url"`
			Headers        map[string]string `json:"headers"`
			Enabled        *bool             `json:"enabled"`
			StartupTimeout int               `json:"startupTimeout"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(jsonc.ToJSON(edit.TrimBOM(raw)), &c); err != nil {
		return nil, fmt.Errorf("%s: %w", config, err)
	}
	for _, id := range slices.Sorted(func(yield func(string) bool) {
		for k := range c.MCP {
			if !yield(k) {
				return
			}
		}
	}) {
		m := c.MCP[id]
		if strings.TrimSpace(id) == "" {
			continue
		}
		o := ordered{{"id", strings.TrimSpace(id)}, {"enabled", m.Enabled == nil || *m.Enabled}}
		if m.URL != "" {
			o = append(o, kv{"transport", "http"}, kv{"url", m.URL})
		} else {
			o = append(o, kv{"transport", "stdio"})
			if len(m.Command) > 0 {
				o = append(o, kv{"command", m.Command[0]})
				if len(m.Command) > 1 {
					o = append(o, kv{"args", m.Command[1:]})
				}
			}
		}
		if len(m.Env) > 0 {
			o = append(o, kv{"env", m.Env})
		}
		if m.Cwd != "" {
			o = append(o, kv{"cwd", m.Cwd})
		}
		if len(m.Headers) > 0 {
			o = append(o, kv{"headers", m.Headers})
		}
		if m.StartupTimeout > 0 {
			o = append(o, kv{"timeoutMs", m.StartupTimeout * 1000})
		}
		b, err := json.Marshal(o)
		if err != nil {
			return nil, err
		}
		d.servers = append(d.servers, b)
	}
	return d, nil
}

// write saves d at path as K-brain saves it: indented JSON, the servers
// first, the file only its owner's to read (it holds their tokens).
func (d *liveAgentDoc) write(path string) error {
	o := ordered{{"servers", d.servers}}
	if d.servers == nil {
		o[0].v = []json.RawMessage{}
	}
	keys := make([]string, 0, len(d.rest))
	for k := range d.rest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		o = append(o, kv{k, d.rest[k]})
	}
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	_, statErr := os.Stat(path)
	if err := edit.WriteAtomic(path, append(b, '\n')); err != nil {
		return err
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		return os.Chmod(path, 0o600)
	}
	return nil
}

// liveAgentEntries is each server of the file by its id, as K-brain reads
// them: the first by an id, none without one (normalizeLiveSettings).
func liveAgentEntries(path string) (map[string]map[string]any, error) {
	d, err := liveAgentRead(path)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for i, id := range d.ids() {
		if id == "" || out[id] != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(d.servers[i], &m) == nil && m != nil {
			out[id] = m
		}
	}
	return out, nil
}

// liveAgentPut writes the server o as name's, in place of the one by that
// id or after the others, and picks it where the user picks servers.
func liveAgentPut(path, name string, o ordered) error {
	d, err := liveAgentRead(path)
	if err != nil {
		return err
	}
	entry := append(ordered{{"id", name}}, slices.DeleteFunc(slices.Clone(o), func(e kv) bool { return e.k == "id" })...)
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if i := slices.Index(d.ids(), name); i >= 0 {
		d.servers[i] = b
	} else {
		d.servers = append(d.servers, b)
	}
	// an empty selected is every server; one the user narrowed gets
	// magpie's too, or it would be there with no tools
	if sel := d.selected(); len(sel) > 0 && !slices.Contains(sel, name) {
		raw, _ := json.Marshal(append(sel, name))
		d.rest["selected"] = raw
	}
	return d.write(path)
}

// liveAgentDel takes the server by that id out, and out of selected but
// for the last one there: an empty selected would be every server.
func liveAgentDel(path, name string) error {
	d, err := liveAgentRead(path)
	if err != nil {
		return err
	}
	ids := d.ids()
	if !slices.Contains(ids, name) {
		return nil
	}
	var kept []json.RawMessage
	for i, id := range ids {
		if id != name {
			kept = append(kept, d.servers[i])
		}
	}
	d.servers = kept
	if sel := d.selected(); len(sel) > 1 && slices.Contains(sel, name) {
		raw, _ := json.Marshal(slices.DeleteFunc(sel, func(s string) bool { return s == name }))
		d.rest["selected"] = raw
	}
	return d.write(path)
}

// syncLiveAgentPicks picks, in K-brain's skills-settings.json, each copy
// of the library's magpie made in LiveAgent's folder, the first time it
// is there, and takes out of the picks one magpie picked when its copy is
// gone. One the user unpicked in LiveAgent stays unpicked: magpie picks a
// skill once (Applied.Picked), not at every sync.
func (l *Library) syncLiveAgentPicks(t *Target, res *Result) {
	if t.Agent.ID != "liveagent" || t.Skills == "" || res.unreached(t) {
		return
	}
	id := t.Agent.ID
	a := l.applied(id)
	var have []string
	es, _ := os.ReadDir(t.Skills)
	for _, e := range es {
		if n := e.Name(); liveAgentNameOK(n) && ours(filepath.Join(t.Skills, n), n) {
			have = append(have, n)
		}
	}
	var add, drop []string
	for _, n := range have {
		if !slices.Contains(a.Picked, n) {
			add = append(add, n)
		}
	}
	for _, n := range a.Picked {
		if !slices.Contains(have, n) {
			drop = append(drop, n)
		}
	}
	if len(add) == 0 && len(drop) == 0 {
		return
	}
	path := filepath.Join(filepath.Dir(t.Skills), liveAgentPicked)
	if err := liveAgentPick(path, add, drop); err != nil {
		res.fail(id, "skills", err)
		return
	}
	picked := slices.DeleteFunc(slices.Clone(a.Picked), func(n string) bool { return slices.Contains(drop, n) })
	a.Picked = append(picked, add...)
	if len(a.Picked) == 0 {
		a.Picked = nil
	}
	res.changed(id)
}

// liveAgentPick adds the names to the file's global.selected and takes
// drop out of it, the rest of the file as it was, its revision one on as
// K-brain's own save makes it (Manager.SaveSettings). With no file,
// K-brain's defaults are written first: skills on, nothing picked.
func liveAgentPick(path string, add, drop []string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if len(add) == 0 {
			return nil
		}
		raw, err = []byte(`{"revision": 0, "global": {"enabled": true, "selected": []}, "projects": {}}`), nil
	}
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(edit.TrimBOM(raw), &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var global map[string]json.RawMessage
	if g := doc["global"]; len(g) > 0 && string(g) != "null" {
		if err := json.Unmarshal(g, &global); err != nil {
			return fmt.Errorf("%s: global: %w", path, err)
		}
	}
	if global == nil {
		// K-brain's default for a file without one: skills on
		global = map[string]json.RawMessage{"enabled": json.RawMessage("true")}
	}
	var sel []string
	if s := global["selected"]; len(s) > 0 && string(s) != "null" {
		if err := json.Unmarshal(s, &sel); err != nil {
			return fmt.Errorf("%s: global.selected: %w", path, err)
		}
	}
	was := slices.Clone(sel)
	sel = slices.DeleteFunc(sel, func(n string) bool { return slices.Contains(drop, n) })
	for _, n := range add {
		if !slices.Contains(sel, n) {
			sel = append(sel, n)
		}
	}
	if _, there := os.Stat(path); there == nil && slices.Equal(sel, was) {
		return nil
	}
	if sel == nil {
		sel = []string{}
	}
	global["selected"], _ = json.Marshal(sel)
	doc["global"], _ = json.Marshal(global)
	var rev uint64
	json.Unmarshal(doc["revision"], &rev)
	doc["revision"], _ = json.Marshal(rev + 1)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(path, out)
}
