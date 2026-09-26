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
	}
}

func (s *usageSniffer) flushEvent() {
	if len(s.data) > 0 {
		s.parse(bytes.TrimSpace(s.data))
		s.data = nil
	}
}

func (s *usageSniffer) parse(b []byte) {
	switch s.proto {
	case provider.Chat:
		var v struct {
			Usage *cUsage `json:"usage"`
		}
		if json.Unmarshal(b, &v) == nil && v.Usage != nil {
			s.u.add(v.Usage.usage())
		}
	case provider.Responses:
		var v struct {
			Usage    *rUsage `json:"usage"`
			Response struct {
				Usage *rUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(b, &v) == nil {
			if v.Response.Usage != nil {
				s.u.add(v.Response.Usage.usage())
			} else if v.Usage != nil {
				s.u.add(v.Usage.usage())
			}
		}
	default:
		var v struct {
			Usage   *aUsage `json:"usage"`
			Message struct {
				Usage *aUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &v) == nil {
			if v.Message.Usage != nil {
				s.u.add(v.Message.Usage.usage())
			}
			if v.Usage != nil {
				s.u.add(v.Usage.usage())
			}
		}
	}
}

// usage is what the reply reported; call it once the body has ended.
func (s *usageSniffer) usage() Usage {
	if s.sse {
		if len(s.buf) > 0 {
			s.line(bytes.TrimSuffix(s.buf, []byte{'\r'}))
			s.buf = nil
		}
		s.flushEvent()
	}
	if !s.sse && !s.over && len(s.buf) > 0 {
		s.parse(bytes.TrimSpace(s.buf))
		s.buf = nil
	}
	return s.u
}
