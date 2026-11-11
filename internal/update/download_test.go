package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A download that drops part way is tried again, and reports its progress.
func TestDownloadRetriesAndReports(t *testing.T) {
	body := make([]byte, 256<<10)
	for i := range body {
		body[i] = byte(i)
	}
	sum := sha256.Sum256(body)
	tries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries++
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if tries == 1 {
			w.Write(body[:1000]) // then the connection goes
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	var done, total int64
	ctx := WithProgress(context.Background(), func(d, t int64) { done, total = d, t })
	path := filepath.Join(t.TempDir(), "magpie.new")
	if err := download(ctx, Asset{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, path); err != nil {
		t.Fatal(err)
	}
	if tries != 2 {
		t.Fatalf("tries = %d, want 2", tries)
	}
	if done != int64(len(body)) || total != int64(len(body)) {
		t.Fatalf("progress = %d/%d, want %d/%d", done, total, len(body), len(body))
	}
	if b, _ := os.ReadFile(path); len(b) != len(body) {
		t.Fatalf("file has %d bytes", len(b))
	}
}

// quickRetries has download's tries follow each other at once.
func quickRetries(t *testing.T) {
	t.Helper()
	old := retryWaits
	retryWaits = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryWaits = old })
}

// A download the connection cuts part way goes on from where it broke
// off: the next try asks for the rest with Range, and the hash is checked
// over the whole file, the bytes of the first try included.
func TestDownloadResumesWhereItBrokeOff(t *testing.T) {
	quickRetries(t)
	body := make([]byte, 512<<10)
	for i := range body {
		body[i] = byte(i * 7)
	}
	sum := sha256.Sum256(body)
	var ranges []string
	cut := []int{100 << 10, 300 << 10} // where the first two tries break off
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		from := 0
		if rg := r.Header.Get("Range"); rg != "" {
			from, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		to := len(body)
		if n := len(ranges) - 1; n < len(cut) {
			to = cut[n]
		}
		w.Write(body[from:to])
		if to < len(body) {
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close() // mid-stream
		}
	}))
	defer srv.Close()

	var done, total int64
	ctx := WithProgress(context.Background(), func(d, t int64) { done, total = d, t })
	path := filepath.Join(t.TempDir(), "magpie.new")
	if err := download(ctx, Asset{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, path); err != nil {
		t.Fatal(err)
	}
	want := []string{"", "bytes=102400-", "bytes=307200-"}
	if !slices.Equal(ranges, want) {
		t.Fatalf("ranges asked = %q, want %q", ranges, want)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, body) {
		t.Fatalf("file has %d bytes, not the release's", len(b))
	}
	if done != int64(len(body)) || total != int64(len(body)) {
		t.Fatalf("progress = %d/%d, want %d/%d", done, total, len(body), len(body))
	}
}

// The rest of a file sent with other bytes than the release's (a host
// that resumes wrongly) fails the hash, which covers the whole file, and
// nothing is kept.
func TestDownloadResumedFileIsStillChecked(t *testing.T) {
	quickRetries(t)
	body := bytes.Repeat([]byte("magpie"), 50<<10)
	sum := sha256.Sum256(body)
	tries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries++
		if tries == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body[:1000])
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 1000-%d/%d", len(body)-1, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(bytes.Repeat([]byte("x"), len(body)-1000))
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "magpie.new")
	err := download(context.Background(), Asset{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, path)
	if err == nil || !strings.Contains(err.Error(), "does not match its checksum") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Fatal("the mismatched file was kept")
	}
}

// A 206 whose Content-Range doesn't start where the ask did (usemagpie.ai
// sent "bytes NaN-NaN/n", JohnnyN70389377 on X) is not laid after what was
// kept and is not a failure: the file is asked for whole.
func TestDownloadBadContentRangeIsFetchedWhole(t *testing.T) {
	quickRetries(t)
	body := bytes.Repeat([]byte("magpie"), 50<<10)
	sum := sha256.Sum256(body)
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		if len(ranges) == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body[:1000])
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes NaN-NaN/%d", len(body)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[1000:])
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "magpie.new")
	if err := download(context.Background(), Asset{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, path); err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "bytes=1000-", ""}; !slices.Equal(ranges, want) {
		t.Fatalf("ranges asked = %q, want %q", ranges, want)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, body) {
		t.Fatalf("file has %d bytes, not the release's", len(b))
	}
}

// A connection dropped before any answer (EOF at the connect, what the
// reporter's Windows box got from GitHub's asset host) is tried again
// every time retryWaits allows, and the error that ends it names the file
// and the cause, not the signed address it was redirected to.
func TestDownloadDroppedConnectionErrorKeepsAddressOut(t *testing.T) {
	quickRetries(t)
	tries := 0
	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries++
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close() // EOF, no answer at all
	}))
	defer assets.Close()
	signed := assets.URL + "/github-production-release-asset/1?sig=SECRETSIG&jwt=SECRETJWT"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, signed, http.StatusFound)
	}))
	defer gh.Close()
	path := filepath.Join(t.TempDir(), "magpie.new")
	err := download(context.Background(), Asset{URL: gh.URL + "/magpie-windows-amd64.exe", SHA256: "00"}, path)
	if err == nil || !Network(err) {
		t.Fatalf("err = %v, want a network error", err)
	}
	if tries != len(retryWaits)+1 {
		t.Fatalf("tries = %d, want %d", tries, len(retryWaits)+1)
	}
	if msg := err.Error(); strings.Contains(msg, "SECRET") || strings.Contains(msg, "http") || !strings.Contains(msg, "magpie.new") || !strings.Contains(msg, "EOF") {
		t.Fatalf("error = %q", msg)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Fatal("a partial file was kept")
	}
}

// A refusal that isn't the network's (404: not uploaded yet) isn't tried
// again and again.
func TestDownloadNotFoundIsNotRetried(t *testing.T) {
	quickRetries(t)
	tries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries++
		http.NotFound(w, r)
	}))
	defer srv.Close()
	err := download(context.Background(), Asset{URL: srv.URL, SHA256: "00"}, filepath.Join(t.TempDir(), "magpie.new"))
	if err == nil || Network(err) || tries != 1 {
		t.Fatalf("err = %v after %d tries, want one 404", err, tries)
	}
}

// A connection that goes silent part way, its bytes held back with the
// connection kept open (what left esir_tech's update at 17% on Discord),
// is broken off after stallAfter and tried again from where it stopped,
// and the steps say so: the download, the wait to try again, the rest,
// then the checksum.
func TestDownloadStallIsTriedAgain(t *testing.T) {
	quickRetries(t)
	old := stallAfter
	stallAfter = 300 * time.Millisecond
	t.Cleanup(func() { stallAfter = old })
	body := make([]byte, 600<<10)
	for i := range body {
		body[i] = byte(i * 13)
	}
	sum := sha256.Sum256(body)
	cut := len(body) * 17 / 100
	var mu sync.Mutex
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		first := len(ranges) == 1
		mu.Unlock()
		if first {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body[:cut])
			w.(http.Flusher).Flush()
			<-r.Context().Done() // silent, the connection open, until the client goes
			return
		}
		from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-"))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[from:])
	}))
	defer srv.Close()

	var steps []string
	var done, total int64
	ctx := WithProgress(context.Background(), func(d, t int64) { done, total = d, t })
	ctx = WithStep(ctx, func(s string) {
		if len(steps) == 0 || steps[len(steps)-1] != s {
			steps = append(steps, s)
		}
	})
	// the update's own bound, far over the stall's, is what the old code
	// waited out
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "magpie.new")
	start := time.Now()
	if err := download(ctx, Asset{URL: srv.URL, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}, path); err != nil {
		t.Fatalf("after %s at %d of %d bytes: %v", time.Since(start).Round(time.Millisecond), done, total, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"", fmt.Sprintf("bytes=%d-", cut)}; !slices.Equal(ranges, want) {
		t.Fatalf("ranges asked = %q, want %q", ranges, want)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, body) {
		t.Fatalf("file has %d bytes, not the release's", len(b))
	}
	if want := []string{StepDownload, StepRetry, StepDownload, StepVerify}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %q, want %q", steps, want)
	}
}

// A stall that never ends is the network's, said so: tried every time
// retryWaits allows, and the error names what happened.
func TestDownloadStallEveryTimeSaysSo(t *testing.T) {
	quickRetries(t)
	old := stallAfter
	stallAfter = 50 * time.Millisecond
	t.Cleanup(func() { stallAfter = old })
	var tries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries.Add(1)
		<-r.Context().Done() // no answer at all
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := download(ctx, Asset{URL: srv.URL, SHA256: "00"}, filepath.Join(t.TempDir(), "magpie.new"))
	if err == nil || !Network(err) || !errors.Is(err, errStalled) || ctx.Err() != nil {
		t.Fatalf("err = %v, want a stall", err)
	}
	if n := int(tries.Load()); n != len(retryWaits)+1 {
		t.Fatalf("tries = %d, want %d", n, len(retryWaits)+1)
	}
}

// A download still moving, however slowly, is never ended by a total time
// (#5572: the 83 MB Windows exe under ~140 KB/s failed at the client's 10
// minutes). Here the feed's client gives up after 300ms and the body
// takes ~1.2s, a byte every 100ms well inside the stall's while: one try
// reads it whole, with no break and no retry.
func TestDownloadSlowButMovingOutlastsTheClientTimeout(t *testing.T) {
	quickRetries(t)
	oldStall, oldClient := stallAfter, client
	stallAfter = time.Second
	client = &http.Client{Timeout: 300 * time.Millisecond, Transport: oldClient.Transport}
	t.Cleanup(func() { stallAfter, client = oldStall, oldClient })
	const chunk, chunks = 4 << 10, 12
	body := make([]byte, chunk*chunks)
	for i := range body {
		body[i] = byte(i * 11)
	}
	sum := sha256.Sum256(body)
	var tries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		for i := 0; i < chunks; i++ {
			if _, err := w.Write(body[i*chunk : (i+1)*chunk]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()
	var steps []string
	ctx := WithStep(context.Background(), func(s string) { steps = append(steps, s) })
	path := filepath.Join(t.TempDir(), "magpie.new")
	if err := download(ctx, Asset{URL: srv.URL, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}, path); err != nil {
		t.Fatalf("a moving download failed: %v", err)
	}
	if n := tries.Load(); n != 1 {
		t.Fatalf("asked %d times, want one try that wasn't broken off", n)
	}
	if want := []string{StepDownload, StepVerify}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %q, want %q", steps, want)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, body) {
		t.Fatalf("file has %d bytes, not the release's", len(b))
	}
}
