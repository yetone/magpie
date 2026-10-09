package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
)

// KV is one key path (dot-separated, e.g. "model.name") and its new value.
type KV struct {
	Path  string
	Value any
}

// GetJSON reads a dot-separated key path from a JSON or JSONC file.
func GetJSON(path, keyPath string) (string, bool) {
	raw, err := Read(path)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	r := gjson.GetBytes(jsonc.ToJSONInPlace(raw), keyPath)
	if !r.Exists() {
		return "", false
	}
	return r.String(), true
}

// SetJSON sets one or more key paths in a JSON/JSONC file, preserving
// comments, key order and indentation. Missing files and parents are created.
func SetJSON(path string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}\n")
	}
	orig := raw
	for _, kv := range kvs {
		raw, err = setJSONBytes(raw, kv.Path, kv.Value)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return WriteAtomic(path, keepCRLF(raw, orig))
}

// keepCRLF is out, an edit of orig, with \r\n at every line break when orig
// used \r\n for all of them: the lines an edit adds are built with \n, and a
// file that mixed endings is left as it was.
func keepCRLF(out, orig []byte) []byte {
	n := bytes.Count(orig, []byte("\n"))
	if n == 0 || bytes.Count(orig, []byte("\r\n")) != n {
		return out
	}
	out = bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
}

// PatchJSON computes a config change without writing a file.
func PatchJSON(raw []byte, set []KV, del []string) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}\n")
	}
	orig := raw
	var err error
	for _, kv := range set {
		raw, err = setJSONBytes(raw, kv.Path, kv.Value)
		if err != nil {
			return nil, err
		}
	}
	for _, key := range del {
		raw, _, err = delJSONBytes(raw, key)
		if err != nil {
			return nil, err
		}
	}
	return keepCRLF(raw, orig), nil
}

func setJSONBytes(raw []byte, keyPath string, value any) ([]byte, error) {
	// Comments are blanked out in a copy of identical length so gjson offsets
	// map 1:1 back onto the original bytes.
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	if !json.Valid(stripped) {
		return nil, fmt.Errorf("invalid JSON/JSONC")
	}
	root := gjson.ParseBytes(stripped)
	if !root.IsObject() {
		return nil, fmt.Errorf("top level is not a JSON object")
	}
	parts := splitPath(keyPath)
	for i := len(parts); i >= 1; i-- {
		r := gjson.GetBytes(stripped, strings.Join(parts[:i], "."))
		if !r.Exists() {
			continue
		}
		if i == len(parts) || !r.IsObject() {
			// Exact key exists (replace value), or an ancestor exists but is a
			// scalar (replace it with the nested object we need).
			return splice(raw, r.Index, len(r.Raw), marshalAt(raw, r.Index, nest(parts[i:], value)))
		}
		return insertKey(raw, stripped, r.Index, unescapeKey(parts[i]), nest(parts[i+1:], value))
	}
	return insertKey(raw, stripped, 0, unescapeKey(parts[0]), nest(parts[1:], value))
}

func nest(parts []string, value any) any {
	for i := len(parts) - 1; i >= 0; i-- {
		value = map[string]any{unescapeKey(parts[i]): value}
	}
	return value
}

// splitPath splits a key path at its dots, as gjson does: a dot escaped
// with a backslash is part of the key (VS Code's settings.json keeps
// "chat.defaultModel" as one key). The parts keep their escapes, for gjson.
func splitPath(keyPath string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(keyPath); i++ {
		switch keyPath[i] {
		case '\\':
			i++
		case '.':
			parts = append(parts, keyPath[start:i])
			start = i + 1
		}
	}
	return append(parts, keyPath[start:])
}

// unescapeKey is a key path's part as the key it names.
func unescapeKey(part string) string { return strings.ReplaceAll(part, `\.`, ".") }

func splice(raw []byte, at, n int, with []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw)-n+len(with))
	out = append(out, raw[:at]...)
	out = append(out, with...)
	out = append(out, raw[at+n:]...)
	return out, nil
}

// insertKey adds key: value as the first member of the object that starts
// at or after position from.
func insertKey(raw, stripped []byte, from int, key string, value any) ([]byte, error) {
	open := from
	for open < len(raw) && stripped[open] != '{' {
		open++
	}
	close := matchBrace(stripped, open)
	if open >= len(raw) || close < 0 {
		return nil, fmt.Errorf("cannot locate object for %q", key)
	}
	k, _ := json.Marshal(key)
	pretty := bytes.IndexByte(raw, '\n') >= 0
	empty := len(bytes.TrimSpace(raw[open+1:close])) == 0

	if !pretty {
		v, _ := json.Marshal(value)
		ins := string(k) + ":" + string(v)
		if !empty {
			ins += ","
		}
		return splice(raw, open+1, 0, []byte(ins))
	}

	base := lineIndent(raw, open)
	unit := indentUnit(raw, open, base)
	child := base + unit
	v, _ := json.MarshalIndent(value, child, unit)
	kv := child + string(k) + ": " + string(v)
	if empty {
		return splice(raw, open, close-open+1, []byte("{\n"+kv+"\n"+base+"}"))
	}
	return splice(raw, open+1, 0, []byte("\n"+kv+","))
}

// marshalAt renders value with indentation matching the line that holds pos.
func marshalAt(raw []byte, pos int, value any) []byte {
	if bytes.IndexByte(raw, '\n') < 0 {
		b, _ := json.Marshal(value)
		return b
	}
	base := lineIndent(raw, pos)
	b, _ := json.MarshalIndent(value, base, indentUnit(raw, pos, base))
	return b
}

func lineIndent(raw []byte, pos int) string {
	start := bytes.LastIndexByte(raw[:pos], '\n') + 1
	i := start
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
		i++
	}
	return string(raw[start:i])
}

// indentUnit guesses the indentation step from the line following pos,
// falling back to two spaces (or a tab when the file uses tabs).
func indentUnit(raw []byte, pos int, base string) string {
	if nl := bytes.IndexByte(raw[pos:], '\n'); nl >= 0 {
		next := lineIndent(raw, pos+nl+1)
		if len(next) > len(base) && strings.HasPrefix(next, base) {
			return next[len(base):]
		}
	}
	if strings.Contains(base, "\t") || bytes.Contains(raw, []byte("\n\t")) {
		return "\t"
	}
	return "  "
}

// matchBrace returns the index of the '}' closing the '{' at open, skipping
// over string literals. It expects comment-free input.
func matchBrace(b []byte, open int) int {
	depth := 0
	inStr := false
	for i := open; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// DelJSON removes key paths from a JSON/JSONC file, preserving everything
// else. Missing keys are ignored; an object left empty collapses to {}.
func DelJSON(path string, keyPaths ...string) error {
	raw, err := Read(path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return err
	}
	changed := false
	for _, kp := range keyPaths {
		out, ok, err := delJSONBytes(raw, kp)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if ok {
			raw, changed = out, true
		}
	}
	if !changed {
		return nil
	}
	return WriteAtomic(path, raw)
}

func delJSONBytes(raw []byte, keyPath string) ([]byte, bool, error) {
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	if !json.Valid(stripped) {
		return nil, false, fmt.Errorf("invalid JSON/JSONC")
	}
	r := gjson.GetBytes(stripped, keyPath)
	if !r.Exists() || r.Index == 0 {
		return raw, false, nil
	}
	// value span → back over ':' and whitespace to the key string
	a := r.Index
	for a > 0 && (stripped[a-1] == ' ' || stripped[a-1] == '\t' || stripped[a-1] == '\n' || stripped[a-1] == '\r' || stripped[a-1] == ':') {
		a--
	}
	if a == 0 || stripped[a-1] != '"' {
		return nil, false, fmt.Errorf("cannot locate key %q", keyPath)
	}
	a-- // closing quote of the key
	for a > 0 && stripped[a-1] != '"' {
		a--
	}
	a-- // opening quote
	b := r.Index + len(r.Raw)

	ws := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	// the member's own span: whole line when it sits alone on one
	from, to := a, b
	ls := bytes.LastIndexByte(raw[:a], '\n') + 1
	alone := len(bytes.TrimSpace(raw[ls:a])) == 0
	j := b
	for j < len(stripped) && ws(stripped[j]) {
		j++
	}
	var out []byte
	if j < len(stripped) && stripped[j] == ',' {
		// not the last member: drop through the trailing comma
		to = j + 1
		if alone {
			from = ls
			if to < len(raw) && raw[to] == '\n' {
				to++
			}
		}
		out, _ = splice(raw, from, to-from, nil)
	} else {
		// last member: drop the line, then the comma that preceded it
		if alone {
			from = ls
			if to < len(raw) && raw[to] == '\n' {
				to++
			}
		}
		out, _ = splice(raw, from, to-from, nil)
		k := a
		for k > 0 && ws(stripped[k-1]) {
			k--
		}
		if k > 0 && stripped[k-1] == ',' {
			out, _ = splice(out, k-1, 1, nil)
		}
	}

	// collapse a now-empty parent object to {}
	s2 := jsonc.ToJSONInPlace(append([]byte(nil), out...))
	parent := gjson.ParseBytes(s2)
	if parts := splitPath(keyPath); len(parts) > 1 {
		parent = gjson.GetBytes(s2, strings.Join(parts[:len(parts)-1], "."))
	}
	if parent.IsObject() && len(parent.Map()) == 0 {
		open := parent.Index
		for open < len(s2) && s2[open] != '{' {
			open++
		}
		close := matchBrace(s2, open)
		if close > open && len(bytes.TrimSpace(out[open+1:close])) == 0 {
			out, _ = splice(out, open, close-open+1, []byte("{}"))
		}
	}
	return out, true, nil
}
