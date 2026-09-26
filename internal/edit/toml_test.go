package edit

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const tomlDoc = `model = "gpt-6-astra"   # keep
approval_policy = "never"

[notice]
hide = true

[projects."/x"]
trust_level = "trusted"
`

func tmpToml(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(tomlDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readTOMLTable(t *testing.T, path, name string) map[string]string {
	t.Helper()
	got, err := GetTOMLTable(path, name)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func readTOMLTables(t *testing.T, path string) []string {
	t.Helper()
	got, err := TOMLTables(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestTOMLTables(t *testing.T) {
	p := tmpToml(t)
	got := strings.Join(readTOMLTables(t, p), ",")
	if got != `notice,projects."/x"` {
		t.Fatalf("tables = %q", got)
	}
}

func TestTOMLTablesOnlyListsOrdinaryHeaders(t *testing.T) {
	const input = `note = """
[model."magpie/top-fake"]
"""

[[skills.config]]
path = "/skill"

  [ model."magpie/a]b" ] # keep the quoted bracket
note = '''
[model."magpie/body-fake"]
[[another.fake]]
'''
matrix = [
[[1, 2], [3, 4]]
]

[[skills.config]]
path = "/other-skill"

[last]
enabled = true
`
	p := tmpFile(t, "config.toml", input)
	if got := strings.Join(readTOMLTables(t, p), ","); got != `model."magpie/a]b",last` {
		t.Fatalf("tables = %q", got)
	}
	if got := readTOMLTable(t, p, `model."magpie/a]b"`); got["note"] != "[model.\"magpie/body-fake\"]\n[[another.fake]]\n" {
		t.Fatalf("table lookup disagrees with the header list: %v", got)
	}
}

func TestDelTOMLTableKeepsChildArrayTables(t *testing.T) {
	const array = "[[a.b]]\ny = 2\n"
	p := tmpFile(t, "config.toml", "[a]\nx = 1\n\n"+array)
	if err := DelTOMLTable(p, "a"); err != nil {
		t.Fatal(err)
	}
	assertTOMLContent(t, p, array)
}

func TestSetAndDelTOMLTable(t *testing.T) {
	p := tmpToml(t)
	if err := SetTOMLTable(p, "model_providers.deepseek", KV{Path: "name", Value: "DeepSeek"}, KV{Path: "wire_api", Value: "responses"}); err != nil {
		t.Fatal(err)
	}
	kv := readTOMLTable(t, p, "model_providers.deepseek")
	if kv["name"] != "DeepSeek" || kv["wire_api"] != "responses" {
		t.Fatalf("table = %v", kv)
	}
	// replace in place, other tables intact
	if err := SetTOMLTable(p, "model_providers.deepseek", KV{Path: "name", Value: "DS"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	s := string(raw)
	if strings.Count(s, "[model_providers.deepseek]") != 1 || strings.Contains(s, "wire_api") || !strings.Contains(s, "trust_level") || !strings.Contains(s, "# keep") {
		t.Fatalf("after replace:\n%s", s)
	}
	if err := DelTOMLTable(p, "model_providers.deepseek"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	if string(raw) != tomlDoc {
		t.Fatalf("delete did not restore:\n%s", raw)
	}
}

func TestSetTOMLTableMiddle(t *testing.T) {
	p := tmpToml(t)
	if err := SetTOMLTable(p, "notice", KV{Path: "hide", Value: false}, KV{Path: "n", Value: 3}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	want := "[notice]\nhide = false\nn = 3\n\n[projects.\"/x\"]"
	if !strings.Contains(string(raw), want) {
		t.Fatalf("got:\n%s", raw)
	}
}

func TestTOMLTablePreservesFollowingArrayTables(t *testing.T) {
	const prefix = `model = "x" # keep

[[before]]
name = "before"

`
	const table = "[model_providers.magpie]\nname = \"old\"\n\n"
	const arrays = `  [[skills.config]] # keep the first skill
name = "skill-one"
path = "/skills/one"
enabled = false

[[skills.config]]
name = "skill-two"
path = "/skills/two"
enabled = true
`
	for _, ending := range []struct {
		name, text string
	}{
		{"EOF", ""},
		{"another table", "\n[notice]\nhide = true\n"},
	} {
		t.Run(ending.name, func(t *testing.T) {
			t.Run("read", func(t *testing.T) {
				input := prefix + table + arrays + ending.text
				p := tmpFile(t, "config.toml", input)
				got := readTOMLTable(t, p, "model_providers.magpie")
				if !maps.Equal(got, map[string]string{"name": "old"}) {
					t.Fatalf("provider contains keys from following array tables: %v", got)
				}
				assertTOMLContent(t, p, input)
			})
			for _, tc := range []struct {
				name string
				edit func(string) error
				want string
			}{
				{"replace table", func(p string) error {
					return SetTOMLTable(p, "model_providers.magpie", KV{Path: "name", Value: "new"})
				}, "[model_providers.magpie]\nname = \"new\"\n\n"},
				{"delete table", func(p string) error {
					return DelTOMLTable(p, "model_providers.magpie")
				}, ""},
				{"set key shared with array", func(p string) error {
					return SetTOMLKey(p, "model_providers.magpie", "path", "/provider")
				}, "[model_providers.magpie]\nname = \"old\"\npath = \"/provider\"\n\n"},
				{"delete last key", func(p string) error {
					return DelTOMLKey(p, "model_providers.magpie", "name")
				}, ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					p := tmpFile(t, "config.toml", prefix+table+arrays+ending.text)
					if err := tc.edit(p); err != nil {
						t.Fatal(err)
					}
					assertTOMLContent(t, p, prefix+tc.want+arrays+ending.text)
				})
			}
		})
	}
}

func TestTOMLTableIgnoresHeadersInsideValues(t *testing.T) {
	for _, tc := range []struct {
		name, value string
	}{
		{"nested_arrays", "[\n[[1, 2], [3, 4]],\n[[1]]\n]"},
		{"basic_string", "\"\"\"\n[[skills.config]]\n\"\"\""},
		{"literal_string", "'''\n[[skills.config]]\n'''"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const tail = "[[skills.config]]\npath = \"/skills/one\"\n"
			p := tmpFile(t, "config.toml", "[model_providers.magpie]\nvalue = "+tc.value+"\nwire_api = \"responses\"\n\n"+tail)
			if got := readTOMLTable(t, p, "model_providers.magpie")["wire_api"]; got != "responses" {
				t.Errorf("stopped inside a multiline value: wire_api = %q", got)
			}
			if err := SetTOMLTable(p, "model_providers.magpie", KV{Path: "name", Value: "new"}); err != nil {
				t.Fatal(err)
			}
			if got, want := read(t, p), "[model_providers.magpie]\nname = \"new\"\n\n"+tail; got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestTOMLTableParseErrorLeavesFileUntouched(t *testing.T) {
	for _, input := range []string{
		"\ufeff[model_providers.magpie]\nname = \"old\"\n",
		"invalid = [\n[model_providers.magpie]\nname = \"old\"\n",
		"[model_providers.magpie]\ninvalid = [\n[[skills.config]]\n",
		"[model_providers.magpie]\ninvalid = [\n",
		"[model_providers.magpie]\nname = \"old\"\n\n[other]\ninvalid = [\n",
	} {
		for _, edit := range []func(string) error{
			func(p string) error {
				got, err := TOMLTables(p)
				if got != nil {
					t.Errorf("returned a partial table list on a parse error: %v", got)
				}
				return err
			},
			func(p string) error { _, err := GetTOMLTable(p, "model_providers.magpie"); return err },
			func(p string) error {
				return SetTOMLTables(p, []string{"model_providers."}, []Table{{Name: "model_providers.magpie", KVs: []KV{{Path: "name", Value: "new"}}}})
			},
			func(p string) error { return SetTOMLTables(p, []string{"model_providers."}, nil) },
			func(p string) error { return SetTOMLTable(p, "model_providers.magpie", KV{Path: "name", Value: "new"}) },
			func(p string) error { return DelTOMLTable(p, "model_providers.magpie") },
			func(p string) error { return SetTOMLKey(p, "model_providers.magpie", "name", "new") },
			func(p string) error { return DelTOMLKey(p, "model_providers.magpie", "name") },
		} {
			p := tmpFile(t, "config.toml", input)
			err := edit(p)
			if err == nil || !strings.HasPrefix(err.Error(), p+": line ") {
				t.Fatalf("expected a parse error naming %s, got %v", p, err)
			}
			var parseErr *unstable.ParserError
			if !errors.As(err, &parseErr) {
				t.Fatalf("parse error was not wrapped: %v", err)
			}
			if got := read(t, p); got != input {
				t.Fatalf("changed file after a parse error:\n%s", got)
			}
		}
	}
}

func TestTOMLParseErrorLocation(t *testing.T) {
	for _, tc := range []struct {
		name, input, location, message string
	}{
		{"BOM", "\ufeff[a]\nx = 1\n", "line 1, column 1", "UTF-8 BOM"},
		{"array", "# config\n[a]\nx = [\n", "line 3, column 5", "array is incomplete"},
		{"number", "# config\n[a]\nx = @\n", "line 3, column 5", "incomplete number"},
		{"EOF value", "[a]\nvalue =", "line 2, column 8", "expected value"},
		{"EOF key", "[a]\nkey", "line 2, column 4", "expected ="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tmpFile(t, "config.toml", tc.input)
			_, err := TOMLTables(p)
			if err == nil || !strings.HasPrefix(err.Error(), p+": "+tc.location+": ") || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected %s and %q, got %v", tc.location, tc.message, err)
			}
			var parseErr *unstable.ParserError
			if !errors.As(err, &parseErr) {
				t.Fatalf("lost the underlying parser error: %v", err)
			}
		})
	}
}

func TestTOMLParseErrorWithNilHighlight(t *testing.T) {
	var p unstable.Parser
	p.Reset([]byte("[a]\nvalue ="))
	parseErr := &unstable.ParserError{Message: "expected value"}
	err := tomlParseError(&p, parseErr)
	if !strings.HasPrefix(err.Error(), "line 2, column 8:") || !errors.Is(err, parseErr) {
		t.Fatalf("EOF error without a highlight: %v", err)
	}
}

func TestTOMLRejectsArrayTable(t *testing.T) {
	for _, tc := range []struct {
		name, header, location string
	}{
		{"unindented", "[[models]]", "line 1, column 3"},
		{"indented", "# config\n\n  [[ models ]]", "line 3, column 6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []struct {
				name string
				run  func(string) error
			}{
				{"get", func(p string) error { _, err := GetTOMLTable(p, "models"); return err }},
				{"set key", func(p string) error { return SetTOMLKey(p, "models", "name", "NEW") }},
				{"set table", func(p string) error { return SetTOMLTable(p, "models", KV{Path: "name", Value: "NEW"}) }},
			} {
				t.Run(op.name, func(t *testing.T) {
					input := tc.header + "\nname = \"first\"\n\n[[models]]\nname = \"second\"\n"
					p := tmpFile(t, "config.toml", input)
					err := op.run(p)
					if err == nil || !strings.HasPrefix(err.Error(), p+": "+tc.location+":") || !strings.Contains(err.Error(), "array table") {
						t.Fatalf("expected an array-table conflict with its location, got %v", err)
					}
					assertTOMLContent(t, p, input)
				})
			}
		})
	}
}

func TestGetTOMLTableAbsent(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		missing     bool
	}{
		{"missing file", "", true},
		{"empty file", "", false},
		{"missing table", "[other]\nname = \"other\"\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			if !tc.missing {
				if err := os.WriteFile(p, []byte(tc.input), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := GetTOMLTable(p, "model_providers.magpie"); got != nil || err != nil {
				t.Fatalf("expected an absent table without error, got %v, %v", got, err)
			}
		})
	}
}

func TestTOMLTablesAbsent(t *testing.T) {
	for _, input := range []string{"", "\n", "name = \"top\"\n", "[[array]]\nname = \"array\"\n"} {
		p := tmpFile(t, "config.toml", input)
		if got, err := TOMLTables(p); got != nil || err != nil {
			t.Fatalf("expected no ordinary tables, got %v, %v", got, err)
		}
	}
}

func TestDelTOMLTop(t *testing.T) {
	p := tmpToml(t)
	if err := DelTOMLTop(p, "model", "hide"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	s := string(raw)
	if strings.Contains(s, "gpt-6-astra") || !strings.Contains(s, "hide = true") || !strings.HasPrefix(s, "approval_policy") {
		t.Fatalf("got:\n%s", s)
	}
}

func TestTOMLKey(t *testing.T) {
	p := tmpToml(t)
	// a table of its own, appended
	if err := SetTOMLKey(p, "agents", "default_subagent_model", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if got := readTOMLTable(t, p, "agents"); got["default_subagent_model"] != "deepseek/pro" {
		t.Fatalf("set: %v", got)
	}
	if err := DelTOMLKey(p, "agents", "default_subagent_model"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(p); string(raw) != tomlDoc {
		t.Fatalf("delete did not restore:\n%s", raw)
	}

	// in a table the user has, beside their keys
	os.WriteFile(p, []byte("model = \"x\"\n\n[agents]\nmax_threads = 6 # mine\n\n[notice]\nhide = true\n"), 0o644)
	if err := SetTOMLKey(p, "agents", "default_subagent_model", "a"); err != nil {
		t.Fatal(err)
	}
	if err := SetTOMLKey(p, "agents", "default_subagent_model", "b"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	want := "model = \"x\"\n\n[agents]\nmax_threads = 6 # mine\ndefault_subagent_model = \"b\"\n\n[notice]\nhide = true\n"
	if string(raw) != want {
		t.Fatalf("set beside:\n%s", raw)
	}
	if err := DelTOMLKey(p, "agents", "default_subagent_model"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	if string(raw) != "model = \"x\"\n\n[agents]\nmax_threads = 6 # mine\n\n[notice]\nhide = true\n" {
		t.Fatalf("delete beside:\n%s", raw)
	}
}

func TestTOMLKeysIgnoreMultilineStringContents(t *testing.T) {
	const content = "name = \"fake\"\nphantom = \"fake\"\n"
	for _, tc := range []struct {
		name, value string
	}{
		{"basic_string", "\"\"\"\n" + content + "\"\"\""},
		{"literal_string", "'''\n" + content + "'''"},
		{"string_in_array", "[\n\"\"\"\n" + content + "\"\"\"\n]"},
		{"string_in_inline_table", "{ text = '''\n" + content + "''' }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := tc.value
			for _, position := range []string{"before", "after", "absent"} {
				t.Run(position, func(t *testing.T) {
					body := "v = " + value + "\n"
					before, after := "", ""
					if position == "before" {
						before = "name = \"real\"\n"
					} else if position == "after" {
						after = "name = \"real\"\n"
					}
					input := "[a]\n" + before + body + after
					p := tmpFile(t, "config.toml", input)
					got := readTOMLTable(t, p, "a")
					if _, ok := got["phantom"]; ok {
						t.Errorf("read a key inside a string: %v", got)
					}
					if name, ok := got["name"]; (position == "absent" && ok) || (position != "absent" && name != "real") {
						t.Errorf("read the wrong name: %v", got)
					}
					if value[0] != '[' && value[0] != '{' && got["v"] != content {
						t.Errorf("multiline string = %q, want %q", got["v"], content)
					}
					if err := SetTOMLKey(p, "a", "name", "NEW"); err != nil {
						t.Fatal(err)
					}
					want := "[a]\n" + body + "name = \"NEW\"\n"
					if position == "before" {
						want = "[a]\nname = \"NEW\"\n" + body
					}
					assertTOMLContent(t, p, want)

					p = tmpFile(t, "config.toml", input)
					if err := DelTOMLKey(p, "a", "phantom"); err != nil {
						t.Fatal(err)
					}
					assertTOMLContent(t, p, input)
					if err := DelTOMLKey(p, "a", "name"); err != nil {
						t.Fatal(err)
					}
					assertTOMLContent(t, p, "[a]\n"+body)
				})
			}
		})
	}
}

func TestTOMLKeyMultilineValueSpans(t *testing.T) {
	for _, value := range []struct {
		name, text string
	}{
		{"basic_string", "\"\"\"\ntext\n\n# still a string\"\"\""},
		{"literal_string", "'''\ntext\n\n# still a string'''"},
		{"continued_string", "\"\"\"text \\\n\n  continued\"\"\""},
		{"empty_array", "[\n# empty array\n\n]"},
		{"nested_arrays", "[\n[[1, 2], [3, 4]],\n[[1]],\n# inside the array\n]"},
		{"string_in_array", "[\n'''\n# still a string''']"},
		{"string_in_inline_table", "{ text = '''\n# still a string''' }"},
	} {
		t.Run(value.name, func(t *testing.T) {
			for _, ending := range []struct {
				name, text string
			}{
				{"EOF", ""},
				{"following_table", "\n# after\n\n[b]\nkeep = true\n"},
			} {
				t.Run(ending.name, func(t *testing.T) {
					const prefix = "[a]\n# before\n"
					body := "v = " + value.text + " # inline\n"
					for _, tc := range []struct {
						name string
						edit func(string) error
						want string
					}{
						{"insert", func(p string) error { return SetTOMLKey(p, "a", "new", true) }, body + "new = true\n"},
						{"replace", func(p string) error { return SetTOMLKey(p, "a", "v", "NEW") }, "v = \"NEW\" # inline\n"},
						{"delete", func(p string) error { return DelTOMLKey(p, "a", "v") }, ""},
					} {
						t.Run(tc.name, func(t *testing.T) {
							p := tmpFile(t, "config.toml", prefix+body+ending.text)
							if err := tc.edit(p); err != nil {
								t.Fatal(err)
							}
							assertTOMLContent(t, p, prefix+tc.want+ending.text)
						})
					}
				})
			}
			t.Run("delete last key", func(t *testing.T) {
				const tail = "[b]\nkeep = true\n"
				p := tmpFile(t, "config.toml", "[a]\nv = "+value.text+"\n\n"+tail)
				if err := DelTOMLKey(p, "a", "v"); err != nil {
					t.Fatal(err)
				}
				assertTOMLContent(t, p, tail)
			})
		})
	}
}

func TestTOMLKeysUseParsedScalars(t *testing.T) {
	const input = `[a]
"na\u006de" = "line\nbreak" # comment
'enabled' = true
count = 1_000
ratio = +1.5
day = 2026-09-25
array = [1, 2]
inline = { nested = "value" }
`
	p := tmpFile(t, "config.toml", input)
	want := map[string]string{
		"name": "line\nbreak", "enabled": "true", "count": "1_000",
		"ratio": "+1.5", "day": "2026-09-25",
	}
	if got := readTOMLTable(t, p, "a"); !maps.Equal(got, want) {
		t.Fatalf("scalar keys = %v, want %v", got, want)
	}
	if err := SetTOMLKey(p, "a", "name", "NEW"); err != nil {
		t.Fatal(err)
	}
	replaced := strings.Replace(input, `"line\nbreak"`, `"NEW"`, 1)
	assertTOMLContent(t, p, replaced)
	if err := DelTOMLKey(p, "a", "enabled"); err != nil {
		t.Fatal(err)
	}
	assertTOMLContent(t, p, strings.Replace(replaced, "'enabled' = true\n", "", 1))
}

func TestSetTOMLKeyPreservesValueSurroundings(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		for _, value := range []string{`"old # text"`, "'''\n# still a value'''", "[\n1, # inside\n2\n]"} {
			prefix := "[a]\n\t\"na=me\" \t=\t "
			suffix := " \t# keep this comment\n\n[b]\nname = \"other\"\n"
			input := strings.ReplaceAll(prefix+value+suffix, "\n", ending)
			p := tmpFile(t, "config.toml", input)
			if err := SetTOMLKey(p, "a", "na=me", "NEW"); err != nil {
				t.Fatal(err)
			}
			assertTOMLContent(t, p, strings.ReplaceAll(prefix+`"NEW"`+suffix, "\n", ending))
		}
	}
}

func assertTOMLContent(t *testing.T, path, want string) {
	t.Helper()
	got := read(t, path)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	var decoded map[string]any
	if err := toml.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("invalid TOML after editing: %v\n%s", err, got)
	}
}

func TestSetTOMLTables(t *testing.T) {
	p := tmpToml(t)
	os.WriteFile(p, []byte("top = 1\n\n[a]\nx = 1\n\n[own.\"m/1\"]\nk = \"old\"\n\n[own.\"m/1\".sub]\ny = 2\n\n[b]\nz = 3\n"), 0o644)
	ts := []Table{{Name: `own."m/2"`, KVs: []KV{{Path: "k", Value: "v"}, {Path: "n", Value: 5}}}}
	if err := SetTOMLTables(p, []string{`own."m/`}, ts); err != nil {
		t.Fatal(err)
	}
	want := "top = 1\n\n[a]\nx = 1\n\n[b]\nz = 3\n\n[own.\"m/2\"]\nk = \"v\"\nn = 5\n"
	if b, _ := os.ReadFile(p); string(b) != want {
		t.Fatalf("got:\n%s", b)
	}
	if err := SetTOMLTables(p, []string{`own."m/`}, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "top = 1\n\n[a]\nx = 1\n\n[b]\nz = 3\n" {
		t.Fatalf("removed:\n%s", b)
	}
}

func TestSetTOMLTablesUsesParsedBlocks(t *testing.T) {
	const before = `note = """
[model."magpie/top-fake"]
"""

[user]
note = '''
[model."magpie/body-fake"]
'''
matrix = [
[[1, 2], [3, 4]]
]

`
	const old = `[model."magpie/old"]
note = '''
[user.fake]
'''

[model."magpie/old".sub]
value = "old"

[[model."magpie/array"]]
value = "owned array"

`
	const after = `  [[skills.config]] # keep this array table
path = "/skill"
note = '''
[model."magpie/another-fake"]
'''

[last]
enabled = true
`
	for _, tc := range []struct {
		name   string
		input  string
		tables []Table
		want   string
	}{
		{"replace", before + old + after, []Table{{Name: `model."magpie/new"`, KVs: []KV{{Path: "model", Value: "new"}}}}, before + after + "\n[model.\"magpie/new\"]\nmodel = \"new\"\n"},
		{"remove", before + old + after, nil, before + after},
		{"only fake headers", before + after, nil, before + after},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tmpFile(t, "config.toml", tc.input)
			if err := SetTOMLTables(p, []string{`model."magpie/`}, tc.tables); err != nil {
				t.Fatal(err)
			}
			assertTOMLContent(t, p, tc.want)
		})
	}
}
