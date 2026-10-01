package gateway

import (
	"bytes"
	"io"
	"net/http"
	"os"
)

// Recent-call bodies are diagnostics, not an unbounded traffic log. Keeping the
// first 256 KiB is enough to inspect ordinary prompts and responses while
// preventing images or long streams from multiplying into tens of MiB across
// 40 calls.
const callBodyLimit = 256 << 10

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
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	w.body.add(p)
	if w.full != nil {
		w.full.add(p)
	}
	return w.ResponseWriter.Write(p)
}

// spool keeps a body whole for the request archive, beyond the first 256
// KiB Recent calls holds: in a temporary file, not in memory, up to limit
// bytes; past that only its size is counted. A file that can't be written
// leaves what was kept so far, cut.
type spool struct {
	f           *os.File
	limit, kept int64
	size        int64 // every byte that came, kept or not
	failed      bool
}

func (s *spool) add(p []byte) {
	s.size += int64(len(p))
	if s.failed || s.kept >= s.limit || len(p) == 0 {
		return
	}
	if s.f == nil {
		f, err := os.CreateTemp("", "magpie-archive-*")
		if err != nil {
			s.failed = true
			return
		}
		s.f = f
	}
	n := min(int64(len(p)), s.limit-s.kept)
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
