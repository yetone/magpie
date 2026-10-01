package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// readSSE walks a server-sent event stream, calling fn with each event's
// name and data. Data lines of one event are joined with newlines.
func readSSE(r io.Reader, fn func(event, data string) error) error {
	return readSSEAlive(r, fn, nil)
}

// readSSEAlive is readSSE that also calls alive, when not nil, after each
// event and each comment: every sign that the stream is alive, a
// keepalive (": keepalive") as much as an answer.
func readSSEAlive(r io.Reader, fn func(event, data string) error, alive func()) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	var event string
	var data []string
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event, data = "", nil
		if err == nil && alive != nil {
			alive()
		}
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			if alive != nil {
				alive()
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return sc.Err()
}

// sseWriter writes events to a client and flushes each one.
type sseWriter struct {
	w     http.ResponseWriter
	f     http.Flusher
	begun bool
	wrote time.Time // when the client was last written to
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, f: f}
}

func (s *sseWriter) begin() {
	if s.begun {
		return
	}
	s.begun = true
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.flush()
	s.wrote = time.Now()
}

func (s *sseWriter) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

// quiet is how long the client has had nothing from s; a stream not yet
// begun has been quiet for ever.
func (s *sseWriter) quiet() time.Duration {
	if !s.begun {
		return 1<<63 - 1
	}
	return time.Since(s.wrote)
}

// comment writes an SSE comment, which every event stream reader skips:
// no event, only the news that the stream is alive.
func (s *sseWriter) comment(text string) {
	s.begin()
	io.WriteString(s.w, ": "+text+"\n\n")
	s.flush()
	s.wrote = time.Now()
}

// event writes one event; a JSON-marshallable value or a raw string.
func (s *sseWriter) event(name string, v any) {
	s.begin()
	var b bytes.Buffer
	if name != "" {
		b.WriteString("event: ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	b.WriteString("data: ")
	switch x := v.(type) {
	case string:
		b.WriteString(x)
	case []byte:
		b.Write(x)
	case json.RawMessage:
		b.Write(x)
	default:
		j, _ := json.Marshal(x)
		b.Write(j)
	}
	b.WriteString("\n\n")
	s.w.Write(b.Bytes())
	s.flush()
	s.wrote = time.Now()
}
