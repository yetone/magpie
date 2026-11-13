package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Ollama's own chat routes, POST /api/chat and POST /api/generate, for a
// client that chats only through them (Zed's Ollama provider,
// super_lalalalala on Discord; the ollama CLI). Each request is written
// as a Chat Completions one and served by the very handler
// /v1/chat/completions is (s.handle(provider.Chat)), on the request that
// came in, so lanGuard, corsGuard, the caller's key and the models it may
// use, its limits, routing, fallbacks and usage are /v1/chat/completions'
// own. What comes back is said Ollama's way: NDJSON lines, one per part,
// the last "done": true with done_reason and the token counts, or one
// JSON object when the request says "stream": false (Ollama's API,
// docs/api.md at v0.12.6).

// ollamaMessage is a message as Ollama's /api/chat takes and gives one.
type ollamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Thinking   string           `json:"thinking,omitempty"`
	Images     []string         `json:"images,omitempty"`
	ToolCalls  []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName   string           `json:"tool_name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// ollamaToolCall is a tool call as Ollama says one: its arguments an
// object, not a JSON string. Zed reads id as required.
type ollamaToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Index     int             `json:"index"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// ollamaRequest is /api/chat's and /api/generate's request: messages for
// the one, prompt, system and images for the other.
type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Prompt   string          `json:"prompt"`
	System   string          `json:"system"`
	Images   []string        `json:"images"`
	Tools    json.RawMessage `json:"tools"`
	Format   json.RawMessage `json:"format"`
	Options  map[string]any  `json:"options"`
	Stream   *bool           `json:"stream"`
	Think    any             `json:"think"`
}

func (s *Server) ollamaChat(generate bool) http.HandlerFunc {
	chat := s.handle(provider.Chat)
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := s.requestBody(w, r, provider.Chat)
		if !ok {
			return
		}
		var req ollamaRequest
		if err := json.Unmarshal(body, &req); err != nil {
			ollamaError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		req.Model = strings.TrimSpace(req.Model)
		if req.Model == "" {
			ollamaError(w, http.StatusBadRequest, "model is required")
			return
		}
		req.Model = ollamaName(r, req.Model)
		stream := req.Stream == nil || *req.Stream
		if generate && req.Prompt == "" && len(req.Images) == 0 || !generate && len(req.Messages) == 0 {
			// what Ollama answers a request with nothing to say: the
			// model loaded, which a client asks to warm it up
			if _, ok := ollamaEntry(r, req.Model); !ok {
				ollamaError(w, http.StatusNotFound, "model '"+req.Model+"' not found")
				return
			}
			out := map[string]any{"model": req.Model, "created_at": ollamaNow(), "done": true, "done_reason": "load"}
			if generate {
				out["response"] = ""
			} else {
				out["message"] = map[string]any{"role": "assistant", "content": ""}
			}
			if stream {
				w.Header().Set("Content-Type", "application/x-ndjson")
			}
			writeJSON(w, 200, out)
			return
		}
		cbody, err := ollamaChatBody(req, generate, stream)
		if err != nil {
			ollamaError(w, http.StatusBadRequest, err.Error())
			return
		}
		in := r.Clone(r.Context())
		in.Body, in.ContentLength = io.NopCloser(bytes.NewReader(cbody)), int64(len(cbody))
		in.Header.Set("Content-Type", "application/json")
		in.Header.Del("Content-Encoding")
		in.Header.Del("Content-Length")
		// a reply that comes back compressed couldn't be read here
		in.Header.Del("Accept-Encoding")
		ow := &ollamaWriter{w: w, header: http.Header{}, model: req.Model, generate: generate, stream: stream,
			noThinking: req.Think == false, start: time.Now()}
		chat(ow, in)
		ow.finish()
	}
}

// ollamaChatBody is req as a Chat Completions request.
func ollamaChatBody(req ollamaRequest, generate, stream bool) ([]byte, error) {
	var msgs []map[string]any
	if generate {
		if req.System != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
		}
		msgs = append(msgs, map[string]any{"role": "user", "content": ollamaContent(req.Prompt, req.Images)})
	} else {
		var err error
		if msgs, err = ollamaMessages(req.Messages); err != nil {
			return nil, err
		}
	}
	out := map[string]any{"model": req.Model, "messages": msgs, "stream": stream}
	if stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if !generate && len(req.Tools) > 0 && !bytes.Equal(bytes.TrimSpace(req.Tools), []byte("null")) {
		// Ollama's tools are Chat Completions' own shape
		out["tools"] = req.Tools
	}
	if f := bytes.TrimSpace(req.Format); len(f) > 0 && !bytes.Equal(f, []byte("null")) && !bytes.Equal(f, []byte(`""`)) {
		var name string
		switch {
		case json.Unmarshal(f, &name) == nil && name == "json":
			out["response_format"] = map[string]any{"type": "json_object"}
		case f[0] == '{':
			out["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "schema": json.RawMessage(f)}}
		default:
			return nil, fmt.Errorf("invalid format: %s", f)
		}
	}
	// the options a Chat Completions API takes; the rest (num_ctx, top_k,
	// repeat_penalty, …) are the local runner's, and have no place there
	o := req.Options
	for _, k := range []string{"temperature", "top_p", "seed", "presence_penalty", "frequency_penalty"} {
		if v, ok := o[k]; ok && v != nil {
			out[k] = v
		}
	}
	if v, ok := o["stop"]; ok && v != nil {
		out["stop"] = v
	}
	// -1 is no limit and -2 fill the window, which a Chat API says by
	// leaving max_tokens out
	if n, ok := o["num_predict"].(float64); ok && n > 0 {
		out["max_tokens"] = int(n)
	}
	switch t := req.Think.(type) {
	case string:
		if t != "" {
			out["reasoning_effort"] = t
		}
	case nil, bool:
	default:
		return nil, fmt.Errorf("invalid think value: %v", t)
	}
	return json.Marshal(out)
}

// ollamaMessages are Ollama's messages in Chat Completions' shape. A tool
// call gets an id when it has none (Ollama's have none before 0.12.x),
// and a tool's result is matched to its call by the id it names, else by
// the tool's name, else in order.
func ollamaMessages(in []ollamaMessage) ([]map[string]any, error) {
	type call struct{ id, name string }
	var open []call
	out := make([]map[string]any, 0, len(in))
	for i, m := range in {
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			out = append(out, map[string]any{"role": role, "content": m.Content})
		case "user":
			out = append(out, map[string]any{"role": "user", "content": ollamaContent(m.Content, m.Images)})
		case "assistant":
			msg := map[string]any{"role": "assistant", "content": m.Content}
			if m.Thinking != "" {
				msg["reasoning_content"] = m.Thinking
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(m.ToolCalls))
				open = open[:0]
				for j, tc := range m.ToolCalls {
					id := tc.ID
					if id == "" {
						id = fmt.Sprintf("call_%d_%d", i, j)
					}
					args := string(bytes.TrimSpace(tc.Function.Arguments))
					var str string
					switch {
					case args == "" || args == "null":
						args = "{}"
					case json.Unmarshal(tc.Function.Arguments, &str) == nil:
						// already a JSON string, as a Chat client sends them
						args = str
					}
					calls = append(calls, map[string]any{"id": id, "type": "function",
						"function": map[string]any{"name": tc.Function.Name, "arguments": args}})
					open = append(open, call{id, tc.Function.Name})
				}
				msg["tool_calls"] = calls
				if m.Content == "" {
					msg["content"] = nil
				}
			}
			out = append(out, msg)
		case "tool":
			id := m.ToolCallID
			pick := -1
			for j, c := range open {
				if id != "" && c.id == id || id == "" && m.ToolName != "" && c.name == m.ToolName {
					pick = j
					break
				}
			}
			if pick < 0 && id == "" && len(open) > 0 {
				pick = 0
			}
			if pick >= 0 {
				id = open[pick].id
				open = append(open[:pick], open[pick+1:]...)
			}
			if id == "" {
				return nil, fmt.Errorf("messages[%d]: a tool result with no tool call before it", i)
			}
			out = append(out, map[string]any{"role": "tool", "tool_call_id": id, "content": m.Content})
			if len(m.Images) > 0 {
				// a Chat tool message carries text alone: its images go
				// in a user message after it
				out = append(out, map[string]any{"role": "user", "content": ollamaContent("", m.Images)})
			}
		default:
			return nil, fmt.Errorf("messages[%d]: unknown role %q", i, m.Role)
		}
	}
	return out, nil
}

// ollamaContent is a message's text with its images, which Ollama sends
// as bare base64, as Chat Completions content parts with data URLs.
func ollamaContent(text string, images []string) any {
	if len(images) == 0 {
		return text
	}
	parts := []map[string]any{}
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		url := img
		if !strings.HasPrefix(img, "data:") {
			url = "data:" + ollamaImageType(img) + ";base64," + img
		}
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
	}
	return parts
}

// ollamaImageType is the media type of a base64 image, from its first
// bytes.
func ollamaImageType(b64 string) string {
	head := b64
	if len(head) > 64 {
		head = head[:64]
	}
	raw, _ := base64.StdEncoding.DecodeString(head[:len(head)/4*4])
	if t := http.DetectContentType(raw); strings.HasPrefix(t, "image/") {
		return t
	}
	return "image/png"
}

func ollamaNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ollamaWriter takes what the Chat Completions handler writes, a stream of
// chunks or one completion or an error, and writes it to w as Ollama
// would have.
type ollamaWriter struct {
	w          http.ResponseWriter
	header     http.Header
	model      string
	generate   bool
	stream     bool
	noThinking bool
	start      time.Time

	status  int
	sse     bool
	buf     []byte // a partial SSE line, or the whole body when it isn't a stream
	started bool   // the reply's own status and first line are written
	done    bool

	content, thinking strings.Builder
	calls             []*ollamaToolCall
	args              []strings.Builder
	finishReason      string
	promptTokens      int
	outputTokens      int
	firstToken        time.Time
}

func (o *ollamaWriter) Header() http.Header { return o.header }

func (o *ollamaWriter) Unwrap() http.ResponseWriter { return o.w }

func (o *ollamaWriter) WriteHeader(code int) {
	if o.status != 0 {
		return
	}
	o.status = code
	o.sse = strings.HasPrefix(o.header.Get("Content-Type"), "text/event-stream")
	// what says why it waits or failed (Retry-After, magpie's own marks)
	// reaches the client as it would on /v1
	for k, vs := range o.header {
		if k == "Retry-After" || strings.HasPrefix(k, "X-") {
			o.w.Header()[k] = vs
		}
	}
}

func (o *ollamaWriter) Write(p []byte) (int, error) {
	if o.status == 0 {
		o.WriteHeader(http.StatusOK)
	}
	o.buf = append(o.buf, p...)
	if !o.sse || o.status >= 300 {
		return len(p), nil
	}
	for {
		i := bytes.IndexByte(o.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(o.buf[:i]), "\r")
		o.buf = o.buf[i+1:]
		o.sseLine(line)
	}
	return len(p), nil
}

func (o *ollamaWriter) Flush() {
	if f, ok := o.w.(http.Flusher); ok && o.started {
		f.Flush()
	}
}

// sseLine takes one line of a Chat Completions stream.
func (o *ollamaWriter) sseLine(line string) {
	data, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return
	}
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return
	}
	if e := gjson.Get(data, "error"); e.Exists() {
		msg := e.Get("message").String()
		if msg == "" {
			msg = e.String()
		}
		o.line(map[string]any{"error": msg})
		o.done = true
		return
	}
	o.chunk(gjson.Parse(data))
}

// chunk takes one stream chunk, or a whole completion (its message where
// a chunk has its delta), and writes what it adds when streaming.
func (o *ollamaWriter) chunk(c gjson.Result) {
	if u := c.Get("usage"); u.IsObject() {
		o.promptTokens, o.outputTokens = int(u.Get("prompt_tokens").Int()), int(u.Get("completion_tokens").Int())
	}
	ch := c.Get("choices.0")
	if !ch.Exists() {
		return
	}
	d := ch.Get("delta")
	if !d.Exists() {
		d = ch.Get("message")
	}
	text := d.Get("content").String()
	think := d.Get("reasoning_content").String()
	if think == "" {
		think = d.Get("reasoning").String()
	}
	if o.noThinking {
		think = ""
	}
	for _, tc := range d.Get("tool_calls").Array() {
		i := len(o.calls)
		if idx := tc.Get("index"); idx.Exists() {
			i = int(idx.Int())
		}
		for len(o.calls) <= i {
			o.calls = append(o.calls, &ollamaToolCall{})
			o.args = append(o.args, strings.Builder{})
		}
		if id := tc.Get("id").String(); id != "" {
			o.calls[i].ID = id
		}
		if n := tc.Get("function.name").String(); n != "" {
			o.calls[i].Function.Name = n
		}
		o.args[i].WriteString(tc.Get("function.arguments").String())
	}
	if f := ch.Get("finish_reason").String(); f != "" {
		o.finishReason = f
	}
	if text == "" && think == "" {
		return
	}
	if o.firstToken.IsZero() {
		o.firstToken = time.Now()
	}
	o.content.WriteString(text)
	o.thinking.WriteString(think)
	if o.stream {
		o.line(o.part(text, think, nil, false))
	}
}

// part is one line of Ollama's reply.
func (o *ollamaWriter) part(text, think string, calls []ollamaToolCall, done bool) map[string]any {
	out := map[string]any{"model": o.model, "created_at": ollamaNow(), "done": done}
	if o.generate {
		out["response"] = text
		if think != "" {
			out["thinking"] = think
		}
	} else {
		msg := map[string]any{"role": "assistant", "content": text}
		if think != "" {
			msg["thinking"] = think
		}
		if len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		out["message"] = msg
	}
	if done {
		reason := "stop"
		if o.finishReason == "length" {
			reason = "length"
		}
		end := time.Now()
		first := o.firstToken
		if first.IsZero() {
			first = end
		}
		out["done_reason"] = reason
		out["total_duration"] = end.Sub(o.start).Nanoseconds()
		out["load_duration"] = 0
		out["prompt_eval_count"] = o.promptTokens
		out["prompt_eval_duration"] = first.Sub(o.start).Nanoseconds()
		out["eval_count"] = o.outputTokens
		out["eval_duration"] = end.Sub(first).Nanoseconds()
	}
	return out
}

// toolCalls are the calls the reply made, their arguments as objects.
func (o *ollamaWriter) toolCalls() []ollamaToolCall {
	var out []ollamaToolCall
	for i, c := range o.calls {
		if c.Function.Name == "" {
			continue
		}
		tc := *c
		tc.Function.Index = i
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d", i)
		}
		args := strings.TrimSpace(o.args[i].String())
		if args == "" || !json.Valid([]byte(args)) {
			if args != "" {
				// what a model wrote that isn't JSON is kept, as a string
				b, _ := json.Marshal(args)
				args = string(b)
			} else {
				args = "{}"
			}
		}
		tc.Function.Arguments = json.RawMessage(args)
		out = append(out, tc)
	}
	return out
}

// line writes one NDJSON line, the reply's status and type first.
func (o *ollamaWriter) line(v any) {
	if !o.started {
		o.started = true
		o.w.Header().Set("Content-Type", "application/x-ndjson")
		o.w.WriteHeader(http.StatusOK)
	}
	b, _ := json.Marshal(v)
	o.w.Write(append(b, '\n'))
	o.Flush()
}

// finish writes the end of the reply, once the Chat handler is done.
func (o *ollamaWriter) finish() {
	if o.status == 0 {
		o.status = http.StatusOK
	}
	if o.status >= 300 {
		msg := gjson.GetBytes(o.buf, "error.message").String()
		if msg == "" {
			msg = gjson.GetBytes(o.buf, "error").String()
		}
		if msg == "" {
			msg = strings.TrimSpace(string(o.buf))
		}
		if msg == "" {
			msg = http.StatusText(o.status)
		}
		if o.started {
			o.line(map[string]any{"error": msg})
		} else {
			ollamaError(o.w, o.status, msg)
		}
		return
	}
	if !o.sse {
		// one completion, as asked with stream false
		o.chunk(gjson.ParseBytes(o.buf))
		o.buf = nil
	} else if len(o.buf) > 0 {
		o.sseLine(strings.TrimRight(string(o.buf), "\r"))
	}
	if o.done {
		return
	}
	calls := o.toolCalls()
	if o.stream {
		if len(calls) > 0 {
			o.line(o.part("", "", calls, false))
		}
		o.line(o.part("", "", nil, true))
		return
	}
	writeJSON(o.w, 200, o.part(o.content.String(), o.thinking.String(), calls, true))
}

// withoutOllamaOptions is a Chat body without its top-level options, the
// rest byte for byte (withoutTopMember): a tool's parameter or a message
// that says "options" is left as it came, and a body without the word is
// not read at all.
func withoutOllamaOptions(body []byte) []byte {
	if !bodyHas(body, `"options"`) {
		return body
	}
	return withoutTopMember(body, "options")
}
