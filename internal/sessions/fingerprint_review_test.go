package sessions

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fullPrefixHash(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.CopyN(h, f, n); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
func TestSampledPrefixFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := []byte(strings.Repeat("history\n", 2<<20))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	n := int64(len(data))
	want := prefixHash(path, n)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("new call\n")
	f.Close()
	if got := prefixHash(path, n); got != want {
		t.Fatal("append changed previous-prefix fingerprint")
	}
	for _, at := range []int64{0, (n - 4096) * 7 / 15, n - 1} {
		f, err := os.OpenFile(path, os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteAt([]byte("X"), at)
		f.Close()
		if prefixHash(path, n) == want {
			t.Fatalf("rewrite at sample %d was not detected", at)
		}
		os.WriteFile(path, data, 0600)
	}
	if prefixHash(path, n+1) != "" {
		t.Fatal("short read accepted")
	}
}
func BenchmarkPrefixFingerprint(b *testing.B) {
	path := filepath.Join(b.TempDir(), "session.jsonl")
	data := []byte(strings.Repeat("synthetic session data\n", 450000))
	if err := os.WriteFile(path, data, 0600); err != nil {
		b.Fatal(err)
	}
	for _, impl := range []struct {
		name string
		f    func(string, int64) string
	}{{"sampled", prefixHash}, {"without_sampling", fullPrefixHash}} {
		b.Run(impl.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if impl.f(path, int64(len(data))) == "" {
					b.Fatal("read failed")
				}
			}
		})
	}
}
