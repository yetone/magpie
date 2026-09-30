package edit

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tidwall/jsonc"
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
	n := lookupYAML(root, strings.Split(keyPath, "."))
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", false
	}
	return n.Value, true
}

// GetYAMLMap reads the scalar entries of the mapping at a key path.
func GetYAMLMap(path, keyPath string) map[string]string {
	root, err := loadYAML(path)
	if err != nil || root == nil {
		return nil
	}
	n := lookupYAML(root, strings.Split(keyPath, "."))
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if v := n.Content[i+1]; v.Kind == yaml.ScalarNode {
			out[n.Content[i].Value] = v.Value
		}
	}
	return out
}

// SetYAML sets key paths in a YAML file; a value may be a scalar, a map,
// a slice or a struct with yaml tags. Missing files and parents are created.
func SetYAML(path string, kvs ...KV) error {
	root, err := loadYAML(path)
	if err != nil {
		return err
	}
	if root == nil {
		root = &yaml.Node{Kind: yaml.MappingNode}
	}
	for _, kv := range kvs {
		var v yaml.Node
		if err := v.Encode(kv.Value); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		setYAML(root, strings.Split(kv.Path, "."), &v)
	}
	return writeYAML(path, root)
}

// DelYAML removes key paths from a YAML file. A missing file stays missing.
func DelYAML(path string, keyPaths ...string) error {
	root, err := loadYAML(path)
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

// EditYAMLStrings calls fn on every string under the key paths — mapping
// keys and values, sequence items, at any depth — and puts fn's answer in
// its place, comments and quoting kept. The file is written only when an
// answer differs, so an fn that answers what it is given only reads.
func EditYAMLStrings(path string, keyPaths []string, fn func(string) string) error {
	root, err := loadYAML(path)
	if err != nil || root == nil {
		return err
	}
	changed := false
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" {
			if v := fn(n.Value); v != n.Value {
				n.Value, changed = v, true
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	for _, kp := range keyPaths {
		if n := lookupYAML(root, strings.Split(kp, ".")); n != nil {
			walk(n)
		}
	}
	if !changed {
		return nil
	}
	return writeYAML(path, root)
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

// loadYAML returns the top-level mapping, or nil for a missing or empty file.
func loadYAML(path string) (*yaml.Node, error) {
	raw, err := Read(path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level is not a YAML mapping", path)
	}
	// comments above the first key belong to the document
	if doc.HeadComment != "" && root.HeadComment == "" {
		root.HeadComment = doc.HeadComment
	}
	return root, nil
}

func writeYAML(path string, root *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return WriteAtomic(path, buf.Bytes())
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
