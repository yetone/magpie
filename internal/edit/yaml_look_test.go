package edit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestYAMLLooksReadAsTheNodeTree holds GetYAML, GetYAMLMap and GetYAMLList,
// which read the slim tree loadYAML keeps, to what yaml.v3's own tree
// gives for every key path in a file with comments, anchors, aliases,
// flow and block lists, quoted and odd keys.
func TestYAMLLooksReadAsTheNodeTree(t *testing.T) {
	src := `# head
base: &base
  url: http://127.0.0.1:3425/v1 # line
  key: "k"
providers:
  magpie:
    <<: *base
    model: gpt-5.4
  other: *base
  "quoted.key": {a: 1, b: [x, y]}
  ? complex
  : value
list:
  - a
  - {b: c}
  - [d, e]
flow: [1, 2, three]
empty: {}
none:
num: 3
? [k1, k2]
: v
`
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := parseYAMLRoot([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	var walk func(n *yaml.Node, pre string)
	walk = func(n *yaml.Node, pre string) {
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := n.Content[i].Value
			if pre != "" {
				p = pre + "." + p
			}
			paths = append(paths, p)
			walk(n.Content[i+1], p)
		}
	}
	walk(root, "")
	paths = append(paths, "missing", "providers.missing", "num.deeper", "list.a")
	if len(paths) < 20 {
		t.Fatalf("walked only %d paths", len(paths))
	}
	for _, p := range paths {
		n := lookupYAML(root, strings.Split(p, "."))
		wantV, wantOK := "", false
		var wantM map[string]string
		var wantL []string
		if n != nil {
			switch n.Kind {
			case yaml.ScalarNode:
				wantV, wantOK = n.Value, true
			case yaml.MappingNode:
				wantM = map[string]string{}
				for i := 0; i+1 < len(n.Content); i += 2 {
					if n.Content[i+1].Kind == yaml.ScalarNode {
						wantM[n.Content[i].Value] = n.Content[i+1].Value
					}
				}
			case yaml.SequenceNode:
				for _, c := range n.Content {
					if c.Kind == yaml.ScalarNode {
						wantL = append(wantL, c.Value)
					}
				}
			}
		}
		if v, ok := GetYAML(path, p); v != wantV || ok != wantOK {
			t.Errorf("GetYAML(%q) = %q, %v; the node tree reads %q, %v", p, v, ok, wantV, wantOK)
		}
		if m := GetYAMLMap(path, p); !reflect.DeepEqual(m, wantM) {
			t.Errorf("GetYAMLMap(%q) = %v; the node tree reads %v", p, m, wantM)
		}
		if l := GetYAMLList(path, p); !reflect.DeepEqual(l, wantL) {
			t.Errorf("GetYAMLList(%q) = %v; the node tree reads %v", p, l, wantL)
		}
	}
	if v, ok := GetYAMLText(path, "flow"); !ok || v != "[1, 2, three]" {
		t.Errorf("GetYAMLText(flow) = %q, %v; want the flow list as written", v, ok)
	}
}
