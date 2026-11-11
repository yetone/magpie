package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
)

func claudeEnv(t *testing.T, p string) map[string]string {
	t.Helper()
	out := map[string]string{}
	gjson.GetBytes(jsonc.ToJSON([]byte(read(t, p))), "env").ForEach(func(k, v gjson.Result) bool {
		out[k.String()] = v.String()
		return true
	})
	return out
}

func codexSet(t *testing.T, p string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := toml.Unmarshal([]byte(read(t, p)), &doc); err != nil {
		t.Fatalf("config.toml is not TOML: %v\n%s", err, read(t, p))
	}
	pol, _ := doc["shell_environment_policy"].(map[string]any)
	set, _ := pol["set"].(map[string]any)
	return set
}

// A variable lands in Claude Code's settings.json env and Codex's
// [shell_environment_policy.set], beside what the user has there, and
// turning it off takes out that and nothing else.
func TestEnvWrittenAndTakenOut(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	cfg := filepath.Join(h, ".codex/config.toml")
	write(t, settings, "{\n  // mine\n  \"model\": \"opus\",\n  \"env\": {\"MINE\": \"1\"}\n}\n")
	write(t, cfg, "model = \"gpt-5\"\n\n[shell_environment_policy]\ninherit = \"core\"\nexclude = [\"AWS_*\"]\n")

	ok(t)(SaveEnv("", EnvVar{Name: "HTTP_PROXY", Value: "http://127.0.0.1:7890", Agents: []string{"claude", "codex"}}))

	if got := claudeEnv(t, settings); got["HTTP_PROXY"] != "http://127.0.0.1:7890" || got["MINE"] != "1" || len(got) != 2 {
		t.Errorf("claude env: %v", got)
	}
	if s := read(t, settings); !strings.Contains(s, "// mine") || !strings.Contains(s, `"model": "opus"`) {
		t.Errorf("claude's settings lost the user's own:\n%s", s)
	}
	if got := codexSet(t, cfg); got["HTTP_PROXY"] != "http://127.0.0.1:7890" || len(got) != 1 {
		t.Errorf("codex set: %v", got)
	}
	if s := read(t, cfg); !strings.Contains(s, `inherit = "core"`) || !strings.Contains(s, `exclude = ["AWS_*"]`) {
		t.Errorf("codex's config lost the user's own:\n%s", s)
	}

	// unticking Codex takes it from Codex alone
	ok(t)(EnvAgents("HTTP_PROXY", []string{"claude"}))
	if got := codexSet(t, cfg); len(got) != 0 {
		t.Errorf("codex still has %v", got)
	}
	if s := read(t, cfg); !strings.Contains(s, `inherit = "core"`) || strings.Contains(s, "HTTP_PROXY") || strings.Contains(s, "shell_environment_policy.set") {
		t.Errorf("codex's config after:\n%s", s)
	}
	if got := claudeEnv(t, settings); got["HTTP_PROXY"] == "" {
		t.Errorf("claude lost it: %v", got)
	}

	ok(t)(RemoveEnv("HTTP_PROXY"))
	if got := claudeEnv(t, settings); len(got) != 1 || got["MINE"] != "1" {
		t.Errorf("claude env after remove: %v", got)
	}
}

// A variable the user set themselves is never written over, and never
// taken out; one they changed after magpie wrote it is theirs from then on.
func TestEnvNeverOverridesTheUsers(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	cfg := filepath.Join(h, ".codex/config.toml")
	write(t, settings, `{"env": {"FOO": "mine", "SAME": "x"}}`)
	write(t, cfg, "[shell_environment_policy.set]\nFOO = \"mine\"\n")

	res, err := SaveEnv("", EnvVar{Name: "FOO", Value: "magpie", Agents: []string{"claude", "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 2 {
		t.Errorf("want a problem for each agent, got %+v", res.Problems)
	}
	if got := claudeEnv(t, settings); got["FOO"] != "mine" {
		t.Errorf("claude's own FOO was overridden: %v", got)
	}
	if got := codexSet(t, cfg); got["FOO"] != "mine" {
		t.Errorf("codex's own FOO was overridden: %v", got)
	}
	// the same value is the user's too: removing it leaves it
	if _, err := RemoveEnv("FOO"); err != nil {
		t.Fatal(err)
	}
	ok(t)(SaveEnv("", EnvVar{Name: "SAME", Value: "x", Agents: []string{"claude"}}))
	ok(t)(RemoveEnv("SAME"))
	if got := claudeEnv(t, settings); got["FOO"] != "mine" || got["SAME"] != "x" {
		t.Errorf("claude's own variables were taken out: %v", got)
	}
	if got := codexSet(t, cfg); got["FOO"] != "mine" {
		t.Errorf("codex's own variable was taken out: %v", got)
	}

	// one magpie wrote and the user then changed is theirs
	ok(t)(SaveEnv("", EnvVar{Name: "BAR", Value: "1", Agents: []string{"claude", "codex"}}))
	if !strings.Contains(read(t, settings), `"BAR":"1"`) {
		t.Fatalf("settings: %s", read(t, settings))
	}
	write(t, settings, strings.Replace(read(t, settings), `"BAR":"1"`, `"BAR":"edited"`, 1))
	write(t, cfg, strings.Replace(read(t, cfg), `BAR = "1"`, `BAR = "edited"`, 1))
	ok(t)(RemoveEnv("BAR"))
	if got := claudeEnv(t, settings); got["BAR"] != "edited" {
		t.Errorf("claude's edited BAR was taken out: %v", got)
	}
	if got := codexSet(t, cfg); got["BAR"] != "edited" {
		t.Errorf("codex's edited BAR was taken out: %v", got)
	}
}

// Changing a value changes it where magpie wrote it, and only there.
func TestEnvValueChanged(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	cfg := filepath.Join(h, ".codex/config.toml")
	ok(t)(SaveEnv("", EnvVar{Name: "A", Value: "1", Agents: []string{"claude", "codex"}}))
	ok(t)(SaveEnv("A", EnvVar{Name: "A", Value: "2", Agents: []string{"claude", "codex"}}))
	if got := claudeEnv(t, settings); got["A"] != "2" || len(got) != 1 {
		t.Errorf("claude: %v", got)
	}
	if got := codexSet(t, cfg); got["A"] != "2" || len(got) != 1 {
		t.Errorf("codex: %v", got)
	}
	// a rename takes the old name out
	ok(t)(SaveEnv("A", EnvVar{Name: "B", Value: "2", Agents: []string{"claude", "codex"}}))
	if got := claudeEnv(t, settings); got["B"] != "2" || len(got) != 1 {
		t.Errorf("claude after rename: %v", got)
	}
	if got := codexSet(t, cfg); got["B"] != "2" || len(got) != 1 {
		t.Errorf("codex after rename: %v", got)
	}
	ok(t)(RemoveEnv("B"))
	if s := strings.TrimSpace(read(t, settings)); strings.Contains(s, "env") {
		t.Errorf("claude's empty env stayed: %s", s)
	}
}

// Codex's `set` written inline by the user is edited in place, never
// defined a second time.
func TestEnvCodexInlineSet(t *testing.T) {
	h := sandbox(t)
	cfg := filepath.Join(h, ".codex/config.toml")
	write(t, cfg, "[shell_environment_policy]\ninherit = \"all\"\nset = { MINE = \"1\" }\n")
	ok(t)(SaveEnv("", EnvVar{Name: "A", Value: "x y", Agents: []string{"codex"}}))
	if got := codexSet(t, cfg); got["A"] != "x y" || got["MINE"] != "1" || len(got) != 2 {
		t.Errorf("set: %v\n%s", got, read(t, cfg))
	}
	ok(t)(RemoveEnv("A"))
	if got := codexSet(t, cfg); got["MINE"] != "1" || len(got) != 1 {
		t.Errorf("set after: %v\n%s", got, read(t, cfg))
	}
	if !strings.Contains(read(t, cfg), `inherit = "all"`) {
		t.Errorf("lost inherit:\n%s", read(t, cfg))
	}
}

// A file that doesn't parse is left as it is, and the variable says why.
func TestEnvUnreadableFileLeftAlone(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	cfg := filepath.Join(h, ".codex/config.toml")
	write(t, settings, `{"env": `)
	write(t, cfg, "[shell_environment_policy\n")
	res, err := SaveEnv("", EnvVar{Name: "A", Value: "1", Agents: []string{"claude", "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 2 {
		t.Errorf("problems: %+v", res.Problems)
	}
	if read(t, settings) != `{"env": ` || read(t, cfg) != "[shell_environment_policy\n" {
		t.Errorf("a file that doesn't parse was written")
	}
}

func TestEnvChecked(t *testing.T) {
	sandbox(t)
	for _, e := range []EnvVar{{Name: "1A"}, {Name: "A-B"}, {Name: ""}, {Name: "A", Value: "x\x00"}, {Name: "A", Agents: []string{"gemini"}}} {
		if _, err := SaveEnv("", e); err == nil {
			t.Errorf("%+v was saved", e)
		}
	}
}

// A variable is carried with the library (anysearch's key, onducoder on
// X). A copy without keys carries its name and no value, and putting it
// neither blanks the key kept here nor writes an empty value into an agent.
func TestEnvIsCarried(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude/settings.json")
	ok(t)(SaveEnv("", EnvVar{Name: "ANYSEARCH_API_KEY", Value: "sk-secret", Agents: []string{"claude"}}))
	keyed, err := Collect()
	if err != nil || keyed.Env == nil || len(*keyed.Env) != 1 || (*keyed.Env)[0].Value != "sk-secret" {
		t.Fatalf("carried: %+v %v", keyed.Env, err)
	}
	bare, _ := Collect()
	bare.WithoutSecrets(func(string) bool { return false }) // whatever the name
	if v := (*bare.Env)[0]; v.Name != "ANYSEARCH_API_KEY" || v.Value != "" {
		t.Fatalf("the key went with a copy without keys: %+v", v)
	}
	if strings.Contains(mustJSON(t, bare), "sk-secret") {
		t.Fatal("the key is in a bundle without keys")
	}
	// this computer's key survives a copy without keys
	ok(t)(Put(bare.WithSecrets(keyed, func(string) bool { return false })))
	if got := claudeEnv(t, settings); got["ANYSEARCH_API_KEY"] != "sk-secret" {
		t.Fatalf("the key here was blanked: %v", got)
	}
	// a computer with none gets the name, and nothing is written for it
	h2 := sandbox(t)
	ok(t)(Put(bare))
	l, _ := Collect()
	if len(*l.Env) != 1 || (*l.Env)[0].Value != "" {
		t.Fatalf("name not carried: %+v", l.Env)
	}
	if _, err := os.Stat(filepath.Join(h2, ".claude/settings.json")); err == nil && strings.Contains(read(t, filepath.Join(h2, ".claude/settings.json")), "ANYSEARCH") {
		t.Fatal("an empty value was written into the agent")
	}
	// a copy with keys brings the key
	ok(t)(Put(keyed))
	if got := claudeEnv(t, filepath.Join(h2, ".claude/settings.json")); got["ANYSEARCH_API_KEY"] != "sk-secret" {
		t.Fatalf("the key didn't arrive: %v", got)
	}
	// one from a magpie that didn't carry them leaves this computer's alone
	old := *keyed
	old.Env = nil
	ok(t)(Put(&old))
	if l, _ := Collect(); len(*l.Env) != 1 {
		t.Fatal("variables lost to an old bundle")
	}
}
