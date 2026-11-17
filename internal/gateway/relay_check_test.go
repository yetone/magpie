package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// checkedRelayUp is an OpenAI-compatible relay that streams answer(prompt) for
// each call, naming named as the model and counting the prompt as in. A
// tool answer is name{arguments}.
func checkedRelayUp(t *testing.T, named string, in int, answer func(prompt string, tools bool) (text, tool string)) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
			Tools []any `json:"tools"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		prompt := ""
		for _, m := range req.Messages {
			prompt += fmt.Sprint(m.Content)
		}
		text, tool := answer(prompt, len(req.Tools) > 0)
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(v any) {
			j, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", j)
		}
		if tool != "" {
			i := strings.Index(tool, "{")
			ev(map[string]any{"id": "c", "model": named, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "t1", "type": "function", "function": map[string]any{"name": tool[:i], "arguments": tool[i:]}}}}}}})
		}
		for _, w := range strings.SplitAfter(text, " ") {
			if w != "" {
				ev(map[string]any{"id": "c", "model": named, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": w}}}})
			}
		}
		finish := "stop"
		if tool != "" {
			finish = "tool_calls"
		}
		ev(map[string]any{"id": "c", "model": named, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
		ev(map[string]any{"id": "c", "model": named, "choices": []any{}, "usage": map[string]any{"prompt_tokens": in, "completion_tokens": 50, "total_tokens": in + 50}})
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "key", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	return &calls
}

// relayParts keys a check's parts by kind and what each asked.
func relayParts(c RelayCheckResult) map[string]RelayPart {
	m := map[string]RelayPart{}
	for _, p := range c.Parts {
		m[p.Kind+" "+p.Ask] = p
	}
	return m
}

// relayRight answers each part as asked: the count, every product, and
// the tool each question needs.
func relayRight(prompt string, tools bool) (string, string) {
	switch {
	case strings.Contains(prompt, "Paris"):
		return "", `get_weather{"city":"Paris"}`
	case strings.Contains(prompt, "Tokyo"):
		return "", `get_time{"city":"Tokyo"}`
	}
	for _, m := range relayMaths {
		if strings.Contains(prompt, m[0]) {
			return m[1], ""
		}
	}
	var n []string
	for i := 1; i <= relayCountTo; i++ {
		n = append(n, fmt.Sprint(i))
	}
	return strings.Join(n, " "), ""
}

// A relay that answers as asked: every part matches, nothing names another
// model, and what it counted the prompt as comes back.
func TestCheckRelayAnsweredAsAsked(t *testing.T) {
	fresh(t)
	calls := checkedRelayUp(t, "gpt-5.5", 31, func(prompt string, tools bool) (string, string) {
		if strings.Contains(prompt, "1234") {
			return "7,006,652", "" // separators are read past
		}
		return relayRight(prompt, tools)
	})
	c := New().CheckRelay(context.Background(), "relay", "gpt-5.5")
	n := 1 + len(relayMaths) + len(relayTools)
	if c.Provider != "relay" || c.Model != "gpt-5.5" || len(c.Parts) != n || int(calls.Load()) != n {
		t.Fatalf("%+v, %d calls", c, calls.Load())
	}
	ps := relayParts(c)
	if len(ps) != n {
		t.Fatalf("parts not apart: %v", ps)
	}
	for k, p := range ps {
		if !p.OK || !p.Match || p.Served != "" || p.By != "" || p.Error != "" || p.Tries != 1 {
			t.Fatalf("%s: %+v", k, p)
		}
	}
	if p := ps["count "]; p.Input != 31 || p.Output != 50 || p.PromptChars == 0 {
		t.Fatalf("count: %+v", p)
	}
	if p := ps["math 987 × 654"]; p.Got != "645498" {
		t.Fatalf("math: %+v", p)
	}
	if p := ps["tool get_time"]; p.Got != `get_time({"city":"Tokyo"})` {
		t.Fatalf("tool: %+v", p)
	}
}

// A model that calls the wrong one of the two tools, or answers one
// product wrong, has those parts not as asked and the others as asked.
func TestCheckRelayCountsEachAnswer(t *testing.T) {
	fresh(t)
	checkedRelayUp(t, "gpt-5.5", 31, func(prompt string, tools bool) (string, string) {
		switch {
		case strings.Contains(prompt, "Tokyo"):
			return "", `get_weather{"city":"Tokyo"}`
		case strings.Contains(prompt, "2468"):
			return "3349176", ""
		}
		return relayRight(prompt, tools)
	})
	ps := relayParts(New().CheckRelay(context.Background(), "relay", "gpt-5.5"))
	var off []string
	for k, p := range ps {
		if !p.OK {
			t.Fatalf("%s: %+v", k, p)
		}
		if !p.Match {
			off = append(off, k)
		}
	}
	slices.Sort(off)
	if strings.Join(off, ",") != "math 2468 × 1357,tool get_time" {
		t.Fatalf("not as asked: %v", off)
	}
}

// A relay that answers with another model, a wrong product, no tool call
// and a prompt counted many times over: each is said as found.
func TestCheckRelaySaysWhatDiffers(t *testing.T) {
	fresh(t)
	checkedRelayUp(t, "qwen3-8b", 4200, func(prompt string, tools bool) (string, string) {
		if tools {
			return "It is sunny.", ""
		}
		if strings.Contains(prompt, "*") {
			return "7006552", ""
		}
		return "1 2 3 4 5", ""
	})
	ps := relayParts(New().CheckRelay(context.Background(), "relay", "gpt-5.5"))
	for k, p := range ps {
		if !p.OK || p.Match || p.Served != "qwen3-8b" {
			t.Fatalf("%s: %+v", k, p)
		}
	}
	if p := ps["math 1234 × 5678"]; p.Got != "7006552" || p.Want != "7006652" {
		t.Fatalf("math: %+v", p)
	}
	if p := ps["count "]; p.Input != 4200 {
		t.Fatalf("count: %+v", p)
	}
}

// A refusal is the call's error in the vendor's words, and nothing matches.
func TestCheckRelaySaysTheRefusal(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"message":"this key has no access to gpt-5.5"}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "key", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range New().CheckRelay(context.Background(), "relay", "gpt-5.5").Parts {
		if p.OK || p.Match || p.Status != 403 || !strings.Contains(p.Error, "no access to gpt-5.5") {
			t.Fatalf("%+v", p)
		}
	}
}

func TestReadChatStreamToolCallsInPieces(t *testing.T) {
	b := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"ci\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"ty\\\":\\\"Paris\\\"}\"}}]}}]}\n\n" +
		"data: [DONE]\n\n")
	text, calls, msg := readChatStream(b)
	if text != "" || msg != "" || len(calls) != 1 || calls[0] != `get_weather({"city":"Paris"})` {
		t.Fatalf("%q %q %q", text, calls, msg)
	}
}
