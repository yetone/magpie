package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstructionsAfterInput(t *testing.T) {
	for model, want := range map[string]bool{
		"gpt-6.2-sol": true, "gpt-6.2-astra": true, "codex/gpt-6.2-sol": true, "GPT-6.3": true, "gpt-7": true, "gpt-7.1-luna": true, "relay/gpt-6.10": true,
		"gpt-6-sol": false, "gpt-6.1-sol": false, "gpt-6": false, "gpt-5.6-sol": false, "gpt-5.5": false, "gpt-4.1": false, "o3": false,
		"claude-opus-5-5": false, "chatgpt-6.2": false, "": false,
	} {
		if got := InstructionsAfterInput(model); got != want {
			t.Errorf("InstructionsAfterInput(%q) = %v, want %v", model, got, want)
		}
	}
}

// A ChatGPT account's GPT-6.2 turn carries Codex's instructions first in
// its input, not on top, where the model reads them after the input and
// the cache never keeps them; an older model's stay on top.
func TestCodexBodyInstructionsFirstInInputOnGPT62(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[
	  {"slug":"gpt-6.2-sol","model_messages":{"instructions_template":"You are Codex, based on GPT-6.2.\nMore."}},
	  {"slug":"gpt-6-sol","model_messages":{"instructions_template":"You are Codex, based on GPT-6.\nMore."}}]}`), 0o644)
	type body struct {
		Instructions *string          `json:"instructions"`
		Input        []map[string]any `json:"input"`
	}
	text := func(it map[string]any) string {
		c, _ := it["content"].([]any)
		if len(c) != 1 {
			return ""
		}
		p, _ := c[0].(map[string]any)
		s, _ := p["text"].(string)
		return s
	}
	for name, tc := range map[string]struct {
		in   string
		want []string // the developer texts first in the input
	}{
		"translated": {`{"model":"gpt-6.2-sol","instructions":"You are Pi.","input":[{"type":"message","role":"user","content":"hi"}]}`,
			[]string{"You are Codex, based on GPT-6.2.\nMore.", "You are Pi."}},
		"codex's own": {`{"model":"gpt-6.2-sol","instructions":"You are Codex, based on GPT-6.2.\nMore.","input":[{"type":"message","role":"user","content":"hi"}]}`,
			[]string{"You are Codex, based on GPT-6.2.\nMore."}},
		"none": {`{"model":"gpt-6.2-sol","input":[{"type":"message","role":"user","content":"hi"}]}`,
			[]string{"You are Codex, based on GPT-6.2.\nMore."}},
	} {
		var m body
		out := codexBody([]byte(tc.in))
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		if m.Instructions != nil {
			t.Errorf("%s: instructions on top: %s", name, out)
		}
		if len(m.Input) != len(tc.want)+1 {
			t.Fatalf("%s: %s", name, out)
		}
		for i, w := range tc.want {
			if m.Input[i]["role"] != "developer" || text(m.Input[i]) != w {
				t.Errorf("%s: input[%d] = %v, want developer %q", name, i, m.Input[i], w)
			}
		}
		if m.Input[len(tc.want)]["role"] != "user" {
			t.Errorf("%s: %s", name, out)
		}
	}

	var m body
	if err := json.Unmarshal(codexBody([]byte(`{"model":"gpt-6-sol","instructions":"You are Pi.","input":[{"type":"message","role":"user","content":"hi"}]}`)), &m); err != nil {
		t.Fatal(err)
	}
	if m.Instructions == nil || *m.Instructions != "You are Codex, based on GPT-6.\nMore." || len(m.Input) != 2 || text(m.Input[0]) != "You are Pi." {
		t.Fatalf("gpt-6-sol: %+v", m)
	}
}

func TestSiwcBodyInstructionsFirstInInputOnGPT62(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(siwcBody([]byte(`{"model":"gpt-6.2-astra","instructions":"Be brief.","input":"hello"}`)), &m); err != nil {
		t.Fatal(err)
	}
	in, _ := m["input"].([]any)
	first, _ := in[0].(map[string]any)
	if _, ok := m["instructions"]; ok || len(in) != 2 || first["role"] != "developer" {
		t.Fatalf("%v", m)
	}
	if k, _ := m["prompt_cache_key"].(string); k == "" {
		t.Fatalf("no prompt_cache_key: %v", m)
	}
	m = nil
	json.Unmarshal(siwcBody([]byte(`{"model":"gpt-6-astra","instructions":"Be brief.","input":"hello"}`)), &m)
	if m["instructions"] != "Be brief." {
		t.Fatalf("gpt-6-astra: %v", m)
	}
}
