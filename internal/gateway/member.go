package gateway

// A reply says which of magpie's providers and models answered it (#822):
// the body's model is the vendor's own name for it (glm-5.3-flash), which
// names neither the provider nor the group's member, so an agent that
// counts usage by it puts every provider's glm-5.3-flash in one bucket.
//
// Every reply carries X-Magpie-Provider (the provider's id) and
// X-Magpie-Model (magpie's provider/model id of the member that answered).
// The body's model is the one the agent asked for whenever the request
// went out under another name — a group's member, an alias, a fallback,
// a provider/model id, a name the user gave the model upstream — since
// agents compare it with what they asked and warn when it differs
// (issennknight on Discord: deepseek-v4.1-flash served as
// deepseek-v4.1-flash-1017). One of magpie's own aliases for Claude
// Desktop or Science (magpieAliasAsked) is not among these: it names the
// model only to Desktop and its Code tab, and a reply that carried it put
// magpie's deployment name in the agent's usage records, where ccusage
// counted it as a model of its own (#4667), so such a request keeps the
// vendor's text, as every reply did before the asked-name rule. A request
// that went out under the name it asked for keeps the vendor's text, a
// dated name included. Settings' ReplyModel says what the body's model
// is for every request and every agent: the model asked for (""),
// magpie's id ("member"), or the vendor's text ("vendor", as before the
// asked-for name). Both were asked for by ZoeCats on Discord, whose
// Claude Code was given the name asked for under "member" until it named
// the member to Claude Code, Claude Desktop and Codex too: the user's
// pick wins, and the id is a provider/model as a name asked for is. A request's X-Magpie-Response-Model (asked,
// member or vendor) says it for that request alone. The vendor's
// own name is still in the usage log (Record.Served) and on the Routing
// page: the request's capture is inside this writer.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const (
	providerHeader      = "X-Magpie-Provider"
	modelHeader         = "X-Magpie-Model"
	responseModelHeader = "X-Magpie-Response-Model" // a request's: asked, member or vendor
)

type memberKey struct{}

// noteMember says on the try's reply which provider and model answer it,
// and tells the request's memberWriter, if it has one, with the name the
// request goes out under.
func noteMember(w http.ResponseWriter, r *http.Request, p provider.Provider, model string) {
	id := p.ID + "/" + model
	if hw, ok := w.(*holdWriter); ok {
		// watch's keepAlive may send them meanwhile
		hw.note(providerHeader, p.ID)
		hw.note(modelHeader, id)
	} else {
		h := w.Header()
		h.Set(providerHeader, p.ID)
		h.Set(modelHeader, id)
	}
	if mw, _ := r.Context().Value(memberKey{}).(*memberWriter); mw != nil {
		mw.setMember(id, provider.UpstreamNameIn(wiresOf(r.Context()), p.ID, model))
	}
}

// replyNaming is what the reply to r has its body's model say: the
// member's id, the vendor's own text, or else the model
// the agent asked for, when the request went out under another name.
// The request's X-Magpie-Response-Model says which, else Settings'
// ReplyModel.
type replyNaming int

const (
	nameAsked replyNaming = iota
	nameMember
	nameVendor
)

func replyNamingOf(r *http.Request) replyNaming {
	// a request's own ask is as it says, for every agent
	switch strings.ToLower(strings.TrimSpace(r.Header.Get(responseModelHeader))) {
	case settings.ReplyAsked:
		return nameAsked
	case settings.ReplyMember:
		return nameMember
	case settings.ReplyVendor:
		return nameVendor
	}
	// the user's pick, for every agent: Claude Code, Claude Desktop and
	// Codex take a provider/model id back as they take the group or
	// provider/model they asked for (ZoeCats on Discord)
	switch settings.Shared().ReplyModel {
	case settings.ReplyVendor:
		return nameVendor
	case settings.ReplyMember:
		return nameMember
	}
	return nameAsked
}

// withMemberNames wraps w in a memberWriter, which the tries tell their
// member (noteMember), for a reply from protocol from to a request that
// asked for model asked. The func returned writes what it holds, once
// the handler is done.
func withMemberNames(w http.ResponseWriter, r *http.Request, from provider.Protocol, asked string) (http.ResponseWriter, *http.Request, func()) {
	naming := replyNamingOf(r)
	if naming == nameVendor || naming == nameAsked && (asked == "" || magpieAliasAsked(asked)) {
		return w, r, func() {}
	}
	mw := &memberWriter{w: w, naming: naming, asked: asked, paths: memberPathsOf(from)}
	return mw, r.WithContext(context.WithValue(r.Context(), memberKey{}, mw)), mw.finish
}

// memberWriter puts the name the reply should say (target) for the model
// it names: in each event of a stream as it goes, in a JSON body once it
// is whole. An error, or a body of another kind, goes as it is.
type memberWriter struct {
	w      http.ResponseWriter
	naming replyNaming
	asked  string   // the model the agent asked for
	paths  []string // where the protocol's replies name their model

	mu     sync.Mutex
	member string // the provider/model id of the try that answers
	wire   string // the name that try goes out under

	mode     int // memberUnknown, …
	buf      bytes.Buffer
	finished bool
}

const (
	memberUnknown = iota
	memberAsIs
	memberStream
	memberWhole
)

func (m *memberWriter) setMember(id, wire string) {
	m.mu.Lock()
	m.member, m.wire = id, wire
	m.mu.Unlock()
}

func (m *memberWriter) memberID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.member
}

// target is the name the reply's model says, "" for the vendor's own:
// the member's id, or the model asked for unless the try went out
// under that very name.
func (m *memberWriter) target() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.naming == nameMember {
		return m.member
	}
	if m.wire == "" || m.wire == m.asked {
		return ""
	}
	return m.asked
}

func (m *memberWriter) Header() http.Header { return m.w.Header() }

// Unwrap is for http.ResponseController.
func (m *memberWriter) Unwrap() http.ResponseWriter { return m.w }

func (m *memberWriter) decide(code int, first []byte) {
	h := m.w.Header()
	ct := h.Get("Content-Type")
	target := m.target()
	switch {
	case code >= 400:
		m.mode = memberAsIs
	case h.Get("Content-Encoding") != "" && !strings.EqualFold(h.Get("Content-Encoding"), "identity"):
		m.mode = memberAsIs
	case strings.Contains(ct, "event-stream"),
		ct == "" && (bytes.HasPrefix(first, []byte("data:")) || bytes.HasPrefix(first, []byte("event:"))):
		// a stream's lines are read whatever the name is now: a held
		// stream's keepalive decides before the try that answers is known
		m.mode = memberStream
	case target != "" && strings.Contains(ct, "json"):
		m.mode = memberWhole
	default:
		m.mode = memberAsIs
	}
	if m.mode != memberAsIs {
		h.Del("Content-Length") // the name put in isn't as long as the vendor's
	}
}

func (m *memberWriter) WriteHeader(code int) {
	if m.mode == memberUnknown {
		m.decide(code, nil)
	}
	m.w.WriteHeader(code)
}

func (m *memberWriter) Write(p []byte) (int, error) {
	if m.mode == memberUnknown {
		m.decide(http.StatusOK, p)
	}
	switch m.mode {
	case memberWhole:
		if m.finished {
			// written after finish, by a writer inside this one that held
			// it longer: no one would write it from the buffer, so it goes
			// now, named when it is the whole body (#4129)
			if json.Valid(p) {
				if _, err := m.w.Write(memberNamedAt(p, m.target(), m.paths)); err != nil {
					return 0, err
				}
				return len(p), nil
			}
			return m.w.Write(p)
		}
		return m.buf.Write(p)
	case memberStream:
		// the whole lines go on in one write, named; the rest waits for
		// its end
		m.buf.Write(p)
		b := m.buf.Bytes()
		if end := bytes.LastIndexByte(b, '\n'); end >= 0 {
			_, err := m.w.Write(memberLines(b[:end+1], m.target(), m.paths))
			m.buf.Next(end + 1)
			if err != nil {
				return len(p), err
			}
		}
		if m.finished && m.buf.Len() > 0 { // no finish is left to write it
			_, err := m.w.Write(m.buf.Bytes())
			m.buf.Reset()
			return len(p), err
		}
		return len(p), nil
	}
	return m.w.Write(p)
}

func (m *memberWriter) Flush() {
	if m.mode == memberWhole && !m.finished {
		return // sent whole, once it is
	}
	if f, ok := m.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (m *memberWriter) finish() {
	m.finished = true
	if m.buf.Len() == 0 {
		return
	}
	b := m.buf.Bytes()
	if m.mode == memberWhole {
		b = memberNamedAt(b, m.target(), m.paths)
	} else {
		b = memberLines(b, m.target(), m.paths)
	}
	m.w.Write(b)
	m.buf.Reset()
}

// modelKey is in every event or body that names a model, "modelVersion"
// included: one without it is passed on unread.
var modelKey = []byte(`"model`)

// memberLines is a run of an event stream's lines with name for the model
// each line's data names, the same slice when none is changed.
func memberLines(b []byte, name string, paths []string) []byte {
	if name == "" || !bytes.Contains(b, modelKey) {
		return b
	}
	var out []byte
	for i := 0; i < len(b); {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			j = len(b)
		} else {
			j += i + 1
		}
		line := b[i:j]
		named := line
		if bytes.Contains(line, modelKey) {
			named = memberLineAt(line, name, paths)
		}
		if out == nil && !bytes.Equal(named, line) {
			out = append(make([]byte, 0, len(b)+64), b[:i]...)
		}
		if out != nil {
			out = append(out, named...)
		}
		i = j
	}
	if out == nil {
		return b
	}
	return out
}

// memberLine is an event stream's line with the member's id for the
// model its data names.
func memberLine(line []byte, id string) []byte {
	return memberLineAt(line, id, memberPaths)
}

// memberLineAt is memberLine for the paths a protocol's events have.
func memberLineAt(line []byte, id string, paths []string) []byte {
	rest, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return line
	}
	at := len(line) - len(rest)
	trimmed := bytes.TrimLeft(rest, " ")
	at += len(rest) - len(trimmed)
	end := len(line)
	for end > at && (line[end-1] == '\n' || line[end-1] == '\r') {
		end--
	}
	data := line[at:end]
	if len(data) == 0 || data[0] != '{' {
		return line
	}
	named := memberNamedAt(data, id, paths)
	if len(named) == len(data) && bytes.Equal(named, data) {
		return line
	}
	out := make([]byte, 0, len(line)+len(named)-len(data))
	out = append(out, line[:at]...)
	out = append(out, named...)
	return append(out, line[end:]...)
}

// memberPaths are where each API's reply names its model: Chat's and a
// whole Anthropic message's or Responses response's model, a Responses
// event's response.model, Anthropic's message_start's message.model,
// Gemini's modelVersion.
var memberPaths = []string{"model", "response.model", "message.model", "modelVersion"}

// memberPathsOf are the memberPaths a reply in protocol from has.
func memberPathsOf(from provider.Protocol) []string {
	switch from {
	case provider.Chat:
		return []string{"model"}
	case provider.Responses:
		return []string{"model", "response.model"}
	case provider.Anthropic:
		return []string{"model", "message.model"}
	case provider.Gemini:
		return []string{"modelVersion"}
	}
	return memberPaths
}

// memberNamedAt is a JSON object with id for the model it names at
// one of paths.
func memberNamedAt(b []byte, id string, paths []string) []byte {
	// a body cut short goes on as it came: nothing is made up in it
	if id == "" || !bytes.Contains(b, modelKey) || !json.Valid(b) {
		return b
	}
	name, _ := json.Marshal(id)
	for _, path := range paths {
		v := gjson.GetBytes(b, path)
		if v.Type != gjson.String || v.Index <= 0 || v.Str == id {
			continue
		}
		out := make([]byte, 0, len(b)+len(name))
		out = append(out, b[:v.Index]...)
		out = append(out, name...)
		b = append(out, b[v.Index+len(v.Raw):]...)
	}
	return b
}
