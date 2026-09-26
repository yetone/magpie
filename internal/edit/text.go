package edit

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Top-level TOML and YAML keys are edited line by line. That keeps every
// comment and every other line byte-for-byte intact, which a round trip
// through a parser would not.

var (
	tomlTable = regexp.MustCompile(`^\s*\[`)
	tomlKV    = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+|"[^"]*")\s*=\s*(.*?)\s*$`)
	yamlKV    = regexp.MustCompile(`^([A-Za-z0-9_.-]+)\s*:\s*(.*?)\s*$`)
	yamlPlain = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// GetTOMLTop reads a top-level (pre-table) key from a TOML file.
func GetTOMLTop(path, key string) (string, bool) {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return "", false
	}
	for _, line := range splitLines(string(raw)) {
		if tomlTable.MatchString(line) {
			break
		}
		if m := tomlKV.FindStringSubmatch(line); m != nil && strings.Trim(m[1], `"`) == key {
			return tomlValue(m[2]), true
		}
	}
	return "", false
}

// SetTOMLTop sets top-level keys in a TOML file. Existing lines are replaced
// in place; new keys go right after the last existing top-level key.
func SetTOMLTop(path string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	for _, kv := range kvs {
		lines = setLine(lines, kv.Path, kv.Path+" = "+strconv.Quote(toString(kv.Value)), tomlTable, func(line string) (string, bool) {
			m := tomlKV.FindStringSubmatch(line)
			if m == nil {
				return "", false
			}
			return strings.Trim(m[1], `"`), true
		})
	}
	return writeTOML(path, lines)
}

// GetYAMLTop reads a top-level scalar key from a YAML file.
func GetYAMLTop(path, key string) (string, bool) {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return "", false
	}
	for _, line := range splitLines(string(raw)) {
		if m := yamlKV.FindStringSubmatch(line); m != nil && m[1] == key {
			return yamlValue(m[2]), true
		}
	}
	return "", false
}

// SetYAMLTop sets top-level scalar keys in a YAML file.
func SetYAMLTop(path string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	for _, kv := range kvs {
		v := toString(kv.Value)
		if !yamlPlain.MatchString(v) {
			v = strconv.Quote(v)
		}
		lines = setLine(lines, kv.Path, kv.Path+": "+v, nil, func(line string) (string, bool) {
			m := yamlKV.FindStringSubmatch(line)
			if m == nil {
				return "", false
			}
			return m[1], true
		})
	}
	return WriteAtomic(path, []byte(joinLines(lines)))
}

// DelYAMLTop removes top-level scalar keys from a YAML file.
func DelYAMLTop(path string, keys ...string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	for _, line := range splitLines(string(raw)) {
		if m := yamlKV.FindStringSubmatch(line); m != nil && drop[m[1]] {
			continue
		}
		out = append(out, line)
	}
	return WriteAtomic(path, []byte(joinLines(out)))
}

// setLine replaces the line whose key matches, or inserts newLine after the
// last key line in the header section (before the first line matching stop).
func setLine(lines []string, key, newLine string, stop *regexp.Regexp, keyOf func(string) (string, bool)) []string {
	lastKey := -1
	for i, line := range lines {
		if stop != nil && stop.MatchString(line) {
			break
		}
		k, ok := keyOf(line)
		if !ok {
			continue
		}
		if k == key {
			lines[i] = newLine
			return lines
		}
		lastKey = i
	}
	at := lastKey + 1
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, newLine)
	out = append(out, lines[at:]...)
	return out
}

// unquote strips a leading quoted scalar (and anything after it, such as an
// inline comment); ok is false when v does not start with a quote.
func unquote(v string) (string, bool) {
	if v == "" || (v[0] != '"' && v[0] != '\'') {
		return "", false
	}
	q := v[0]
	for i := 1; i < len(v); i++ {
		if q == '"' && v[i] == '\\' {
			i++
			continue
		}
		if v[i] == q {
			if q == '"' {
				if s, err := strconv.Unquote(v[:i+1]); err == nil {
					return s, true
				}
			}
			return v[1:i], true
		}
	}
	return v[1:], true
}

func tomlValue(v string) string {
	if s, ok := unquote(v); ok {
		return s
	}
	if i := strings.Index(v, "#"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func yamlValue(v string) string {
	if s, ok := unquote(v); ok {
		return s
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

// splitLines splits on '\n' but keeps a trailing newline as an empty final
// element so joinLines can restore the file exactly.
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

func joinLines(lines []string) string {
	s := strings.Join(lines, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}
