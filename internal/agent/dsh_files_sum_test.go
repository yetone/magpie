package agent

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDshFilesSumReadsNoFileWhole says a round's sum of dsh's files is
// what it was when each file was read whole, and costs no more for a large
// patch list than a small one: a list names every model magpie serves,
// and both dsh homes' rounds every 30s read each file into memory again
// only to hash it.
func TestDshFilesSumReadsNoFileWhole(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "profiles", "a", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(list), 0o755); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("- id: magpie/some-provider/some-model\n"), 30000) // ~1.1MB, past one buffer
	for _, f := range []struct {
		path string
		b    []byte
	}{{list, big}, {filepath.Join(dir, "settings.yaml"), []byte("a: 1\n")}} {
		if err := os.WriteFile(f.path, f.b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{list}

	// the sum as it was, each file's bytes written at once
	whole := func() uint64 {
		h := sha256.New()
		for _, f := range append(files, filepath.Join(dir, "cordis.patch.yml"), filepath.Join(dir, "settings.yaml"),
			filepath.Join(dir, ".env"), filepath.Join(dir, ".credentials.yaml"), filepath.Join(dir, "config.yaml")) {
			h.Write([]byte(f))
			h.Write([]byte{0})
			if b, err := os.ReadFile(f); err == nil {
				h.Write([]byte{1})
				h.Write(b)
			} else {
				h.Write([]byte{2})
			}
			h.Write([]byte{0})
		}
		return dshSum64(h.Sum(nil))
	}
	if got, want := dshFilesSum(dir, files), whole(); got != want {
		t.Fatalf("sum %x, want %x as for the files read whole", got, want)
	}
	before := dshFilesSum(dir, files)
	big[len(big)-2] = 'X'
	if err := os.WriteFile(list, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if dshFilesSum(dir, files) == before {
		t.Fatal("a change at the end of a large patch list left the sum as it was")
	}

	var total uint64
	allocs := testing.AllocsPerRun(10, func() {
		m0 := memBytes()
		dshFilesSum(dir, files)
		total += memBytes() - m0
	})
	if per := total / 11; per > 64<<10 {
		t.Errorf("a sum allocates %d bytes (%v allocations) for a %d-byte patch list; want it read through a buffer", per, allocs, len(big))
	}
}

func memBytes() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}
