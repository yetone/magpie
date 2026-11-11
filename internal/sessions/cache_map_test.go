package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The kept parses' file is read at start without a heap copy of it, ~25 MB
// on a long history (#2278), and what is decoded from it holds none of its
// bytes: on a system that maps it, a parse pointing into it after done
// would read unmapped memory.
func TestCacheFileReadWithoutACopy(t *testing.T) {
	unraised(t)
	var b strings.Builder
	fmt.Fprintf(&b, `{"version":%d,"zone":"z","files":{`, cacheVersion)
	for i := range 3000 {
		if i > 0 {
			b.WriteString(",")
		}
		key := fmt.Sprintf(`/s/%d.jsonl`, i)
		if i%2 == 1 { // an escaped name is unescaped into a copy; a plain one is read in place
			key = fmt.Sprintf(`/s/\"%d\".jsonl`, i)
		}
		fmt.Fprintf(&b, `"%s":{"size":%d,"mod":%d,"title":"té %d","cwd":"/w/%d","models":{"m%d":{"input":%d}},"claude":[{"key":"message:%d","m":{"request":"r%d"}}],"days":{"2026-10-%02d":{"models":{"m%d":{"output":%d}}}}}`, key, i, i*7, i, i, i, i, i, i, 1+i%28, i, i)
	}
	b.WriteString(`}}`)
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	want, ok := decodeCache([]byte(b.String()), "z")
	if !ok || len(want) != 3000 {
		t.Fatalf("the fixture decodes to %d files, %t", len(want), ok)
	}

	data, done, err := readCacheFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decodeCache(data, "z")
	done()
	runtime.GC()
	if !ok || !reflect.DeepEqual(got, want) {
		gj, _ := json.Marshal(got)
		t.Fatalf("read through readCacheFile: %t %.300s", ok, gj)
	}

	if runtime.GOOS == "windows" {
		return // read onto the heap there
	}
	var total uint64
	runs := 5
	for range runs {
		m0 := memBytes()
		_, done, _ := readCacheFile(path)
		total += memBytes() - m0
		done()
	}
	if per := total / uint64(runs); per > 64<<10 {
		t.Errorf("reading a %d-byte kept file allocates %d bytes; want it mapped", b.Len(), per)
	}
}

func memBytes() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}
