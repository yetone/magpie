package edit

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestYAMLTopStringScalars(t *testing.T) {
	for _, v := range []string{"null", "true", "123", "a\t\nb"} {
		p := filepath.Join(t.TempDir(), "goose.yaml")
		if e := SetYAMLTop(p, KV{"GOOSE_MODEL", v}); e != nil {
			t.Fatal(e)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if e := yaml.Unmarshal(b, &m); e != nil {
			t.Fatal(e)
		}
		if got, ok := m["GOOSE_MODEL"].(string); !ok || got != v {
			t.Errorf("value %q serialized %q reads %T %v", v, b, m["GOOSE_MODEL"], m["GOOSE_MODEL"])
		}
	}
}
func TestYAMLTopSingleQuotedEscape(t *testing.T) {
	p := tmpFile(t, "goose.yaml", "GOOSE_MODEL: 'owner''s/model'\n")
	v, ok := GetYAMLTop(p, "GOOSE_MODEL")
	if !ok {
		t.Fatal("model not found")
	}
	if v != "owner's/model" {
		t.Fatalf("got %q", v)
	}
}

func TestYAMLTopTypedValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := SetYAMLTop(p, KV{"enabled", true}, KV{"count", 123}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["enabled"] != true || m["count"] != 123 {
		t.Fatalf("types changed: %#v", m)
	}
}
