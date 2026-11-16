package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// codexSchema is Codex's own config.schema.json (codex-rs/core, at
// rust-v0.169.0), its descriptions and defaults left out. Codex warns on
// every launch about a key it doesn't know ("`disable_response_storage`
// is ignored", #6828), and `codex --strict-config` refuses to start.
func codexSchema(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "codex-config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// codexUnknownKeys lists the keys of v that the schema node s closes
// out: where an object's schema has additionalProperties false, every key
// must be one of its properties.
func codexUnknownKeys(root, s map[string]any, at string, v any) []string {
	if ref, ok := s["$ref"].(string); ok {
		def, _ := root["definitions"].(map[string]any)[strings.TrimPrefix(ref, "#/definitions/")].(map[string]any)
		if def == nil {
			return []string{at + ": schema has no " + ref}
		}
		return codexUnknownKeys(root, def, at, v)
	}
	var bad []string
	for _, sub := range asSchemas(s["allOf"]) {
		bad = append(bad, codexUnknownKeys(root, sub, at, v)...)
	}
	for _, one := range []string{"anyOf", "oneOf"} {
		subs := asSchemas(s[one])
		if len(subs) == 0 {
			continue
		}
		var least []string
		for i, sub := range subs {
			b := codexUnknownKeys(root, sub, at, v)
			if i == 0 || len(b) < len(least) {
				least = b
			}
		}
		bad = append(bad, least...)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return bad
	}
	props, _ := s["properties"].(map[string]any)
	for k, sub := range obj {
		where := strings.TrimPrefix(at+"."+k, ".")
		if p, ok := props[k].(map[string]any); ok {
			bad = append(bad, codexUnknownKeys(root, p, where, sub)...)
			continue
		}
		switch extra := s["additionalProperties"].(type) {
		case bool:
			if !extra && (props != nil || s["type"] == "object") {
				bad = append(bad, where)
			}
		case map[string]any:
			bad = append(bad, codexUnknownKeys(root, extra, where, sub)...)
		}
	}
	return bad
}

func asSchemas(v any) []map[string]any {
	var out []map[string]any
	list, _ := v.([]any)
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func codexKeysCodexDoesntKnow(t *testing.T, root map[string]any, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v\n%s", file, err, b)
	}
	bad := codexUnknownKeys(root, root, "", doc)
	slices.Sort(bad)
	return bad
}

// Every key magpie writes into Codex's config is one Codex knows: none it
// has removed (disable_response_storage went in Codex 0.30, #6828), and
// none it never had. Checked against Codex's own schema, for the config
// magpie connects in place and for the magpie.config.toml profile, with a
// model, an effort, subagents, memories and a sync. A key the user wrote
// stays theirs: magpie neither adds one nor takes it out.
func TestCodexConfigHoldsOnlyKeysCodexKnows(t *testing.T) {
	root := codexSchema(t)
	// the schema itself closes the top level, and doesn't know the key
	// Codex removed: else this test could pass with anything written
	if bad := codexUnknownKeys(root, root, "", map[string]any{"disable_response_storage": true, "model": "m"}); !slices.Equal(bad, []string{"disable_response_storage"}) {
		t.Fatalf("schema check: %v", bad)
	}
	const auth = `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`
	for _, tc := range []struct{ name, scope, config string }{
		{"empty", "", ""},
		{"cc switch", "", ccSwitchCodex},
		{"profile", "profile", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, read := codexHome(t, auth, tc.config)
			cx := codex(home)
			if tc.scope != "" {
				if err := cx.Field("scope").Set(tc.scope); err != nil {
					t.Fatal(err)
				}
				cx = codex(home)
			}
			for _, set := range [][2]string{{"model", "fake/m1"}, {"effort", "high"}, {"subagent", "fake/m1"}, {"subagent_effort", "low"}, {"memories", "fake/m1"}} {
				if cx.Field(set[0]) == nil {
					if tc.scope == "" {
						t.Fatalf("no field %s", set[0])
					}
					continue // the profile has fewer
				}
				if err := cx.Apply(set[0], set[1]); err != nil {
					t.Fatalf("%s: %v", set[0], err)
				}
			}
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{filepath.Join(home, ".codex", "config.toml"), cx.Path} {
				if bad := codexKeysCodexDoesntKnow(t, root, f); len(bad) > 0 {
					b, _ := os.ReadFile(f)
					t.Errorf("%s holds keys Codex doesn't know: %v\n%s", filepath.Base(f), bad, b)
				}
			}
			if tc.scope == "" && !strings.Contains(read(), `model = "fake/m1"`) {
				t.Fatalf("not connected:\n%s", read())
			}
		})
	}
	// the user's own line (CC Switch before 4.0.28 wrote it, and relays'
	// setup pages still say to) is left as it is, and only it is unknown
	home, read := codexHome(t, auth, "disable_response_storage = true\n"+ccSwitchCodex)
	cx := codex(home)
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if bad := codexKeysCodexDoesntKnow(t, root, filepath.Join(home, ".codex", "config.toml")); !slices.Equal(bad, []string{"disable_response_storage"}) {
		t.Fatalf("unknown keys %v, want the user's line only:\n%s", bad, read())
	}
	if n := strings.Count(read(), "disable_response_storage = true"); n != 1 {
		t.Fatalf("user's line %d times:\n%s", n, read())
	}
}
