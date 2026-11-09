package edit

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestTOMLWithoutDropsOnlyThatTable(t *testing.T) {
	src := `model_provider = "magpie" # projects = no
profile = "p"
sqlite_home = "/s"

[projects."/a"]
trust_level = "trusted"
note = """
[profiles.fake]
model = "inside a string"
"""

  [ projects . "/b" . sub ]
x = 1

[profiles.p]
model = "m"

[[projects.list]]
y = 2

[features]
projects_view = true
[model_providers.magpie]
name = "projects"
`
	got := string(TOMLWithout([]byte(src), "projects"))
	if strings.Contains(got, "trust_level") || strings.Contains(got, "x = 1") || strings.Contains(got, "y = 2") || strings.Contains(got, "inside a string") {
		t.Fatalf("a projects table was kept:\n%s", got)
	}
	var want, have map[string]any
	if err := toml.Unmarshal([]byte(src), &want); err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal([]byte(got), &have); err != nil {
		t.Fatalf("%v:\n%s", err, got)
	}
	delete(want, "projects")
	if !reflect.DeepEqual(want, have) {
		t.Fatalf("\nwant %v\nhave %v", want, have)
	}
}

func TestTOMLWithoutLeavesTheRestAlone(t *testing.T) {
	for _, src := range []string{
		"",
		"model = \"m\"\n[profiles.p]\nmodel = \"x\"\n",
		"[projects.\"/a\"\nbroken",                    // a parse error: the decode after it fails the same
		"projects.\"/a\".trust_level = \"trusted\"\n", // a dotted key, not a table
	} {
		if got := TOMLWithout([]byte(src), "projects"); string(got) != src {
			t.Fatalf("%q became %q", src, got)
		}
	}
}

func manyProjects(n int) []byte {
	var b strings.Builder
	b.WriteString("model_provider = \"magpie\"\nprofile = \"p\"\n[features]\nmulti_agent = true\n")
	for i := range n {
		fmt.Fprintf(&b, "\n[projects.\"/Users/someone/workspace/projects/repo-%05d\"]\ntrust_level = \"trusted\"\n", i)
	}
	b.WriteString("\n[profiles.p]\nmodel = \"m\"\n")
	return []byte(b.String())
}

func BenchmarkCodexConfigDecode(b *testing.B) {
	raw := manyProjects(10933)
	type top struct {
		Provider string         `toml:"model_provider"`
		Profiles map[string]any `toml:"profiles"`
	}
	b.Run("whole", func(b *testing.B) {
		for b.Loop() {
			var c top
			toml.Unmarshal(raw, &c)
		}
	})
	b.Run("without", func(b *testing.B) {
		for b.Loop() {
			var c top
			toml.Unmarshal(TOMLWithout(raw, "projects"), &c)
		}
	})
}

// TestValidateTOMLDefinitionsInGroups: a config of many projects is checked
// project by project, and a table or key defined twice anywhere is still
// refused, with the error the whole file gives.
func TestValidateTOMLDefinitionsInGroups(t *testing.T) {
	good := manyProjects(200)
	if err := validateTOMLDefinitions(good); err != nil {
		t.Fatal(err)
	}
	var whole map[string]any
	for name, bad := range map[string]string{
		"a project twice":         "\n[projects.\"/Users/someone/workspace/projects/repo-00007\"]\ntrust_level = \"x\"\n",
		"a key twice":             "\n[projects.\"/n\"]\na = 1\na = 2\n",
		"a subtable then its key": "\n[projects.\"/m\".s]\nb = 1\n[projects.\"/m\"]\ns = 2\n",
		"the rest's table twice":  "\n[features]\nz = 1\n",
		"a bare projects table":   "\n[projects]\n\"/Users/someone/workspace/projects/repo-00003\" = 1\n",
	} {
		raw := append(append([]byte{}, good...), bad...)
		want := toml.Unmarshal(raw, &whole)
		got := validateTOMLDefinitions(raw)
		if want == nil || got == nil || got.Error() != want.Error() {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	top := append([]byte("projects.\"/Users/someone/workspace/projects/repo-00001\".trust_level = \"x\"\n"), good...)
	if validateTOMLDefinitions(top) == nil {
		t.Error("a dotted projects key clashing with a table passed")
	}
}

func BenchmarkValidateCodexConfig(b *testing.B) {
	raw := manyProjects(10933)
	for b.Loop() {
		if err := validateTOMLDefinitions(raw); err != nil {
			b.Fatal(err)
		}
	}
}
