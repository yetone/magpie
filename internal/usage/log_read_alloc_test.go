package usage

import (
	"bufio"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// The log's first read decodes every line into one Record from the
// reader's own buffer: a Record a line, which Unmarshal's pointer put on
// the heap, and a copy of each line were ~42 MB of a real log's read
// (#2278).
func TestLogReadReusesItsRecord(t *testing.T) {
	pageHome(t)
	holdClock(t, time.Now())
	const n = 20000
	historyLog(t, n)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	s := readLogSnapshot()
	runtime.ReadMemStats(&after)
	got := 0
	for _, c := range s.blocks {
		got += c.Count
	}
	if got != n {
		t.Fatalf("read %d records of %d", got, n)
	}
	records := int64(n) * int64(unsafe.Sizeof(Record{}))
	allocated := int64(after.TotalAlloc - before.TotalAlloc)
	t.Logf("read of %d records allocated %d bytes; a Record each is %d", n, allocated, records)
	if allocated > records {
		t.Fatalf("a read of %d records allocated %d bytes, more than a Record each (%d)", n, allocated, records)
	}
}

// A line longer than the reader's buffer is read whole, and the partial
// line at the end is left for the next read.
func TestReadLineGathersALongLine(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	rd := bufio.NewReaderSize(strings.NewReader("a\n"+long+"\nb\npartial"), 64<<10)
	var kept []byte
	var want = []string{"a\n", long + "\n", "b\n"}
	for _, w := range want {
		var b []byte
		var err error
		b, kept, err = readLine(rd, kept)
		if err != nil || string(b) != w {
			t.Fatalf("read %d bytes (%v), want %d", len(b), err, len(w))
		}
	}
	if b, _, err := readLine(rd, kept); err == nil || string(b) != "partial" {
		t.Fatalf("the partial line: %q, %v", b, err)
	}
}
