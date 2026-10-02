package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// usageSniffer picks the usage block out of a provider reply that is being
// copied to the client untouched. Streams are read line by line as they
// pass; a plain JSON body is kept and parsed at the end.
type usageSniffer struct {
	proto provider.Protocol
	sse   bool
	buf   []byte
	data  []byte // SSE data lines of the event in progress
	over  bool   // the JSON body outgrew the cap; give up on it
	u     Usage
	// served: the model the reply says answered it, the last it named
	served string
	// ended: the stream's last event went by (message_stop, [DONE],
	// response.completed …) or an error that ends it
	ended bool
}

func newSniffer(proto provider.Protocol, contentType string) *usageSniffer {
	return &usageSniffer{proto: proto, sse: strings.HasPrefix(contentType, "text/event-stream")}
}

func (s *usageSniffer) write(b []byte) {
	if !s.sse {
		if len(s.buf)+len(b) > 8<<20 {
			s.over = true
			return
		}
		s.buf = append(s.buf, b...)
		return
	}
	s.buf = append(s.buf, b...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		s.line(bytes.TrimSuffix(s.buf[:i], []byte{'\r'}))
		s.buf = s.buf[i+1:]
	}
	if len(s.buf) > 1<<20 { // a runaway line is not one we can use
		s.buf = nil
		s.data = nil
	}
}

func (s *usageSniffer) line(line []byte) {
	if len(line) == 0 {
		s.flushEvent()
		return
	}
	if rest, ok := bytes.CutPrefix(line, []byte("event:")); ok && lastEvent(string(bytes.TrimSpace(rest))) {
		s.ended = true // the name says so even when the data is too big to read
	}
	if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		rest = bytes.TrimPrefix(rest, []byte{' '})
		if len(s.data)+len(rest)+1 > 1<<20 {
			s.data = nil // never let an unbounded event accumulate
			return
		}
		if len(s.data) > 0 {
			s.data = append(s.data, '\n')
		}
		s.data = append(s.data, rest...)
		// Relays also send one JSON event per data line without blank separators.
		// Parse a complete value now, but keep incomplete multiline JSON.
		if json.Valid(bytes.TrimSpace(s.data)) {
			s.flushEvent()
		}
	}
}

func (s *usageSniffer) flushEvent() {
	if len(s.data) > 0 {
		s.parse(bytes.TrimSpace(s.data))
		s.data = nil
	}
}

func (s *usageSniffer) parse(b []byte) {
	if s.sse && string(b) == "[DONE]" {
		s.ended = true
		return
	}
	var t struct {
		Type  string `json:"type"`
		Error any    `json:"error"`
	}
	if s.sse && json.Unmarshal(b, &t) == nil {
		switch {
		case lastEvent(t.Type):
			s.ended = true
		case t.Type == "":
			s.ended = s.ended || t.Error != nil // a Chat stream's error
		}
	}
	switch s.proto {
	case provider.Gemini:
		// Factory's generateContent chunks, the same usageMetadata Code
		// Assist wraps. Relaying one used to count nothing.
		var v struct {
			ModelVersion  string `json:"modelVersion"`
			UsageMetadata *struct {
				Prompt     int `json:"promptTokenCount"`
				Candidates int `json:"candidatesTokenCount"`
				Thoughts   int `json:"thoughtsTokenCount"`
				Cached     int `json:"cachedContentTokenCount"`
			} `json:"usageMetadata"`
		}
		if json.Unmarshal(b, &v) == nil {
			s.saw(v.ModelVersion)
			if u := v.UsageMetadata; u != nil {
				s.u.add(Usage{Input: max(u.Prompt-u.Cached, 0), CacheRead: u.Cached,
					Output: u.Candidates + u.Thoughts, Reasoning: u.Thoughts})
			}
		}
	case provider.Chat:
		var v struct {
			Model string  `json:"model"`
			Usage *cUsage `json:"usage"`
		}
		if json.Unmarshal(b, &v) == nil {
			s.saw(v.Model)
			if v.Usage != nil {
				s.u.add(v.Usage.usage())
			}
		}
	case provider.Responses:
		var v struct {
			Model    string  `json:"model"`
			Usage    *rUsage `json:"usage"`
			Response struct {
				Model string  `json:"model"`
				Usage *rUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(b, &v) == nil {
			s.saw(v.Response.Model)
			s.saw(v.Model)
			if v.Response.Usage != nil {
				s.u.add(v.Response.Usage.usage())
			} else if v.Usage != nil {
				s.u.add(v.Usage.usage())
			}
		}
	default:
		var v struct {
			Model   string  `json:"model"`
			Usage   *aUsage `json:"usage"`
			Message struct {
				Model string  `json:"model"`
				Usage *aUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &v) == nil {
			s.saw(v.Message.Model)
			s.saw(v.Model)
			if v.Message.Usage != nil {
				s.u.add(v.Message.Usage.usage())
			}
			if v.Usage != nil {
				s.u.add(v.Usage.usage())
			}
		}
	}
}

// saw keeps a model the reply named.
func (s *usageSniffer) saw(model string) {
	if model != "" {
		s.served = model
	}
}

// lastEvent reports whether an event of this type ends a stream.
func lastEvent(t string) bool {
	switch t {
	case "message_stop", "error", "response.completed", "response.incomplete", "response.failed":
		return true
	}
	return false
}

// whole reports whether the stream got to its last event; call it once
// the body has ended.
func (s *usageSniffer) whole() bool {
	s.drain()
	return s.ended
}

func (s *usageSniffer) drain() {
	if s.sse {
		if len(s.buf) > 0 {
			s.line(bytes.TrimSuffix(s.buf, []byte{'\r'}))
			s.buf = nil
		}
		s.flushEvent()
	}
}

// usage is what the reply reported, the model it named in Served; call it
// once the body has ended.
func (s *usageSniffer) usage() Usage {
	s.drain()
	if !s.sse && !s.over && len(s.buf) > 0 {
		s.parse(bytes.TrimSpace(s.buf))
		s.buf = nil
	}
	u := s.u
	u.Served = s.served
	return u
}
