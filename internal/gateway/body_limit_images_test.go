package gateway

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/provider"
)

// NeoPengZhile's Codex session had read many images; Codex sends the
// whole conversation each turn, screenshots inline, zstd-compressed, and
// past 128 MB decoded magpie answered 413 on all five retries (#1422).
// Such a turn is read now: a zstd body of inline images decoding to
// 136 MB, on Codex's backend path, with the gateway's own limit.
func TestImageHeavyCodexTurnIsRead(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a few hundred MB")
	}
	img := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 1, 2, 3, 4}, 1<<17)) // ~1.3 MB each
	var turn strings.Builder
	turn.WriteString(`{"model":"gpt-5.5","input":[`)
	for i := 0; turn.Len() < 136<<20; i++ {
		if i > 0 {
			turn.WriteByte(',')
		}
		turn.WriteString(`{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,`)
		turn.WriteString(img)
		turn.WriteString(`"}]}`)
	}
	turn.WriteString(`]}`)
	plain := turn.String()
	zw, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	sent := zw.EncodeAll([]byte(plain), nil)
	r := httptest.NewRequest("POST", CodexPath+"/responses", bytes.NewReader(sent))
	r.Header.Set("Content-Encoding", "zstd")
	w := httptest.NewRecorder()
	body, status, err := readBoundedRequestBody(w, r, requestLimits{})
	if err != nil {
		t.Fatalf("a %d MB image-heavy turn (%d bytes on the wire): %d %v", len(plain)>>20, len(sent), status, err)
	}
	if len(body) != len(plain) {
		t.Fatalf("read %d bytes of %d", len(body), len(plain))
	}
}

// One past the limit is still refused, and the 413 says it is magpie's
// own limit, that the provider wasn't asked, and to compact (#1422): the
// reporter saw only "request body exceeds gateway limit" and couldn't
// tell whose it was.
func TestOverLimitSaysWhoseAndWhatToDo(t *testing.T) {
	var compressed bytes.Buffer
	zw, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	zw.Write([]byte(`{"model":"gpt-5.5","input":"` + strings.Repeat("x", 4096) + `"}`))
	zw.Close()
	s := New()
	s.requestLimits.body = 1024
	r := httptest.NewRequest("POST", CodexPath+"/responses", &compressed)
	r.Header.Set("Content-Encoding", "zstd")
	w := httptest.NewRecorder()
	s.codexBackend(w, r)
	if w.Code != 413 {
		t.Fatalf("status %d, want 413", w.Code)
	}
	got := w.Body.String()
	for _, want := range []string{"magpie's gateway limit of 1 KB", "the provider was not asked", "/compact", "not the model's context"} {
		if !strings.Contains(got, want) {
			t.Errorf("413 body %s doesn't say %q", got, want)
		}
	}
}

// codexMemory has this test's machine memTotal bytes of memory, as
// codexBodyLimit and bodyRoomSize read it.
func codexMemory(t *testing.T, memTotal int64) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc/meminfo"), []byte(fmt.Sprintf("MemTotal: %d kB\n", memTotal>>10)), 0o644); err != nil {
		t.Fatal(err)
	}
	wasGOOS, wasRoot := memGOOS, memRoot
	memGOOS, memRoot = "linux", root
	t.Cleanup(func() { memGOOS, memRoot = wasGOOS, wasRoot })
}

// A turn's limit on Codex's own path is a sixteenth of the machine's
// memory, never under the gateway's 512 MB nor over 2 GB.
func TestCodexBodyLimitFromMemory(t *testing.T) {
	for _, c := range []struct{ mem, want int64 }{
		{2 << 30, defaultBodyLimit},
		{8 << 30, defaultBodyLimit},
		{16 << 30, 1 << 30},
		{24 << 30, 1536 << 20},
		{64 << 30, codexBodyMost},
	} {
		codexMemory(t, c.mem)
		if got := codexBodyLimit(); got != c.want {
			t.Errorf("%d GiB: %d MiB, want %d MiB", c.mem>>30, got>>20, c.want>>20)
		}
	}
	// memory not known is not a small machine, nor a large one
	memGOOS, memRoot = "linux", t.TempDir()
	if got := codexBodyLimit(); got != defaultBodyLimit {
		t.Errorf("unknown: %d MiB", got>>20)
	}
}

// dallascao on Discord: Codex generating images and revising them again
// and again, until magpie answered 413 "over magpie's gateway limit of
// 512 MB (decoded)" on /backend-api/codex/responses, while Codex signed
// in to OpenAI went on. Codex keeps each generated image in the
// conversation as a function_call_output's input_image, and over HTTP
// sends it whole every turn. On a 16 GB machine such a turn of a little
// over 512 MB, on one of Codex's own models, now reaches the ChatGPT
// backend whole; the same turn for a magpie model is still refused, as a
// magpie model's turn is translated at several times its size, and the
// 413 says Codex's own models take more.
func TestCodexGeneratedImagesPastOldLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a few GB")
	}
	codexMemory(t, 16<<30)
	setup(t, provider.Chat, &fake{t: t})
	var got atomic.Int64
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		got.Store(n)
		w.Header()["Content-Type"] = nil
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	// a generated 1536x1024 PNG of ~3 MB, ~4 MB as base64
	img := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 5, 6, 7, 8}, 3<<17))
	var turn bytes.Buffer
	turn.Grow(defaultBodyLimit + 16<<20)
	turn.WriteString(`{"model":"gpt-5.5","stream":true,"store":false,"input":[`)
	for i := 0; turn.Len() <= defaultBodyLimit+4<<20; i++ {
		if i > 0 {
			turn.WriteByte(',')
		}
		fmt.Fprintf(&turn, `{"type":"message","role":"user","content":[{"type":"input_text","text":"bluer, take %d"}]},`, i)
		fmt.Fprintf(&turn, `{"type":"function_call","name":"imagegen","namespace":"image_gen","arguments":"{\"prompt\":\"a cat, take %d\"}","call_id":"call_%d"},`, i, i)
		fmt.Fprintf(&turn, `{"type":"function_call_output","call_id":"call_%d","output":[{"type":"input_image","image_url":"data:image/png;base64,%s","detail":"high"}]}`, i, img)
	}
	turn.WriteString(`]}`)
	body := turn.Bytes()
	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		New().Handler().ServeHTTP(rec, req)
		return rec
	}
	start := time.Now()
	rec := post()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"input_tokens":9`) {
		t.Fatalf("a %d MB turn of generated images: %d %.300s", len(body)>>20, rec.Code, rec.Body.String())
	}
	if got.Load() < int64(len(body))-1<<20 {
		t.Fatalf("the backend got %d bytes of %d", got.Load(), len(body))
	}
	t.Logf("a %d MB turn went on in %s", len(body)>>20, time.Since(start))

	got.Store(0)
	copy(body[len(`{"model":"`):], "x/gpt-5") // a magpie model, as long as gpt-5.5
	rec = post()
	if rec.Code != 413 || got.Load() != 0 {
		t.Fatalf("a magpie model's %d MB turn: %d, backend got %d", len(body)>>20, rec.Code, got.Load())
	}
	for _, want := range []string{"magpie's gateway limit of 512 MB", "the provider was not asked", "one of Codex's own models takes up to 1024 MB", "/compact"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("413 %s doesn't say %q", rec.Body.String(), want)
		}
	}
}
