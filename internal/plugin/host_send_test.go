package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"testing"
)

// This file is the wire contract of Fetch's request encoding: the body reaches
// the child as the base64 string encoding/json writes for a []byte, and the
// frame magpie writes is the very one the older EncodeToString params produced,
// byte for byte. Everything here goes through the production Fetch and its
// send, against a host this test builds itself; no test here starts Bun, and
// nothing here reaches into the host's stream bookkeeping.

// wireIn is a host's stdin. It keeps what magpie wrote when asked to, and tells
// the answering goroutine that a frame came, so every fetch is answered (a
// frame left unanswered would park the production Fetch).
type wireIn struct {
	keep  bool
	wrote chan struct{} // buffered 1: one frame, taken by the driver
	mu    sync.Mutex
	lines [][]byte
}

func newWireIn(keep bool) *wireIn { return &wireIn{keep: keep, wrote: make(chan struct{}, 1)} }

func (c *wireIn) Write(p []byte) (int, error) {
	if c.keep {
		c.mu.Lock()
		c.lines = append(c.lines, append([]byte(nil), p...))
		c.mu.Unlock()
	}
	// Only a fetch frame needs an answer. An abort magpie sends when the body
	// is dropped is left alone: answering it would move the driver's ids on
	// from the host's own, and the next fetch would be answered as another.
	if bytes.HasPrefix(p, []byte(`{"id":`)) && bytes.Contains(p[:min(len(p), 64)], []byte(`"method":"fetch"`)) {
		c.wrote <- struct{}{} // the driver is a goroutine of its own: it never holds a host lock
	}
	return len(p), nil
}

func (c *wireIn) Close() error { return nil }

// lastLine is the frame magpie wrote last, nil when none came.
func (c *wireIn) lastLine() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lines) == 0 {
		return nil
	}
	return c.lines[len(c.lines)-1]
}

// last is the frame magpie wrote last.
func (c *wireIn) last(t *testing.T) []byte {
	t.Helper()
	line := c.lastLine()
	if line == nil {
		t.Fatal("magpie wrote nothing to the child")
	}
	return line
}

// fetchFrame is the fetch request magpie wrote, whatever else it wrote (an
// abort follows when a test ends a body by cancelling it).
func (c *wireIn) fetchFrame(t *testing.T) []byte {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, line := range c.lines {
		if bytes.HasPrefix(line, []byte(`{"id":`)) && bytes.Contains(line[:min(len(line), 64)], []byte(`"method":"fetch"`)) {
			return line
		}
	}
	t.Fatal("magpie wrote no fetch request")
	return nil
}

// newWireHost is the host the tests and benchmarks drive: its stdout the test
// writes, its stdin kept, its read and dispatch the real ones.
func newWireHost(tb testing.TB, in *wireIn) (*host, *io.PipeWriter) {
	tb.Helper()
	pr, pw := io.Pipe()
	h := &host{cmd: &exec.Cmd{}, in: in, calls: map[int64]*call{}, dead: make(chan struct{})}
	go h.read(pr)
	tb.Cleanup(func() { _ = pw.CloseWithError(io.EOF) })
	return h, pw
}

// fetchHost makes h the host Fetch talks to and answers each fetch with only
// a head; callers cancel the context afterward. byFrame takes the id off the frame
// (tests, which keep them); otherwise the ids are the host's own, in order
// (benchmarks, which must not pay for parsing).
func fetchHost(tb testing.TB, h *host, pw *io.PipeWriter, in *wireIn, byFrame bool) {
	tb.Helper()
	// A plugin-list change another test made would have get() retire this host
	// for a real one, so the change is absorbed here first. The flag that says
	// a host is stale is taken rather than dropped, and put back when the
	// helper is done: a restart another magpie asked for still happens after
	// this test, and a newer signal is never overwritten with false.
	checkList()
	hostMu.Lock()
	prev, prevStale := current, hostStale.Swap(false)
	current = h
	hostMu.Unlock()
	write := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		_, _ = pw.Write(append(b, '\n'))
	}
	stop := make(chan struct{})
	go func() {
		id := int64(0)
		for {
			select {
			case <-stop:
				return
			case <-in.wrote:
			}
			if byFrame {
				var m struct {
					ID int64 `json:"id"`
				}
				line := in.lastLine()
				if line == nil || json.Unmarshal(bytes.TrimSuffix(line, []byte("\n")), &m) != nil {
					continue
				}
				id = m.ID
			} else {
				id++
			}
			if id == 0 {
				continue
			}
			// the head the child would have sent. The answer is never sent: it
			// would race the head through Fetch's select, so a test ends the
			// call by cancelling its context instead.
			write(map[string]any{"id": id, "event": "head", "status": 200, "headers": map[string]string{"content-type": "text/event-stream"}})
		}
	}()
	tb.Cleanup(func() {
		close(stop)
		hostMu.Lock()
		current = prev
		if prevStale {
			hostStale.Store(true)
		}
		hostMu.Unlock()
	})
}

// fetchOnce sends one request through the production Fetch and gives back the
// frame magpie wrote for it.
func fetchOnce(t *testing.T, r FetchRequest) []byte {
	t.Helper()
	in := newWireIn(true)
	h, pw := newWireHost(t, in)
	fetchHost(t, h, pw, in, true)
	ctx, cancel := context.WithCancel(context.Background())
	res, err := Fetch(ctx, r)
	if err != nil {
		cancel()
		t.Fatalf("Fetch: %v", err)
	}
	line := in.fetchFrame(t) // read before ending the call: the abort follows it
	if res.StatusCode != 200 {
		cancel()
		t.Fatalf("status = %d", res.StatusCode)
	}
	cancel() // the host sent no chunks: the call ends by cancelling, not by an answer
	if b, err := io.ReadAll(res.Body); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("the body = %q, %v", b, err)
	} else if len(b) != 0 {
		t.Fatalf("the host sent no chunks, so the body = %q", b)
	}
	res.Body.Close()
	return line
}

// legacyFetchParams is the anonymous params struct Fetch built before the body
// went to encoding/json as a []byte: the same tags, the body as the base64
// string EncodeToString made of it.
func legacyFetchParams(r FetchRequest) any {
	return struct {
		FetchRequest
		Body string `json:"body,omitempty"`
	}{r, base64.StdEncoding.EncodeToString(r.Body)}
}

// legacyFrame is the frame the older Fetch would have written for r, id and all.
func legacyFrame(t *testing.T, id int64, r FetchRequest) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"id": id, "method": "fetch", "params": legacyFetchParams(r)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(b, '\n')
}

// frameOf is a written frame, taken apart.
type frameOf struct {
	ID     int64                      `json:"id"`
	Method string                     `json:"method"`
	Params map[string]json.RawMessage `json:"params"`
}

func parseFrame(t *testing.T, line []byte) frameOf {
	t.Helper()
	var m frameOf
	if err := json.Unmarshal(bytes.TrimSuffix(line, []byte("\n")), &m); err != nil {
		t.Fatalf("the frame is not one JSON object: %v (%q)", err, clip(line))
	}
	return m
}

// clip shortens a frame for a failure message: a 1MiB body must not fill one.
func clip(b []byte) string {
	const n = 200
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…(" + strconv.Itoa(len(b)) + " bytes)"
}

// TestFetchHostKeepsTheRestartSignal: installing the test's host takes the flag
// that says the plugin list changed (get() would otherwise retire it for a real
// host), so the helper has to put that flag back when it is done — a restart
// another magpie asked for must not be dropped by a test.
func TestFetchHostKeepsTheRestartSignal(t *testing.T) {
	prev := hostStale.Load()
	t.Cleanup(func() { hostStale.Store(prev) }) // these tests run one at a time
	hostMu.Lock()
	before := current
	hostMu.Unlock()
	hostStale.Store(true) // what another magpie's change leaves behind

	t.Run("a helper used and cleaned up", func(t *testing.T) {
		in := newWireIn(false)
		h, pw := newWireHost(t, in)
		fetchHost(t, h, pw, in, false)
		if hostStale.Load() {
			t.Fatal("the test's host is installed with the stale flag still set: get() would retire it")
		}
	})

	if !hostStale.Load() {
		t.Fatal("the restart signal did not survive the helper's cleanup")
	}
	hostMu.Lock()
	after := current
	hostMu.Unlock()
	if after != before {
		t.Fatalf("current = %p, want the host from before the helper (%p)", after, before)
	}
}

func TestFetchBodyWireIsUnchanged(t *testing.T) {
	htmlUnicode := "a<b>&\"c\" — 你好 <script>"
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"json text", []byte(`{"model":"fake-1","messages":[{"role":"user","content":"héllo — 你好 <b>&amp;</b>"}]}`)},
		{"binary", []byte{0x00, 0x01, 0xfb, 0xff, 0x80, 0x7f}},
		{"one byte", []byte("a")},      // base64 "=" padding
		{"two bytes", []byte("ab")},    // "=="
		{"three bytes", []byte("abc")}, // no padding
		{"nil body", nil},
		{"empty body", []byte{}},
		{"1MiB", bytes.Repeat([]byte("0123456789abcdef"), 1<<16)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := FetchRequest{
				Provider: "fakeco", Account: "acct-1", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
				URL:     "https://vendor.example/v1/chat/completions?model=a&x=<b>&y=%E4%BD%A0%E5%A5%BD",
				Method:  "POST",
				Headers: map[string]string{"content-type": "application/json", "x-note": htmlUnicode},
				Body:    tc.body, Session: "s-1", Proxy: "direct",
			}
			line := fetchOnce(t, r)

			// one frame, newline-terminated, nothing raw inside it
			if !bytes.HasSuffix(line, []byte("\n")) || bytes.Count(line, []byte("\n")) != 1 {
				t.Fatalf("framing: %q", clip(line))
			}
			got := parseFrame(t, line)
			if got.Method != "fetch" || got.ID == 0 {
				t.Fatalf("frame = %+v", got)
			}

			// the older params build the same frame, byte for byte (the
			// metadata above is there to keep escaping and ordering pinned)
			want := legacyFrame(t, got.ID, r)
			if !bytes.Equal(line, want) {
				t.Fatalf("the wire changed:\n got %s\nwant %s", clip(line), clip(want))
			}

			// the body's field presence and its bytes, off the wire itself
			wantParams := parseFrame(t, want).Params
			raw, present := got.Params["body"]
			_, wantPresent := wantParams["body"]
			if present != wantPresent {
				t.Fatalf("the body field is there %v, the older frame has it %v", present, wantPresent)
			}
			if !present {
				return
			}
			var b64 string
			if err := json.Unmarshal(raw, &b64); err != nil {
				t.Fatalf("the body field is not a base64 string: %v", err)
			}
			back, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				t.Fatalf("the body does not decode: %v", err)
			}
			if !bytes.Equal(back, tc.body) {
				t.Fatalf("the body came back %d bytes, want %d", len(back), len(tc.body))
			}
		})
	}
}

// benchBodies are the payloads the benchmark sends, built once, outside the
// timer: what is timed is the encoding and the send, not the making of a body.
func benchBodies() []struct {
	name string
	body []byte
} {
	return []struct {
		name string
		body []byte
	}{
		{"1MiB", bytes.Repeat([]byte("0123456789abcdef"), 1<<16)},
		{"8MiB", bytes.Repeat([]byte("0123456789abcdef"), 1<<19)},
		{"16MiB", bytes.Repeat([]byte("0123456789abcdef"), 1<<20)},
	}
}

func benchRequest(body []byte) FetchRequest {
	return FetchRequest{
		Provider: "fakeco", Account: "acct-1", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL:     "https://vendor.example/chat/completions",
		Method:  "POST",
		Headers: map[string]string{"content-type": "application/json"},
		Body:    body, Session: "s-1", Proxy: "direct",
	}
}

// BenchmarkFetchBodyProd is the production path: Fetch builds its params with
// the body as a []byte and sends them. The pipe write, head dispatch and
// cancellation cleanup are part of every iteration. The paired comparison
// with the older encoding is this same benchmark run twice, overlaying host.go
// with the old Fetch body representation — there is no mirrored copy of it here
// to drift from the production path.
func BenchmarkFetchBodyProd(b *testing.B) {
	for _, tc := range benchBodies() {
		b.Run(tc.name, func(b *testing.B) {
			in := newWireIn(false)
			h, pw := newWireHost(b, in)
			fetchHost(b, h, pw, in, false)
			r := benchRequest(tc.body)
			b.SetBytes(int64(len(tc.body)))
			b.ReportAllocs()
			for b.Loop() {
				ctx, cancel := context.WithCancel(context.Background())
				res, err := Fetch(ctx, r)
				if err != nil {
					cancel()
					b.Fatal(err)
				}
				cancel() // ends the call: the harness sends a head and nothing else
				_, _ = io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
		})
	}
}
