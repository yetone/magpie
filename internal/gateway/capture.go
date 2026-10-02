package gateway

import (
	"bytes"
	"io"
	"net/http"
	"os"

	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/usage"
)

// Recent-call bodies are diagnostics, not an unbounded traffic log. Keeping the
// first 256 KiB is enough to inspect ordinary prompts and responses while
// preventing images or long streams from multiplying into tens of MiB across
// 40 calls.
const callBodyLimit = 256 << 10

// wholeBodyLimit asks a spool for every byte it is given, for a body the OTLP
// export wants uncut (#538).
const wholeBodyLimit = -1

type capturedBody struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *capturedBody) add(p []byte) {
	if c.buf.Len() >= callBodyLimit {
		c.truncated = c.truncated || len(p) > 0
		return
	}
	n := min(len(p), callBodyLimit-c.buf.Len())
	_, _ = c.buf.Write(p[:n])
	if n < len(p) {
		c.truncated = true
	}
}

func (c *capturedBody) text() string { return c.buf.String() }

func captureRequestBody(p []byte) (string, bool) {
	var c capturedBody
	c.add(p)
	return c.text(), c.truncated
}

type captureResponseWriter struct {
	http.ResponseWriter
	body capturedBody
	// full: the whole body, for the request archive, when it is on
	full *spool
	// otel: the whole body, for the OTLP export, when it asks for it uncut (#538)
	otel *spool
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	w.body.add(p)
	if w.full != nil {
		w.full.add(p)
	}
	if w.otel != nil {
		w.otel.add(p)
	}
	return w.ResponseWriter.Write(p)
}

// spool keeps a body whole for the request archive, beyond the first 256
// KiB Recent calls holds: in a temporary file, not in memory, up to limit
// bytes; past that only its size is counted. A negative limit keeps every
// byte (#538). A file that can't be written leaves what was kept so far, cut.
type spool struct {
	pattern string // empty uses the request archive

	f           *os.File
	limit, kept int64
	size        int64 // every byte that came, kept or not
	failed      bool
}

func (s *spool) add(p []byte) {
	s.size += int64(len(p))
	if s.failed || (s.limit >= 0 && s.kept >= s.limit) || len(p) == 0 {
		return
	}
	if s.f == nil {
		pattern := s.pattern
		if pattern == "" {
			pattern = "magpie-archive-*"
		}
		f, err := os.CreateTemp("", pattern)
		if err != nil {
			s.failed = true
			return
		}
		s.f = f
	}
	n := int64(len(p))
	if s.limit >= 0 {
		n = min(n, s.limit-s.kept)
	}
	if _, err := s.f.Write(p[:n]); err != nil {
		s.failed = true
		return
	}
	s.kept += n
}

// cut is whether less than the whole body was kept.
func (s *spool) cut() bool { return s.kept < s.size }

// read is what was kept, the file gone after.
func (s *spool) read() ([]byte, error) {
	if s.f == nil {
		return nil, nil
	}
	defer s.discard()
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(s.f, s.kept))
}

// discard removes the file: nothing is kept.
func (s *spool) discard() {
	if s.f != nil {
		s.f.Close()
		os.Remove(s.f.Name())
		s.f = nil
	}
}

func (w *captureResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withBodies hands a call's captured request and reply to its usage
// record when the OTLP export sends them (#538) — their secrets scrubbed
// as the request archive's are, on top of the placeholders Mask secrets
// puts in — and leaves the record without them otherwise. A call captured
// whole (OTelWhole) carries them in otelIn/otelOut; otherwise the first
// 256 KiB Recent calls keeps is what goes.
func withBodies(rec *usage.Record, c *Call) {
	if !usage.OTelBodies() {
		return
	}
	in, inCut := c.RequestBody, c.RequestTruncated
	if c.otelIn != nil {
		in, inCut = string(c.otelIn), c.otelInCut
	}
	out, outCut := c.ResponseBody, c.ResponseTruncated
	if c.otelOut != nil {
		out, outCut = string(c.otelOut), c.otelOutCut
	}
	rec.BodyIn = bodyForExport(in, inCut)
	rec.BodyOut = bodyForExport(out, outCut)
}

func bodyForExport(body string, cut bool) string {
	if body == "" {
		return ""
	}
	body = string(redact.ScrubJSON([]byte(body)))
	if cut {
		body += usage.BodyCut
	}
	return body
}
