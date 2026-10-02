package gateway

// PLUGIN-SERVED (see AGENTS.md): Devin ("devin") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-devin-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/devin) and raise the
// mover's min in internal/provider/migrate_side.go.

// A Devin subscription is served through the API the devin CLI talks to —
// Windsurf's GetChatMessage, a Connect RPC in protobuf — the way a Kiro one
// is (kiro.go): each request goes whole, the conversation with the caller's
// tools, and the reply streams back as Connect frames, decoded here into
// events. The sign-in is the CLI's credentials.toml (provider/devin.go).

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// devinAuth and devinVariant are the sign-in and the model the API is
// asked for; vars so tests can stand in for them.
var (
	devinAuth    = provider.DevinAuthAt
	devinVariant = provider.DevinVariant
)

// devinCLIVersion is the CLI the requests say they come from.
const devinCLIVersion = "3000.11.3"

// serveDevin answers a request through Devin's API, as the account signed
// in in home ("" for the CLI's own).
func (s *Server) serveDevin(w http.ResponseWriter, r *http.Request, from provider.Protocol, home, model string, body []byte, usage *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	req.Model = model
	ask := s.askDevin(home, model)
	if req.WebSearch && !searching(r.Context()) {
		if canSearch() {
			return s.searchReply(w, r, from, "Devin", req, usage, ask)
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, status, msg := ask(ctx, req)
	if events == nil {
		return writeError(w, from, status, msg), msg
	}
	return relay(w, r, from, "Devin", req, events, usage, cancel, func(string, string, bool) {})
}

// askDevin is a round for Devin's API.
func (s *Server) askDevin(home, model string) round {
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		key, server, err := devinAuth(home)
		if err != nil {
			return nil, 401, "Devin: " + err.Error()
		}
		uid := devinVariant(ctx, model, req.Effort)
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/exa.api_server_pb.ApiServerService/GetChatMessage",
			bytes.NewReader(connectFrame(buildDevin(req, uid, key))))
		if err != nil {
			return nil, 500, "Devin: " + err.Error()
		}
		hr.Header.Set("Content-Type", "application/connect+proto")
		hr.Header.Set("Connect-Protocol-Version", "1")
		hr.Header.Set("Authorization", "Basic "+key)
		res, err := s.client.Do(hr)
		if err != nil {
			return nil, 502, "Devin: " + err.Error()
		}
		if res.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			status, msg := devinFailure(res.StatusCode, b)
			return nil, status, "Devin: " + msg
		}
		// an error comes as the stream's end, before anything else: read
		// that far so it is answered with its own status
		br := bufio.NewReaderSize(res.Body, 64<<10)
		first, err := readConnectFrame(br)
		if err != nil {
			res.Body.Close()
			return nil, 502, "Devin: the reply broke off: " + err.Error()
		}
		if first.end {
			res.Body.Close()
			status, msg := devinFailure(200, first.data)
			if status/100 == 2 {
				status, msg = 502, "an empty reply"
			}
			return nil, status, "Devin: " + msg
		}
		events := make(chan Event, 16)
		go func() {
			defer res.Body.Close()
			decodeDevin(ctx, first, br, events, model)
		}()
		return events, 0, ""
	}
}

// devinFailure is the status and message for a Connect error:
// {"code": "...", "message": "..."}, or at a stream's end
// {"error": {"code": "...", "message": "..."}}.
func devinFailure(status int, body []byte) (int, string) {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	code := ""
	if json.Unmarshal(body, &e) == nil {
		if e.Error != nil {
			code, msg = e.Error.Code, e.Error.Message
		} else if e.Code != "" {
			code, msg = e.Code, e.Message
		}
		if msg == "" {
			msg = code
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	lower := strings.ToLower(msg)
	switch {
	case code == "unauthenticated":
		return 401, msg + " — sign in to Devin again"
	case code == "resource_exhausted" || strings.Contains(lower, "quota") || strings.Contains(lower, "rate limit"):
		return 429, "usage limit reached: " + msg
	case strings.Contains(lower, "content policy"):
		// Devin turns away some system prompts outright; sent again, the
		// same one is turned away again
		return 400, msg
	case strings.Contains(lower, "too long") || strings.Contains(lower, "context length") || strings.Contains(lower, "too many tokens"):
		return 400, "input is too long for the model's context: " + msg
	case code == "invalid_argument":
		return 400, msg
	case code == "unavailable":
		return 503, msg
	case code != "":
		return 502, msg
	}
	return status, msg
}

// ---- protobuf, by hand ---------------------------------------------------

// pb builds a protobuf message field by field.
type pb []byte

func (b pb) tag(num, wire int) pb { return binary.AppendUvarint(b, uint64(num<<3|wire)) }

func (b pb) varint(num int, v uint64) pb { return binary.AppendUvarint(b.tag(num, 0), v) }

func (b pb) bytes(num int, v []byte) pb {
	return append(binary.AppendUvarint(b.tag(num, 2), uint64(len(v))), v...)
}

func (b pb) str(num int, v string) pb { return b.bytes(num, []byte(v)) }

func (b pb) double(num int, v float64) pb {
	return binary.LittleEndian.AppendUint64(b.tag(num, 1), math.Float64bits(v))
}

// pbField is one field of a message being read.
type pbField struct {
	num  int
	wire int
	n    uint64 // a varint or fixed value
	data []byte // a length-delimited one
}

// pbFields reads a message's fields; a malformed tail is dropped.
func pbFields(b []byte) []pbField {
	var out []pbField
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		b = b[n:]
		f := pbField{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return out
			}
			f.n, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return out
			}
			f.n, b = binary.LittleEndian.Uint64(b), b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return out
			}
			f.data, b = b[n:n+int(l)], b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return out
			}
			f.n, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return out
		}
		out = append(out, f)
	}
	return out
}

// connectFrame wraps a message as the one frame of a Connect stream.
func connectFrame(msg []byte) []byte {
	out := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(out[1:], uint32(len(msg)))
	return append(out, msg...)
}

// connectMsg is one frame of a Connect stream: a message, or the stream's
// end with its JSON.
type connectMsg struct {
	end  bool
	data []byte
}

func readConnectFrame(r *bufio.Reader) (connectMsg, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return connectMsg{}, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > 64<<20 {
		return connectMsg{}, errors.New("a malformed stream")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return connectMsg{}, err
	}
	if head[0]&1 != 0 {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return connectMsg{}, err
		}
		if data, err = io.ReadAll(io.LimitReader(zr, 64<<20)); err != nil {
			return connectMsg{}, err
		}
	}
	return connectMsg{end: head[0]&2 != 0, data: data}, nil
}

// ---- the request ---------------------------------------------------------

// Devin's roles for a message.
const (
	devinUser      = 1
	devinAssistant = 2
	devinTool      = 4
)

// devinNoResult answers a call the conversation never answered.
const devinNoResult = "Tool use was interrupted and did not produce a result."

// devinMsg is a message of the conversation, before it is encoded.
type devinMsg struct {
	role     int
	text     string
	calls    []Part
	callID   string // what a tool result answers
	images   []Part
	thinking []Part // with Devin's own signatures
}

// buildDevin is the GetChatMessage request for r, to the model uid.
func buildDevin(r *Request, uid, key string) []byte {
	var msgs []devinMsg
	var pending []Part // calls the last reply made, not yet answered
	answer := func() {
		for _, c := range pending {
			msgs = append(msgs, devinMsg{role: devinTool, callID: c.ID, text: devinNoResult})
		}
		pending = nil
	}
	for _, m := range r.Messages {
		if m.Role == "assistant" {
			a := devinMsg{role: devinAssistant}
			var texts []string
			for _, p := range m.Parts {
				switch p.Kind {
				case Text:
					if p.Text != "" {
						texts = append(texts, p.Text)
					}
				case ToolCall:
					a.calls = append(a.calls, p)
				case Thinking:
					if strings.HasPrefix(p.Signature, "sealed.") {
						a.thinking = append(a.thinking, p)
					}
				}
			}
			a.text = strings.Join(texts, "\n\n")
			if a.text == "" && len(a.calls) == 0 {
				continue // a turn that only thought, or failed
			}
			answer()
			msgs = append(msgs, a)
			pending = a.calls
			continue
		}
		u := devinMsg{role: devinUser}
		var texts []string
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if p.Text != "" {
					texts = append(texts, p.Text)
				}
			case File:
				texts = append(texts, attachmentText(p))
			case Image:
				if p.Data != "" {
					u.images = append(u.images, p)
				}
			case ToolResult:
				// only a call just made is answered, and only once
				for i, c := range pending {
					if c.ID != p.CallID {
						continue
					}
					out := p.Text
					if out == "" {
						out = "(no output)"
					}
					if p.IsError {
						out = "Error: " + out
					}
					msgs = append(msgs, devinMsg{role: devinTool, callID: c.ID, text: out})
					pending = append(pending[:i:i], pending[i+1:]...)
					break
				}
			}
		}
		u.text = strings.Join(texts, "\n\n")
		if u.text == "" && len(u.images) == 0 {
			continue
		}
		answer()
		msgs = append(msgs, u)
	}
	answer()
	if len(msgs) == 0 || msgs[len(msgs)-1].role == devinAssistant && len(msgs[len(msgs)-1].calls) == 0 {
		msgs = append(msgs, devinMsg{role: devinUser, text: "Please proceed with the task."})
	}

	// the instructions go at the head of the first message, as Kiro's do:
	// Devin turns away some agents' own in its system field (Claude Code's
	// "You are a Claude agent, built on Anthropic's Claude Agent SDK"). The
	// tools' descriptions go there too, each tool sent with only a pointer
	// to its own: Devin answers "an internal error occurred" to some agents'
	// tools as they describe themselves (WorkBuddy's Read, "Reads a file
	// from the local filesystem. You can access any file directly by using
	// this tool."), and the same words in a message go through
	tools := r.Tools
	if r.ToolChoice == "none" {
		tools = nil
	}
	instructions := r.System
	if described := devinToolDescriptions(tools); described != "" {
		instructions = joinNonEmpty(instructions, described)
	}
	if instructions != "" {
		at := slices.IndexFunc(msgs, func(m devinMsg) bool { return m.role == devinUser })
		if at < 0 {
			msgs = slices.Insert(msgs, 0, devinMsg{role: devinUser})
			at = 0
		}
		msgs[at].text = joinNonEmpty(instructions, msgs[at].text)
	}

	meta := pb{}.str(1, "devin-cli").str(2, devinCLIVersion).str(3, key).str(4, "en").
		str(5, runtime.GOOS).str(7, devinCLIVersion).str(12, "chisel").str(28, "chisel")
	out := pb{}.bytes(1, meta)
	for _, m := range msgs {
		out = out.bytes(3, encodeDevinMsg(m))
	}
	out = out.varint(7, 5)
	max := r.MaxTokens
	if max <= 0 {
		max = 128000 // the server holds it to the model's own
	}
	topP := 0.95
	if r.TopP != nil {
		topP = *r.TopP
	}
	temp := 1.0
	if r.Temp != nil {
		temp = *r.Temp
	}
	out = out.bytes(8, pb{}.varint(1, 1).varint(2, uint64(max)).varint(3, 400).double(5, temp).varint(7, 40).double(8, topP))

	// the caller's tools, and any the conversation used that it no longer
	// offers
	offered := map[string]bool{}
	tool := func(name, desc string, schema json.RawMessage) {
		if len(bytes.TrimSpace(schema)) == 0 || string(bytes.TrimSpace(schema)) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if desc == "" {
			desc = name
		}
		offered[name] = true
		out = out.bytes(10, pb{}.str(1, name).str(2, desc).bytes(3, schema))
	}
	for _, t := range tools {
		desc := t.Description
		if desc != "" {
			desc = "Described under <tool name=\"" + t.Name + "\"> in <tool_descriptions>, in the instructions."
		}
		tool(t.Name, desc, t.Schema)
	}
	for _, m := range msgs {
		for _, c := range m.calls {
			if !offered[c.Name] {
				tool(c.Name, "Tool", nil)
			}
		}
	}
	return out.str(21, uid)
}

// devinToolDescriptions is what each tool says of itself, for the
// instructions.
func devinToolDescriptions(tools []Tool) string {
	var b strings.Builder
	for _, t := range tools {
		if t.Description == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("<tool_descriptions>\n")
		}
		b.WriteString("<tool name=\"" + t.Name + "\">\n" + strings.TrimSpace(t.Description) + "\n</tool>\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "</tool_descriptions>"
}

func encodeDevinMsg(m devinMsg) []byte {
	b := pb{}.str(1, newUUID()).varint(2, uint64(m.role))
	if m.text != "" {
		b = b.str(3, m.text)
	}
	for _, c := range m.calls {
		b = b.bytes(6, pb{}.str(1, c.ID).str(2, c.Name).bytes(3, argsOf(c)))
	}
	if m.callID != "" {
		b = b.str(7, m.callID)
	}
	for _, p := range m.images {
		mt := p.MediaType
		if mt == "" {
			mt = "image/png"
		}
		b = b.bytes(10, pb{}.str(1, p.Data).str(2, mt))
	}
	for _, t := range m.thinking {
		b = b.str(11, t.Text).str(12, t.Signature).str(18, "sealed")
	}
	return b
}

// ---- the reply -----------------------------------------------------------

// decodeDevin turns Devin's reply, from its first frame on, into events.
func decodeDevin(ctx context.Context, first connectMsg, br *bufio.Reader, out chan<- Event, model string) {
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
	var d devinDecoder
	var failed string
	for f := first; ; {
		if f.end {
			if status, msg := devinFailure(200, f.data); status/100 != 2 {
				failed = msg
			}
			break
		}
		for _, ev := range d.frame(f.data) {
			if !send(ev) {
				return
			}
		}
		var err error
		if f, err = readConnectFrame(br); err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				failed = "the reply broke off: " + err.Error()
			}
			break
		}
	}
	if failed != "" {
		send(Event{Kind: KError, Text: failed})
		return
	}
	u := d.usage
	if u.Output == 0 {
		u.Output = (d.said + 3) / 4
	}
	send(Event{Kind: KUsage, Usage: u})
	stop := "stop"
	switch {
	case d.tools > 0 || d.stop == 10:
		stop = "tool"
	case d.stop == 3:
		stop = "length"
	}
	send(Event{Kind: KStop, Stop: stop})
}

// devinDecoder follows a reply: its text and thinking in pieces, its tool
// calls — each opened with its id and name, the arguments in pieces after
// — why it stopped, and what it used.
type devinDecoder struct {
	tools int
	stop  uint64
	usage Usage
	said  int
}

func (d *devinDecoder) frame(b []byte) []Event {
	var evs []Event
	for _, f := range pbFields(b) {
		switch {
		case f.num == 3 && f.wire == 2 && len(f.data) > 0:
			d.said += len(f.data)
			evs = append(evs, Event{Kind: KText, Text: string(f.data)})
		case f.num == 9 && f.wire == 2 && len(f.data) > 0:
			d.said += len(f.data)
			evs = append(evs, Event{Kind: KThink, Text: string(f.data)})
		case f.num == 6 && f.wire == 2:
			var id, name, args string
			for _, g := range pbFields(f.data) {
				switch g.num {
				case 1:
					id = string(g.data)
				case 2:
					name = string(g.data)
				case 3:
					args += string(g.data)
				}
			}
			if name != "" {
				d.tools++
				if id == "" {
					id = "call_" + randomToken()[:24]
				}
				evs = append(evs, Event{Kind: KToolStart, ID: id, Name: name})
			}
			if args != "" {
				evs = append(evs, Event{Kind: KToolArgs, Text: args})
			}
		case f.num == 5 && f.wire == 0:
			d.stop = f.n
		case f.num == 7 && f.wire == 2:
			for _, g := range pbFields(f.data) {
				if g.wire != 0 || g.n == 0 {
					continue
				}
				switch g.num {
				case 2:
					d.usage.Input = int(g.n)
				case 3:
					d.usage.Output = int(g.n)
				case 4:
					d.usage.CacheWrite = int(g.n)
				case 5:
					d.usage.CacheRead = int(g.n)
				}
			}
		}
	}
	return evs
}
