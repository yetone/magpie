package edit

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/lazyre"
)

// A TOML file edited by key path, as SetJSON and SetYAML edit theirs: the
// path's last part is a key of the table its other parts name (a key at
// the top level for a path of one part), and a value that is an object is
// a table of its own. "model_providers.magpie.base_url" is base_url in
// [model_providers.magpie]; "model_providers.magpie" set to an object is
// that table, written whole, while the file's other tables, keys and
// comments stay as they are. A path's part may hold a '.' escaped as
// gjson.Escape writes it. Every edit is made on the file as read, checked
// to be TOML, and written in one temp-and-rename (writeTOML); a file that
// doesn't parse is never written.

// TOMLText is a value as a TOML file has it written, from GetTOMLText, for
// SetTOML to put back as it was: a key's value from '=' to its end, or a
// table's lines under its header (Table), comments included.
type TOMLText struct {
	Text  string
	Table bool
}

// tomlBare is a key TOML takes without quotes.
var tomlBare = lazyre.New(`^[A-Za-z0-9_-]+$`)

// tomlKeySpelled is key as a TOML file spells it: bare where it can be.
func tomlKeySpelled(key string) string {
	if tomlBare.MatchString(key) {
		return key
	}
	return tomlString(key)
}

// tomlPathParts is keyPath's parts, each the key it names.
func tomlPathParts(keyPath string) []string {
	parts := splitPath(keyPath)
	for i, p := range parts {
		parts[i] = unescapeKey(p)
	}
	return parts
}

// tomlHeader is the header name of the table parts name.
func tomlHeader(parts []string) string {
	spelled := make([]string, len(parts))
	for i, p := range parts {
		spelled[i] = tomlKeySpelled(p)
	}
	return strings.Join(spelled, ".")
}

// tomlAt is where a key path is in a parsed file: the key in its table
// (in, the table's index, -1 for the top level), or the ordinary table
// the whole path names.
type tomlAt struct {
	parent  int // the table the key is in, -1 for the top level, -2 for none
	key     int // the key in it, -1 for none
	table   int // the table the path names, -1 for none
	keyName string
	header  string // the parent's header, for a key to be added
}

func (a tomlAt) keySpan(doc tomlFile) (tomlKeySpan, bool) {
	switch {
	case a.key < 0:
		return tomlKeySpan{}, false
	case a.parent == -1:
		return doc.root.keys[a.key], true
	}
	return doc.tables[a.parent].keys[a.key], true
}

// tomlFind finds parts in doc. A table is matched by the keys its header
// names, however it quotes them; a key by its name in its table.
func tomlFind(doc tomlFile, parts []string) tomlAt {
	at := tomlAt{parent: -2, key: -1, table: -1, keyName: parts[len(parts)-1], header: tomlHeader(parts[:len(parts)-1])}
	parentParts := parts[:len(parts)-1]
	if len(parentParts) == 0 {
		at.parent = -1
	}
	for i := range doc.tables {
		t := &doc.tables[i]
		if t.array {
			continue
		}
		hp := tomlHeaderParts(t.name)
		if at.parent == -2 && slices.Equal(hp, parentParts) {
			at.parent = i
		}
		if at.table < 0 && slices.Equal(hp, parts) {
			at.table = i
		}
	}
	var keys []tomlKeySpan
	switch {
	case at.parent == -1:
		keys = doc.root.keys
	case at.parent >= 0:
		keys = doc.tables[at.parent].keys
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if keys[i].name == at.keyName {
			at.key = i
			break
		}
	}
	return at
}

// GetTOML reads a key path from a TOML file: a string decoded, any other
// value as written; a table its lines as written. A file that doesn't
// parse reads as having nothing.
func GetTOML(path, keyPath string) (string, bool) {
	t, ok := GetTOMLText(path, keyPath)
	if !ok || t.Table {
		return t.Text, ok
	}
	doc, _ := tomlFileOf(path)
	at := tomlFind(doc, tomlPathParts(keyPath))
	if kv, ok := at.keySpan(doc); ok && kv.scalar {
		return kv.value, true
	}
	return t.Text, true
}

// GetTOMLText reads the value at a key path as the file has it written,
// for SetTOML to put back so (see TOMLText). A file that doesn't parse
// reads as having nothing.
func GetTOMLText(path, keyPath string) (TOMLText, bool) {
	doc, err := tomlFileOf(path)
	if err != nil {
		return TOMLText{}, false
	}
	at := tomlFind(doc, tomlPathParts(keyPath))
	if kv, ok := at.keySpan(doc); ok {
		return TOMLText{Text: kv.text}, true
	}
	if at.table < 0 {
		return TOMLText{}, false
	}
	raw, err := readHeld(path)
	if err != nil {
		return TOMLText{}, false
	}
	lines := splitLines(string(raw))
	t := doc.tables[at.table]
	body := trimBlank(withoutNotes(slices.Clone(lines[t.from+1 : min(t.to, len(lines))])))
	return TOMLText{Text: strings.Join(body, "\n"), Table: true}, true
}

// SetTOML sets key paths in a TOML file, in one write. A value may be a
// string, a number, a boolean, a slice or map of them, JSON text (a
// json.RawMessage, its objects' keys in the order it has them) or a
// TOMLText; nil takes the key out, TOML having no null. An object is a
// table of its own ([path]), written whole, unless the file has the key
// as a value in its table, which then becomes an inline table; an object
// inside one is an inline table. A missing file is made.
func SetTOML(path string, kvs ...KV) error {
	return editTOMLPaths(path, kvs, nil)
}

// DelTOML takes key paths out of a TOML file, in one write: a key, and
// its table with it when nothing is left in it, or the table the path
// names, its own subtables left where they are. A missing file stays
// missing.
func DelTOML(path string, keyPaths ...string) error {
	if raw, err := Read(path); err != nil || raw == nil {
		return err
	}
	return editTOMLPaths(path, nil, keyPaths)
}

func editTOMLPaths(path string, kvs []KV, del []string) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	orig := splitLines(string(raw))
	lines := slices.Clone(orig)
	if _, err := parseTOMLFile(lines); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, k := range del {
		if lines, err = delTOMLPath(lines, tomlPathParts(k)); err != nil {
			return fmt.Errorf("%s: %s: %w", path, k, err)
		}
	}
	for _, kv := range kvs {
		parts := tomlPathParts(kv.Path)
		if kv.Value == nil {
			lines, err = delTOMLPath(lines, parts)
		} else {
			lines, err = setTOMLPath(lines, parts, kv.Value)
		}
		if err != nil {
			return fmt.Errorf("%s: %s: %w", path, kv.Path, err)
		}
	}
	if slices.Equal(lines, orig) {
		return nil
	}
	return writeTOML(path, lines)
}

// setTOMLPath is lines with parts set to v.
func setTOMLPath(lines []string, parts []string, v any) ([]string, error) {
	doc, err := parseTOMLFile(lines)
	if err != nil {
		return nil, err
	}
	at := tomlFind(doc, parts)
	var literal string
	var body []string
	table := false
	switch x := v.(type) {
	case TOMLText:
		table = x.Table
		if table {
			body = trimBlank(splitLines(x.Text))
			if len(body) == 1 && body[0] == "" {
				body = nil
			}
		} else {
			literal = x.Text
		}
	default:
		r, err := tomlJSONOf(v)
		if err != nil {
			return nil, err
		}
		if r.IsObject() && at.key < 0 {
			table = true
			if body, err = tomlTableBody(r); err != nil {
				return nil, err
			}
		} else if literal, err = tomlOfJSON(r); err != nil {
			return nil, err
		}
	}
	if !table {
		if kv, ok := at.keySpan(doc); ok {
			// replaced where it is, its spacing and comment kept
			line := kv.prefix + literal + kv.suffix
			return slices.Concat(lines[:kv.from], []string{line}, lines[kv.to:]), nil
		}
		spelled := tomlKeySpelled(at.keyName)
		if at.parent == -1 {
			i := 0
			if n := len(doc.root.keys); n > 0 {
				i = doc.root.keys[n-1].to
			}
			return slices.Concat(lines[:i], []string{spelled + " = " + literal}, lines[i:]), nil
		}
		if at.parent == -2 && slices.ContainsFunc(doc.tables, func(t tomlTableSpan) bool {
			return t.array && slices.Equal(tomlHeaderParts(t.name), parts[:len(parts)-1])
		}) {
			return nil, fmt.Errorf("%q is an array of tables", at.header)
		}
		return setTOMLKeyLines(lines, at.header, at.keyName, spelled, Raw(literal))
	}
	if at.table >= 0 {
		t := doc.tables[at.table]
		out := slices.Concat(lines[:t.from+1], body)
		if t.to < len(lines) && strings.TrimSpace(lines[t.to]) != "" {
			out = append(out, "")
		}
		return append(out, lines[t.to:]...), nil
	}
	out := trimBlank(slices.Clone(lines))
	if len(out) > 0 {
		out = append(out, "")
	}
	out = append(out, "["+tomlHeader(parts)+"]")
	return append(append(out, body...), ""), nil
}

// delTOMLPath is lines without parts: its key, and its table with it when
// nothing else is left there, or the table it names.
func delTOMLPath(lines []string, parts []string) ([]string, error) {
	doc, err := parseTOMLFile(lines)
	if err != nil {
		return nil, err
	}
	at := tomlFind(doc, parts)
	if kv, ok := at.keySpan(doc); ok {
		from := kv.from
		// magpie's note over it goes with it
		for from > 0 && IsNote(lines[from-1]) {
			from--
		}
		out := slices.Concat(lines[:from], lines[kv.to:])
		if at.parent < 0 {
			return out, nil
		}
		t := doc.tables[at.parent]
		gone := kv.to - from
		for _, l := range out[t.from+1 : t.to-gone] {
			if strings.TrimSpace(l) != "" {
				return out, nil
			}
		}
		// the table is left empty
		return delTOMLBlock(out, t.from, t.to-gone), nil
	}
	if at.table >= 0 {
		t := doc.tables[at.table]
		return delTOMLBlock(lines, t.from, t.to), nil
	}
	return lines, nil
}

// delTOMLBlock is lines without [from, to), a table's header and body,
// and the blank lines before it, as DelTOMLTable takes one out.
func delTOMLBlock(lines []string, from, to int) []string {
	for from > 0 && strings.TrimSpace(lines[from-1]) == "" {
		from--
	}
	out := slices.Clone(lines[:from])
	if to < len(lines) && len(out) > 0 && strings.TrimSpace(lines[to]) != "" {
		out = append(out, "")
	}
	return append(out, lines[to:]...)
}

// tomlJSONOf is v as JSON, parsed in its own order.
func tomlJSONOf(v any) (gjson.Result, error) {
	var b []byte
	switch x := v.(type) {
	case json.RawMessage:
		b = x
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			return gjson.Result{}, err
		}
	}
	if !gjson.ValidBytes(b) {
		return gjson.Result{}, fmt.Errorf("not JSON: %q", string(b))
	}
	return gjson.ParseBytes(b), nil
}

// tomlTableBody is an object's members as a table's lines, each key = its
// value, an object inside an inline table.
func tomlTableBody(r gjson.Result) ([]string, error) {
	var out []string
	var err error
	r.ForEach(func(k, v gjson.Result) bool {
		if v.Type == gjson.Null {
			return true
		}
		var lit string
		if lit, err = tomlOfJSON(v); err != nil {
			return false
		}
		out = append(out, tomlKeySpelled(k.String())+" = "+lit)
		return true
	})
	return out, err
}

// tomlOfJSON is a JSON value as a TOML value's text: an object an inline
// table, in the order JSON has its keys.
func tomlOfJSON(r gjson.Result) (string, error) {
	switch r.Type {
	case gjson.String:
		return tomlString(r.Str), nil
	case gjson.True, gjson.False:
		return strconv.FormatBool(r.Bool()), nil
	case gjson.Number:
		if _, err := strconv.ParseInt(r.Raw, 10, 64); err == nil {
			return r.Raw, nil
		}
		f, err := strconv.ParseFloat(r.Raw, 64)
		if err != nil {
			return "", fmt.Errorf("number %s", r.Raw)
		}
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eEn") {
			s += ".0"
		}
		return s, nil
	case gjson.Null:
		return "", fmt.Errorf("TOML has no null")
	}
	var parts []string
	var err error
	if r.IsArray() {
		for _, e := range r.Array() {
			var s string
			if s, err = tomlOfJSON(e); err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	}
	r.ForEach(func(k, v gjson.Result) bool {
		if v.Type == gjson.Null {
			return true
		}
		var s string
		if s, err = tomlOfJSON(v); err != nil {
			return false
		}
		parts = append(parts, tomlKeySpelled(k.String())+" = "+s)
		return true
	})
	if err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "{}", nil
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}
