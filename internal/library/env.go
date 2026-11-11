package library

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
)

// Environment variables (paopao_baobao on X) are set once in the library
// and written into each agent ticked for them, in the place that agent
// keeps variables it sets for what it runs:
//
//	Claude Code  <claude dir>/settings.json  env: {NAME: "value"} (its settings docs: "Environment variables
//	             that will be applied to every session")
//	Codex        <codex home>/config.toml    [shell_environment_policy.set] NAME = "value" (its config docs:
//	             "set" is the variables forced into the environment of every command it runs)
//
// What magpie wrote is recorded name and value (Applied.Env). A variable
// the file already has is the user's: another value is never written over
// it, and one the user changed after magpie wrote it is theirs from then
// on. Turning a variable off takes out only what is still as magpie wrote
// it.

// EnvVar is a variable the agents ticked for it are given.
type EnvVar struct {
	Name   string   `json:"name"`
	Value  string   `json:"value"`
	Agents []string `json:"agents"`
}

// EnvEntry is a variable magpie wrote into an agent, with its value.
type EnvEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// envAgents are the agents magpie can give variables to.
var envAgents = []string{"claude", "codex"}

const (
	maxEnvName  = 128
	maxEnvValue = 8192
)

var envName = lazyre.New(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (e *EnvVar) check() error {
	if !envName.MatchString(e.Name) || len(e.Name) > maxEnvName {
		return fmt.Errorf("%q isn't a variable name: letters, digits and _, not starting with a digit", e.Name)
	}
	if strings.ContainsRune(e.Value, 0) || !utf8.ValidString(e.Value) || len(e.Value) > maxEnvValue {
		return fmt.Errorf("%s has a value that can't be written into an agent's settings", e.Name)
	}
	for _, a := range e.Agents {
		if !slices.Contains(envAgents, a) {
			return fmt.Errorf("magpie gives variables to Claude Code and Codex, not %s", a)
		}
	}
	return nil
}

func (l *Library) env(name string) *EnvVar {
	for _, e := range l.Env {
		if e.Name == name {
			return e
		}
	}
	return nil
}

// envFile is where t keeps the variables it sets, or why magpie can't give
// it any.
func envFile(t *Target) (string, string) {
	switch {
	case !slices.Contains(envAgents, t.Agent.ID):
		return "", fmt.Sprintf("%s has no setting for variables magpie can write", t.Agent.Name)
	case t.Agent.WSL != "":
		return "", fmt.Sprintf("magpie gives variables only to agents on this computer, not yet to %s in WSL", t.Agent.Name)
	case t.Agent.ID == "claude":
		return filepath.Join(claudeDir(), "settings.json"), ""
	}
	return filepath.Join(codexDir(), "config.toml"), ""
}

// the table Codex's variables are in
const codexEnvTable = "shell_environment_policy"

// readEnv is the variables path holds, in the setting this agent keeps
// them in. A file that can't be read as that is an error, never "none".
func readEnv(id, path string) (map[string]string, error) {
	out := map[string]string{}
	if id == "claude" {
		doc, err := readHooksFile(path)
		if err != nil {
			return nil, err
		}
		e := gjson.GetBytes(doc, "env")
		if !e.Exists() {
			return out, nil
		}
		if !e.IsObject() {
			return nil, fmt.Errorf("%s has an env that isn't an object: it is left as it is", path)
		}
		e.ForEach(func(k, v gjson.Result) bool {
			out[k.String()] = v.String()
			return true
		})
		return out, nil
	}
	raw, err := edit.Read(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(edit.TrimBOM(raw), &doc); err != nil {
		return nil, fmt.Errorf("%s isn't TOML magpie can read: it is left as it is", path)
	}
	pol, ok := doc[codexEnvTable]
	if !ok {
		return out, nil
	}
	pm, ok := pol.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s has a %s that isn't a table: it is left as it is", path, codexEnvTable)
	}
	s, ok := pm["set"]
	if !ok {
		return out, nil
	}
	sm, ok := s.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s has a %s.set that isn't a table: it is left as it is", path, codexEnvTable)
	}
	for k, v := range sm {
		out[k] = fmt.Sprint(v)
	}
	return out, nil
}

// codexHasSet is whether the file has a shell_environment_policy.set, in
// any spelling.
func codexHasSet(path string) bool {
	raw, err := edit.Read(path)
	if err != nil {
		return false
	}
	var doc struct {
		Policy map[string]any `toml:"shell_environment_policy"`
	}
	if toml.Unmarshal(edit.TrimBOM(raw), &doc) != nil {
		return false
	}
	_, ok := doc.Policy["set"]
	return ok
}

// writeEnv sets and takes out variables where the agent keeps them,
// leaving the rest of the file as it is.
func writeEnv(id, path string, set map[string]string, del []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if id == "claude" {
		raw, err := edit.Read(path)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = []byte("{}\n")
		}
		var kvs []edit.KV
		for _, k := range slices.Sorted(maps.Keys(set)) {
			kvs = append(kvs, edit.KV{Path: "env." + k, Value: set[k]})
		}
		var dels []string
		for _, k := range del {
			dels = append(dels, "env."+k)
		}
		out, err := edit.PatchJSON(raw, kvs, dels)
		if err == nil && len(del) > 0 && len(set) == 0 {
			// an env left empty by magpie goes with what it held
			if e := gjson.GetBytes(jsonc.ToJSON(edit.TrimBOM(out)), "env"); e.IsObject() && len(e.Map()) == 0 {
				out, err = edit.PatchJSON(out, nil, []string{"env"})
			}
		}
		if err != nil {
			return err
		}
		return edit.WriteAtomic(path, out)
	}
	// Codex: [shell_environment_policy.set] is a table of its own, unless
	// the user wrote `set` inline in [shell_environment_policy], which is
	// then rewritten as the one key it is
	tables, err := edit.TOMLTables(path)
	if err != nil {
		return err
	}
	sub := codexEnvTable + ".set"
	if !slices.Contains(tables, sub) {
		cur, err := readEnv(id, path)
		if err != nil {
			return err
		}
		if codexHasSet(path) {
			keys, err := edit.GetTOMLTable(path, codexEnvTable)
			if err != nil || !slices.Contains(tables, codexEnvTable) {
				return fmt.Errorf("%s has its set variables in a spelling magpie doesn't edit: it is left as it is", path)
			}
			for k := range keys {
				if strings.HasPrefix(k, "set.") {
					return fmt.Errorf("%s has its set variables as dotted keys, which magpie doesn't edit: it is left as it is", path)
				}
			}
			for _, k := range del {
				delete(cur, k)
			}
			maps.Copy(cur, set)
			var in edit.Inline
			for _, k := range slices.Sorted(maps.Keys(cur)) {
				in = append(in, edit.KV{Path: k, Value: cur[k]})
			}
			return edit.SetTOMLKey(path, codexEnvTable, "set", in)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		if err := edit.SetTOMLKey(path, sub, k, set[k]); err != nil {
			return err
		}
	}
	for _, k := range del {
		if err := edit.DelTOMLKey(path, sub, k); err != nil {
			return err
		}
	}
	return nil
}

func (l *Library) syncEnv(t *Target, b *backups, res *Result) {
	id := t.Agent.ID
	a := l.applied(id)
	want := map[string]string{}
	for _, e := range l.Env {
		// no value is a variable carried without its key: nothing to give
		if slices.Contains(e.Agents, id) && e.Value != "" {
			want[e.Name] = e.Value
		}
	}
	if len(a.Env) == 0 && len(want) == 0 {
		return
	}
	path, why := envFile(t)
	if path == "" {
		for _, n := range slices.Sorted(maps.Keys(want)) {
			res.fail(id, "env:"+n, errors.New(why))
		}
		return
	}
	failAll := func(err error) {
		for _, n := range slices.Sorted(maps.Keys(want)) {
			res.fail(id, "env:"+n, err)
		}
		if len(want) == 0 {
			res.fail(id, "env", err)
		}
	}
	cur, err := readEnv(id, path)
	if err != nil {
		failAll(err)
		return
	}
	var kept []EnvEntry
	set := map[string]string{}
	var del []string
	mine := map[string]bool{} // names whose record stands, or whose value magpie is writing
	for _, rec := range a.Env {
		v, there := cur[rec.Name]
		switch w, wanted := want[rec.Name]; {
		case !there || v != rec.Value:
			// gone, or changed by the user: theirs now
		case wanted && w == rec.Value:
			kept = append(kept, rec)
			mine[rec.Name] = true
		case wanted:
			set[rec.Name] = w
			kept = append(kept, EnvEntry{Name: rec.Name, Value: w})
			mine[rec.Name] = true
		default:
			del = append(del, rec.Name)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(want)) {
		if mine[n] {
			continue
		}
		if v, there := cur[n]; there {
			// the user's own: the same value is no change, another is
			// never written over it
			if v != want[n] {
				res.fail(id, "env:"+n, fmt.Errorf("%s already sets %s to another value: magpie leaves it as it is", path, n))
			}
			continue
		}
		if t.Gone {
			continue
		}
		set[n] = want[n]
		kept = append(kept, EnvEntry{Name: n, Value: want[n]})
	}
	if len(set) == 0 && len(del) == 0 {
		a.Env = kept
		return
	}
	if err := b.keep(id, path); err != nil {
		failAll(err)
		return
	}
	if err := writeEnv(id, path, set, del); err != nil {
		failAll(fmt.Errorf("%s: %w", path, err))
		return
	}
	res.changed(id)
	a.Env = kept
}

// ---- changes ---------------------------------------------------------------

// SaveEnv adds a variable, or replaces the one called old (renaming it).
func SaveEnv(old string, e EnvVar) (*Result, error) {
	if err := e.check(); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if e.Name != old && l.env(e.Name) != nil {
			return fmt.Errorf("the library already has a variable called %s", e.Name)
		}
		if old != "" {
			i := slices.IndexFunc(l.Env, func(x *EnvVar) bool { return x.Name == old })
			if i < 0 {
				return fmt.Errorf("no variable called %s", old)
			}
			l.Env = slices.Delete(l.Env, i, i+1)
		}
		e.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(e.Agents))))
		l.Env = append(l.Env, &e)
		return nil
	})
}

// EnvAgents sets which agents get a variable.
func EnvAgents(name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		e := l.env(name)
		if e == nil {
			return fmt.Errorf("no variable called %s", name)
		}
		c := *e
		c.Agents = agents
		if err := c.check(); err != nil {
			return err
		}
		e.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(agents))))
		return nil
	})
}

// RemoveEnv takes a variable out of the library, and what magpie wrote of
// it out of every agent.
func RemoveEnv(name string) (*Result, error) {
	return change(func(l *Library) error {
		if l.env(name) == nil {
			return fmt.Errorf("no variable called %s", name)
		}
		l.Env = slices.DeleteFunc(l.Env, func(e *EnvVar) bool { return e.Name == name })
		return nil
	})
}

// ---- the page --------------------------------------------------------------

// EnvView is a library variable, and why an agent couldn't be given it.
type EnvView struct {
	*EnvVar
	Problems map[string]string `json:"problems,omitempty"`
}

// EnvAgentView is an agent the Variables tab lists: File is where its
// variables are, No why magpie can't give it any.
type EnvAgentView struct {
	ID   string `json:"id"`
	File string `json:"file,omitempty"`
	No   string `json:"no,omitempty"`
	WSL  bool   `json:"wsl,omitempty"`
}

func (l *Library) envView(targets []*Target, of func(string) map[string]string) ([]EnvView, []EnvAgentView) {
	agents := []EnvAgentView{}
	for _, t := range targets {
		f, why := envFile(t)
		agents = append(agents, EnvAgentView{ID: t.Agent.ID, File: f, No: why, WSL: t.Agent.WSL != ""})
	}
	vars := []EnvView{}
	for _, e := range l.Env {
		c := *e
		c.Agents = orNone(c.Agents)
		ev := EnvView{EnvVar: &c, Problems: of("env:" + e.Name)}
		for _, av := range agents {
			if av.No != "" && slices.Contains(e.Agents, av.ID) {
				if ev.Problems == nil {
					ev.Problems = map[string]string{}
				}
				if ev.Problems[av.ID] == "" {
					ev.Problems[av.ID] = av.No
				}
			}
		}
		vars = append(vars, ev)
	}
	return vars, agents
}
