package gateway

// A reply says which of magpie's providers and models answered it (#822):
// the body's model is the vendor's own name for it (glm-5.3-flash), which
// names neither the provider nor the group's member, so an agent that
// counts usage by it puts every provider's glm-5.3-flash in one bucket.
//
// Every reply carries X-Magpie-Provider (the provider's id) and
// X-Magpie-Model (magpie's provider/model id of the member that answered),
// the body left as the vendor wrote it: agents read the body's model
// themselves (Claude Code its features, Codex its catalog), so a name they
// don't know there is not given them unasked. Settings' MemberModel, or a
// request's X-Magpie-Response-Model: member, has the body's model say
// magpie's id as well (memberWriter); the vendor's own name is still in
// the usage log (Record.Served).

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
	responseModelHeader = "X-Magpie-Response-Model" // a request's: member, or vendor
)

// vendorNamed are the agents whose replies always keep the vendor's model
// name: they read it themselves.
var vendorNamed = []string{"claude", "claude-desktop", "codex"}

type memberKey struct{}

// noteMember says on the try's reply which provider and model answer it,
// and tells the request's memberWriter, if it has one.
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
		mw.setMember(id)
	}
}

// namesMember is whether the reply to r has its body's model say the
// member's id.
func namesMember(r *http.Request, agent string) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get(responseModelHeader))) {
	case "member":
		return true
	case "vendor":
		return false
	}
	if !settings.Load().MemberModel {
		return false
	}
	for _, a := range vendorNamed {
		if agent == a {
			return false
		}
	}
	return true
}

// withMemberNames wraps w, when the reply to r names the member, in a
// memberWriter, which the tries tell their member (noteMember). The func
// returned writes what it holds, once the handler is done.
func withMemberNames(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, *http.Request, func()) {
	if !namesMember(r, agentOf(r)) {
		return w, r, func() {}
	}
	mw := &memberWriter{w: w}
	return mw, r.WithContext(context.WithValue(r.Context(), memberKey{}, mw)), mw.finish
}

// memberWriter puts the member's id for the model a reply names: in each
// event of a stream as it goes, in a JSON body once it is whole. An error,
// or a body of another kind, goes as it is.
type memberWriter struct {
	w http.ResponseWriter

	mu     sync.Mutex
	member string

	mode int // memberUnknown, …
	buf  bytes.Buffer
}

const (
	memberUnknown = iota
	memberAsIs
	memberStream
	memberWhole
)

func (m *memberWriter) setMember(id string) {
	m.mu.Lock()
	m.member = id
	m.mu.Unlock()
}

func (m *memberWriter) memberID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.member
}

func (m *memberWriter) Header() http.Header { return m.w.Header() }

// Unwrap is for http.ResponseController.
func (m *memberWriter) Unwrap() http.ResponseWriter { return m.w }

func (m *memberWriter) decide(code int, first []byte) {
	h := m.w.Header()
	ct := h.Get("Content-Type")
	switch {
	case code >= 400, m.memberID() == "":
		m.mode = memberAsIs
	case h.Get("Content-Encoding") != "" && !strings.EqualFold(h.Get("Content-Encoding"), "identity"):
		m.mode = memberAsIs
	case strings.Contains(ct, "event-stream"),
		ct == "" && (bytes.HasPrefix(first, []byte("data:")) || bytes.HasPrefix(first, []byte("event:"))):
		m.mode = memberStream
	case strings.Contains(ct, "json"):
		m.mode = memberWhole
	default:
		m.mode = memberAsIs
	}
	if m.mode != memberAsIs {
		h.Del("Content-Length") // the id isn't as long as the name
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
		return m.buf.Write(p)
	case memberStream:
		m.buf.Write(p)
		for {
			b := m.buf.Bytes()
			i := bytes.IndexByte(b, '\n')
			if i < 0 {
				break
			}
			line := memberLine(append([]byte(nil), b[:i+1]...), m.memberID())
			m.buf.Next(i + 1)
			if _, err := m.w.Write(line); err != nil {
				return len(p), err
			}
		}
		return len(p), nil
	}
	return m.w.Write(p)
}

func (m *memberWriter) Flush() {
	if m.mode == memberWhole {
		return // sent whole, once it is
	}
	if f, ok := m.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (m *memberWriter) finish() {
	if m.buf.Len() == 0 {
		return
	}
	b := m.buf.Bytes()
	if m.mode == memberWhole {
		b = memberNamed(b, m.memberID())
	} else {
		b = memberLine(b, m.memberID())
	}
	m.w.Write(b)
	m.buf.Reset()
}

// memberLine is an event stream's line with the member's id for the
// model its data names.
func memberLine(line []byte, id string) []byte {
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
	named := memberNamed(data, id)
	if len(named) == len(data) && bytes.Equal(named, data) {
		return line
	}
	out := make([]byte, 0, len(line)+len(named)-len(data))
	out = append(out, line[:at]...)
	out = append(out, named...)
	return append(out, line[end:]...)
}

// memberPaths are where each API's reply names its model: Chat's and a
// whole Anthropic message's model, a Responses event's response.model,
// Anthropic's message_start's message.model, Gemini's modelVersion.
var memberPaths = []string{"model", "response.model", "message.model", "modelVersion"}

// memberNamed is a JSON object with id for the model it names.
func memberNamed(b []byte, id string) []byte {
	if id == "" {
		return b
	}
	name, _ := json.Marshal(id)
	for _, path := range memberPaths {
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
