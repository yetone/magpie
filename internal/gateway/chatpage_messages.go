package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
)

// The chat page (ChatPagePath) speaks DeepSeek's Anthropic-compatible
// Messages API with two server tools of DeepSeek's: deepseek_web_search
// and deepseek_read_url, whose calls and results it shows in its own
// shapes (its parseEvent.ts). chatMessages answers it with any of magpie's
// models: the search is Anthropic's web_search tool, which magpie runs for
// a model that can't search (searchRounds), and the link reading a tool of
// the request's own, read_url, which is run here with magpie's web_fetch
// (fetchPage) and its result handed back to the model, round after round,
// as one reply to the page.

const (
	chatRounds    = 10     // a reply's rounds of reading links at most
	chatReadChars = 40_000 // a page's characters a model is given
	chatReadTool  = "read_url"
	// the page's own system prompt, naming DeepSeek: another model is
	// not told it is DeepSeek
	chatDemoSystem = "该助手为DeepSeek，由深度求索公司创造。"
	// chatSigned is what a read page's text is kept under in the
	// result's signature, which the page sends back with its history
	chatSigned = "m1."
)

var chatReadDef = map[string]any{
	"name":        chatReadTool,
	"description": "Read a web page and return its text. Use it for a link the user gives, or for a page a search found when its snippet isn't enough.",
	"input_schema": map[string]any{"type": "object", "properties": map[string]any{
		"url": map[string]any{"type": "string", "description": "The page's http:// or https:// URL"},
	}, "required": []string{"url"}},
}

type chatRequest struct {
	System   json.RawMessage `json:"system"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Thinking *struct {
		Type string `json:"type"`
	} `json:"thinking"`
	Tools []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tools"`
	Title *struct {
		Enabled bool `json:"enabled"`
	} `json:"title"`
}

// chatMessages is the page's POST /anthropic/v1/messages.
func (s *Server) chatMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<20))
	if err != nil {
		chatError(w, http.StatusRequestEntityTooLarge, "the request is too large")
		return
	}
	var in chatRequest
	if err := json.Unmarshal(body, &in); err != nil {
		chatError(w, http.StatusBadRequest, "the body isn't the chat page's JSON: "+err.Error())
		return
	}
	model := chatModel(r)
	if model == "" {
		chatError(w, http.StatusServiceUnavailable, "magpie has no model to chat with: add a provider in magpie first")
		return
	}
	req, title := chatUpstream(r, &in, model)
	t := &chatTurn{s: s, r: r, w: w, req: req, model: model, title: title}
	t.run()
}

// chatUpstream is the Messages request the page's asks for, and the
// title the page asked for (""): its reminder of date and place a
// system line, its files the blocks they are, the pages it read before
// with the question they were read for, its tools Anthropic's search
// and read_url.
func chatUpstream(r *http.Request, in *chatRequest, model string) (map[string]any, string) {
	var system []string
	var sys string
	if json.Unmarshal(in.System, &sys) == nil && sys != "" && strings.TrimSpace(sys) != chatDemoSystem {
		system = append(system, sys)
	}
	var msgs []map[string]any
	reminder, title := "", ""
	for _, m := range in.Messages {
		switch m.Role {
		case "latest_reminder":
			json.Unmarshal(m.Content, &reminder)
		case "user":
			content := chatUserContent(r, m.Content)
			if title == "" {
				title = chatTitle(chatQuestion(m.Content))
			}
			msgs = append(msgs, map[string]any{"role": "user", "content": content})
		case "assistant":
			content, read := chatAssistantContent(m.Content)
			// the pages read for a question go after it, as the
			// page's own API puts them
			if len(read) > 0 && len(msgs) > 0 && msgs[len(msgs)-1]["role"] == "user" {
				prev := msgs[len(msgs)-1]["content"].([]map[string]any)
				msgs[len(msgs)-1]["content"] = append(prev, read...)
			}
			if len(content) > 0 {
				msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
			}
		}
	}
	if line := chatReminder(reminder); line != "" {
		system = append(system, line)
	}
	out := map[string]any{"model": model, "messages": msgs, "stream": true}
	if len(system) > 0 {
		out["system"] = strings.Join(system, "\n\n")
	}
	if in.Thinking != nil {
		switch in.Thinking.Type {
		case "enabled":
			out["thinking"] = map[string]any{"type": "adaptive"}
		case "disabled":
			out["thinking"] = map[string]any{"type": "disabled"}
		}
	}
	var tools []any
	for _, t := range in.Tools {
		switch t.Type {
		case "deepseek_web_search":
			tools = append(tools, map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 5})
		case "deepseek_read_url":
			tools = append(tools, chatReadDef)
		}
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if in.Title == nil || !in.Title.Enabled {
		title = ""
	}
	return out, title
}

// chatTitle is a conversation's title, from its first question.
func chatTitle(q string) string {
	q = strings.TrimSpace(q)
	if i := strings.IndexByte(q, '\n'); i >= 0 {
		q = strings.TrimSpace(q[:i])
	}
	if r := []rune(q); len(r) > 24 {
		q = string(r[:24]) + "…"
	}
	return q
}

// chatQuestion is a question's own words, without its files.
func chatQuestion(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &blocks)
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return b.Text
		}
	}
	return ""
}

// chatReminder is the page's latest_reminder ("date,weekday,place,Web,
// language", the place empty when it has none) as a line of the system
// prompt.
func chatReminder(s string) string {
	parts := strings.Split(s, ",")
	if len(parts) < 5 {
		return ""
	}
	date, weekday := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	lang := strings.TrimSpace(parts[len(parts)-1])
	place := strings.TrimSpace(strings.Join(parts[2:len(parts)-2], ","))
	line := "Today is " + date
	if weekday != "" {
		line += ", " + weekday
	}
	line += "."
	if place != "" {
		line += " The user is in " + place + "."
	}
	if lang != "" {
		line += " The user's language is " + lang + "."
	}
	return line
}

// chatUserContent is a question's blocks: its text, and each file the
// page uploaded as the block a model takes it as.
func chatUserContent(r *http.Request, raw json.RawMessage) []map[string]any {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []map[string]any{{"type": "text", "text": s}}
	}
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source struct {
			Type   string `json:"type"`
			FileID string `json:"file_id"`
		} `json:"source"`
	}
	json.Unmarshal(raw, &blocks)
	out := []map[string]any{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": b.Text})
			}
		case "image", "document":
			if f := chatFileOf(r, b.Source.FileID); f != nil {
				out = append(out, chatFileBlock(f))
			} else {
				out = append(out, map[string]any{"type": "text", "text": "(A file attached here is no longer available.)"})
			}
		}
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"type": "text", "text": "…"})
	}
	return out
}

// chatAssistantContent is an answer the page sends back: its text, and
// the thinking of an answer it asks to go on with; the pages it read are
// given back as text, to go after the question they were read for.
func chatAssistantContent(raw json.RawMessage) (content, read []map[string]any) {
	var blocks []map[string]any
	json.Unmarshal(raw, &blocks)
	for _, b := range blocks {
		switch b["type"] {
		case "text":
			if t, _ := b["text"].(string); t != "" {
				content = append(content, map[string]any{"type": "text", "text": t})
			}
		case "thinking":
			t, _ := b["thinking"].(string)
			sig, _ := b["signature"].(string)
			if strings.TrimSpace(t) != "" && sig != "" {
				content = append(content, map[string]any{"type": "thinking", "thinking": t, "signature": sig})
			}
		case "web_fetch_tool_result":
			if text := chatReadBack(b); text != "" {
				read = append(read, map[string]any{"type": "text", "text": text})
			}
		}
	}
	return content, read
}

// chatReadBack is a page read before, from its result block: where it
// was read and its text, which chatSign kept in the signature.
func chatReadBack(b map[string]any) string {
	sig, _ := b["signature"].(string)
	enc, ok := strings.CutPrefix(sig, chatSigned)
	if !ok {
		return ""
	}
	text, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return ""
	}
	content, _ := b["content"].(map[string]any)
	u, _ := content["url"].(string)
	return "Content of " + u + ":\n\n" + string(text)
}

func chatSign(text string) string {
	return chatSigned + base64.StdEncoding.EncodeToString([]byte(text))
}

// ---- the reply --------------------------------------------------------------

// chatTurn is one reply to the page: rounds of the model, each read_url
// call it makes run between them, all said as one message.
type chatTurn struct {
	s       *Server
	r       *http.Request
	w       http.ResponseWriter
	req     map[string]any
	model   string
	title   string
	started bool // the page was sent message_start
	index   int  // the page's next block index
	usage   [4]int
	failed  bool
}

func (t *chatTurn) event(name string, v any) bool {
	b, _ := json.Marshal(v)
	if _, err := fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return false
	}
	if f, ok := t.w.(http.Flusher); ok {
		f.Flush()
	}
	return true
}

func (t *chatTurn) start() {
	if t.started {
		return
	}
	t.started = true
	h := t.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	t.w.WriteHeader(http.StatusOK)
	t.event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": randomHex(16), "type": "message", "role": "assistant", "model": t.model, "content": []any{},
		"usage": map[string]any{"input_tokens": 0, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 0},
	}})
}

// block sends the page one whole block.
func (t *chatTurn) block(b map[string]any) {
	t.start()
	t.event("content_block_start", map[string]any{"type": "content_block_start", "index": t.index, "content_block": b})
	t.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": t.index})
	t.index++
}

func (t *chatTurn) fail(msg string) {
	if !t.started {
		chatError(t.w, http.StatusBadGateway, msg)
		t.failed = true
		return
	}
	t.event("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
	t.failed = true
}

func (t *chatTurn) run() {
	stop := "end_turn"
	for n := 0; ; n++ {
		res := t.round()
		if t.failed {
			return
		}
		stop = res.stop
		if res.stop != "tool_use" || len(res.calls) == 0 || n+1 >= chatRounds {
			break
		}
		results := []map[string]any{}
		for _, c := range res.calls {
			results = append(results, t.read(c))
		}
		msgs := t.req["messages"].([]map[string]any)
		t.req["messages"] = append(msgs,
			map[string]any{"role": "assistant", "content": res.content},
			map[string]any{"role": "user", "content": results})
	}
	switch stop {
	case "end_turn", "max_tokens", "refusal":
	default:
		// tool_use left with rounds spent, stop_sequence, pause_turn
		stop = "end_turn"
	}
	t.start()
	delta := map[string]any{"stop_reason": stop, "stop_sequence": nil}
	if t.title != "" {
		delta["title"] = t.title
	}
	t.event("message_delta", map[string]any{"type": "message_delta", "delta": delta, "usage": map[string]any{
		"input_tokens": t.usage[0], "cache_creation_input_tokens": t.usage[1], "cache_read_input_tokens": t.usage[2], "output_tokens": t.usage[3]}})
	t.event("message_stop", map[string]any{"type": "message_stop"})
}

type chatPageCall struct {
	id, name string
	input    map[string]any
}

type chatRound struct {
	stop    string
	content []map[string]any // the answer, to send back with the calls' results
	calls   []chatPageCall
}

// chatBlock is a block of a round as it arrives.
type chatBlock struct {
	start map[string]any
	out   int // the page's index, -1 for one it is shown whole at its end
	text  strings.Builder
	sig   string
	args  strings.Builder
}

// round asks the model once, through the gateway's own Messages route,
// and tells the page what it says as it says it.
func (t *chatTurn) round() chatRound {
	body, _ := json.Marshal(t.req)
	in := chatInner(t.r, "/v1/messages", "application/json", body)
	in.Header.Set("Accept", "text/event-stream")
	var res chatRound
	blocks := map[int]*chatBlock{}
	var order []*chatBlock
	var errMsg string
	tap := &sseTap{h: http.Header{}, on: func(name string, data []byte) bool {
		var ev map[string]any
		if json.Unmarshal(data, &ev) != nil {
			return true
		}
		idx := -1
		if f, ok := ev["index"].(float64); ok {
			idx = int(f)
		}
		switch name {
		case "message_start":
			msg, _ := ev["message"].(map[string]any)
			t.addUsage(msg["usage"], true)
		case "content_block_start":
			cb, _ := ev["content_block"].(map[string]any)
			b := &chatBlock{start: cb, out: -1}
			blocks[idx] = b
			order = append(order, b)
			switch cb["type"] {
			case "text":
				b.out = t.index
				t.index++
				t.start()
				return t.event(name, map[string]any{"type": name, "index": b.out, "content_block": map[string]any{"type": "text", "text": ""}})
			case "thinking":
				b.out = t.index
				t.index++
				t.start()
				return t.event(name, map[string]any{"type": name, "index": b.out, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
			case "web_search_tool_result":
				t.block(chatSearchResult(cb))
			}
		case "content_block_delta":
			b := blocks[idx]
			if b == nil {
				return true
			}
			d, _ := ev["delta"].(map[string]any)
			switch d["type"] {
			case "text_delta":
				s, _ := d["text"].(string)
				b.text.WriteString(s)
			case "thinking_delta":
				s, _ := d["thinking"].(string)
				b.text.WriteString(s)
			case "signature_delta":
				s, _ := d["signature"].(string)
				b.sig += s
			case "input_json_delta":
				s, _ := d["partial_json"].(string)
				b.args.WriteString(s)
				return true
			}
			if b.out >= 0 {
				return t.event(name, map[string]any{"type": name, "index": b.out, "delta": d})
			}
		case "content_block_stop":
			b := blocks[idx]
			if b == nil {
				return true
			}
			if b.out >= 0 {
				return t.event(name, map[string]any{"type": name, "index": b.out})
			}
			if b.start["type"] == "server_tool_use" && b.start["name"] == "web_search" {
				var q struct {
					Query string `json:"query"`
				}
				json.Unmarshal([]byte(b.args.String()), &q)
				t.block(map[string]any{"type": "server_tool_use", "id": b.start["id"], "name": "deepseek_web_search",
					"kind": "search", "input": map[string]any{"action": "search", "queries": []string{q.Query}}})
			}
		case "message_delta":
			d, _ := ev["delta"].(map[string]any)
			if s, _ := d["stop_reason"].(string); s != "" {
				res.stop = s
			}
			t.addUsage(ev["usage"], false)
		case "error":
			e, _ := ev["error"].(map[string]any)
			errMsg, _ = e["message"].(string)
			if errMsg == "" {
				errMsg = string(data)
			}
		case "ping":
			if t.started {
				return t.event("ping", map[string]any{"type": "ping"})
			}
		}
		return true
	}}
	keepsSignIn(t.s.handle(provider.Anthropic))(tap, in)
	if tap.status != 0 && tap.status != http.StatusOK || !tap.sse {
		msg := tap.body.String()
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(tap.body.Bytes(), &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if !t.started {
			status := tap.status
			if status == 0 || status == http.StatusOK {
				status = http.StatusBadGateway
			}
			chatError(t.w, status, msg)
			t.failed = true
			return res
		}
		t.fail(msg)
		return res
	}
	if errMsg != "" {
		t.fail(errMsg)
		return res
	}
	// the answer as it goes back to the model with the calls' results
	for _, b := range order {
		switch b.start["type"] {
		case "text":
			res.content = append(res.content, map[string]any{"type": "text", "text": b.text.String()})
		case "thinking":
			res.content = append(res.content, map[string]any{"type": "thinking", "thinking": b.text.String(), "signature": b.sig})
		case "redacted_thinking", "web_search_tool_result":
			res.content = append(res.content, b.start)
		case "server_tool_use", "tool_use":
			input := map[string]any{}
			json.Unmarshal([]byte(b.args.String()), &input)
			if len(input) == 0 {
				if in, ok := b.start["input"].(map[string]any); ok {
					input = in
				}
			}
			id, _ := b.start["id"].(string)
			name, _ := b.start["name"].(string)
			res.content = append(res.content, map[string]any{"type": b.start["type"], "id": id, "name": name, "input": input})
			if b.start["type"] == "tool_use" {
				res.calls = append(res.calls, chatPageCall{id: id, name: name, input: input})
			}
		}
	}
	return res
}

func (t *chatTurn) addUsage(v any, start bool) {
	u, _ := v.(map[string]any)
	for i, k := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"} {
		f, ok := u[k].(float64)
		if !ok {
			continue
		}
		if start || i < 3 && f == 0 {
			// message_start's count; message_delta's input counts, when
			// it gives them, are the same round's again
			if start {
				t.usage[i] += int(f)
			}
			continue
		}
		if i == 3 {
			t.usage[i] += int(f)
		}
	}
}

// chatSearchResult is a web_search_tool_result as the page shows it.
func chatSearchResult(cb map[string]any) map[string]any {
	items := []map[string]any{}
	switch c := cb["content"].(type) {
	case []any:
		for _, it := range c {
			m, _ := it.(map[string]any)
			if m["type"] != "web_search_result" {
				continue
			}
			u, _ := m["url"].(string)
			title, _ := m["title"].(string)
			var site any
			if pu, err := url.Parse(u); err == nil && pu.Host != "" {
				site = strings.TrimPrefix(pu.Hostname(), "www.")
			}
			age, _ := m["page_age"].(string)
			var pageAge any
			if age != "" {
				pageAge = age
			}
			items = append(items, map[string]any{"type": "web_search_result", "url": u, "title": title,
				"snippet": nil, "site_name": site, "site_icon": nil, "encrypted_content": "", "page_age": pageAge})
		}
	case map[string]any:
		items = append(items, map[string]any{"type": "web_search_tool_result_error", "error_code": "unavailable"})
	}
	return map[string]any{"type": "web_search_tool_result", "tool_use_id": cb["tool_use_id"], "kind": "search", "content": items}
}

// read runs a read_url call: the page is shown the call and the page it
// read, and the model is given the page's text.
func (t *chatTurn) read(c chatPageCall) map[string]any {
	result := func(text string, isErr bool) map[string]any {
		return map[string]any{"type": "tool_result", "tool_use_id": c.id, "content": text, "is_error": isErr}
	}
	if c.name != chatReadTool {
		return result("There is no tool named "+c.name+".", true)
	}
	raw, _ := c.input["url"].(string)
	raw = strings.TrimSpace(raw)
	t.block(map[string]any{"type": "server_tool_use", "id": c.id, "name": "deepseek_read_url", "kind": "read_link",
		"input": map[string]any{"action": "read_link", "url": raw}})
	failed := func(why string) map[string]any {
		t.block(map[string]any{"type": "web_fetch_tool_result", "tool_use_id": c.id, "kind": "read_link",
			"content": map[string]any{"type": "web_fetch_tool_result_error", "error_code": "url_not_accessible"}})
		return result(why, true)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return failed("read_url reads only http:// and https:// URLs, not " + raw)
	}
	release, refused := budget.Reserve(access.Caller(t.r.Context()), int64(len(raw)), "", limitClock())
	if refused != nil {
		return failed(refused.Error())
	}
	defer release()
	ctx, cancel := context.WithTimeout(netproxy.With(t.r.Context(), ""), webFetchTimeout)
	defer cancel()
	title, text, final, err := fetchPage(ctx, u)
	if err != nil {
		return failed("couldn't read " + u.Redacted() + ": " + err.Error())
	}
	if r := []rune(text); len(r) > chatReadChars {
		text = string(r[:chatReadChars])
	}
	var titled any
	if title != "" {
		titled = title
	}
	t.block(map[string]any{"type": "web_fetch_tool_result", "tool_use_id": c.id, "kind": "read_link",
		"content": map[string]any{"type": "web_fetch_result", "url": final, "published_at": nil,
			"content": map[string]any{"type": "document", "title": titled,
				"source": map[string]any{"type": "text", "media_type": "text/plain", "data": ""}}},
		"signature": chatSign(fetchPart(final, title, text, 0, chatReadChars))})
	return result(fetchPart(final, title, text, 0, chatReadChars), false)
}

// sseTap is the ResponseWriter a round is answered into: a stream's
// events are handed to on as each is written whole, anything else is
// kept to be read after.
type sseTap struct {
	h      http.Header
	status int
	sse    bool
	buf    []byte
	body   bytes.Buffer
	on     func(name string, data []byte) bool
	gone   bool
}

func (t *sseTap) Header() http.Header { return t.h }

func (t *sseTap) WriteHeader(code int) {
	if t.status == 0 {
		t.status = code
		t.sse = code == http.StatusOK && strings.HasPrefix(t.h.Get("Content-Type"), "text/event-stream")
	}
}

func (t *sseTap) Flush() {}

func (t *sseTap) Write(p []byte) (int, error) {
	t.WriteHeader(http.StatusOK)
	if t.gone {
		return 0, http.ErrHandlerTimeout
	}
	if !t.sse {
		if t.body.Len() < 1<<20 {
			t.body.Write(p)
		}
		return len(p), nil
	}
	t.buf = append(t.buf, p...)
	for {
		t.buf = bytes.ReplaceAll(t.buf, []byte("\r\n"), []byte("\n"))
		end := bytes.Index(t.buf, []byte("\n\n"))
		if end < 0 {
			break
		}
		frame := t.buf[:end]
		t.buf = t.buf[end+2:]
		var name string
		var data []byte
		for line := range bytes.SplitSeq(frame, []byte("\n")) {
			if v, ok := bytes.CutPrefix(line, []byte("event:")); ok {
				name = string(bytes.TrimSpace(v))
			} else if v, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(v, []byte(" "))...)
			}
		}
		if len(data) == 0 {
			continue
		}
		if name == "" {
			var ev struct {
				Type string `json:"type"`
			}
			json.Unmarshal(data, &ev)
			name = ev.Type
		}
		if !t.on(name, data) {
			t.gone = true
			return 0, http.ErrHandlerTimeout
		}
	}
	return len(p), nil
}

var _ = time.Second
