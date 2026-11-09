package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// #1454: Claude Code's auto mode classifier, through magpie, asked
// claude-sonnet-5-5 with thinking turned off and got 400 "To turn thinking
// off on this model, send "thinking": {"type": "between_tools"} instead of
// {"type": "disabled"}". Each model that refuses disabled is sent what its
// vendor asks for; every other request goes byte for byte as it came.
func TestThinkingOffAsTheModelTakesIt(t *testing.T) {
	for _, c := range []struct {
		name, path, body string
		thinking         string // the thinking sent, "" for none
		effort           string
		maxTokens        int64
	}{
		{"Sonnet 5.5 is sent between_tools", "/v1/messages",
			`{"model":"claude-sonnet-5-5","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
			`{"type":"between_tools"}`, "", 64},
		{"Sonnet 5.5 at xhigh is asked high", "/v1/messages",
			`{"model":"claude-sonnet-5-5","max_tokens":64,"thinking":{"type":"disabled"},"output_config":{"effort":"xhigh"},"messages":[]}`,
			`{"type":"between_tools"}`, "high", 64},
		{"Sonnet 5.5's between_tools takes no other field", "/v1/messages",
			`{"model":"claude-sonnet-5-5","max_tokens":64,"thinking":{"type":"between_tools","display":"omitted"},"messages":[]}`,
			`{"type":"between_tools"}`, "", 64},
		{"a relay's claude-sonnet-5.5", "/v1/messages",
			`{"model":"claude-sonnet-5.5","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
			`{"type":"between_tools"}`, "", 64},
		{"a relay's sonnet-5.5", "/v1/messages",
			`{"model":"sonnet-5.5","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
			`{"type":"between_tools"}`, "", 64},
		{"Bedrock names the model in the path", "/model/anthropic.claude-sonnet-5-5-v1:0/invoke",
			`{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
			`{"type":"between_tools"}`, "", 64},
		{"Opus 5.5 is sent no thinking, at effort low, with room", "/v1/messages",
			`{"model":"claude-opus-5-5","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
			"", "low", 64 + classifierRoom},
		{"Opus 5.5 keeps an effort asked", "/v1/messages",
			`{"model":"claude-opus-5-5","max_tokens":8000,"thinking":{"type":"disabled"},"output_config":{"effort":"high"},"messages":[]}`,
			"", "high", 8000},
		{"Fable 5.1 is sent no thinking", "/v1/messages",
			`{"model":"claude-fable-5-1","max_tokens":256,"thinking":{"type":"disabled"},"messages":[]}`,
			"", "low", 256 + classifierRoom},
		{"Fable 5.2 is sent no thinking", "/v1/messages",
			`{"model":"claude-fable-5-2","max_tokens":256,"thinking":{"type":"disabled"},"messages":[]}`,
			"", "low", 256 + classifierRoom},
		{"Mythos 5.1 is sent no thinking", "/v1/messages",
			`{"model":"claude-mythos-5-1","max_tokens":4096,"thinking":{"type":"disabled"},"messages":[]}`,
			"", "low", 4096},
		{"Opus 5 off at max is asked high", "/v1/messages",
			`{"model":"claude-opus-5","max_tokens":64,"thinking":{"type":"disabled"},"output_config":{"effort":"max"},"messages":[]}`,
			`{"type":"disabled"}`, "high", 64},
		{"Haiku 5.5 off at xhigh is asked high", "/v1/messages",
			`{"model":"claude-haiku-5-5","max_tokens":64,"thinking":{"type":"disabled"},"output_config":{"effort":"xhigh"},"messages":[]}`,
			`{"type":"disabled"}`, "high", 64},
		{"between_tools to Sonnet 5 goes as disabled", "/v1/messages",
			`{"model":"claude-sonnet-5","max_tokens":64,"thinking":{"type":"between_tools"},"messages":[]}`,
			`{"type":"disabled"}`, "", 64},
		{"between_tools to Opus 5.5 goes as none", "/v1/messages",
			`{"model":"claude-opus-5-5","max_tokens":64,"thinking":{"type":"between_tools"},"messages":[]}`,
			"", "low", 64 + classifierRoom},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := thinkingOffAsTaken([]byte(c.body), c.path)
			if !json.Valid(got) {
				t.Fatalf("not JSON: %s", got)
			}
			th := gjson.GetBytes(got, "thinking").Raw
			if th != c.thinking {
				t.Errorf("thinking = %s, want %s (sent %s)", th, c.thinking, got)
			}
			if e := gjson.GetBytes(got, "output_config.effort").String(); e != c.effort {
				t.Errorf("effort = %q, want %q (sent %s)", e, c.effort, got)
			}
			if n := gjson.GetBytes(got, "max_tokens").Int(); n != c.maxTokens {
				t.Errorf("max_tokens = %d, want %d", n, c.maxTokens)
			}
			if gjson.GetBytes(got, "messages").Raw != "[]" {
				t.Errorf("the rest of the request changed: %s", got)
			}
		})
	}
}

// Another vendor's model and every Claude that takes thinking turned off
// get the request byte for byte as it came.
func TestThinkingOffLeavesOtherModelsAlone(t *testing.T) {
	for _, body := range []string{
		`{"model":"deepseek-v4-pro","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
		`{"model":"glm-5.3","max_tokens":64,"thinking":{"type":"disabled"},"output_config":{"effort":"max"},"messages":[]}`,
		`{"model":"kimi-k2.5","max_tokens":64,"thinking":{"type":"between_tools"},"messages":[]}`,
		`{"model":"claude-sonnet-5","max_tokens":64,"thinking":{"type":"disabled"},"output_config":{"effort":"max"},"messages":[]}`,
		`{"model":"claude-opus-5","max_tokens":64,"thinking":{"type":"disabled"},"output_config":{"effort":"high"},"messages":[]}`,
		`{"model":"claude-haiku-5-5","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
		`{"model":"claude-opus-4-8","max_tokens":64,"thinking":{"type":"disabled"},"output_config":{"effort":"max"},"messages":[]}`,
		`{"model":"claude-sonnet-4-6","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
		`{"model":"claude-haiku-4-5-20251001","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
		`{"model":"claude-3-7-sonnet-20250219","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
		`{"model":"claude-sonnet-5-20261001","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
		// Sonnet 5.5 thinking, or not told: nothing to turn off
		`{"model":"claude-sonnet-5-5","max_tokens":64,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[]}`,
		`{"model":"claude-sonnet-5-5","max_tokens":64,"messages":[]}`,
		`{"model":"claude-sonnet-5-5","max_tokens":64,"thinking":{"type":"between_tools"},"output_config":{"effort":"high"},"messages":[]}`,
		// a later line than these rules know
		`{"model":"claude-sonnet-6","max_tokens":64,"thinking":{"type":"disabled"},"messages":[]}`,
	} {
		if got := thinkingOffAsTaken([]byte(body), "/v1/messages"); string(got) != body {
			t.Errorf("%s\n sent as %s", body, got)
		}
	}
}

// claudeFiveVendor answers as Anthropic does for the 5.5 models: Sonnet
// 5.5 refuses disabled in the words the reporter got through a relay, and
// between_tools above high; Opus 5.5 refuses disabled; every other model
// refuses between_tools.
type claudeFiveVendor struct {
	mu    sync.Mutex
	paths []string
	sent  []string
}

func (v *claudeFiveVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	v.paths = append(v.paths, r.URL.Path)
	v.sent = append(v.sent, string(b))
	v.mu.Unlock()
	model := gjson.GetBytes(b, "model").String()
	t := gjson.GetBytes(b, "thinking.type").String()
	effort := gjson.GetBytes(b, "output_config.effort").String()
	refuse := func(msg string) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":`+msg+`},"type":"error"}`)
	}
	sonnet55 := strings.Contains(model, "sonnet-5-5") || strings.Contains(model, "sonnet-5.5") || strings.Contains(model, "s55")
	haiku55 := strings.Contains(model, "haiku-5-5") || strings.Contains(model, "h55")
	temp := gjson.GetBytes(b, "temperature")
	switch {
	case haiku55 && temp.Exists() && temp.Float() != 1:
		refuse("\"`temperature` is deprecated for this model. (tid: 2026101003072022550024879833267)\"")
		return
	case haiku55 && gjson.GetBytes(b, "top_p").Exists():
		refuse("\"`top_p` is deprecated for this model. (tid: 2026101003072327076516219357568)\"")
		return
	case haiku55 && gjson.GetBytes(b, "top_k").Exists():
		refuse("\"`top_k` is deprecated for this model. (tid: 2026101003072467419049331484203)\"")
		return
	case sonnet55 && t == "disabled":
		refuse(`"To turn thinking off on this model, send \"thinking\": {\"type\": \"between_tools\"} instead of {\"type\": \"disabled\"}. The model does not think before responding. The short updates it writes between tool calls come back as thinking blocks. (tid: 2026101002490321168731647902503)"`)
		return
	case sonnet55 && t == "between_tools" && (effort == "xhigh" || effort == "max"):
		refuse(`"output_config.effort 'xhigh' is not supported when thinking is disabled on this model. Use effort 'high' or below."`)
		return
	case strings.Contains(model, "opus-5-5") && t == "disabled":
		refuse(`"\"thinking.type.disabled\" is not supported for this model. Use \"thinking.type.adaptive\" and \"output_config.effort\" to control thinking behavior."`)
		return
	case !sonnet55 && t == "between_tools":
		refuse(`"thinking.type.between_tools is not supported for this model."`)
		return
	}
	if gjson.GetBytes(b, "stream").Bool() {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\""+model+"\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		io.WriteString(w, `{"input_tokens":7}`)
		return
	}
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`","content":[{"type":"text","text":"<block>no</block>"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (v *claudeFiveVendor) last() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.sent[len(v.sent)-1]
}

// Every route to an Anthropic endpoint that turns thinking off — the
// classifier relayed as Claude Code sends it, count_tokens, and magpie's
// own thinking-off (an effort of none fitted, a budget that doesn't fit),
// all made before forwardOnce — reaches the 5.5 models as they take it, and a Sonnet 5.5 by a name magpie can't read is asked again with
// between_tools, once.
func TestClaudeFiveFiveThinkingOffOnTheWire(t *testing.T) {
	fresh(t)
	up := &claudeFiveVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-sonnet-5", "s55"}}); err != nil {
		t.Fatal(err)
	}
	classifier := `{"model":"relay/%s","max_tokens":64,"temperature":0,"stop_sequences":["</block>"],"thinking":{"type":"disabled"},"system":[{"type":"text","text":"Answer <block>yes</block> or <block>no</block>."}],"messages":[{"role":"user","content":"<transcript>ls</transcript>"}]}`
	for _, c := range []struct {
		name, path, body string
		calls            int
		thinking         string
	}{
		{"classifier to Sonnet 5.5", "/v1/messages", strings.ReplaceAll(classifier, "%s", "claude-sonnet-5-5"), 1, `{"type":"between_tools"}`},
		{"classifier to Opus 5.5", "/v1/messages", strings.ReplaceAll(classifier, "%s", "claude-opus-5-5"), 1, ""},
		{"classifier to Sonnet 5 as it came", "/v1/messages", strings.ReplaceAll(classifier, "%s", "claude-sonnet-5"), 1, `{"type":"disabled"}`},
		{"count_tokens to Sonnet 5.5", "/v1/messages/count_tokens",
			`{"model":"relay/claude-sonnet-5-5","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`, 1, `{"type":"between_tools"}`},
		{"a name magpie can't read is asked again", "/v1/messages", strings.ReplaceAll(classifier, "%s", "s55"), 2, `{"type":"between_tools"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			up.mu.Lock()
			before := len(up.sent)
			up.mu.Unlock()
			code, body := post(t, c.path, c.body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			up.mu.Lock()
			calls := len(up.sent) - before
			up.mu.Unlock()
			if calls != c.calls {
				t.Errorf("%d calls upstream, want %d", calls, c.calls)
			}
			got := up.last()
			if th := gjson.Get(got, "thinking").Raw; th != c.thinking {
				t.Errorf("thinking = %s, want %s: %s", th, c.thinking, got)
			}
		})
	}
}

// An agent turning thinking off as Sonnet 5.5 takes it, between_tools, has
// it off as disabled does on a model magpie translates the request for.
func TestBetweenToolsReadAsThinkingOff(t *testing.T) {
	for _, typ := range []string{"disabled", "between_tools"} {
		r, err := parseAnthropic([]byte(`{"model":"m","max_tokens":64,"thinking":{"type":"` + typ + `"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if !r.ThinkOff || r.Thinking || r.Effort != "" {
			t.Errorf("%s: ThinkOff = %v, Thinking = %v, Effort = %q; want thinking off", typ, r.ThinkOff, r.Thinking, r.Effort)
		}
	}
}

// Claude Code's auto mode classifier asks at temperature 0, and Haiku 5.5
// answers "`temperature` is deprecated for this model" (#1454). The models
// Anthropic documents as refusing temperature, top_p and top_k are sent
// none; every other request goes byte for byte as it came.
func TestSamplingAsTheModelTakesIt(t *testing.T) {
	sampled := `{"model":"%s","max_tokens":64,"temperature":0,"top_p":0.5,"top_k":5,"stop_sequences":["</block>"],"messages":[{"role":"user","content":"a <b> & c"}]}`
	for _, m := range []string{"claude-haiku-5-5", "claude-haiku-5.5", "anthropic.claude-haiku-5-5-20261001-v1:0",
		"claude-sonnet-5-5", "claude-sonnet-5", "claude-sonnet-5-20260801", "claude-opus-5-5", "claude-opus-5",
		"claude-opus-4-8", "claude-opus-4-7", "claude-fable-5-2", "claude-fable-5-1", "claude-fable-5", "claude-mythos-5-1"} {
		got := samplingAsTaken([]byte(strings.ReplaceAll(sampled, "%s", m)), "/v1/messages")
		if hasSampling(got) {
			t.Errorf("%s: sampling still sent: %s", m, got)
		}
		if gjson.GetBytes(got, "stop_sequences.0").String() != "</block>" || gjson.GetBytes(got, "max_tokens").Int() != 64 {
			t.Errorf("%s: other fields changed: %s", m, got)
		}
	}
	// Bedrock names the model in the path alone
	body := `{"max_tokens":64,"temperature":0,"messages":[]}`
	if got := samplingAsTaken([]byte(body), "/model/anthropic.claude-haiku-5-5-20261001-v1:0/invoke"); hasSampling(got) {
		t.Errorf("Bedrock path: sampling still sent: %s", got)
	}
	for _, m := range []string{"claude-haiku-4-5", "claude-haiku-4-5-20251001", "claude-sonnet-4-6", "claude-sonnet-4-5",
		"claude-opus-4-6", "claude-opus-4-1", "claude-3-7-sonnet-latest", "claude-sonnet-6", "claude-haiku-6",
		"deepseek-v4-pro", "glm-5.3", "kimi-k3", "gpt-6"} {
		in := strings.ReplaceAll(sampled, "%s", m)
		if got := samplingAsTaken([]byte(in), "/v1/messages"); string(got) != in {
			t.Errorf("%s changed:\n got %s\nwant %s", m, got, in)
		}
	}
	if in := `{"model":"claude-haiku-5-5","max_tokens":64,"messages":[]}`; string(samplingAsTaken([]byte(in), "/v1/messages")) != in {
		t.Error("a body with no sampling changed")
	}
}

// Haiku 5.5 on the wire: the classifier relayed, magpie's own classifier
// built from chat at temperature 0, and a Haiku 5.5 by a name magpie can't
// read, asked again without sampling, once. Haiku 4.5 keeps its
// temperature.
func TestHaikuFiveFiveSamplingOnTheWire(t *testing.T) {
	fresh(t)
	up := &claudeFiveVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-haiku-5-5", "claude-haiku-4-5", "h55"}}); err != nil {
		t.Fatal(err)
	}
	classifier := `{"model":"relay/%s","max_tokens":64,"temperature":0,"stop_sequences":["</block>"],"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"<transcript>ls</transcript>"}]}`
	for _, c := range []struct {
		name, path, body string
		calls            int
		temperature      string // as sent, "" for none
	}{
		{"classifier to Haiku 5.5", "/v1/messages", strings.ReplaceAll(classifier, "%s", "claude-haiku-5-5"), 1, ""},
		{"top_p and top_k to Haiku 5.5", "/v1/messages",
			`{"model":"relay/claude-haiku-5-5","max_tokens":64,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hi"}]}`, 1, ""},
		{"magpie's own classifier from chat", "/v1/chat/completions",
			`{"model":"relay/claude-haiku-5-5","stream":false,"temperature":0,"max_tokens":2048,"messages":[{"role":"user","content":"which kind?"}]}`, 1, ""},
		{"Haiku 4.5 keeps its temperature", "/v1/messages", strings.ReplaceAll(classifier, "%s", "claude-haiku-4-5"), 1, "0"},
		{"a name magpie can't read is asked again", "/v1/messages", strings.ReplaceAll(classifier, "%s", "h55"), 2, ""},
		// the same request from a Chat client is translated, where the
		// name can't be read to leave the sampling out up front either
		{"a name magpie can't read is asked again from chat", "/v1/chat/completions",
			`{"model":"relay/h55","stream":false,"temperature":0,"max_tokens":2048,"messages":[{"role":"user","content":"which kind?"}]}`, 2, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			up.mu.Lock()
			before := len(up.sent)
			up.mu.Unlock()
			code, body := post(t, c.path, c.body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			up.mu.Lock()
			calls := len(up.sent) - before
			up.mu.Unlock()
			if calls != c.calls {
				t.Errorf("%d calls upstream, want %d", calls, c.calls)
			}
			got := up.last()
			if tp := gjson.Get(got, "temperature").Raw; tp != c.temperature {
				t.Errorf("temperature = %s, want %q: %s", tp, c.temperature, got)
			}
		})
	}
}
