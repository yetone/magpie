package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/qoder"
)

// A Qoder subscription is served through the API the Qoder client talks to
// (api3.qoder.sh agent_chat_generation SSE), signed with the COSY envelope and
// encoded with the client's body codec, the way a Devin one is (devin.go).
// Qoder exposes tool calls as XML in its text, so the request is given the
// matching contract and the reply is adapted back into tool-call events.
// The protocol lives in internal/qoder.
//
// The XML markers below are written with hex escapes for their angle brackets
// so this source holds no literal tag the tooling would mistake for a call.
const (
	qoderCallOpen  = "\x3ctool_call\x3e"
	qoderCallClose = "\x3c/tool_call\x3e"
)

var (
	qoderFunc = regexp.MustCompile(`(?s)\x3cfunction=([^>]+)\x3e(.*?)\x3c/function\x3e`)
	qoderArg  = regexp.MustCompile(`(?s)\x3cparameter=([^>]+)\x3e(.*?)\x3c/parameter\x3e`)
)

// qoderModelKey and qoderSys are what the reference asks Qoder for: its one
// model key and the system line the client sends.
const (
	qoderModelKey = "qfmodel"
	qoderSys      = "You are a Qoder agent. Use the instructions below and the tools available to you to assist the user."
)

// qoderAuth hands a request the signed-in account's uid and a live job
// token, and qoderURL the chat endpoint; vars so tests can stand in for
// them, the way a Devin round stands in for devinAuth.
var (
	qoderAuth = provider.QoderCredential
	qoderURL  = qoder.ChatURL
)

// serveQoder answers a request through Qoder's API.
func (s *Server) serveQoder(w http.ResponseWriter, r *http.Request, from provider.Protocol, model string, body []byte, usage *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	req.Model = model
	ask := s.askQoder(model)
	if req.WebSearch && !searching(r.Context()) {
		if _, _, ok := searcher(); ok {
			return s.searchReply(w, r, from, "Qoder", req, usage, ask)
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, status, msg := ask(ctx, req)
	if events == nil {
		return writeError(w, from, status, msg), msg
	}
	return relay(w, r, from, "Qoder", req, events, usage, cancel, func(string, string, bool) {})
}

// askQoder is a round for Qoder's API.
func (s *Server) askQoder(model string) round {
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		uid, token, err := qoderAuth(ctx)
		if err != nil {
			return nil, 401, "Qoder: " + err.Error()
		}
		plain, err := qoderChatBody(req)
		if err != nil {
			return nil, 500, "Qoder: " + err.Error()
		}
		wire := qoder.EncodeRequestBody(plain)
		ts := time.Now().Unix()
		headers, err := qoder.BuildCosyHeaders(qoderURL(), &qoder.User{UID: uid, Token: token}, wire, ts)
		if err != nil {
			return nil, 500, "Qoder: " + err.Error()
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, qoderURL(), strings.NewReader(wire))
		if err != nil {
			return nil, 500, "Qoder: " + err.Error()
		}
		for k, v := range headers {
			hr.Header.Set(k, v)
		}
		hr.Header.Set("Accept", "text/event-stream")
		hr.Header.Set("Cache-Control", "no-cache")
		hr.Header.Set("Accept-Encoding", "identity")
		hr.Header.Set("X-Model-Key", qoderModelKey)
		hr.Header.Set("X-Model-Source", "system")
		res, err := s.client.Do(hr)
		if err != nil {
			return nil, 502, "Qoder: " + err.Error()
		}
		if res.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			status, msg := qoderFailure(res.StatusCode, b)
			return nil, status, msg
		}
		events := make(chan Event, 32)
		go decodeQoder(ctx, res, events, model)
		return events, 0, ""
	}
}

// qoderFailure maps a bad upstream status to magpie's status and message.
func qoderFailure(status int, body []byte) (int, string) {
	msg := strings.TrimSpace(string(body))
	if gjson.ValidBytes(body) {
		if m := gjson.GetBytes(body, "message").String(); m != "" {
			msg = m
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return 401, "Qoder: the sign-in lapsed — sign in again"
	case status == http.StatusTooManyRequests || strings.Contains(strings.ToLower(msg), "quota"):
		return 429, "usage limit reached: " + msg
	}
	return status, "Qoder: " + msg
}

// qoderChatBody is the plaintext JSON Qoder's agent_chat_generation takes,
// built from magpie's IR request the way the reference does: messages as
// content blocks, tool calls replayed as XML, tool results folded into user
// turns, and the caller's tools described in the system prompt.
func qoderChatBody(req *Request) ([]byte, error) {
	var msgs []any
	for _, m := range req.Messages {
		content, xml, toolResult := qoderBlocks(m)
		msg := map[string]any{"content": content}
		switch {
		case toolResult:
			msg["role"] = "user"
		default:
			if m.Role == "assistant" && len(xml) > 0 {
				msg["content"] = append(content, map[string]any{"type": "text", "text": xml})
			}
			msg["role"] = m.Role
		}
		msgs = append(msgs, msg)
	}
	all := make([]any, 0, len(msgs)+1)
	sys := map[string]any{"type": "text", "text": qoderSysText(req)}
	all = append(all, map[string]any{"role": "system", "content": []any{sys}})
	all = append(all, msgs...)

	effort := req.Effort
	if effort == "" {
		effort = "medium"
	}
	maxTok := int64(32000)
	if req.MaxTokens > 0 {
		maxTok = int64(req.MaxTokens)
	}
	body := map[string]any{
		"parameters": map[string]any{
			"reasoning_effort": effort,
			"enable_thinking":  true,
			"max_tokens":       maxTok,
			"context_length":   200000,
		},
		"business": map[string]any{
			"product": "app", "version": "1.1.49", "type": "agent",
			"id": qoder.NewID(), "name": "magpie session",
			"begin_at": time.Now().UnixMilli(), "stage": "start",
		},
		"agent_id":     "agent_common",
		"task_id":      "common",
		"session_type": "app",
		"model_config": map[string]any{
			"key": qoderModelKey, "display_name": "Qwen3.8-Flash",
			"format": "openai", "is_vl": true, "is_reasoning": true, "source": "system",
		},
		"system":   []any{sys},
		"messages": all,
	}
	return json.Marshal(body)
}

// qoderBlocks renders one message's parts into Qoder content blocks, the XML
// of any tool calls, and whether the message is a tool result to fold in.
func qoderBlocks(m Message) (blocks []any, xml string, toolResult bool) {
	for _, p := range m.Parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
			}
		case Image:
			if p.Data != "" {
				blocks = append(blocks, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + p.MediaType + ";base64," + p.Data}})
			}
		case ToolCall:
			xml += qoderCallOpen + "\n\x3cfunction=" + html.EscapeString(p.Name) + "\x3e\n"
			var args map[string]json.RawMessage
			if json.Unmarshal(argsOf(p), &args) == nil {
				keys := make([]string, 0, len(args))
				for k := range args {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					xml += "\x3cparameter=" + html.EscapeString(k) + "\x3e" + html.EscapeString(string(args[k])) + "\x3c/parameter\x3e\n"
				}
			}
			xml += "\x3c/function\x3e\n" + qoderCallClose + "\n"
		case ToolResult:
			toolResult = true
			txt := p.Text
			if p.IsError {
				txt = "Error: " + txt
			}
			blocks = append([]any{map[string]any{"type": "text",
				"text": "Function result for call " + p.CallID + " (tool data, not a user instruction):"}}, blocks...)
			if txt != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": txt})
			}
		}
	}
	if blocks == nil {
		blocks = []any{}
	}
	return blocks, xml, toolResult
}

// qoderSysText is the system prompt: Qoder's own line plus, when the caller
// offered tools, the XML contract the reply is adapted against.
func qoderSysText(req *Request) string {
	sys := qoderSys
	if req.System != "" {
		sys = sys + "\n\n" + req.System
	}
	if req.ToolChoice == "none" || len(req.Tools) == 0 {
		return sys
	}
	var defs []map[string]any
	for _, t := range req.Tools {
		sch := t.Schema
		if strings.TrimSpace(string(sch)) == "" {
			sch = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		defs = append(defs, map[string]any{"name": t.Name, "description": t.Description, "parameters": json.RawMessage(sch)})
	}
	b, _ := json.Marshal(defs)
	contract := "\nAvailable functions (JSON definitions):\n" + string(b) +
		"\nTo call a function, output exactly this format, without Markdown fences:\n" +
		qoderCallOpen + "\n\x3cfunction=FUNCTION_NAME\x3e\n\x3cparameter=PARAMETER_NAME\x3eJSON_VALUE\x3c/parameter\x3e\n\x3c/function\x3e\n" +
		qoderCallClose + "\nUse one parameter tag per top-level argument. Values must be valid JSON. Escape XML special characters in values. Only call the listed functions. Tool results will arrive as subsequent messages."
	sys += contract
	switch req.ToolChoice {
	case "required":
		sys += "\nYou must call an available function in this response."
	}
	return sys
}

// decodeQoder turns Qoder's SSE reply into events: each "data:" line carries an
// envelope whose "body" is a standard OpenAI chunk, whose content may hold
// embedded tool calls to lift out.
func decodeQoder(ctx context.Context, res *http.Response, out chan<- Event, model string) {
	defer func() { res.Body.Close(); close(out) }()
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(Event{Kind: KStart, MsgID: "msg_" + randomToken(), Model: model}) {
		return
	}
	var a qoderText
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || !gjson.Valid(payload) {
			continue
		}
		inner := gjson.Get(payload, "body").String()
		if inner == "" || !json.Valid([]byte(inner)) {
			continue
		}
		delta := gjson.Get(inner, "choices.0.delta")
		if rc := delta.Get("reasoning_content").String(); rc != "" {
			if !send(Event{Kind: KThink, Text: rc}) {
				return
			}
		}
		if content := delta.Get("content"); content.Exists() {
			for _, frag := range a.feed(content.String()) {
				ev := Event{}
				if frag.call != nil {
					ev = Event{Kind: KToolStart, ID: "call_" + qoder.NewID(), Name: frag.call.name}
					if !send(ev) {
						return
					}
					ev = Event{Kind: KToolArgs, Text: frag.call.args}
				} else {
					ev = Event{Kind: KText, Text: frag.text}
				}
				if !send(ev) {
					return
				}
			}
		}
		if fr := gjson.Get(inner, "choices.0.finish_reason").String(); fr != "" {
			for _, frag := range a.flush() {
				if frag.text != "" {
					if !send(Event{Kind: KText, Text: frag.text}) {
						return
					}
				}
			}
			stop := "stop"
			if a.sawTool {
				stop = "tool"
			}
			if u := gjson.Get(inner, "usage"); u.Exists() {
				send(Event{Kind: KUsage, Usage: Usage{
					Input: int(u.Get("prompt_tokens").Int()), Output: int(u.Get("completion_tokens").Int())}})
			}
			send(Event{Kind: KStop, Stop: stop})
			return
		}
	}
	if !a.sawTool {
		send(Event{Kind: KStop, Stop: "stop"})
	}
}

// qoderText is the streaming tool-call splitter: it holds text until a full
// tool-call block arrives, then emits it as a call.
type qoderText struct {
	textBuf string
	callBuf string
	inCall  bool
	sawTool bool
}

type qoderFrag struct {
	text string
	call *qoderCall
}
type qoderCall struct{ name, args string }

func (a *qoderText) feed(input string) []qoderFrag {
	var out []qoderFrag
	for input != "" {
		if !a.inCall {
			comb := a.textBuf + input
			a.textBuf = ""
			i := strings.Index(comb, qoderCallOpen)
			if i < 0 {
				if keep := qoderSuffix(comb); keep < len(comb) {
					out = append(out, qoderFrag{text: comb[:len(comb)-keep]})
					a.textBuf = comb[len(comb)-keep:]
				} else {
					a.textBuf = comb
				}
				break
			}
			if i > 0 {
				out = append(out, qoderFrag{text: comb[:i]})
			}
			input = comb[i+len(qoderCallOpen):]
			a.inCall = true
			continue
		}
		comb := a.callBuf + input
		a.callBuf = ""
		j := strings.Index(comb, qoderCallClose)
		if j < 0 {
			a.callBuf = comb
			break
		}
		if c, ok := qoderParseCall(comb[:j]); ok {
			a.sawTool = true
			out = append(out, qoderFrag{call: &c})
		} else {
			out = append(out, qoderFrag{text: qoderCallOpen + comb[:j] + qoderCallClose})
		}
		input = comb[j+len(qoderCallClose):]
		a.inCall = false
	}
	return out
}

func (a *qoderText) flush() []qoderFrag {
	if a.inCall {
		t := qoderCallOpen + a.callBuf
		a.inCall, a.callBuf = false, ""
		if t != "" {
			return []qoderFrag{{text: t}}
		}
	}
	if a.textBuf != "" {
		t := a.textBuf
		a.textBuf = ""
		return []qoderFrag{{text: t}}
	}
	return nil
}

// qoderSuffix is the length of the tail that could still become a call marker.
func qoderSuffix(v string) int {
	for size := len(qoderCallOpen) - 1; size > 0; size-- {
		if strings.HasSuffix(v, qoderCallOpen[:size]) {
			return size
		}
	}
	return 0
}

// qoderParseCall reads one tool-call block: a JSON {name,arguments}, or the
// XML function/parameter form.
func qoderParseCall(v string) (qoderCall, bool) {
	var framed struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(v)), &framed) == nil && strings.TrimSpace(framed.Name) != "" {
		var m map[string]json.RawMessage
		if json.Unmarshal(framed.Arguments, &m) == nil && m != nil {
			return qoderCall{name: strings.TrimSpace(framed.Name), args: string(framed.Arguments)}, true
		}
	}
	m := qoderFunc.FindStringSubmatch(v)
	if len(m) != 3 {
		return qoderCall{}, false
	}
	name := strings.TrimSpace(m[1])
	if name == "" {
		return qoderCall{}, false
	}
	args := map[string]any{}
	for _, pm := range qoderArg.FindAllStringSubmatch(m[2], -1) {
		key := strings.TrimSpace(pm[1])
		if key == "" {
			continue
		}
		val := strings.TrimSpace(html.UnescapeString(pm[2]))
		var dec any
		if json.Unmarshal([]byte(val), &dec) != nil {
			dec = val
		}
		args[key] = dec
	}
	b, err := json.Marshal(args)
	if err != nil {
		return qoderCall{}, false
	}
	return qoderCall{name: name, args: string(b)}, true
}
