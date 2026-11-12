package gateway

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// WorkBuddy's stream goes on sending an empty reasoning_content with the
// text once thinking is over; it reaches the agent without it, the
// thinking and the rest as they came.
func TestChatRelayDropsEmptyReasoning(t *testing.T) {
	fresh(t)
	up := sse(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"The user said hi."}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi","reasoning_content":""}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"!","reasoning_content":""}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		`data: [DONE]`)
	a := &scripted{replies: []reply{{200, "text/event-stream", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, `"reasoning_content":""`) ||
		!strings.Contains(body, `"reasoning_content":"The user said hi."`) ||
		!strings.Contains(body, `"content":"Hi"`) || !strings.Contains(body, `"content":"!"`) ||
		!strings.Contains(body, `"finish_reason":"stop"`) || !strings.Contains(body, `"total_tokens":5`) ||
		!strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("%d %s", rec.Code, body)
	}
}

// CodeBuddy CN's own stream shapes, verified against copilot.tencent.com:
// every chunk that hasn't finished says "finish_reason": "" where the
// contract has null, and every tool-call argument fragment after the
// first repeats "function": {"name": ""}. Both reach the agent mended —
// null, and no empty name — with the real name and the arguments as they
// came.
func TestChatRelayTidiesCodeBuddyStream(t *testing.T) {
	fresh(t)
	up := sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","reasoning_content":"The user","tool_calls":[]},"finish_reason":""}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","reasoning_content":"","tool_calls":[]},"finish_reason":""}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"grep","arguments":""},"index":0}]},"finish_reason":""}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"function":{"name":"","arguments":"{\"pat"},"index":0}]},"finish_reason":""}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"function":{"name":"","arguments":"tern\"}"},"index":0}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`)
	a := &scripted{replies: []reply{{200, "text/event-stream", up}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, `"finish_reason":""`) || strings.Contains(body, `"name":""`) ||
		strings.Contains(body, `"reasoning_content":""`) ||
		!strings.Contains(body, `"finish_reason":null`) || !strings.Contains(body, `"finish_reason":"tool_calls"`) ||
		!strings.Contains(body, `"reasoning_content":"The user"`) ||
		!strings.Contains(body, `"name":"grep"`) ||
		!strings.Contains(body, `"arguments":"{\"pat`) || !strings.Contains(body, `"arguments":"tern\"}"`) ||
		!strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("%d %s", rec.Code, body)
	}
}

// Lines split across reads come out whole; one without an empty
// reasoning_content comes out byte for byte.
func TestChatTidySplitLines(t *testing.T) {
	in := "data: {\"choices\":[{\"delta\":{\"content\":\"a\",\"reasoning_content\":\"\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}],  \"x\":1}\r\n\r\ndata: [DONE]"
	var tidy chatTidy
	var out []byte
	for i := 0; i < len(in); i += 7 {
		out = append(out, tidy.write([]byte(in[i:min(i+7, len(in))]))...)
	}
	out = append(out, tidy.flush()...)
	want := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}],  \"x\":1}\r\n\r\ndata: [DONE]"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
}

// The empty finish_reason of an unfinished chunk becomes null, on every
// choice; a real one and an already null one pass as they came.
func TestChatTidyFinishReason(t *testing.T) {
	var tidy chatTidy
	in := `data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":""},{"index":1,"delta":{"content":"b"},"finish_reason":""}]}` + "\n"
	out := string(tidy.write([]byte(in)))
	if strings.Contains(out, `"finish_reason":""`) || strings.Count(out, `"finish_reason":null`) != 2 {
		t.Fatalf("got %q", out)
	}
	same := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n"
	if got := string(tidy.write([]byte(same))) + string(tidy.flush()); got != same {
		t.Fatalf("got %q", got)
	}
}

// A tool-call fragment's repeated empty name is dropped while the first
// fragment's real name stands, and a last-wins merge of the mended
// fragments assembles the call as it was meant.
func TestChatTidyToolCallNames(t *testing.T) {
	fragments := []string{
		`{"id":"call_1","type":"function","function":{"name":"grep","arguments":""},"index":0}`,
		`{"function":{"name":"","arguments":"{\"pat"},"index":0}`,
		`{"function":{"name":"","arguments":"tern\"}"},"index":0}`,
	}
	var tidy chatTidy
	name, args := "", ""
	for _, frag := range fragments {
		line := `data: {"choices":[{"index":0,"delta":{"tool_calls":[` + frag + `]}}]}` + "\n"
		out := string(tidy.write([]byte(line)))
		if strings.Contains(out, `"name":""`) {
			t.Fatalf("empty name left in %q", out)
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Name      *string `json:"name"`
							Arguments string  `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(out), "data:")), &chunk); err != nil {
			t.Fatalf("unreadable %q: %v", out, err)
		}
		fn := chunk.Choices[0].Delta.ToolCalls[0].Function
		if fn.Name != nil { // a last-wins client overwrites with any name present
			name = *fn.Name
		}
		args += fn.Arguments
	}
	if name != "grep" || args != `{"pattern"}` {
		t.Fatalf("merged %q %q", name, args)
	}

	// every empty name of a multi-call, multi-choice chunk goes
	multi := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"a"}},{"index":1,"function":{"name":"","arguments":"b"}}]}},{"index":1,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"c"}}]}}]}` + "\n"
	out := string(tidy.write([]byte(multi)))
	if strings.Contains(out, `"name":""`) || !strings.Contains(out, `"arguments":"a"`) ||
		!strings.Contains(out, `"arguments":"b"`) || !strings.Contains(out, `"arguments":"c"`) {
		t.Fatalf("got %q", out)
	}
}

// A streamed call with no index is given its call's (#3386): one index for
// a call's fragments, the next for the next call (a new id, or a later
// place in its delta), each choice its own, a null index none. A stream
// whose calls have their index goes as it came.
func TestChatTidyGivesCallsAnIndex(t *testing.T) {
	var tidy chatTidy
	for i, c := range []struct {
		choices string
		want    [][]float64 // each choice's calls' indexes
	}{
		{`[{"index":0,"delta":{"tool_calls":[{"id":"a","type":"function","function":{"name":"grep","arguments":""}}]}}]`, [][]float64{{0}}},
		{`[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"{\"pat"}}]}}]`, [][]float64{{0}}},
		{`[{"index":1,"delta":{"tool_calls":[{"id":"x","type":"function","function":{"name":"ls","arguments":"{}"}}]}}]`, [][]float64{{0}}},
		{`[{"index":0,"delta":{"tool_calls":[{"id":"a","function":{"arguments":"tern\"}"}}]}}]`, [][]float64{{0}}},
		{`[{"index":0,"delta":{"tool_calls":[{"id":"b","type":"function","function":{"name":"ls","arguments":"{}"}},{"type":"function","function":{"name":"cat","arguments":"{"}}]}}]`, [][]float64{{1, 2}}},
		{`[{"index":0,"delta":{"tool_calls":[{"index":null,"function":{"arguments":"}"}}]}}]`, [][]float64{{2}}},
		{`[{"index":0,"delta":{"tool_calls":[{"id":"d","type":"function","function":{"name":"pwd","arguments":"{}"}}]}},{"index":1,"delta":{"tool_calls":[{"id":"y","type":"function","function":{"name":"pwd","arguments":"{}"}}]}}]`, [][]float64{{3}, {1}}},
	} {
		out := tidy.write([]byte(`data: {"choices":` + c.choices + "}\n"))
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(bytes.TrimPrefix(bytes.TrimSpace(out), []byte("data:")), &chunk); err != nil {
			t.Fatalf("line %d unreadable %q: %v", i, out, err)
		}
		var got [][]float64
		for _, ch := range chunk.Choices {
			var idx []float64
			for _, tc := range ch.Delta.ToolCalls {
				n, ok := tc["index"].(float64)
				if !ok {
					n = -1
				}
				idx = append(idx, n)
			}
			got = append(got, idx)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("line %d: indexes %v, want %v: %s", i, got, c.want, out)
		}
	}

	indexed := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"ls","arguments":"{}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"pwd","arguments":"{}"}}]}}]}` + "\n"
	var other chatTidy
	if out := string(other.write([]byte(indexed))); out != indexed {
		t.Fatalf("a stream with its indexes changed: %q", out)
	}
}

// A second parallel call sent on the first one's index (jiezhengj on
// Discord: OpenDesign's amr-link numbered three calls 0,0,1) goes by an
// index of its own, with every later fragment at that index, and the
// call after it by the next: 0,1,2. A client that adds fragments up by
// index wrote the two calls' arguments into one and ended the turn. A
// fragment that only carries arguments keeps its index's call whatever
// id it has.
func TestChatTidyRenumbersAReusedIndex(t *testing.T) {
	var tidy chatTidy
	frames := []string{
		`{"index":0,"id":"call_00_a","type":"function","function":{"name":"read_file","arguments":""}}`,
		`{"index":0,"function":{"arguments":"{\"path\":\"/etc/hostname\"}"}}`,
		`{"index":0,"id":"call_01_b","type":"function","function":{"name":"list_dir","arguments":""}}`,
		`{"index":0,"function":{"arguments":"{\"path\":"}}`,
		`{"index":0,"id":"rotated","function":{"arguments":"\"/etc\"}"}}`,
		`{"index":1,"id":"call_02_c","type":"function","function":{"name":"write_file","arguments":""}}`,
		`{"index":1,"function":{"arguments":"{}"}}`,
	}
	want := []float64{0, 0, 1, 1, 1, 2, 2}
	for i, f := range frames {
		out := tidy.write([]byte(`data: {"id":"resp_02","choices":[{"index":0,"delta":{"tool_calls":[` + f + `]}}]}` + "\n"))
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(bytes.TrimPrefix(bytes.TrimSpace(out), []byte("data:")), &chunk); err != nil {
			t.Fatalf("frame %d unreadable %q: %v", i, out, err)
		}
		if got := chunk.Choices[0].Delta.ToolCalls[0]["index"]; got != want[i] {
			t.Errorf("frame %d: index %v, want %v: %s", i, got, want[i], out)
		}
	}
}

// hixonchan on X (OpenDesign, deepseek-v4.1-flash): a reply of one
// sentence and then three parallel calls came with Responses'
// output_index as the calls' index, the text holding 0: 1,1,2,3. A client
// that keeps calls in an array by index broke on the gap. The calls go
// by 0, 1, 2 in the order they start.
func TestChatTidyNumbersCallsFromZero(t *testing.T) {
	var tidy chatTidy
	frames := []string{
		`{"index":1,"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}`,
		`{"index":1,"function":{"arguments":"{\"path\":\"a\"}"}}`,
		`{"index":2,"id":"call_b","type":"function","function":{"name":"list_dir","arguments":""}}`,
		`{"index":2,"function":{"arguments":"{}"}}`,
		`{"index":3,"id":"call_c","type":"function","function":{"name":"pwd","arguments":""}}`,
		`{"index":3,"function":{"arguments":"{}"}}`,
	}
	want := []float64{0, 0, 1, 1, 2, 2}
	for i, f := range frames {
		out := tidy.write([]byte(`data: {"id":"resp_03","choices":[{"index":0,"delta":{"tool_calls":[` + f + `]}}]}` + "\n"))
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(bytes.TrimPrefix(bytes.TrimSpace(out), []byte("data:")), &chunk); err != nil {
			t.Fatalf("frame %d unreadable %q: %v", i, out, err)
		}
		if got := chunk.Choices[0].Delta.ToolCalls[0]["index"]; got != want[i] {
			t.Errorf("frame %d: index %v, want %v: %s", i, got, want[i], out)
		}
	}
}
