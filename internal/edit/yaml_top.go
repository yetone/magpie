package edit

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// GetYAMLTop reads a top-level scalar key from a YAML file. Quoted keys and
// string values are decoded; malformed files return ("", false).
func GetYAMLTop(path, key string) (string, bool) {
	raw, err := Read(path)
	if err != nil {
		return "", false
	}
	root, err := parseYAMLTop(raw)
	if err != nil || root == nil {
		return "", false
	}
	n := lookupYAML(root, []string{key})
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", false
	}
	return n.Value, true
}

// SetYAMLTop sets top-level scalar keys without reformatting other entries.
// Only a single block mapping can be edited; invalid input or output leaves
// the file untouched. Missing files are created.
func SetYAMLTop(path string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	for _, kv := range kvs {
		entries, at, indent, err := yamlTopEntries(lines)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		from, to := at, at
		for _, entry := range entries {
			if entry.key == kv.Path {
				from, to = entry.from, entry.to
				break
			}
		}
		line := indent + yamlTopLiteral(kv.Path) + ": " + yamlTopLiteral(kv.Value)
		out := append(lines[:from:from], line)
		lines = append(out, lines[to:]...)
	}
	return writeYAMLTop(path, lines)
}

// DelYAMLTop removes whole top-level entries from a single block mapping.
// A missing file stays missing; parse errors leave the file untouched.
func DelYAMLTop(path string, keys ...string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	lines := splitLines(string(raw))
	entries, _, _, err := yamlTopEntries(lines)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	drop := map[string]bool{}
	for _, key := range keys {
		drop[key] = true
	}
	var out []string
	at := 0
	for _, entry := range entries {
		if drop[entry.key] {
			out = append(out, lines[at:entry.from]...)
			at = entry.to
		}
	}
	if at == 0 {
		return nil
	}
	out = append(out, lines[at:]...)
	return writeYAMLTop(path, out)
}

var yamlPlain = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func yamlTopLiteral(value any) string {
	v := toString(value)
	var decoded any
	_, isString := value.(string)
	if !yamlPlain.MatchString(v) || (isString && (yaml.Unmarshal([]byte(v), &decoded) != nil || decoded != v)) {
		v = strconv.Quote(v)
	}
	return v
}

// Decode as well as parse: yaml.Node alone accepts duplicate mapping keys.
// A second document must not silently disappear from the editor's view.
func parseYAMLTop(raw []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err == io.EOF {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("expected a single YAML document")
		}
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" && root.Value == "" {
		return nil, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: top level is not a YAML mapping", root.Line)
	}
	var decoded any
	if err := root.Decode(&decoded); err != nil {
		return nil, err
	}
	return root, nil
}

type yamlTopEntry struct {
	key      string
	from, to int
}

// The next parsed key bounds an entry, even when its value contains key-like
// lines or a flow collection whose closing bracket is at column zero.
func yamlTopEntries(lines []string) ([]yamlTopEntry, int, string, error) {
	root, err := parseYAMLTop([]byte(strings.Join(lines, "\n")))
	if err != nil {
		return nil, 0, "", err
	}
	end := len(lines)
	if lines[end-1] == "" {
		end-- // splitLines' final empty element is not another physical line.
	}
	// Keep an explicit document terminator and the comments following it.
	for i := end - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(lines[i], "...") && (line == "..." ||
			strings.HasPrefix(lines[i], "... ") || strings.HasPrefix(lines[i], "...\t")) {
			end = i
		}
		break
	}
	if root == nil {
		return nil, end, "", nil
	}
	if root.Style&yaml.FlowStyle != 0 {
		return nil, 0, "", fmt.Errorf("line %d: top level must be a block YAML mapping", root.Line)
	}
	indent := strings.Repeat(" ", root.Column-1)
	var entries []yamlTopEntry
	at := end
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		from, limit := key.Line-1, end
		if i+2 < len(root.Content) {
			limit = root.Content[i+2].Line - 1
		}
		// Explicit or complex keys may start before the key node's line.
		// Refuse those layouts instead of dropping only part of an entry.
		if key.Kind != yaml.ScalarNode || key.Column != root.Column || from < 0 || from >= limit || limit > end ||
			key.Column < 1 || key.Column-1 > len([]rune(lines[from])) ||
			strings.TrimSpace(string([]rune(lines[from])[:key.Column-1])) != "" {
			return nil, 0, "", fmt.Errorf("line %d: expected a scalar YAML key on its own line", key.Line)
		}
		at = yamlEntryEnd(lines, key, value, limit)
		entries = append(entries, yamlTopEntry{key.Value, from, at})
	}
	return entries, at, indent, nil
}

// Nodes have start positions, not end positions. Leave trailing comments and
// blank lines in place only if the prefix still parses to the same value.
// This keeps |+ blank lines and comment-like lines inside quoted/block values.
func yamlEntryEnd(lines []string, key, value *yaml.Node, limit int) int {
	min := limit
	for min > key.Line {
		line := strings.TrimSpace(lines[min-1])
		if line != "" && !strings.HasPrefix(line, "#") {
			break
		}
		min--
	}
	// Once a prefix contains the whole value, extra trailing comments cannot
	// change it. Binary search avoids reparsing once per trailing blank line.
	return min + sort.Search(limit-min, func(i int) bool {
		var doc yaml.Node
		if yaml.Unmarshal([]byte(strings.Join(lines[:min+i], "\n")+"\n"), &doc) != nil || len(doc.Content) == 0 {
			return false
		}
		content := doc.Content[0].Content
		if len(content) < 2 || content[len(content)-2].Value != key.Value {
			return false
		}
		return sameYAMLValue(content[len(content)-1], value)
	})
}

// Positions, comments and spelling do not affect a value. Alias names can be
// compared directly: their definitions are in the unchanged prefix.
func sameYAMLValue(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || a.Anchor != b.Anchor || len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !sameYAMLValue(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

func writeYAMLTop(path string, lines []string) error {
	raw := []byte(joinLines(lines))
	if _, err := parseYAMLTop(raw); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return WriteAtomic(path, raw)
}
