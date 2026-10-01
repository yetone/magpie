package profile

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// Empty values mean the agent's default, including quiet fields that
// follow the main model. Saving and loading a snapshot must keep them.
func TestSnapshotRestoresDefaults(t *testing.T) {
	for _, c := range []struct {
		agent, field, value string
		routed              bool
	}{
		{"claude", "model", "opus", false},
		{"claude", "effort", "high", false},
		{"claude", "ultracode", "on", false},
		{"claude", "haiku", "relay/flash", true},
		{"codex", "model", "gpt-5.5", false},
		{"codex", "effort", "high", false},
		{"codex", "subagent", "gpt-5.5", false},
		{"codex", "login", "api", false},
	} {
		t.Run(c.agent+"."+c.field, func(t *testing.T) {
			sandbox(t)
			a := must[*agent.Agent](t)(agent.Find(c.agent))
			if c.routed {
				if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1",
					Key: "synthetic-key", Models: []string{"pro", "flash"}}); err != nil {
					t.Fatal(err)
				}
				if err := a.Apply("model", "relay/pro"); err != nil {
					t.Fatal(err)
				}
			}
			f := a.Field(c.field)
			if f == nil || f.Get() != "" {
				t.Fatalf("%s is not at its default", c.field)
			}
			p := must[Profile](t)(Snapshot())
			key := c.agent + "." + c.field
			if v, ok := p.Fields[key]; !ok || v != "" {
				t.Errorf("snapshot omitted default %s: %+v", key, p.Fields)
			}
			if _, ok := p.Fields["gemini.model"]; ok {
				t.Error("snapshot included an undetected agent")
			}
			if err := Save("defaults", p); err != nil {
				t.Fatal(err)
			}
			ps := must[map[string]Profile](t)(Load())
			if v, ok := ps["defaults"].Fields[key]; !ok || v != "" {
				t.Errorf("saved snapshot omitted default %s", key)
			}
			if err := a.Apply(c.field, c.value); err != nil {
				t.Fatal(err)
			}
			if f.Get() == "" {
				t.Fatalf("%s did not change", c.field)
			}
			got := must[Applied](t)(Apply(ps["defaults"]))
			if f.Get() != "" || got.Changed == 0 {
				t.Errorf("%s stayed %q instead of its default; applied %+v", key, f.Get(), got)
			}
			if again := must[Applied](t)(Apply(ps["defaults"])); again.Changed != 0 {
				t.Errorf("applying defaults twice: %+v", again)
			}
		})
	}
}

// Existing partial snapshots leave missing fields alone. An explicit
// empty value, already supported by Apply, resets only that field.
func TestPartialProfileKeepsMissingFields(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".claude/settings.json"), `{"model":"opus","theme":"dark"}`)
	codex := filepath.Join(h, ".codex/config.toml")
	original := "model = \"gpt-5.5\"\nmodel_reasoning_effort = \"high\"\n"
	write(t, codex, original)
	write(t, Path(), `{"legacy":{"claude.model":""}}`)
	ps := must[map[string]Profile](t)(Load())
	if len(ps["legacy"].Fields) != 1 {
		t.Fatalf("legacy fields: %+v", ps["legacy"].Fields)
	}
	if got := must[Applied](t)(Apply(ps["legacy"])); got.Changed != 1 {
		t.Fatalf("applied legacy profile: %+v", got)
	}
	a := must[*agent.Agent](t)(agent.Find("claude"))
	if a.Field("model").Get() != "" || read(t, codex) != original {
		t.Fatalf("an explicit reset changed missing fields: claude %q, codex %s", a.Field("model").Get(), read(t, codex))
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(h, ".claude/settings.json"))), &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings) != 1 || settings["theme"] != "dark" {
		t.Fatalf("unrelated Claude settings changed: %+v", settings)
	}
}

// Defaults stored in snapshots must not add blank model entries to the
// short summary; non-default models still appear in display order.
func TestSummaryDefaults(t *testing.T) {
	for _, c := range []struct {
		fields map[string]string
		want   string
	}{
		{map[string]string{"claude.model": "", "codex.model": "", "codex.effort": ""}, ""},
		{map[string]string{"claude.model": "", "codex.model": "gpt-5.5"}, "codex gpt-5.5"},
		{map[string]string{"claude.model": "opus", "codex.model": "gpt-5.5"}, "claude opus · codex gpt-5.5"},
	} {
		if got := Summary(Profile{Fields: c.fields}); got != c.want {
			t.Errorf("summary %q, want %q", got, c.want)
		}
	}
}
