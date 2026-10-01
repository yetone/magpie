package edit

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TOML and top-level YAML entries are located by their parsers (toml.go and
// yaml_top.go), then edited without reformatting the rest of the file.

var (
	// tomlTable and tomlKV only read a TOML file its parser refuses; see
	// GetTOMLTop.
	tomlTable = regexp.MustCompile(`^\s*\[`)
	tomlKV    = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+|"[^"]*")\s*=\s*(.*?)\s*$`)
)

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
