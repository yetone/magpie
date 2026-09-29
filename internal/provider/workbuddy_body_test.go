package provider

import (
	"encoding/json"
	"testing"
)

func TestWorkBuddyBody(t *testing.T) {
	roles := func(body []byte) (out []string) {
		var m struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		for _, msg := range m.Messages {
			out = append(out, msg.Role)
		}
		return out
	}

	// none: one is put first, the rest kept as they were
	in := []byte(`{"model":"deepseek-v4.1-flash","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"<b>hi</b> & 1.50"}]}`)
	out := wbBody(in)
	if got := roles(out); len(got) != 2 || got[0] != "system" || got[1] != "user" {
		t.Fatalf("roles %v: %s", got, out)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["model"] != "deepseek-v4.1-flash" || m["stream"] != true || m["max_tokens"] != 1024.0 {
		t.Fatalf("fields: %s", out)
	}
	if first := m["messages"].([]any)[0].(map[string]any); first["content"] != wbSystem {
		t.Fatalf("system: %v", first)
	}
	if user := m["messages"].([]any)[1].(map[string]any); user["content"] != "<b>hi</b> & 1.50" {
		t.Fatalf("user: %v", user)
	}

	// a system prompt later on still wants one first
	if got := roles(wbBody([]byte(`{"messages":[{"role":"user","content":"a"},{"role":"system","content":"s"}]}`))); len(got) != 3 || got[0] != "system" {
		t.Fatalf("late system: %v", got)
	}

	// already first, not a chat, or not JSON: the same bytes
	for _, b := range []string{
		`{"messages":[{"role":"system","content":"own"},{"role":"user","content":"a"}]}`,
		`{"messages":[]}`,
		`{"prompt":"a cat"}`,
		`{"messages":`,
	} {
		if got := string(wbBody([]byte(b))); got != b {
			t.Errorf("%s became %s", b, got)
		}
	}
}
