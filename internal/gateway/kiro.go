package gateway

// A Kiro subscription is served through Kiro's own API, the one kiro-cli
// talks to (after github.com/mikeyobrien/pi-provider-kiro): each request
// is sent whole as a conversation — the earlier turns as its history, the
// last as its current message, with the caller's tools — and the reply
// comes back as an AWS event stream, decoded here into events. The
// sign-in is kiro-cli's or the Kiro IDE's (provider/kiro.go).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// kiroAuth and kiroRuntime are the account's credentials and where its
// API is; vars so tests can stand in for Kiro.
var (
	kiroAuth    = provider.KiroAuthOf
	kiroRuntime = func(region string) string { return "https://runtime." + region + ".kiro.dev" }
)

// serveKiro answers a request through Kiro's API.
func (s *Server) serveKiro(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte, usage *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	req.Model = model
	ask := s.askKiro(p, model)
	if req.WebSearch && !searching(r.Context()) {
		if _, _, ok := searcher(); ok {
			return s.searchReply(w, r, from, "Kiro", req, usage, ask)
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, status, msg := ask(ctx, req)
	if events == nil {
		return writeError(w, from, status, msg), msg
	}
	return relay(w, r, from, "Kiro", req, events, usage, cancel, func(string, string, bool) {})
}

// askKiro is a round for Kiro's API.
func (s *Server) askKiro(p provider.Provider, model string) round {
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		auth, err := kiroAuth(ctx, p.Key, false)
		if err != nil {
			return nil, 401, "Kiro: " + err.Error()
		}
		window, _ := provider.KiroModel(model)
		thinking := kiroThinks(req, model)
		res, err := s.sendKiro(ctx, auth, buildKiro(req, model, auth.Profile, thinking))
		if err == nil && res.StatusCode == http.StatusForbidden {
			// an expired or revoked token: refreshed, it goes once more
			res.Body.Close()
			if auth, err = kiroAuth(ctx, p.Key, true); err == nil {
				res, err = s.sendKiro(ctx, auth, buildKiro(req, model, auth.Profile, thinking))
			}
		}
		if err != nil {
			return nil, 502, "Kiro: " + err.Error()
		}
		if res.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			status, msg := kiroFailure(res.StatusCode, b)
			return nil, status, "Kiro: " + msg
		}
		events := make(chan Event, 16)
		go func() {
			defer res.Body.Close()
			decodeKiro(ctx, res.Body, events, model, window, thinking)
		}()
		return events, 0, ""
	}
}

func (s *Server) sendKiro(ctx context.Context, auth provider.KiroAuth, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kiroRuntime(auth.Region)+"/generateAssistantResponse", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	auth.Header(req.Header)
	ua := "aws-sdk-rust/1.0.0 ua/2.1 os/other lang/rust api/codewhispererstreaming#1.28.3 m/E app/AmazonQ-For-CLI md/appVersion-1.28.3-" + strings.ReplaceAll(newUUID(), "-", "")
	for k, v := range map[string]string{
		"Content-Type":                "application/json",
		"Accept":                      "application/vnd.amazon.eventstream",
		"x-amzn-codewhisperer-optout": "true",
		"amz-sdk-invocation-id":       newUUID(),
		"amz-sdk-request":             "attempt=1; max=1",
		"x-amzn-kiro-agent-mode":      "vibe",
		"x-amz-user-agent":            ua,
		"User-Agent":                  ua,
	} {
		req.Header.Set(k, v)
	}
	return s.client.Do(req)
}

// kiroFailure is the status and message for an error Kiro answered with:
// {"message": "...", "reason": "..."}.
func kiroFailure(status int, body []byte) (int, string) {
	var e struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		msg = e.Message
		if e.Reason != "" {
			msg += " (" + e.Reason + ")"
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch {
	case strings.Contains(msg, "CONTENT_LENGTH_EXCEEDS_THRESHOLD") || strings.Contains(strings.ToLower(msg), "input is too long"):
		// what the rest of magpie takes for a conversation that no longer fits
		return 400, "input is too long for the model's context: " + msg
	case strings.Contains(msg, "INSUFFICIENT_MODEL_CAPACITY"):
		return 503, msg
	case strings.Contains(msg, "MONTHLY_REQUEST_COUNT") || strings.Contains(msg, "USAGE_LIMIT"):
		return 429, "usage limit reached: " + msg
	}
	return status, msg
}

// kiroThinks is whether the model is asked to think aloud: when the
// client asked to see its reasoning, of a Claude model (or Kiro's Auto,
// which picks one).
func kiroThinks(r *Request, model string) bool {
	m := strings.ToLower(model)
	return (r.Thinking || r.Effort != "" && r.Effort != "low") && (strings.Contains(m, "claude") || m == "auto")
}

// kiroThinkingBudget is how long a model thinking aloud may think.
func kiroThinkingBudget(effort string) int {
	switch effort {
	case "low":
		return 10000
	case "high":
		return 30000
	case "xhigh", "max":
		return 50000
	}
	return 20000
}

// ---- the request ---------------------------------------------------------

type kiroEntry struct {
	User *kiroUser `json:"userInputMessage,omitempty"`
	Asst *kiroAsst `json:"assistantResponseMessage,omitempty"`
}

type kiroUser struct {
	Content string       `json:"content"`
	ModelID string       `json:"modelId"`
	Origin  string       `json:"origin"`
	Images  []kiroImage  `json:"images,omitempty"`
	Context *kiroUserCtx `json:"userInputMessageContext,omitempty"`
}

type kiroUserCtx struct {
	Tools       []kiroTool       `json:"tools,omitempty"`
	ToolResults []kiroToolResult `json:"toolResults,omitempty"`
}

type kiroImage struct {
	Format string `json:"format"`
	Source struct {
		Bytes string `json:"bytes"`
	} `json:"source"`
}

type kiroTool struct {
	Spec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			JSON json.RawMessage `json:"json"`
		} `json:"inputSchema"`
	} `json:"toolSpecification"`
}

type kiroText struct {
	Text string `json:"text"`
}

type kiroToolResult struct {
	ToolUseID string     `json:"toolUseId"`
	Status    string     `json:"status"`
	Content   []kiroText `json:"content"`
}

type kiroAsst struct {
	Content  string        `json:"content"`
	ToolUses []kiroToolUse `json:"toolUses,omitempty"`
}

type kiroToolUse struct {
	Name      string          `json:"name"`
	ToolUseID string          `json:"toolUseId"`
	Input     json.RawMessage `json:"input"`
}

const (
	// what a turn with nothing to say says: Kiro takes no empty message
	kiroProceed = "Please proceed with the task."
	// what answers a tool call no result was sent for
	kiroNoResult = "Tool use was interrupted and did not produce a result."
	// a tool's output is cut at this, as Kiro's own agent cuts it
	kiroResultLimit = 250000
)

var kiroToolIDRe = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,64}$`)

// kiroToolID is a tool call's id as Kiro takes one: another vendor's, which
// Kiro may not, becomes one of its own, the same each time.
func kiroToolID(id string) string {
	if kiroToolIDRe.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "t_" + base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

// buildKiro is the body of a generateAssistantResponse call.
func buildKiro(r *Request, model, profile string, thinking bool) []byte {
	var entries []kiroEntry
	user := func() *kiroUser { return &kiroUser{ModelID: model, Origin: "KIRO_CLI"} }
	for _, m := range r.Messages {
		var texts []string
		if m.Role == "assistant" {
			a := &kiroAsst{}
			for _, p := range m.Parts {
				switch p.Kind {
				case Text:
					if p.Text != "" {
						texts = append(texts, p.Text)
					}
				case ToolCall:
					args := argsOf(p)
					if !bytes.HasPrefix(bytes.TrimSpace(args), []byte("{")) {
						args = json.RawMessage("{}")
					}
					a.ToolUses = append(a.ToolUses, kiroToolUse{Name: p.Name, ToolUseID: kiroToolID(p.ID), Input: args})
				}
			}
			a.Content = strings.Join(texts, "\n\n")
			if a.Content == "" && len(a.ToolUses) == 0 {
				continue // a turn that only thought, or failed
			}
			if n := len(entries); n > 0 && entries[n-1].Asst != nil {
				prev := entries[n-1].Asst
				prev.Content = joinNonEmpty(prev.Content, a.Content)
				prev.ToolUses = append(prev.ToolUses, a.ToolUses...)
			} else {
				entries = append(entries, kiroEntry{Asst: a})
			}
			continue
		}
		u := user()
		var results []kiroToolResult
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if p.Text != "" {
					texts = append(texts, p.Text)
				}
			case Image:
				if p.Data != "" {
					img := kiroImage{Format: kiroImageFormat(p.MediaType)}
					img.Source.Bytes = p.Data
					u.Images = append(u.Images, img)
				}
			case ToolResult:
				out := p.Text
				if len(out) > kiroResultLimit {
					out = out[:kiroResultLimit] + "\n… (cut)"
				}
				if out == "" {
					out = "(no output)"
				}
				status := "success"
				if p.IsError {
					status = "error"
				}
				results = append(results, kiroToolResult{ToolUseID: kiroToolID(p.CallID), Status: status, Content: []kiroText{{Text: out}}})
			}
		}
		u.Content = strings.Join(texts, "\n\n")
		if len(results) > 0 {
			u.Context = &kiroUserCtx{ToolResults: results}
		}
		if n := len(entries); n > 0 && entries[n-1].User != nil {
			prev := entries[n-1].User
			prev.Content = joinNonEmpty(prev.Content, u.Content)
			prev.Images = append(prev.Images, u.Images...)
			if u.Context != nil {
				if prev.Context == nil {
					prev.Context = &kiroUserCtx{}
				}
				prev.Context.ToolResults = append(prev.Context.ToolResults, results...)
			}
		} else {
			entries = append(entries, kiroEntry{User: u})
		}
	}
	// the conversation opens with something the user said: tool results
	// with no call before them go, and a reply the caller began with
	for len(entries) > 0 {
		if u := entries[0].User; u != nil {
			if u.Context != nil && u.Content == "" && len(u.Images) == 0 {
				entries = entries[1:]
				continue
			}
			if u.Context != nil {
				u.Context = nil
			}
			break
		}
		entries = entries[1:]
	}
	// and ends with it, the message being answered
	if n := len(entries); n == 0 || entries[n-1].User == nil {
		entries = append(entries, kiroEntry{User: user()})
	}
	// each call is answered in the message after it, and only calls are
	for i, e := range entries {
		u := e.User
		if u == nil {
			continue
		}
		var calls []kiroToolUse
		if i > 0 && entries[i-1].Asst != nil {
			calls = entries[i-1].Asst.ToolUses
		}
		var results []kiroToolResult
		if u.Context != nil {
			results = u.Context.ToolResults
		}
		answered := map[string]bool{}
		var kept []kiroToolResult
		for _, res := range results {
			if !answered[res.ToolUseID] && hasKiroCall(calls, res.ToolUseID) {
				answered[res.ToolUseID] = true
				kept = append(kept, res)
			}
		}
		for _, c := range calls {
			if !answered[c.ToolUseID] {
				answered[c.ToolUseID] = true
				kept = append(kept, kiroToolResult{ToolUseID: c.ToolUseID, Status: "error", Content: []kiroText{{Text: kiroNoResult}}})
			}
		}
		u.Context = nil
		if len(kept) > 0 {
			u.Context = &kiroUserCtx{ToolResults: kept}
		}
		if u.Content == "" && u.Context == nil {
			u.Content = kiroProceed
		}
	}
	// only the latest images are sent again, as Kiro's own agent does
	latest := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if u := entries[i].User; u != nil && len(u.Images) > 0 {
			latest = i
			break
		}
	}
	for i := range entries {
		if u := entries[i].User; u != nil && i != latest {
			u.Images = nil
		}
	}
	// the instructions go at the head of the first message
	system := r.System
	if thinking {
		system = fmt.Sprintf("<thinking_mode>enabled</thinking_mode><max_thinking_length>%d</max_thinking_length>", kiroThinkingBudget(r.Effort)) +
			prefixNonEmpty("\n", system)
	}
	if system != "" {
		entries[0].User.Content = system + "\n\n" + entries[0].User.Content
	}

	// the caller's tools, and any the conversation used that it no longer
	// offers — Kiro rejects a history naming a tool it wasn't given
	current := entries[len(entries)-1].User
	var tools []kiroTool
	offered := map[string]bool{}
	for _, t := range r.Tools {
		kt := kiroTool{}
		kt.Spec.Name, kt.Spec.Description = t.Name, t.Description
		if kt.Spec.Description == "" {
			kt.Spec.Description = t.Name
		}
		kt.Spec.InputSchema.JSON = t.Schema
		if len(bytes.TrimSpace(t.Schema)) == 0 || string(bytes.TrimSpace(t.Schema)) == "null" {
			kt.Spec.InputSchema.JSON = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		offered[t.Name] = true
		tools = append(tools, kt)
	}
	for _, e := range entries {
		if e.Asst == nil {
			continue
		}
		for _, c := range e.Asst.ToolUses {
			if !offered[c.Name] {
				offered[c.Name] = true
				kt := kiroTool{}
				kt.Spec.Name, kt.Spec.Description = c.Name, "Tool"
				kt.Spec.InputSchema.JSON = json.RawMessage(`{"type":"object","properties":{}}`)
				tools = append(tools, kt)
			}
		}
	}
	if len(tools) > 0 {
		if current.Context == nil {
			current.Context = &kiroUserCtx{}
		}
		current.Context.Tools = tools
	}

	state := map[string]any{
		"chatTriggerType": "MANUAL",
		"agentTaskType":   "vibe",
		"conversationId":  newUUID(),
		"currentMessage":  map[string]any{"userInputMessage": current},
	}
	if len(entries) > 1 {
		state["history"] = entries[:len(entries)-1]
	}
	out := map[string]any{"conversationState": state, "agentMode": "vibe"}
	if profile != "" {
		out["profileArn"] = profile
	}
	b, _ := json.Marshal(out)
	return b
}

func hasKiroCall(calls []kiroToolUse, id string) bool {
	for _, c := range calls {
		if c.ToolUseID == id {
			return true
		}
	}
	return false
}

func joinNonEmpty(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n\n" + b
}

func prefixNonEmpty(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}

func kiroImageFormat(mediaType string) string {
	f := strings.TrimPrefix(strings.ToLower(mediaType), "image/")
	switch f {
	case "jpg":
		return "jpeg"
	case "", mediaType:
		return "png"
	}
	return f
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ---- the reply -----------------------------------------------------------

// kiroFrame is one message of an AWS event stream.
type kiroFrame struct {
	headers map[string]string
	payload []byte
}

// readKiroFrame reads one message: its length, its headers' length and a
// checksum, the headers, the payload, and a checksum of the whole.
func readKiroFrame(r *bufio.Reader) (kiroFrame, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return kiroFrame{}, err
	}
	total := binary.BigEndian.Uint32(prelude[0:4])
	hlen := binary.BigEndian.Uint32(prelude[4:8])
	if total < 16 || total > 16<<20 || hlen > total-16 {
		return kiroFrame{}, errors.New("a malformed event stream")
	}
	rest := make([]byte, total-12)
	if _, err := io.ReadFull(r, rest); err != nil {
		return kiroFrame{}, err
	}
	f := kiroFrame{headers: map[string]string{}, payload: rest[hlen : len(rest)-4]}
	h := rest[:hlen]
	for len(h) > 0 {
		n := int(h[0])
		if len(h) < 2+n {
			break
		}
		name, typ := string(h[1:1+n]), h[1+n]
		h = h[2+n:]
		var size int
		switch typ {
		case 0, 1: // true, false
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8:
			size = 8
		case 9:
			size = 16
		case 6, 7: // bytes, a string: a length, then it
			if len(h) < 2 {
				return f, nil
			}
			l := int(binary.BigEndian.Uint16(h))
			if len(h) < 2+l {
				return f, nil
			}
			if typ == 7 {
				f.headers[name] = string(h[2 : 2+l])
			}
			h = h[2+l:]
			continue
		default:
			return f, nil
		}
		if len(h) < size {
			return f, nil
		}
		h = h[size:]
	}
	return f, nil
}

// decodeKiro turns Kiro's reply into events.
func decodeKiro(ctx context.Context, body io.Reader, out chan<- Event, model string, window int, thinking bool) {
	defer close(out)
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(Event{Kind: KStart, MsgID: "msg_" + randomToken()[:24], Model: model}) {
		return
	}
	d := kiroDecoder{thinking: thinking}
	br := bufio.NewReaderSize(body, 64<<10)
	var failed string
	for {
		f, err := readKiroFrame(br)
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				failed = "the reply broke off: " + err.Error()
			}
			break
		}
		evs, msg := d.frame(f)
		for _, ev := range evs {
			if !send(ev) {
				return
			}
		}
		if msg != "" {
			failed = msg
			break
		}
	}
	for _, ev := range d.flush() {
		if !send(ev) {
			return
		}
	}
	if failed != "" {
		send(Event{Kind: KError, Text: failed})
		return
	}
	u := d.usage
	if u.Input == 0 && d.contextPct > 0 && window > 0 {
		// Kiro says how full the context is rather than how many tokens
		u.Input = int(d.contextPct / 100 * float64(window))
	}
	if u.Output == 0 {
		u.Output = (d.said + 3) / 4
	}
	send(Event{Kind: KUsage, Usage: u})
	stop := "stop"
	switch {
	case d.tools > 0:
		stop = "tool"
	case strings.EqualFold(d.stop, "MAX_TOKENS"):
		stop = "length"
	case strings.EqualFold(d.stop, "CONTENT_FILTERED"):
		stop = "filter"
	}
	send(Event{Kind: KStop, Stop: stop})
}

// kiroDecoder follows a reply: its text (whose thinking, asked for, comes
// first between <thinking> tags), its tool calls — each a run of frames
// naming it, the arguments in pieces — and what it says of itself.
type kiroDecoder struct {
	thinking bool
	think    kiroThinkParser

	tool       string // the id of the call being streamed
	tools      int
	stop       string
	usage      Usage
	contextPct float64
	said       int
}

func (d *kiroDecoder) frame(f kiroFrame) (evs []Event, failed string) {
	kind := f.headers[":event-type"]
	if f.headers[":message-type"] == "exception" || f.headers[":message-type"] == "error" {
		kind = f.headers[":exception-type"]
		if kind == "" {
			kind = f.headers[":error-code"]
		}
		return nil, kiroException(kind, f.payload)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(f.payload, &m) != nil {
		return nil, ""
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(m[k], &s)
		return s
	}
	switch kind {
	case "assistantResponseEvent":
		text := str("content")
		d.said += len(text)
		if d.thinking {
			return d.think.feed(text), ""
		}
		if text != "" {
			return []Event{{Kind: KText, Text: text}}, ""
		}
	case "reasoningContentEvent":
		if t := str("text"); t != "" {
			d.said += len(t)
			return []Event{{Kind: KThink, Text: t}}, ""
		}
		if s := str("signature"); s != "" {
			return []Event{{Kind: KSig, Text: s}}, ""
		}
	case "toolUseEvent":
		id, name := str("toolUseId"), str("name")
		if id != "" && id != d.tool {
			// the text before a call is all said
			evs = append(evs, d.think.end()...)
			d.tool = id
			d.tools++
			evs = append(evs, Event{Kind: KToolStart, ID: id, Name: name})
		}
		if raw := m["input"]; len(raw) > 0 {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				if s != "" {
					evs = append(evs, Event{Kind: KToolArgs, Text: s})
				}
			} else if string(raw) != "{}" && string(raw) != "null" {
				evs = append(evs, Event{Kind: KToolArgs, Text: string(raw)})
			}
			d.said += len(raw)
		}
		return evs, ""
	case "metadataEvent", "messageMetadataEvent":
		if s := str("stopReason"); s != "" {
			d.stop = s
		}
		var tu struct {
			Uncached   int     `json:"uncachedInputTokens"`
			Input      int     `json:"inputTokens"`
			Output     int     `json:"outputTokens"`
			CacheRead  int     `json:"cacheReadInputTokens"`
			CacheWrite int     `json:"cacheWriteInputTokens"`
			Pct        float64 `json:"contextUsagePercentage"`
		}
		if json.Unmarshal(m["tokenUsage"], &tu) == nil {
			in := tu.Uncached
			if in == 0 {
				in = tu.Input
			}
			d.usage.add(Usage{Input: in, Output: tu.Output, CacheRead: tu.CacheRead, CacheWrite: tu.CacheWrite})
			if tu.Pct > 0 {
				d.contextPct = tu.Pct
			}
		}
	case "contextUsageEvent":
		var pct float64
		if json.Unmarshal(m["contextUsagePercentage"], &pct) == nil && pct > 0 {
			d.contextPct = pct
		}
	case "error", "throttlingError", "validationError", "serviceUnavailableError", "internalServerException":
		return nil, kiroException(kind, f.payload)
	}
	return nil, ""
}

// flush ends the reply's text.
func (d *kiroDecoder) flush() []Event { return d.think.end() }

// kiroException is what an error in the stream says.
func kiroException(kind string, payload []byte) string {
	var e struct {
		Message string `json:"message"`
		Upper   string `json:"Message"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(payload, &e)
	msg := e.Message
	if msg == "" {
		msg = e.Upper
	}
	if msg == "" {
		msg = strings.TrimSpace(string(payload))
	}
	if e.Reason != "" {
		msg += " (" + e.Reason + ")"
	}
	switch {
	case strings.Contains(strings.ToLower(kind), "throttl"):
		return "rate limited: " + msg
	case kind != "":
		return kind + ": " + msg
	}
	return msg
}

// kiroThinkParser splits a reply that opens with <thinking>…</thinking>
// into its thinking and the text after, however the tags fall across
// pieces.
type kiroThinkParser struct {
	state int // 0: not yet known, 1: thinking, 2: text
	buf   string
	said  bool // of the thinking, which opens on a line of its own
}

const (
	kiroThinkOpen  = "<thinking>"
	kiroThinkClose = "</thinking>"
)

func (p *kiroThinkParser) feed(s string) []Event {
	p.buf += s
	var out []Event
	for {
		switch p.state {
		case 0:
			t := strings.TrimLeft(p.buf, " \t\r\n")
			if strings.HasPrefix(t, kiroThinkOpen) {
				p.buf, p.state = t[len(kiroThinkOpen):], 1
				continue
			}
			if len(t) < len(kiroThinkOpen) && strings.HasPrefix(kiroThinkOpen, t) {
				return out // may yet be the tag
			}
			p.state = 2
			continue
		case 1:
			if !p.said {
				if p.buf = strings.TrimLeft(p.buf, "\r\n"); p.buf == "" {
					return out
				}
			}
			if i := strings.Index(p.buf, kiroThinkClose); i >= 0 {
				if i > 0 {
					out = append(out, Event{Kind: KThink, Text: p.buf[:i]})
					p.said = true
				}
				p.buf, p.state = strings.TrimLeft(p.buf[i+len(kiroThinkClose):], "\r\n"), 2
				continue
			}
			// hold back what may be the start of the closing tag
			keep := partialSuffix(p.buf, kiroThinkClose)
			if n := len(p.buf) - keep; n > 0 {
				out = append(out, Event{Kind: KThink, Text: p.buf[:n]})
				p.buf, p.said = p.buf[n:], true
			}
			return out
		default:
			if p.buf != "" {
				out = append(out, Event{Kind: KText, Text: p.buf})
				p.buf = ""
			}
			return out
		}
	}
}

// end says what is held back: the reply, or a part of it, has ended.
func (p *kiroThinkParser) end() []Event {
	if p.buf == "" {
		return nil
	}
	kind := KText
	if p.state == 1 {
		kind = KThink
	}
	ev := Event{Kind: kind, Text: p.buf}
	p.buf = ""
	if p.state == 0 {
		p.state = 2
	}
	return []Event{ev}
}

// partialSuffix is how much of the end of s is the start of tag.
func partialSuffix(s, tag string) int {
	for n := min(len(tag)-1, len(s)); n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
