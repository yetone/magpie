package gateway

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// chunked hands its bytes out a piece at a time, as a socket does.
type chunked struct {
	b    []byte
	size int
}

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), c.size)], c.b)
	c.b = c.b[n:]
	return n, nil
}

// allocated is the bytes f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// An agent's whole conversation, sent with its length, is read into one
// slice of its own size: io.ReadAll read it into chunks and copied them
// into a slice of their sum, twice the body on every turn (yg6888 on X:
// ~1 GB).
func TestRequestBodyReadOnceAtItsLength(t *testing.T) {
	sent := bytes.Repeat([]byte("0123456789abcdef"), 50_000) // 800 KB
	var got []byte
	var err error
	once := allocated(func() { got, err = readSized(&chunked{sent, 32 << 10}, int64(len(sent))) })
	if err != nil || !bytes.Equal(got, sent) {
		t.Fatalf("read %d bytes, %v", len(got), err)
	}
	if cap(got) != len(got) {
		t.Fatalf("cap %d for a body of %d", cap(got), len(got))
	}
	if limit := uint64(len(sent)) * 11 / 10; once > limit {
		t.Fatalf("an %d-byte body allocated %d bytes to read", len(sent), once)
	}
	// the measure tells the two apart
	if all := allocated(func() { io.ReadAll(&chunked{sent, 32 << 10}) }); all < uint64(len(sent))*3/2 {
		t.Fatalf("io.ReadAll allocated only %d for %d bytes: the check above shows nothing", all, len(sent))
	}

	// larger than the first slice: its final slice is still its own size
	big := bytes.Repeat(sent, 4)
	got, err = readSized(&chunked{big, 64 << 10}, int64(len(big)))
	if err != nil || !bytes.Equal(got, big) || cap(got) != len(got) {
		t.Fatalf("a %d-byte body read as %d (cap %d), %v", len(big), len(got), cap(got), err)
	}
}

// A body a little over a doubling of the first slice is not copied whole
// again for its last bytes: a Codex turn of 516 MB of generated images was
// read into 1+2+…+512 MB and then all of it into 516 MB, 1.5 GB allocated
// and copied outside the body's wait, and under -race on a loaded CI
// machine the turn was answered 504 for magpie's own work (#5281).
func TestRequestBodyNotCopiedForItsLastBytes(t *testing.T) {
	sent := bytes.Repeat([]byte("0123456789abcdef"), (4*sizedReadFirst+64<<10)/16) // 4 MB + 64 KB
	var got []byte
	var err error
	read := allocated(func() { got, err = readSized(&chunked{sent, 64 << 10}, int64(len(sent))) })
	if err != nil || !bytes.Equal(got, sent) || cap(got) != len(got) {
		t.Fatalf("read %d bytes (cap %d), %v", len(got), cap(got), err)
	}
	if limit := uint64(len(sent)) * 2; read > limit {
		t.Fatalf("a %d-byte body allocated %d bytes to read, over twice its size", len(sent), read)
	}
}

// Content-Length is the client's word: a body shorter than it said costs
// no more than the first slice, one longer reads on, a failed read is
// returned as io.ReadAll returns it, and no length is io.ReadAll.
func TestRequestBodyLengthNotTrusted(t *testing.T) {
	var got []byte
	lied := allocated(func() { got, _ = readSized(strings.NewReader("short"), defaultBodyLimit) })
	if string(got) != "short" {
		t.Fatalf("read %q", got)
	}
	if lied > sizedReadFirst+64<<10 {
		t.Fatalf("a client that said 512 MB and sent 5 bytes made magpie allocate %d bytes", lied)
	}
	long := strings.Repeat("y", 5000)
	if got, err := readSized(&chunked{[]byte(long), 700}, 10); err != nil || string(got) != long {
		t.Fatalf("a body longer than it said read as %d bytes, %v", len(got), err)
	}
	if got, err := readSized(io.MultiReader(strings.NewReader("abc"), failedBodyReader{}), 100); !errors.Is(err, io.ErrUnexpectedEOF) || string(got) != "abc" {
		t.Fatalf("failed read: %q, %v", got, err)
	}
	if got, err := readSized(strings.NewReader("abc"), -1); err != nil || string(got) != "abc" {
		t.Fatalf("no length: %q, %v", got, err)
	}
}

// Through the server: a body sent as it is is read at its length; an
// encoded one, whose Content-Length is the wire's, is decoded whole.
func TestRequestBodyReadAtItsLengthServed(t *testing.T) {
	sent := strings.Repeat(`{"role":"user","content":"hi"},`, 20_000)
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = New().requestBody(w, r, provider.Chat)
	}))
	defer srv.Close()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(sent))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(got) != sent || cap(got) != len(got) {
		t.Fatalf("read %d bytes (cap %d) of %d", len(got), cap(got), len(sent))
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(sent))
	zw.Close()
	req, _ := http.NewRequest("POST", srv.URL, &gz)
	req.Header.Set("Content-Encoding", "gzip")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(got) != sent {
		t.Fatalf("a gzip body of %d bytes read as %d", len(sent), len(got))
	}
}
