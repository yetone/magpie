package gateway

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
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

const qoderSys = "You are a Qoder agent. Use the instructions below and the tools available to you to assist the user."

// qoderAuth hands a request the signed-in account's uid and a live job
// token, and qoderURL the chat endpoint on the credential's site (qoder.com's
// or Qoder CN's); vars so tests can stand in for them, the way a Devin round
// stands in for devinAuth.
var (
	qoderAuth  = provider.QoderCredentialOf
	qoderModel = provider.QoderModelOf
	qoderURL   = func(c *qoder.Credential) string { return c.OnSite().ChatURL() }
)

// serveQoder answers a request through Qoder's API.
func (s *Server) serveQoder(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte, usage *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	req.Model = model
	ask := s.askQoder(p.Account.Agent, model, p.Account.User)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	q := req
	var tool string
	if req.WebSearch && !searching(r.Context()) {
		if canSearch() {
			copy := *req
			copy.WebSearch = false
			search := searchTool(copy.Tools)
			copy.Tools = append(slices.Clone(copy.Tools), search)
			copy.Messages = slices.Clone(copy.Messages)
			q, tool = &copy, search.Name
		}
	}
	events, status, msg := ask(ctx, q)
	if events == nil {
		return writeError(w, from, status, msg), msg
	}
	if tool != "" {
		first := events
		out := make(chan Event, 16)
		go func() {
			defer close(out)
			s.searchRounds(ctx, q, tool, first, ask, out)
		}()
		events = out
	}
	return relayQoder(w, from, req, events, usage, cancel)
}

// relayQoder preserves upstream failures before committing a response, including
// failures after partial non-streaming text. Streaming failures after output
// has begun are terminal error events. Keep this policy to the backends that
// report their own statuses: Qoder's, and Zed's (relayStatus).
func relayQoder(w http.ResponseWriter, from provider.Protocol, req *Request, events <-chan Event, usage *Usage, abort context.CancelFunc) (int, string) {
	return relayStatus(w, from, "Qoder", req, events, usage, abort)
}

// relayStatus is relayQoder for the backend called name.
func relayStatus(w http.ResponseWriter, from provider.Protocol, name string, req *Request, events <-chan Event, usage *Usage, abort context.CancelFunc) (int, string) {
	fail := func(ev Event) (int, string) {
		abort()
		code := ev.Status
		if code < 400 || code > 599 {
			code = 502
		}
		return writeError(w, from, code, ev.Text), ev.Text
	}
	if req.Stream {
		var head []Event
		for ev := range events {
			if ev.Kind == KError {
				return fail(ev)
			}
			head = append(head, ev)
			if ev.Kind != KStart && ev.Kind != KUsage {
				break
			}
		}
		if len(head) == 0 || head[len(head)-1].Kind == KStart || head[len(head)-1].Kind == KUsage {
			return fail(Event{Text: name + " ended without an answer"})
		}
		sw := newSSEWriter(w)
		enc := encoder(from, sw, req)
		var failed string
		see := func(ev Event) bool {
			if ev.Kind == KStart || ev.Kind == KUsage {
				usage.add(ev.Usage)
			}
			enc.event(ev)
			if ev.Kind == KError {
				failed = ev.Text
				return false
			}
			return true
		}
		for _, ev := range head {
			see(ev)
		}
		// kept from the client's idle timeout while none comes (#436)
		relayEvents(events, sw, enc, see)
		if failed != "" {
			abort()
			return 200, failed
		}
		enc.finish()
		return 200, ""
	}
	var col collector
	for ev := range events {
		if ev.Kind == KError {
			return fail(ev)
		}
		col.add(ev)
	}
	res := col.finish()
	usage.add(res.Usage)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(render(from, res, req))
	return 200, ""
}

// askQoder is a round for Qoder's API, on the site agent ("qoder" or
// "qoder-cn") names.
func (s *Server) askQoder(agent, model, user string) round {
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		cred, err := qoderAuth(ctx, agent, user)
		if err != nil {
			// only a sign-in that is gone asks the agent to log in again;
			// a refresh that timed out or never reached Qoder may pass
			status := 502
			if errors.Is(err, provider.ErrQoderSignIn) {
				status = 401
			}
			return nil, status, "Qoder: " + err.Error()
		}
		config, err := qoderModel(ctx, agent, user, model)
		if err != nil {
			return nil, 400, err.Error()
		}
		plain, err := qoderChatBody(req, config)
		if err != nil {
			return nil, 500, "Qoder: " + err.Error()
		}
		wire := qoder.EncodeRequestBody(plain)
		ts := time.Now().Unix()
		chat := qoderURL(cred)
		headers, err := qoder.BuildCosyHeaders(chat, &qoder.User{UID: cred.UID, Token: cred.Token,
			Name: cred.Name, Email: cred.Email, MachineID: cred.MachineID}, wire, ts)
		if err != nil {
			return nil, 500, "Qoder: " + err.Error()
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, chat, strings.NewReader(wire))
		if err != nil {
			return nil, 500, "Qoder: " + err.Error()
		}
		for k, v := range headers {
			hr.Header.Set(k, v)
		}
		hr.Header.Set("Accept", "text/event-stream")
		hr.Header.Set("Cache-Control", "no-cache")
		hr.Header.Set("Accept-Encoding", "identity")
		hr.Header.Set("X-Model-Key", config.Key)
		hr.Header.Set("X-Model-Source", config.Source)
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
	if status < 400 || status > 599 {
		status = 502
	}
	msg := strings.TrimSpace(string(body))
	if gjson.ValidBytes(body) {
		if m := gjson.GetBytes(body, "message").String(); m != "" {
			msg = m
		}
		// the model vendor's own reason, e.g. an effort the model refuses
		if d := gjson.Get(gjson.GetBytes(body, "details").String(), "error.message").String(); d != "" {
			msg += ": " + d
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
// content blocks, tool calls and results as OpenAI tool turns, and the
// caller's tools beside them.
func qoderChatBody(req *Request, model qoder.ModelInfo) ([]byte, error) {
	if model.Key == "" || len(model.Config) == 0 {
		return nil, fmt.Errorf("Qoder: missing model configuration")
	}
	sys := map[string]any{"type": "text", "text": qoderSysText(req)}
	all := []any{map[string]any{"role": "system", "content": []any{sys}}}

	asked := req.Effort
	if req.ThinkOff {
		asked = "none" // Effort reads none as low
	}
	thinking, effort := qoderEffort(asked, model)
	maxTok := int64(32000)
	if req.MaxTokens > 0 {
		maxTok = int64(req.MaxTokens)
	}
	params := map[string]any{"enable_thinking": thinking, "max_tokens": maxTok}
	if effort != "" {
		params["reasoning_effort"] = effort
	}
	if model.MaxInputTokens > 0 {
		params["context_length"] = model.MaxInputTokens
	}
	body := map[string]any{
		"parameters": params,
		"business": map[string]any{
			"product": "app", "version": "1.1.49", "type": "agent",
			"id": qoder.NewID(), "name": "magpie session",
			"begin_at": time.Now().UnixMilli(), "stage": "start",
		},
		"agent_id":     "agent_common",
		"task_id":      "common",
		"session_type": "app",
		"model_config": model.Config,
		"system":       []any{sys},
		"messages":     append(all, qoderMessages(req.Messages)...),
	}
	if req.ToolChoice != "none" && len(req.Tools) > 0 {
		body["tools"] = qoderTools(req.Tools)
	}
	return json.Marshal(body)
}

// qoderMessages renders magpie's turns as OpenAI turns, the format Qoder's
// chat endpoint takes: an assistant's tool calls as tool_calls and their
// results as tool turns named by tool_call_id, so a result is paired with
// its call and not by order. A tool turn holds text only, so the images a
// tool returned go after the tool turns in a user turn — as the start of
// the user's own turn when one comes next — the way buildChat sends them.
func qoderMessages(ms []Message) []any {
	var out []any
	names := map[string]string{}
	var seen []any
	seeLater := func(p Part) int {
		var ims []any
		for _, im := range p.Images {
			if im.Data != "" {
				ims = append(ims, map[string]any{"type": "image_url",
					"image_url": map[string]any{"url": "data:" + im.MediaType + ";base64," + im.Data}})
			}
		}
		if len(ims) == 0 {
			return 0
		}
		of := "tool call " + p.CallID
		if name := cmp.Or(names[p.CallID], p.Name); name != "" {
			of = name + " (" + of + ")"
		}
		seen = append(seen, map[string]any{"type": "text", "text": "[From the result of " + of + ":]"})
		seen = append(seen, ims...)
		return len(ims)
	}
	showSeen := func() {
		if len(seen) > 0 {
			out = append(out, map[string]any{"role": "user", "content": seen})
			seen = nil
		}
	}
	for _, m := range ms {
		var blocks []any
		var calls []map[string]any
		var text string
		flush := func() {
			if len(blocks) == 0 {
				return
			}
			if m.Role == "user" && len(seen) > 0 {
				blocks, seen = append(seen, blocks...), nil
			}
			out = append(out, map[string]any{"role": m.Role, "content": blocks})
			blocks = nil
		}
		if m.Role != "user" {
			showSeen()
		}
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
				if text == "" {
					text = p.Text
				}
			case Image:
				if p.Data != "" {
					blocks = append(blocks, map[string]any{"type": "image_url",
						"image_url": map[string]any{"url": "data:" + p.MediaType + ";base64," + p.Data}})
				}
			case ToolCall:
				if m.Role != "assistant" {
					continue
				}
				id := p.ID
				if id == "" {
					id = "call_" + qoder.NewID()
				}
				names[id] = p.Name
				calls = append(calls, map[string]any{"id": id, "type": "function",
					"function": map[string]any{"name": p.Name, "arguments": string(argsOf(p))}})
			case ToolResult:
				flush()
				txt := p.Text
				if p.IsError {
					txt = "Error: " + txt
				}
				if n := seeLater(p); n > 0 {
					note := fmt.Sprintf("[The tool returned %d images; they follow in the next message.]", n)
					if n == 1 {
						note = "[The tool returned an image; it follows in the next message.]"
					}
					if strings.TrimSpace(txt) != "" {
						txt += "\n\n"
					}
					txt += note
				}
				out = append(out, map[string]any{"role": "tool", "tool_call_id": p.CallID, "content": txt})
			}
		}
		if m.Role == "assistant" && len(calls) > 0 {
			out = append(out, map[string]any{"role": "assistant", "content": text, "tool_calls": calls})
			continue
		}
		flush()
	}
	showSeen()
	return out
}

// qoderTools is the caller's tools as Qoder's own native function tools.
func qoderTools(ts []Tool) []any {
	var out []any
	for _, t := range ts {
		fn := map[string]any{"name": t.Name, "description": t.Description}
		sch := t.Schema
		if strings.TrimSpace(string(sch)) == "" {
			sch = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		fn["parameters"] = json.RawMessage(sch)
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

// qoderEffort is whether to think and at which of the model's own efforts
// ("" to send none). Asked for none, a model that always thinks thinks at
// its lowest; asked for nothing, or for a level Qoder doesn't name, it
// thinks at its own default.
func qoderEffort(asked string, model qoder.ModelInfo) (bool, string) {
	want := strings.ToLower(asked)
	if !slices.Contains(effortRank, want) {
		want = ""
	}
	if !model.Thinks || want == "none" && !model.AlwaysThinks {
		return false, ""
	}
	if len(model.Efforts) == 0 {
		return true, ""
	}
	switch want {
	case "":
		return true, model.DefaultEffort
	case "none":
		return true, model.Efforts[0]
	}
	return true, fitEffort(want, model.Efforts)
}

// qoderSysText is the system prompt: Qoder's own line plus the caller's.
// The caller's tools go to Qoder as native function tools.
func qoderSysText(req *Request) string {
	sys := qoderSys
	if req.System != "" {
		sys = sys + "\n\n" + req.System
	}
	if req.ToolChoice == "required" && len(req.Tools) > 0 {
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
	a := qoderText{tool: -1}
	finish := func() {
		for _, frag := range a.flush() {
			if frag.text != "" && !send(Event{Kind: KText, Text: frag.text}) {
				return
			}
		}
		stop := "stop"
		if a.sawTool {
			stop = "tool"
		}
		send(Event{Kind: KStop, Stop: stop})
	}
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
		if status := gjson.Get(payload, "statusCodeValue"); status.Exists() && status.Int() != 200 {
			data := []byte(inner)
			if len(data) == 0 {
				data = []byte(payload)
			}
			code, msg := qoderFailure(int(status.Int()), data)
			send(Event{Kind: KError, Status: code, Text: msg})
			return
		}
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
		// Qoder also serves tool calls in the OpenAI way: the id it sends
		// is the one a result is answered with, so it is kept.
		for _, tc := range delta.Get("tool_calls").Array() {
			idx := int(tc.Get("index").Int())
			if idx != a.tool {
				a.tool = idx
				for _, frag := range a.flush() {
					if frag.text != "" && !send(Event{Kind: KText, Text: frag.text}) {
						return
					}
				}
				id := tc.Get("id").String()
				if id == "" {
					id = "call_" + qoder.NewID()
				}
				if !send(Event{Kind: KToolStart, ID: id, Name: tc.Get("function.name").String()}) {
					return
				}
				a.sawTool = true
			}
			if args := tc.Get("function.arguments").String(); args != "" {
				if !send(Event{Kind: KToolArgs, Text: args}) {
					return
				}
			}
		}
		if fr := gjson.Get(inner, "choices.0.finish_reason").String(); fr != "" {
			if u := gjson.Get(inner, "usage"); u.Exists() {
				send(Event{Kind: KUsage, Usage: Usage{
					Input: int(u.Get("prompt_tokens").Int()), Output: int(u.Get("completion_tokens").Int())}})
			}
			finish()
			return
		}
	}
	if err := sc.Err(); err != nil {
		send(Event{Kind: KError, Status: 502, Text: "Qoder: reading stream: " + err.Error()})
		return
	}
	finish()
}

// qoderText is the streaming tool-call splitter: it holds text until a full
// tool-call block arrives, then emits it as a call.
type qoderText struct {
	textBuf string
	callBuf string
	inCall  bool
	sawTool bool
	tool    int // index of the native tool call being assembled, -1 for none
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
		// A value is taken as it stands: nothing is told to escape them.
		val := strings.TrimSpace(pm[2])
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
