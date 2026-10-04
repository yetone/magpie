package edit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/jsonc"
)

// a top-level array of groups, as VS Code's chatLanguageModels.json: one
// group found by its members, set, replaced and taken out with the rest of
// the file as it was
func TestJSONItem(t *testing.T) {
	where := map[string]string{"vendor": "customendpoint", "name": "magpie"}
	mine := map[string]any{"name": "magpie", "vendor": "customendpoint", "models": []any{map[string]any{"id": "a/b"}}}
	mine2 := map[string]any{"name": "magpie", "vendor": "customendpoint", "models": []any{map[string]any{"id": "c/d"}}}
	other := "{\n\t\t\"name\": \"Ollama, local\", // mine\n\t\t\"vendor\": \"ollama\"\n\t}"
	cases := []struct{ name, in string }{
		{"missing", ""},
		{"empty", "[]\n"},
		{"one", "// my groups\n[\n\t" + other + "\n]\n"},
		{"trailing comma", "[\n\t" + other + ", // last, with a comma\n]\n"},
		{"compact", `[{"name":"x","vendor":"openai"}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "chatLanguageModels.json")
			if c.in != "" {
				os.WriteFile(path, []byte(c.in), 0o644)
			}
			valid := func(when string) []map[string]any {
				t.Helper()
				b, _ := os.ReadFile(path)
				var got []map[string]any
				if err := json.Unmarshal(jsonc.ToJSON(b), &got); err != nil {
					t.Fatalf("%s: not valid JSONC: %v\n%s", when, err, b)
				}
				for _, keep := range []string{"// my groups", "// mine", "// last, with a comma"} {
					if strings.Contains(c.in, keep) && !strings.Contains(string(b), keep) {
						t.Fatalf("%s: comment %q lost:\n%s", when, keep, b)
					}
				}
				return got
			}
			before := 0
			if c.in != "" {
				before = len(valid("before"))
			}
			if err := SetJSONItem(path, where, mine); err != nil {
				t.Fatal(err)
			}
			got := valid("set")
			if len(got) != before+1 || got[len(got)-1]["name"] != "magpie" {
				t.Fatalf("set: %v", got)
			}
			if v, ok := GetJSONItem(path, where); !ok || !strings.Contains(v, `"a/b"`) {
				t.Fatalf("get: %q %v", v, ok)
			}
			if err := SetJSONItem(path, where, mine2); err != nil {
				t.Fatal(err)
			}
			got = valid("replace")
			if len(got) != before+1 {
				t.Fatalf("replace added one: %v", got)
			}
			if v, _ := GetJSONItem(path, where); !strings.Contains(v, `"c/d"`) || strings.Contains(v, `"a/b"`) {
				t.Fatalf("replace: %q", v)
			}
			if err := DelJSONItem(path, where); err != nil {
				t.Fatal(err)
			}
			got = valid("delete")
			if len(got) != before {
				t.Fatalf("delete: %v", got)
			}
			if _, ok := GetJSONItem(path, where); ok {
				t.Fatal("still there")
			}
			if c.in != "" && c.in != "[]\n" {
				b, _ := os.ReadFile(path)
				bare := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, ",", "")), "") }
				if bare(string(b)) != bare(c.in) {
					t.Fatalf("not as it was:\n%s\nwas:\n%s", b, c.in)
				}
			}
		})
	}
}

// taken out from between others, and from the front, the commas stay right
func TestDelJSONItemAmongOthers(t *testing.T) {
	where := map[string]string{"name": "magpie"}
	for _, in := range []string{
		"[\n  {\"name\": \"a\"},\n  {\"name\": \"magpie\"},\n  {\"name\": \"b\"}\n]\n",
		"[\n  {\"name\": \"magpie\"},\n  {\"name\": \"a\"},\n  {\"name\": \"b\"}\n]\n",
		"[\n  {\"name\": \"a\"},\n  {\"name\": \"b\"},\n  {\"name\": \"magpie\"}\n]\n",
		`[{"name":"a"},{"name":"magpie"},{"name":"b"}]`,
	} {
		path := filepath.Join(t.TempDir(), "x.json")
		os.WriteFile(path, []byte(in), 0o644)
		if err := DelJSONItem(path, where); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(path)
		var got []map[string]string
		if err := json.Unmarshal(b, &got); err != nil || len(got) != 2 {
			t.Fatalf("%v %v\n%s", err, got, b)
		}
	}
}

// a key with a dot in it, as VS Code's settings.json keeps chat.defaultModel
func TestJSONEscapedDot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte("{\n\t// mine\n\t\"editor.fontSize\": 14\n}\n"), 0o644)
	if err := SetJSON(path, KV{Path: `chat\.defaultModel`, Value: "a/b"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"chat.defaultModel": "a/b"`) || !strings.Contains(string(b), "// mine") {
		t.Fatalf("set:\n%s", b)
	}
	if v, _ := GetJSON(path, `chat\.defaultModel`); v != "a/b" {
		t.Fatalf("get %q", v)
	}
	if err := SetJSON(path, KV{Path: `chat\.defaultModel`, Value: "c/d"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := GetJSON(path, `chat\.defaultModel`); v != "c/d" {
		t.Fatalf("replace %q", v)
	}
	if err := DelJSON(path, `chat\.defaultModel`); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "{\n\t// mine\n\t\"editor.fontSize\": 14\n}\n" {
		t.Fatalf("delete:\n%q", b)
	}
}
