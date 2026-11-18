package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The reporter's call (wizcheu on X, 2026-11-19: "messages.422.2:
// `tool_use` ids were found without `tool_result` blocks immediately
// after: call_00_wIys8uRO8314ixexw3r35173"), with its result in the next
// message: the block DeepSeek names, the third, is words after the call.
const textAfterCallID = "call_00_wIys8uRO8314ixexw3r35173"

var textAfterCall = map[string]string{
	"thinking, call, words": `[{"type":"thinking","thinking":"Write the mock.","signature":""},{"type":"tool_use","id":"` + textAfterCallID + `","name":"Write","input":{"file_path":"/tmp/iap-mock/server.py","content":"#!/usr/bin/env python3"}},{"type":"text","text":"Now the server."}]`,
	"call, words, call":     `[{"type":"thinking","thinking":"Two files.","signature":""},{"type":"tool_use","id":"` + textAfterCallID + `","name":"Write","input":{}},{"type":"text","text":"And the other."},{"type":"tool_use","id":"call_01_b","name":"Write","input":{}}]`,
	"empty words after":     `[{"type":"thinking","thinking":"Write it.","signature":""},{"type":"tool_use","id":"` + textAfterCallID + `","name":"Write","input":{}},{"type":"text","text":""}]`,
	"thinking again, words": `[{"type":"text","text":"Writing."},{"type":"tool_use","id":"` + textAfterCallID + `","name":"Write","input":{}},{"type":"thinking","thinking":"Then the server.","signature":""},{"type":"text","text":"Next, the server."}]`,
}

// resultsFor is the user message answering every call of content.
func resultsFor(t *testing.T, content string) string {
	t.Helper()
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		t.Fatal(err)
	}
	var res []string
	for _, b := range blocks {
		if b["type"] == "tool_use" {
			res = append(res, `{"type":"tool_result","tool_use_id":"`+b["id"].(string)+`","content":"File created successfully"}`)
		}
	}
	return `{"role":"user","content":[` + strings.Join(res, ",") + `]}`
}

// A Claude Code request whose reply wrote words after its call reaches
// DeepSeek with the words before the call and the call's own result
// after it: sent as it came, DeepSeek refused it and every later request
// of the conversation, "400 due to tool use concurrency issues".
func TestWordsAfterACallGoBeforeIt(t *testing.T) {
	for name, content := range textAfterCall {
		t.Run(name, func(t *testing.T) {
			fresh(t)
			srv, sent := strictDeepSeek(t)
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k", Anthropic: srv.URL, Models: []string{"deepseek-flash"}}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"deepseek/deepseek-flash","max_tokens":32,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"refactor the page"},{"role":"assistant","content":` +
				content + `},` + resultsFor(t, content) + `]}`
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			got := sent()
			if len(got) != 1 {
				t.Fatalf("sent %d requests, want 1", len(got))
			}
			if strings.Contains(got[0], interruptedTool) {
				t.Fatalf("the call got a made-up result, though its own came:\n%s", got[0])
			}
			for _, words := range []string{"Now the server.", "And the other.", "Next, the server."} {
				if strings.Contains(content, words) && !strings.Contains(got[0], words) {
					t.Fatalf("%q is gone:\n%s", words, got[0])
				}
			}
		})
	}
}

// pairedMessages puts the words first and keeps thinking where it was; a
// message whose words come before its calls comes back byte for byte.
func TestCallsLast(t *testing.T) {
	got, changed := pairedMessages([]byte(`[{"role":"assistant","content":` + textAfterCall["thinking again, words"] + `},` + resultsFor(t, textAfterCall["thinking again, words"]) + `]`))
	if !changed {
		t.Fatal("unchanged")
	}
	var msgs []struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(got, &msgs); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, b := range msgs[0].Content {
		order = append(order, b["type"].(string))
	}
	if strings.Join(order, ",") != "text,text,tool_use,thinking" {
		t.Fatalf("order %v: %s", order, got)
	}
	paired := `{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"a","signature":""},{"type":"text","text":"b"},{"type":"tool_use","id":"x","name":"W","input":{}},{"type":"thinking","thinking":"c","signature":""},{"type":"tool_use","id":"y","name":"W","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"1"},{"type":"tool_result","tool_use_id":"y","content":"2"}]}]}`
	if out := toolBlocksPaired([]byte(paired)); string(out) != paired {
		t.Fatalf("a paired body changed:\n%s", out)
	}
	// the translated route: a built message's words after its call
	built := pairToolBlocks([]aMessage{
		{Role: "assistant", Content: []aBlock{{Type: "tool_use", ID: "x", Name: "W"}, {Type: "text", Text: " "}, {Type: "text", Text: "after"}}},
		{Role: "user", Content: []aBlock{{Type: "tool_result", ToolUseID: "x"}}},
	})
	var types []string
	for _, b := range built[0].Content {
		types = append(types, b.Type+":"+b.Text)
	}
	if strings.Join(types, ",") != "text:after,tool_use:" {
		t.Fatalf("built %v", types)
	}
}
