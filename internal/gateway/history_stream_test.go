package gateway

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// gzipFile holds one route at a time, not the day: a 7 GB day took a 1.6 GB
// host down when the file was read whole (tsingchao on X).
func TestGzipFileHoldsNoWholeDay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-01-01.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("x", 900)
	const lines, size = 75000, 64 << 20 // about 64 MB
	for i := 0; i < lines; i++ {
		fmt.Fprintf(f, `{"id":%d,"pad":"%s%d"}`+"\n", i, pad, i)
	}
	f.Close()
	if fi, _ := os.Stat(path); fi.Size() < size {
		t.Fatalf("fixture is %d bytes", fi.Size())
	}
	// a .gz holding a part of the day: the dedupe path
	pre := filepath.Join(dir, "2026-01-01.jsonl.gz")
	pf, _ := os.Create(pre)
	zw := gzip.NewWriter(pf)
	fmt.Fprintf(zw, `{"id":0,"pad":"%s0"}`+"\n", pad)
	zw.Close()
	pf.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	gzipFile(path)
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > 24<<20 {
		t.Fatalf("gzipFile allocated %d MB for a 64 MB day", used>>20)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the day's file is left")
	}
	if _, err := os.Stat(path + ".gz.tmp"); err == nil {
		t.Fatal("temp file left")
	}
	g, _ := os.Open(pre)
	defer g.Close()
	z, err := gzip.NewReader(g)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(z)
	if n := strings.Count(string(b), "\n"); n != lines {
		t.Fatalf("%d routes in the .gz, want %d", n, lines)
	}
}

// A day that outgrows a file goes on in a numbered one, and is read whole.
func TestDayGoesOnInANumberedFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := historyFileBytes
	historyFileBytes = 2000
	defer func() { historyFileBytes = old }()
	now := time.Now()
	for i := 1; i <= 30; i++ {
		saveRoute(Route{ID: int64(i), Time: now, Model: strings.Repeat("m", 300), Done: true})
	}
	day := now.Local().Format(dayForm)
	if _, err := os.Stat(filepath.Join(HistoryDir(), day+".1.jsonl")); err != nil {
		t.Fatalf("no numbered file: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(HistoryDir(), day+".jsonl")); fi.Size() > 2000+400 {
		t.Fatalf("first file is %d bytes", fi.Size())
	}
	_, routes, _ := History(day)
	if len(routes) != 30 {
		t.Fatalf("%d routes read", len(routes))
	}
	if r, ok := HistoryRoute(17, now); !ok || r.ID != 17 {
		t.Fatalf("route 17 in a numbered file: %+v %v", r, ok)
	}
}

// Today's files hold at most historyDayBytes: the oldest go, the latest stays.
func TestTodayCapped(t *testing.T) {
	dir := t.TempDir()
	old := historyDayBytes
	historyDayBytes = 250
	defer func() { historyDayBytes = old }()
	day := "2026-01-02"
	for _, n := range []string{day + ".jsonl", day + ".1.jsonl", day + ".2.jsonl"} {
		os.WriteFile(filepath.Join(dir, n), []byte(strings.Repeat("a", 100)), 0o600)
	}
	capToday(dir, day)
	if _, err := os.Stat(filepath.Join(dir, day+".jsonl")); err == nil {
		t.Fatal("the oldest file is kept")
	}
	if _, err := os.Stat(filepath.Join(dir, day+".2.jsonl")); err != nil {
		t.Fatal("the latest file is gone")
	}
}
