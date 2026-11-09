package edit

import (
	"bytes"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// TOMLWithout is raw without the tables under the top-level key name
// ([name], [name.…] and [[name.…]]), for a reader that wants nothing
// under it. Codex's config.toml holds a [projects."…"] table for each
// project it was trusted in, the owner's 10,933 in 1.5 MB, and
// toml.Unmarshal checks each table against its siblings one by one: a
// third of a second of CPU a decode, which every reader of the file paid
// each time Codex wrote it. The parser here is a single pass. raw itself
// is given back when there is no such table or it doesn't parse, so a
// broken file stays broken for the decode that follows.
func TOMLWithout(raw []byte, name string) []byte {
	rest, _, _ := tomlUnder(raw, name, false)
	return rest
}

// tomlUnder is raw without name's tables (rest) and, when group is set,
// each of those tables' text by the key under name it is for ([name.k]
// and [name.k.…] under "k"). bare is true when a table is name itself
// ([name] or [[name]]), which no group holds. rest is raw when there is
// no such table or raw doesn't parse.
func tomlUnder(raw []byte, name string, group bool) (rest []byte, groups map[string][]byte, bare bool) {
	if !bytes.Contains(raw, []byte(name)) {
		return raw, nil, false
	}
	var p unstable.Parser
	p.Reset(raw)
	var out []byte
	kept := 0  // where the text kept since the last cut starts; -1 while cutting
	from := -1 // where the table being cut starts
	key := ""
	end := func(at int) {
		if from >= 0 && group {
			if groups == nil {
				groups = map[string][]byte{}
			}
			g := append(groups[key], raw[from:at]...)
			groups[key] = append(g, '\n')
		}
		from = -1
	}
	for p.NextExpression() {
		e := p.Expression()
		if e.Kind != unstable.Table && e.Kind != unstable.ArrayTable {
			continue
		}
		keys := e.Key()
		if !keys.Next() {
			continue
		}
		first := keys.Node()
		// a header starts its own line, after any indent
		at := bytes.LastIndexByte(raw[:first.Raw.Offset], '\n') + 1
		end(at)
		if string(first.Data) != name {
			if kept < 0 {
				kept = at
			}
			continue
		}
		if kept >= 0 {
			if out == nil {
				out = make([]byte, 0, len(raw))
			}
			out = append(out, raw[kept:at]...)
			kept = -1
		}
		from, key = at, ""
		if keys.Next() {
			key = string(keys.Node().Data)
		} else {
			bare = true
		}
	}
	if p.Error() != nil || out == nil {
		return raw, nil, false
	}
	end(len(raw))
	if kept >= 0 {
		out = append(out, raw[kept:]...)
	}
	return out, groups, bare
}

// validateTOMLDefinitions is toml.Unmarshal's check of data's definitions
// (no key or table defined twice). With many [projects."…"] tables, as a
// config of Codex's has, the rest of the file and each project's tables
// are decoded apart: they can't clash with one another, and the
// whole file at once took a third of a second (see TOMLWithout). A file
// where they could clash, or any error, is decoded whole, so the error
// is the one the whole file gives.
func validateTOMLDefinitions(data []byte) error {
	if rest, groups, bare := tomlUnder(data, "projects", true); len(groups) > 64 && !bare {
		var top map[string]any
		ok := toml.Unmarshal(rest, &top) == nil
		if _, set := top["projects"]; set {
			ok = false // a dotted projects.… key outside the tables
		}
		// projects decoded 128 at a time: few enough siblings for the
		// decoder's scan, and few decodes
		var batch []byte
		n, left := 0, len(groups)
		for _, g := range groups {
			batch, n, left = append(batch, g...), n+1, left-1
			if n == 128 || left == 0 {
				var m map[string]any // into a struct, a key defined twice passes
				if ok = ok && toml.Unmarshal(batch, &m) == nil; !ok {
					break
				}
				batch, n = batch[:0], 0
			}
		}
		if ok {
			return nil
		}
	}
	var decoded map[string]any
	return toml.Unmarshal(data, &decoded)
}
