package sessions

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// oldPrefixHash is prefixHash as it was, through io.CopyN: a stored hash
// must still match after the change, or every cache is read again.
func oldPrefixHash(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	const window, samples = int64(4096), int64(16)
	if n <= window*samples {
		if _, err = io.CopyN(h, f, n); err != nil {
			return ""
		}
	} else {
		for i := int64(0); i < samples; i++ {
			off := (n - window) * i / (samples - 1)
			if _, err = io.CopyN(h, io.NewSectionReader(f, off, window), window); err != nil {
				return ""
			}
		}
	}
	return fmt.Sprintf("sample-v1:%x", h.Sum(nil))
}

func TestPrefixHashUnchangedAndUnbuffered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	b := make([]byte, 3<<20)
	for i := range b {
		b[i] = byte(i*7 + i/4096)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{0, 1, 4095, 4096, 65535, 65536, 65537, 1 << 20, 3 << 20} {
		if got, want := prefixHash(path, n), oldPrefixHash(path, n); got != want || got == "" {
			t.Fatalf("n=%d: %q, was %q", n, got, want)
		}
	}
	// Past the file's end is unknown, as it was.
	for _, n := range []int64{3<<20 + 1, 4 << 20} {
		if got := prefixHash(path, n); got != "" {
			t.Fatalf("n=%d past the end: %q", n, got)
		}
		if old := oldPrefixHash(path, n); old != "" {
			t.Fatalf("n=%d: the old hash was %q", n, old)
		}
	}

	prefixHash(path, 1<<20)
	var a, z runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	const runs = 50
	for i := 0; i < runs; i++ {
		prefixHash(path, 1<<20)
	}
	runtime.ReadMemStats(&z)
	// io.CopyN's buffers came to 65 KB a call. Under -race the pool drops
	// some of what it is given (13 KB a call), so the bar sits between.
	if per := (z.TotalAlloc - a.TotalAlloc) / runs; per > 24<<10 {
		t.Fatalf("hashing a long log allocated %d KB", per>>10)
	}
}
