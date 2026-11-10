package agentplug

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func compileSrc(t *testing.T, src string) (*Agent, error) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "agent.js")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return Compile(dir, file)
}

// What agent.launch says goes into the user's shell profiles: the command,
// the variables' names and their values. A module can say only names a
// shell takes as they are, and values that are magpie's address or key,
// so no plugin puts a command of its own, or a value of its choosing
// (LD_PRELOAD, NODE_OPTIONS), into the user's shells.
func TestLaunchSaysOnlyWhatAShellTakesAsItIs(t *testing.T) {
	desc := func(launch, bin string) string {
		return `export const agent = { id: "lx", bin: "` + bin + `", config: "~/.lx.json", model: "model", prefix: "magpie/", launch: ` + launch + ` };`
	}
	ok := []string{
		`{ env: { GEMINI_API_KEY: "{key}", GOOGLE_GEMINI_BASE_URL: "{url}" }, model: "--model" }`,
		`{ env: { BASE: "{v1}" } }`,
		`{ env: { BASE: "{url}/api/v2/" } }`,
		`{ model: "-m" }`,
		`{ model: "--model", args: { "--settings": "{config}" }, plain: ["remote-control", "--remote-control"] }`,
	}
	for _, l := range ok {
		a, err := compileSrc(t, desc(l, "lx"))
		if err != nil || a.Desc.Launch == nil {
			t.Errorf("%s: %v", l, err)
		}
	}
	bad := map[string]string{
		desc(`{ env: { K: "{key}; rm -rf ~" } }`, "lx"):                 "agent.launch.env K",
		desc(`{ env: { LD_PRELOAD: "/tmp/x.so" } }`, "lx"):              "agent.launch.env LD_PRELOAD",
		desc(`{ env: { NODE_OPTIONS: "--require=/x {key}" } }`, "lx"):   "agent.launch.env NODE_OPTIONS",
		desc(`{ env: { BASE: "{url}/$(id)" } }`, "lx"):                  "agent.launch.env BASE",
		desc(`{ env: { BASE: "{url}/../x" } }`, "lx"):                   "",
		desc(`{ env: { "A B": "{key}" } }`, "lx"):                       "a variable's name",
		desc(`{ model: "--model; id" }`, "lx"):                          "a flag",
		desc(`{ model: "--model" }`, "lx; id"):                          "agent.bin",
		desc(`{}`, "lx"):                                                "neither env nor a model flag",
		desc(`{ model: "-m", args: { "--settings": "/tmp/x" } }`, "lx"): "agent.launch.args --settings",
		desc(`{ model: "-m", args: { "--x; id": "{config}" } }`, "lx"):  "agent.launch.args",
		desc(`{ model: "-m", args: { "-m": "{config}" } }`, "lx"):       "not the model's",
		desc(`{ model: "-m", plain: ["a b"] }`, "lx"):                   "agent.launch.plain",
		desc(`{ model: "-m", plain: ["x;id"] }`, "lx"):                  "agent.launch.plain",
	}
	for src, want := range bad {
		_, err := compileSrc(t, src)
		if want == "" {
			// a dot path is still a path under the address: a shell takes it as it is
			if err != nil {
				t.Errorf("%s: %v", src, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
}

// own is given the agent's file as parsed and the model it is on, and
// what it lists without a value is left out.
func TestOwnModels(t *testing.T) {
	a, err := compileSrc(t, `export const agent = { id: "lx", config: "~/.lx.json", model: "model" };
export function own({ config, base, model }) {
  return [...Object.keys((base ?? config).models ?? {}).map((k) => ({ value: k, direct: "Google" })), { value: model, model: "m-" + model }, { value: "" }];
}`)
	if err != nil {
		t.Fatal(err)
	}
	if !a.HasOwn() {
		t.Fatal("own not found")
	}
	got, err := a.OwnModels(map[string]any{"models": map[string]any{"mine": true}}, nil, "cur")
	if err != nil || len(got) != 2 || got[0] != (Own{Value: "mine", Direct: "Google"}) || got[1] != (Own{Value: "cur", Model: "m-cur"}) {
		t.Fatalf("own: %+v %v", got, err)
	}
	// the agent's own file, when it has one, is given as base
	got, err = a.OwnModels(map[string]any{}, map[string]any{"models": map[string]any{"theirs": true}}, "cur")
	if err != nil || len(got) != 2 || got[0].Value != "theirs" {
		t.Fatalf("own with base: %+v %v", got, err)
	}
	b, err := compileSrc(t, `export const agent = { id: "lx", config: "~/.lx.json", model: "model" };`)
	if err != nil || b.HasOwn() {
		t.Fatalf("no own: %v", err)
	}
	if got, err := b.OwnModels(nil, nil, ""); got != nil || err != nil {
		t.Fatalf("no own: %v %v", got, err)
	}
}

// agent.base, the agent's own file beside magpie's, is only for a config
// the agent is handed by agent.launch.args, and is never the config.
func TestBaseIsForAHandedConfig(t *testing.T) {
	src := func(base, launch string) string {
		return `export const agent = { id: "lx", bin: "lx", config: "~/.lx/magpie.json", base: "` + base + `", model: "model", launch: ` + launch + ` };`
	}
	if a, err := compileSrc(t, src("~/.lx/settings.json", `{ model: "-m", args: { "--settings": "{config}" } }`)); err != nil || a.Desc.Base != "~/.lx/settings.json" {
		t.Fatalf("base: %v", err)
	}
	bad := map[string]string{
		src("~/.lx/settings.json", `{ model: "-m" }`):                                     "agent.launch.args hands the agent",
		src("~/.lx/magpie.json", `{ model: "-m", args: { "--settings": "{config}" } }`):   "agent.base is agent.config",
		src("~/.lx/settings.yaml", `{ model: "-m", args: { "--settings": "{config}" } }`): "a file of agent.format",
	}
	for s, want := range bad {
		if _, err := compileSrc(t, s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", s, err, want)
		}
	}
}
