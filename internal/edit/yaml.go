package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/filememo"
	"gopkg.in/yaml.v3"
)

// Nested YAML keys are edited through yaml.v3's node tree, which keeps
// comments and key order; indentation comes out as two spaces.

// GetYAML reads a dot-separated key path to a scalar from a YAML file.
func GetYAML(path, keyPath string) (string, bool) {
	root, err := loadYAML(path)
	if err != nil || root == nil {
		return "", false
	}
	n := root.lookup(strings.Split(keyPath, "."))
	if n == nil || n.kind != yaml.ScalarNode {
		return "", false
	}
	return n.value, true
}

// GetYAMLMap reads the scalar entries of the mapping at a key path.
func GetYAMLMap(path, keyPath string) map[string]string {
	root, err := loadYAML(path)
	if err != nil || root == nil {
		return nil
	}
	n := root.lookup(strings.Split(keyPath, "."))
	if n == nil || n.kind != yaml.MappingNode {
		return nil
	}
	out := map[string]string{}
	for i := 0; i+1 < len(n.content); i += 2 {
		if v := &n.content[i+1]; v.kind == yaml.ScalarNode {
			out[n.content[i].value] = v.value
		}
	}
	return out
}

// GetYAMLList reads the scalar items of the list at a key path.
func GetYAMLList(path, keyPath string) []string {
	root, err := loadYAML(path)
	if err != nil || root == nil {
		return nil
	}
	n := root.lookup(strings.Split(keyPath, "."))
	if n == nil || n.kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for i := range n.content {
		if c := &n.content[i]; c.kind == yaml.ScalarNode {
			out = append(out, c.value)
		}
	}
	return out
}

// GetYAMLText reads the value at a key path as the YAML it is written in —
// a list flow ("[a, b]") or block as it is — for SetYAML to put back as a
// YAMLText. It reads yaml.v3's whole tree, styles and comments, which the
// other looks don't keep (loadYAML).
func GetYAMLText(path, keyPath string) (string, bool) {
	root, err := filememo.Read("yaml text root", path, parseYAMLRoot)
	if err != nil || root == nil {
		return "", false
	}
	n := lookupYAML(root, strings.Split(keyPath, "."))
	if n == nil {
		return "", false
	}
	b, err := yaml.Marshal(n)
	if err != nil {
		return "", false
	}
	return strings.TrimSuffix(string(b), "\n"), true
}

// YAMLText, as a KV's value, is set as the YAML it reads as rather than as
// a string: a list read with GetYAMLText goes back written as it was.
type YAMLText string

// EditYAMLTextStrings is EditYAMLStrings on YAML text (a YAMLText) rather
// than a file: fn's answers in place of every string in it. Invalid input
// or output returns the original text and an error.
func EditYAMLTextStrings(text YAMLText, fn func(string) string) (YAMLText, error) {
	root, err := parseYAMLDocument([]byte(text))
	if err != nil || root == nil {
		return text, err
	}
	if !mapStrings(root, fn) {
		return text, nil
	}
	b, err := yaml.Marshal(root)
	if err != nil {
		return text, err
	}
	if _, err := parseYAMLDocument(b); err != nil {
		return text, err
	}
	return YAMLText(strings.TrimSuffix(string(b), "\n")), nil
}

// SetYAML sets key paths in a YAML file; a value may be a scalar, a map,
// a slice, a struct with yaml tags or YAMLText. Missing files and parents
// are created. Only the first mapping document can be edited, with empty or
// null trailing documents allowed; invalid input or output leaves the file
// untouched.
func SetYAML(path string, kvs ...KV) error {
	root, err := loadYAMLForEdit(path)
	if err != nil {
		return err
	}
	if root == nil {
		root = &yaml.Node{Kind: yaml.MappingNode}
	}
	for _, kv := range kvs {
		var v yaml.Node
		if t, ok := kv.Value.(YAMLText); ok {
			n, err := parseYAMLDocument([]byte(t))
			if err != nil {
				return fmt.Errorf("%s: %s: %w", path, kv.Path, err)
			}
			if n == nil {
				return fmt.Errorf("%s: %s: not YAML: %q", path, kv.Path, string(t))
			}
			v = *n
		} else if r, ok := kv.Value.(json.RawMessage); ok {
			n, err := yamlOfJSON(r)
			if err != nil {
				return fmt.Errorf("%s: %s: %w", path, kv.Path, err)
			}
			v = *n
		} else if err := v.Encode(kv.Value); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		setYAML(root, strings.Split(kv.Path, "."), &v)
	}
	return writeYAML(path, root)
}

// yamlOfJSON is JSON text as a YAML node in block style, its object keys in
// the order the text has them, where encoding a Go map would sort them.
func yamlOfJSON(r json.RawMessage) (*yaml.Node, error) {
	if !json.Valid(r) {
		return nil, fmt.Errorf("not JSON: %q", string(r))
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(r, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("not one JSON value: %q", string(r))
	}
	var block func(n *yaml.Node)
	block = func(n *yaml.Node) {
		// a string keeps its tag, so the encoder still quotes one that
		// would read as another type ("true", "1")
		n.Style = 0
		for _, c := range n.Content {
			block(c)
		}
	}
	n := doc.Content[0]
	block(n)
	return n, nil
}

// DelYAML removes key paths from a YAML file. A missing file stays missing.
func DelYAML(path string, keyPaths ...string) error {
	root, err := loadYAMLForEdit(path)
	if err != nil || root == nil {
		return err
	}
	changed := false
	for _, kp := range keyPaths {
		parts := strings.Split(kp, ".")
		parent := root
		if len(parts) > 1 {
			parent = lookupYAML(root, parts[:len(parts)-1])
		}
		if parent == nil || parent.Kind != yaml.MappingNode {
			continue
		}
		key := parts[len(parts)-1]
		for i := 0; i+1 < len(parent.Content); i += 2 {
			if parent.Content[i].Value == key {
				parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
				changed = true
				break
			}
		}
	}
	if !changed {
		return nil
	}
	return writeYAML(path, root)
}

// EditYAMLNode hands fn the node at a key path, nil when there is none, to
// change in place: a list grown by one item keeps the comments on the rest,
// where SetYAML would write the whole value anew. fn says whether it changed
// the node, and whether the key is to go instead. The file is written only
// on a change.
func EditYAMLNode(path, keyPath string, fn func(n *yaml.Node) (changed, remove bool)) error {
	root, err := loadYAMLForEdit(path)
	if err != nil || root == nil {
		return err
	}
	changed, remove := fn(lookupYAML(root, strings.Split(keyPath, ".")))
	switch {
	case remove:
		return DelYAML(path, keyPath)
	case changed:
		return writeYAML(path, root)
	}
	return nil
}

// EditYAMLStrings calls fn on every string under the key paths — mapping
// keys and values, sequence items, at any depth — and puts fn's answer in
// its place, comments and quoting kept. The file is written only when an
// answer differs, so an fn that answers what it is given only reads.
func EditYAMLStrings(path string, keyPaths []string, fn func(string) string) error {
	root, err := loadYAMLForEdit(path)
	if err != nil || root == nil {
		return err
	}
	changed := false
	for _, kp := range keyPaths {
		if n := lookupYAML(root, strings.Split(kp, ".")); n != nil {
			changed = mapStrings(n, fn) || changed
		}
	}
	if !changed {
		return nil
	}
	return writeYAML(path, root)
}

// mapStrings puts fn's answer in place of every string under n, and says
// whether any differs.
func mapStrings(n *yaml.Node, fn func(string) string) bool {
	changed := false
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" {
		if v := fn(n.Value); v != n.Value {
			n.Value, changed = v, true
		}
	}
	for _, c := range n.Content {
		changed = mapStrings(c, fn) || changed
	}
	return changed
}

// JSONToYAML writes a JSON or JSONC file out as block-style YAML, key
// order kept, for agents that moved from one to the other.
func JSONToYAML(src, dst string) error {
	raw, err := Read(src)
	if err != nil || raw == nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(jsonc.ToJSON(raw), &doc); err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not an object", src)
	}
	blockStyle(root)
	return writeYAML(dst, root)
}

// loadYAML reads the first document's mapping without decoding its values.
// Unrelated duplicate keys, complex keys or invalid tags must not hide a
// readable model setting. Writers use loadYAMLForEdit to validate the whole file.
func loadYAML(path string) (*yamlLook, error) {
	root, err := filememo.Read("yaml root", path, parseYAMLLook)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return root, nil
}

// yamlLook is what the looks (GetYAML, GetYAMLMap, GetYAMLList) read of a
// node: its kind, a scalar's value and the nodes under it, held inline. A
// yaml.Node is ~160 bytes behind a pointer, with a tag, styles, comments
// and places none of them reads: kept for dsh's 68 KB settings.yaml it
// was ~4.5 MB of an idle tray's 10 MB heap.
type yamlLook struct {
	kind    yaml.Kind
	value   string
	content []yamlLook
}

// parseYAMLLook is parseYAMLRoot's tree as a yamlLook.
func parseYAMLLook(b []byte) (*yamlLook, error) {
	root, err := parseYAMLRoot(b)
	if err != nil || root == nil {
		return nil, err
	}
	l := lookOf(root)
	return &l, nil
}

func lookOf(n *yaml.Node) yamlLook {
	l := yamlLook{kind: n.Kind}
	if n.Kind == yaml.ScalarNode {
		l.value = n.Value
	}
	if len(n.Content) > 0 {
		l.content = make([]yamlLook, len(n.Content))
		for i, c := range n.Content {
			if n.Kind == yaml.MappingNode && i%2 == 0 {
				// a key is looked at by its value whatever its kind, as
				// lookupYAML does
				l.content[i] = yamlLook{kind: c.Kind, value: c.Value}
				continue
			}
			l.content[i] = lookOf(c)
		}
	}
	return l
}

// lookup is lookupYAML on a yamlLook.
func (n *yamlLook) lookup(parts []string) *yamlLook {
	for _, p := range parts {
		if n.kind != yaml.MappingNode {
			return nil
		}
		var next *yamlLook
		for i := 0; i+1 < len(n.content); i += 2 {
			if n.content[i].value == p {
				next = &n.content[i+1]
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

// parseYAMLRoot is the looks' parse, made once each time the file is read
// and shared by every look (GetYAML, GetYAMLMap, GetYAMLList through
// parseYAMLLook, GetYAMLText), so read only. Each look parsed the file again: dsh's
// 80 KB settings.yaml, read per field by every /api/drift poll, was
// 465 MB of what a live magpie allocated in 37 minutes.
func parseYAMLRoot(b []byte) (*yaml.Node, error) {
	raw := TrimBOM(b)
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("top level is not a YAML mapping")
	}
	if doc.HeadComment != "" && root.HeadComment == "" {
		root.HeadComment = doc.HeadComment
	}
	return root, nil
}

// loadYAMLForEdit validates input even when the requested key is unchanged
// or missing.
func loadYAMLForEdit(path string) (*yaml.Node, error) {
	raw, err := Read(path)
	if err != nil {
		return nil, err
	}
	root, _, err := parseYAMLTop(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return root, nil
}

// parseYAMLDocument validates the first document and any empty/null trailing ones.
func parseYAMLDocument(raw []byte) (*yaml.Node, error) {
	root, _, err := parseYAMLDocuments(raw)
	return root, err
}

// Decode as well as parse: yaml.Node alone accepts duplicate mapping keys.
// Only empty or null trailing documents can be discarded safely. nextLine
// bounds the first document for line edits; it is zero when no other follows.
func parseYAMLDocuments(raw []byte) (root *yaml.Node, nextLine int, err error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := dec.Decode(&doc); err == io.EOF {
		return nil, 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	for {
		var extra yaml.Node
		if err := dec.Decode(&extra); err == io.EOF {
			break
		} else if err != nil {
			return nil, 0, err
		}
		if nextLine == 0 {
			nextLine = extra.Line
		}
		if len(extra.Content) == 0 {
			continue
		}
		n := extra.Content[0]
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!null" {
			return nil, 0, fmt.Errorf("expected a single YAML document")
		}
		var null any
		if err := n.Decode(&null); err != nil {
			return nil, 0, err
		}
	}
	if len(doc.Content) == 0 {
		return nil, nextLine, nil
	}
	root = doc.Content[0]
	var decoded any
	if err := root.Decode(&decoded); err != nil {
		return nil, 0, err
	}
	// comments above the first key belong to the document
	if doc.HeadComment != "" && root.HeadComment == "" {
		root.HeadComment = doc.HeadComment
	}
	return root, nextLine, nil
}

func writeYAML(path string, root *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// Reparse the rendered text: replacing or removing a node can leave a
	// dangling alias even though the original node tree was valid.
	if _, err := parseYAMLDocument(buf.Bytes()); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	orig, _ := Read(path)
	out := buf.Bytes()
	if Mark != nil {
		lines := splitLines(string(out))
		out = []byte(strings.Join(MarkYAMLLines(lines, string(orig)), "\n"))
	}
	return WriteAtomic(path, keepCRLF(out, orig))
}

func lookupYAML(n *yaml.Node, parts []string) *yaml.Node {
	for _, p := range parts {
		if n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == p {
				next = n.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

// setYAML puts v at parts under the mapping m. An existing value is
// replaced in place, keeping the comments beside it; a scalar in the way
// of a deeper key becomes a mapping.
func setYAML(m *yaml.Node, parts []string, v *yaml.Node) {
	for i, p := range parts {
		last := i == len(parts)-1
		var cur *yaml.Node
		for j := 0; j+1 < len(m.Content); j += 2 {
			if m.Content[j].Value == p {
				cur = m.Content[j+1]
				break
			}
		}
		if cur == nil {
			// the {} a key taken out left (Hermes's providers: {} after
			// a disconnect) is filled in block style, not on one line
			if len(m.Content) == 0 {
				m.Style &^= yaml.FlowStyle
			}
			cur = &yaml.Node{Kind: yaml.MappingNode}
			if last {
				cur = v
			}
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p}, cur)
			m = cur
			continue
		}
		if last {
			v.HeadComment, v.LineComment, v.FootComment = cur.HeadComment, cur.LineComment, cur.FootComment
			*cur = *v
			return
		}
		if cur.Kind != yaml.MappingNode {
			*cur = yaml.Node{Kind: yaml.MappingNode, HeadComment: cur.HeadComment, LineComment: cur.LineComment}
		}
		m = cur
	}
}

func blockStyle(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style = 0
	} else if n.Kind == yaml.ScalarNode {
		n.Style &^= yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle
	}
	for _, c := range n.Content {
		blockStyle(c)
	}
}

// blockSequence is whether raw's first document is a block list: its
// first line past blank ones, comments, directives and a "---" starts
// with "- ". BlockList has nothing to do for one, and decoding the whole
// file to learn so was most of what reading a dsh patch list cost (every
// /api/drift poll reads each profile's: 444 MB in 37 minutes of a live
// magpie).
func blockSequence(raw string) bool {
	for len(raw) > 0 {
		line, rest, _ := strings.Cut(raw, "\n")
		raw = rest
		t := strings.TrimLeft(strings.TrimRight(line, "\r"), " \t")
		switch {
		case t == "", strings.HasPrefix(t, "#"), strings.HasPrefix(line, "%"):
			continue
		case line == "---" || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "---\t") || strings.HasPrefix(line, "---\r"):
			// a document start: what follows it on the line is the
			// document's first
			t = strings.TrimLeft(strings.TrimRight(line[3:], "\r"), " \t")
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
		}
		return t == "-" || strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "-\t")
	}
	return false
}

// BlockList is raw, a file whose one document is a top-level sequence
// written in flow style ([ {id: a}, {id: b} ], what dsh's own writers keep
// a file that starts as []), with that sequence written as a block list
// instead: one "- " item after another, each entry's mappings and lists in
// block style too, its scalars quoted as they were. The lines before the
// sequence and the comment lines after it stay as written. ok is false, and
// raw is left to the caller, for anything else: a block list, a mapping,
// several documents, or text that isn't YAML.
func BlockList(raw string) (string, bool) {
	if blockSequence(raw) {
		return "", false
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	dec := yaml.NewDecoder(strings.NewReader(raw))
	var doc, more yaml.Node
	if dec.Decode(&doc) != nil || dec.Decode(&more) == nil {
		return "", false
	}
	if len(doc.Content) != 1 {
		return "", false
	}
	seq := doc.Content[0]
	if seq.Kind != yaml.SequenceNode || seq.Style&yaml.FlowStyle == 0 {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	start := min(max(seq.Line-1, 0), len(lines))
	end := len(lines)
	for end > start {
		if t := strings.TrimSpace(lines[end-1]); t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		end--
	}
	out := append([]string{}, lines[:start]...)
	if len(seq.Content) == 0 {
		out = append(out, "[]")
	} else {
		seq.HeadComment, seq.LineComment, seq.FootComment = "", "", ""
		unflow(seq)
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if enc.Encode(seq) != nil || enc.Close() != nil {
			return "", false
		}
		out = append(out, strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")...)
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n") + "\n", true
}

// unflow writes n's mappings and sequences in block style.
func unflow(n *yaml.Node) {
	n.Style &^= yaml.FlowStyle
	for _, c := range n.Content {
		unflow(c)
	}
}
