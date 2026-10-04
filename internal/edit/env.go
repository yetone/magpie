package edit

import (
	"regexp"
	"strings"
)

// dotenv files (KEY=value per line) as Gemini CLI reads from ~/.gemini/.env.

var envLine = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$`)

// GetEnvFile reads one key from a dotenv file.
func GetEnvFile(path, key string) (string, bool) {
	raw, err := Read(path)
	if err != nil {
		return "", false
	}
	for _, l := range splitLines(string(raw)) {
		if m := envLine.FindStringSubmatch(l); m != nil && m[1] == key {
			if v, ok := unquote(m[2]); ok {
				return v, true
			}
			v := m[2]
			if i := strings.IndexByte(v, '#'); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			return v, true
		}
	}
	return "", false
}

// SetEnvFile sets keys in a dotenv file, replacing lines in place and
// appending new ones. Other lines are kept byte for byte.
func SetEnvFile(path string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	for _, kv := range kvs {
		v := toString(kv.Value)
		if strings.ContainsAny(v, " #\"'$\r\n") {
			// Gemini CLI's dotenv keeps backslashes literal inside single quotes.
			if !strings.ContainsAny(v, "'\r\n") {
				v = "'" + v + "'"
			} else {
				v = `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
			}
		}
		lines = setLine(lines, kv.Path, kv.Path+"="+v, nil, func(l string) (string, bool) {
			if m := envLine.FindStringSubmatch(l); m != nil {
				return m[1], true
			}
			return "", false
		})
	}
	return WriteAtomic(path, []byte(joinLines(lines)))
}

// DelEnvFile removes keys from a dotenv file, and the file when they were
// all it held (Gemini CLI's .env, made by magpie for its key).
func DelEnvFile(path string, keys ...string) error {
	raw, err := Read(path)
	if err != nil || len(raw) == 0 {
		return err
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	changed := false
	for _, l := range splitLines(string(raw)) {
		if m := envLine.FindStringSubmatch(l); m != nil && drop[m[1]] {
			changed = true
			continue
		}
		out = append(out, l)
	}
	if !changed {
		return nil
	}
	if strings.TrimSpace(strings.Join(out, "")) == "" {
		return Remove(path)
	}
	return WriteAtomic(path, []byte(joinLines(out)))
}
