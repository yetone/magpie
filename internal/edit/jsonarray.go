package edit

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
)

// A JSON/JSONC file whose top level is an array of objects (VS Code's
// chatLanguageModels.json, a list of model provider groups): one element is
// found by the string members it has (where), and read, put in place or
// taken out with the rest of the file — comments, trailing commas and the
// other elements — as it was.

// GetJSONItem returns the raw JSON of the first element of the file's
// top-level array that has every member of where.
func GetJSONItem(path string, where map[string]string) (string, bool) {
	raw, err := Read(path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	r, ok := findItem(jsonc.ToJSONInPlace(raw), where)
	if !ok {
		return "", false
	}
	return r.Raw, true
}

// SetJSONItem replaces the first element that has every member of where
// with value, or adds value at the end of the array when none does. A
// missing or empty file is made an array of value alone.
func SetJSONItem(path string, where map[string]string, value any) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		v, _ := json.MarshalIndent(value, "\t", "\t")
		return WriteAtomic(path, []byte("[\n\t"+string(v)+"\n]\n"))
	}
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	root, open, close := topArray(stripped)
	if close < 0 {
		return fmt.Errorf("%s: top level is not a JSON array", path)
	}
	if r, ok := findItem(stripped, where); ok {
		out, _ := splice(raw, r.Index, len(r.Raw), marshalAt(raw, r.Index, value))
		return WriteAtomic(path, out)
	}
	items := root.Array()
	if len(items) == 0 {
		v, _ := json.MarshalIndent(value, "\t", "\t")
		out, _ := splice(raw, open, close-open+1, []byte("[\n\t"+string(v)+"\n]"))
		return WriteAtomic(path, out)
	}
	last := items[len(items)-1]
	at := last.Index + len(last.Raw)
	// a trailing comma after the last element (blanked in stripped) stays,
	// the new element after it
	sep := ","
	if j := nextToken(raw, at); j < close && raw[j] == ',' {
		at, sep = j+1, ""
	}
	if bytes.IndexByte(raw, '\n') < 0 {
		v, _ := json.Marshal(value)
		out, _ := splice(raw, at, 0, []byte(sep+string(v)))
		return WriteAtomic(path, out)
	}
	indent := lineIndent(raw, last.Index)
	v := marshalAt(raw, last.Index, value)
	out, _ := splice(raw, at, 0, []byte(sep+"\n"+indent+string(v)))
	return WriteAtomic(path, out)
}

// DelJSONItem removes every element of the file's top-level array that has
// every member of where. A file without one is left as it is.
func DelJSONItem(path string, where map[string]string) error {
	raw, err := Read(path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return err
	}
	changed := false
	for {
		stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
		r, ok := findItem(stripped, where)
		if !ok {
			break
		}
		raw, changed = delItem(raw, stripped, r), true
	}
	if !changed {
		return nil
	}
	return WriteAtomic(path, raw)
}

// topArray is stripped's top-level array, with the offsets of its brackets
// (close -1 when the top level is no array); its elements' Index are their
// offsets in stripped.
func topArray(stripped []byte) (gjson.Result, int, int) {
	open := 0
	for open < len(stripped) && isSpace(stripped[open]) {
		open++
	}
	close := len(stripped) - 1
	for close > open && isSpace(stripped[close]) {
		close--
	}
	if open >= len(stripped) || stripped[open] != '[' || stripped[close] != ']' {
		return gjson.Result{}, 0, -1
	}
	root := gjson.ParseBytes(stripped[open : close+1])
	if !root.IsArray() {
		return gjson.Result{}, 0, -1
	}
	root.Index = open
	return root, open, close
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// nextToken is the offset of the first byte at or after i in raw that is
// neither white space nor in a comment: between two tokens, where a
// trailing comma jsonc blanks would be.
func nextToken(raw []byte, i int) int {
	for i < len(raw) {
		switch {
		case isSpace(raw[i]):
			i++
		case bytes.HasPrefix(raw[i:], []byte("//")):
			if n := bytes.IndexByte(raw[i:], '\n'); n >= 0 {
				i += n + 1
			} else {
				return len(raw)
			}
		case bytes.HasPrefix(raw[i:], []byte("/*")):
			if n := bytes.Index(raw[i+2:], []byte("*/")); n >= 0 {
				i += n + 4
			} else {
				return len(raw)
			}
		default:
			return i
		}
	}
	return i
}

// findItem is the first element of stripped's top-level array that has
// every member of where, its Index its offset in stripped.
func findItem(stripped []byte, where map[string]string) (gjson.Result, bool) {
	root, _, close := topArray(stripped)
	if close < 0 {
		return gjson.Result{}, false
	}
	for _, it := range root.Array() {
		if !it.IsObject() || it.Index == 0 {
			continue
		}
		match := true
		for k, want := range where {
			if v := it.Get(gjson.Escape(k)); !v.Exists() || v.String() != want {
				match = false
				break
			}
		}
		if match {
			return it, true
		}
	}
	return gjson.Result{}, false
}

// delItem cuts the element r out of raw with the comma that went with it:
// the one after it, or for the last element the one before; on lines of
// its own, its lines go too.
func delItem(raw, stripped []byte, r gjson.Result) []byte {
	a, b := r.Index, r.Index+len(r.Raw)
	ls := bytes.LastIndexByte(raw[:a], '\n') + 1
	alone := len(bytes.TrimSpace(stripped[ls:a])) == 0
	j := b
	for j < len(stripped) && isSpace(stripped[j]) {
		j++
	}
	from, to := a, b
	if j < len(stripped) && stripped[j] == ',' {
		// not the last element: through the comma after it
		to = j + 1
		if alone {
			from = ls
			if to < len(raw) && raw[to] == '\n' {
				to++
			}
		}
		out, _ := splice(raw, from, to-from, nil)
		return out
	}
	// the last element: its trailing comma too, then the comma before it
	if t := nextToken(raw, b); t < len(raw) && raw[t] == ',' {
		to = t + 1
	}
	if alone {
		from = ls
		if to < len(raw) && raw[to] == '\n' {
			to++
		}
	}
	out, _ := splice(raw, from, to-from, nil)
	k := a
	for k > 0 && isSpace(stripped[k-1]) {
		k--
	}
	if k > 0 && stripped[k-1] == ',' {
		out, _ = splice(out, k-1, 1, nil)
	}
	return out
}
