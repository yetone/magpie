package library

// DeepSeek Harness has no MCP file: a server is a row of the plugin
// @deepseek-ai/dsh-mcp-client, one a server, which a patch list (a
// profile's cordis.patch.yml, or config.yaml before profiles) adds with a
// top-level insert:
//
//	- insert: # magpie
//	    - id: magpie-mcp-fs
//	      name: "@deepseek-ai/dsh-mcp-client"
//	      config:
//	        serverName: fs
//	        transport: stdio
//	        command: npx
//
// magpie writes each server as an insert of its own, marked; a row the
// user inserted is read like any other agent's entry, and one by a name
// magpie writes is taken out of the user's insert for magpie's.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/edit"
)

const dshMCPPlugin = "@deepseek-ai/dsh-mcp-client"

const dshMark = "- insert: # magpie"

// dshServerName is what dsh-mcp-client takes as a serverName.
var dshServerName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// errDshName is what the page says of a server whose name dsh can't take.
var errDshName = errors.New("DeepSeek Harness takes a server name of at most 32 letters, digits, - and _")

// dshJS is a value dsh works out when it loads (a !!js expression).
type dshJS string

func hasJS(v any) bool {
	switch x := v.(type) {
	case dshJS:
		return true
	case []any:
		return slices.ContainsFunc(x, hasJS)
	case map[string]any:
		for _, e := range x {
			if hasJS(e) {
				return true
			}
		}
	}
	return false
}

// dshPatches is a patch list, as its lines: what comes before the first
// entry, and each entry.
type dshPatches struct {
	head  []string
	items [][]string
}

// dshRow is an mcp-client row an insert adds: its item, and its lines in it.
type dshRow struct {
	item, from, to int
	alone, magpie  bool
	config         map[string]any
}

func dshRead(path string) (*dshPatches, error) {
	raw, err := edit.Read(path)
	if err != nil {
		return nil, err
	}
	p := &dshPatches{}
	if b, ok := edit.BlockList(string(raw)); ok { // [ {...} ]: read, and written back, as a block list
		raw = []byte(b)
	}
	s := strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if s == "" {
		return p, nil
	}
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "- ") || l == "-":
			p.items = append(p.items, []string{l})
		case len(p.items) == 0:
			if t != "" && !strings.HasPrefix(t, "#") && t != "[]" {
				return nil, fmt.Errorf("%s is not a list of entries magpie can edit", path)
			}
			if t != "[]" {
				p.head = append(p.head, l)
			}
		case t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(l, " "):
			return nil, fmt.Errorf("%s is not a list of entries magpie can edit", path)
		default:
			p.items[len(p.items)-1] = append(p.items[len(p.items)-1], l)
		}
	}
	return p, nil
}

func (p *dshPatches) write(path string) error {
	out := append([]string{}, p.head...)
	for _, it := range p.items {
		out = append(out, it...)
	}
	if len(p.items) == 0 {
		if _, err := os.Stat(path); err != nil {
			return nil
		}
		out = append(out, "[]") // dsh wants a list, even an empty one
	}
	return edit.WriteAtomic(path, []byte(strings.Join(out, "\n")+"\n"))
}

func mapKey(m *yaml.Node, k string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == k {
			return m.Content[i+1]
		}
	}
	return nil
}

// dshValue is the node as Go values, a !!js expression as a dshJS.
func dshValue(n *yaml.Node) any {
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			m[n.Content[i].Value] = dshValue(n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		a := []any{}
		for _, c := range n.Content {
			a = append(a, dshValue(c))
		}
		return a
	case yaml.ScalarNode:
		if n.Tag == "!!js" || n.Tag == "tag:yaml.org,2002:js" {
			return dshJS(n.Value)
		}
	}
	var v any
	n.Decode(&v)
	return v
}

// rows are the mcp-client rows of the list's top-level inserts, by
// serverName.
func (p *dshPatches) rows() map[string]dshRow {
	out := map[string]dshRow{}
	for i, it := range p.items {
		var doc yaml.Node
		if yaml.Unmarshal([]byte(strings.Join(it, "\n")), &doc) != nil || len(doc.Content) == 0 {
			continue
		}
		seq := doc.Content[0]
		if seq.Kind != yaml.SequenceNode || len(seq.Content) != 1 {
			continue
		}
		patch := seq.Content[0]
		ins := mapKey(patch, "insert")
		if ins == nil || ins.Kind != yaml.SequenceNode || mapKey(patch, "id") != nil {
			continue
		}
		for j, r := range ins.Content {
			name, cfg := mapKey(r, "name"), mapKey(r, "config")
			if name == nil || name.Value != dshMCPPlugin || cfg == nil || cfg.Kind != yaml.MappingNode {
				continue
			}
			sn := mapKey(cfg, "serverName")
			if sn == nil || sn.Value == "" {
				continue
			}
			row := dshRow{item: i, from: r.Line - 1, to: len(it), alone: len(ins.Content) == 1,
				magpie: strings.TrimSpace(it[0]) == dshMark}
			if j+1 < len(ins.Content) {
				row.to = ins.Content[j+1].Line - 1
			}
			// a row that starts on the insert's line or shares one is written
			// flow style: only taken out whole
			if !row.alone && (row.from <= 0 || row.to <= row.from) {
				row.from, row.to = -1, -1
			}
			row.config, _ = dshValue(cfg).(map[string]any)
			out[sn.Value] = row
		}
	}
	return out
}

// take takes the row out: its insert with it when it was the only one.
func (p *dshPatches) take(r dshRow, name string) error {
	switch {
	case r.alone:
		p.items = slices.Delete(p.items, r.item, r.item+1)
	case r.from < 0:
		return fmt.Errorf("the insert with DeepSeek Harness's server %s is written on one line; take it out there", name)
	default:
		p.items[r.item] = slices.Delete(p.items[r.item], r.from, r.to)
	}
	return nil
}

func dshEntries(path string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	p, err := dshRead(path)
	if err != nil {
		return nil, err
	}
	for name, r := range p.rows() {
		if r.config != nil {
			out[name] = r.config
		}
	}
	return out, nil
}

// dshPut writes the server as magpie's insert, in place of magpie's one
// by that name, or of the user's row, which leaves the user's insert.
func dshPut(path, name string, o ordered) error {
	p, err := dshRead(path)
	if err != nil {
		return err
	}
	lines := []string{
		dshMark,
		"    - id: magpie-mcp-" + name,
		"      name: " + dshScalar(dshMCPPlugin),
		"      config:",
	}
	for _, e := range o {
		lines = append(lines, dshYAML("        ", e.k, e.v)...)
	}
	r, ok := p.rows()[name]
	switch {
	case ok && r.magpie && r.alone:
		p.items[r.item] = lines
	case ok:
		if err := p.take(r, name); err != nil {
			return err
		}
		fallthrough
	default:
		p.items = append(p.items, lines)
	}
	return p.write(path)
}

func dshDel(path, name string) error {
	p, err := dshRead(path)
	if err != nil {
		return err
	}
	r, ok := p.rows()[name]
	if !ok {
		return nil
	}
	if err := p.take(r, name); err != nil {
		return err
	}
	return p.write(path)
}

// dshScalar is a string as YAML reads it back: JSON's quoting, which YAML
// shares.
func dshScalar(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSpace(b.String())
}

var dshPlain = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// dshKey is a key bare where YAML reads it so, quoted where not.
func dshKey(k string) string {
	if dshPlain.MatchString(k) && !slices.Contains([]string{"true", "false", "null", "yes", "no", "on", "off", "y", "n"}, strings.ToLower(k)) {
		return k
	}
	return dshScalar(k)
}

// dshYAML is k: v at the indent, in block style so that a !!js value the
// user gave can be written back as one.
func dshYAML(indent, k string, v any) []string {
	key := indent + dshKey(k) + ":"
	switch x := v.(type) {
	case dshJS:
		return []string{key + " !!js " + dshScalar(string(x))}
	case string:
		return []string{key + " " + dshScalar(x)}
	case map[string]string:
		m := map[string]any{}
		for k, s := range x {
			m[k] = s
		}
		return dshYAML(indent, k, m)
	case map[string]any:
		if len(x) == 0 {
			return []string{key + " {}"}
		}
		out := []string{key}
		for _, sub := range slices.Sorted(maps.Keys(x)) {
			out = append(out, dshYAML(indent+"  ", sub, x[sub])...)
		}
		return out
	case []any:
		if !hasJS(x) {
			break
		}
		out := []string{key}
		for _, e := range x {
			if js, ok := e.(dshJS); ok {
				out = append(out, indent+"  - !!js "+dshScalar(string(js)))
			} else {
				b, _ := json.Marshal(e)
				out = append(out, indent+"  - "+string(b))
			}
		}
		return out
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return []string{key + " " + strings.TrimSpace(b.String())}
}
