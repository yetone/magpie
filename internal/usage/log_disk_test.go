package usage

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/probelog"
	"github.com/yetone/magpie/internal/provider"
)

// logLines appends n calls from the i-th on to the log, as a gateway that
// isn't running reads them at its next start: in two zones, with hosts,
// keys, a try handed on and probes among them.
func logLines(t *testing.T, from, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.FixedZone("", 8*3600))
	for i := from; i < from+n; i++ {
		r := Record{Time: at.Add(time.Duration(i) * time.Minute), Agent: "codex", Provider: fmt.Sprintf("p%d", i%3), Model: "m",
			Host: fmt.Sprintf("h%d.example", i%5), Session: fmt.Sprintf("s%d", i/40), Input: i, Status: 200}
		switch {
		case i%7 == 0:
			r.Time = r.Time.UTC()
		case i%11 == 0:
			r.ProviderKeyID, r.ProviderKeyName = fmt.Sprintf("k%d", i), "Team"
		case i%13 == 0:
			r.Source, r.Kind, r.Endpoint, r.Status, r.Error = SourceProbe, probelog.KindTest, "/v1/chat/completions", 500, "down"
		case i%17 == 0:
			r.RouteID, r.Status, r.Error = int64(i), 502, "bad gateway"
		case i%17 == 1:
			r.RouteID = int64(i - 1)
		}
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// logRead is what a snapshot gives its readers, written out.
func logRead(t *testing.T, s *logSnapshot) map[string]any {
	t.Helper()
	var rows, probes []string
	s.visit(time.Time{}, func(r Record) {
		b, _ := json.Marshal(r)
		rows = append(rows, string(b))
	})
	eachProbe(s.probes, time.Time{}, func(r Record, off int64) {
		b, _ := json.Marshal(r)
		probes = append(probes, fmt.Sprintf("%d %s", off, b))
	})
	first, _ := s.first.MarshalText()
	firstOwn, _ := s.firstOwn.MarshalText()
	return map[string]any{"rows": rows, "probes": probes, "first": string(first), "firstOwn": string(firstOwn), "off": s.off, "hash": s.hash,
		"blocks": len(s.blocks), "keys": slices.Sorted(maps.Keys(s.keyProviders)), "hosts": s.hosts}
}

func restartLog() {
	logIndex.Lock()
	logIndex.snapshot = nil
	logIndex.Unlock()
	logDiskWrote.Lock()
	logDiskWrote.path, logDiskWrote.off, logDiskWrote.at = "", 0, time.Time{}
	logDiskWrote.Unlock()
}

// A start reads the log's full blocks from log-v1.bin and parses only the
// lines after them, into the snapshot a parse of the whole log makes; a
// log whose first lines changed is parsed whole again (#2278).
func TestStartReadsKeptLogBlocks(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	t.Cleanup(restartLog)
	loaded := -1
	logDiskLoaded = func(n int) { loaded = n }
	t.Cleanup(func() { logDiskLoaded = nil })
	restartLog()
	logLines(t, 0, 2*logBlockRows+300)
	first := logRead(t, readLogSnapshot())
	if loaded != -1 {
		t.Fatalf("the first start read %d blocks from a file it hadn't written", loaded)
	}
	if _, err := os.Stat(logDiskPath()); err != nil {
		t.Fatalf("the full blocks weren't kept: %v", err)
	}
	if n := len(first["probes"].([]string)); n < 50 {
		t.Fatalf("%d probes", n)
	}

	// the log grows while magpie isn't running
	logLines(t, 2*logBlockRows+300, logBlockRows)
	restartLog()
	kept := logRead(t, readLogSnapshot())
	if loaded != 2 {
		t.Fatalf("started again, %d blocks read from the file, want 2", loaded)
	}
	loaded = -1
	os.Remove(logDiskPath())
	restartLog()
	whole := logRead(t, readLogSnapshot())
	if loaded != -1 {
		t.Fatal("read blocks from a file that was removed")
	}
	if !reflect.DeepEqual(kept, whole) {
		for k := range whole {
			if !reflect.DeepEqual(kept[k], whole[k]) {
				t.Errorf("%s from the kept blocks differs from the log's parse:\n%v\nwant\n%v", k, kept[k], whole[k])
			}
		}
	}

	// the log's first line is written over: its blocks are parsed again
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(b, '1'); i < 0 {
		t.Fatal("no digit")
	} else {
		b[i] = '2'
	}
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	restartLog()
	rewritten := logRead(t, readLogSnapshot())
	if loaded != -1 {
		t.Fatalf("read %d blocks kept of the log before it was written over", loaded)
	}
	os.Remove(logDiskPath())
	restartLog()
	if want := logRead(t, readLogSnapshot()); !reflect.DeepEqual(rewritten, want) {
		t.Fatal("the log written over isn't read as a parse of it reads it")
	}
}

// probeLines appends n MCP tool calls from the i-th on to the log.
func probeLines(t *testing.T, from, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	for i := from; i < from+n; i++ {
		r := Record{Time: at.Add(time.Duration(i) * time.Second), Source: SourceMCP, Agent: "codex", Provider: fmt.Sprintf("p%d", i%3),
			Model: fmt.Sprintf("tool%d", i%9), Millis: int64(i), Status: 200}
		if i%5 == 0 {
			r.Source, r.Kind, r.Host, r.Status, r.Error = SourceProbe, probelog.KindTest, "h.example", 500, "down"
		}
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// The probes are kept in blocks as the gateway rows are: a read continued
// over appended probes reopens the last block as a copy, so the snapshot
// before it reads as it did, and the two read as one parse of the log.
func TestProbeBlocksContinued(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	t.Cleanup(restartLog)
	restartLog()
	probeLines(t, 0, 2*logBlockRows+100)
	before := readLogSnapshot()
	want := logRead(t, before)
	if n := len(want["probes"].([]string)); n != 2*logBlockRows+100 {
		t.Fatalf("%d probes, want %d", n, 2*logBlockRows+100)
	}
	if len(before.probes) != 3 {
		t.Fatalf("%d probe blocks, want 3", len(before.probes))
	}
	probeLines(t, 2*logBlockRows+100, logBlockRows)
	after := readLogSnapshot()
	if after == before || len(after.probes) < 4 {
		t.Fatalf("not read again: %d blocks", len(after.probes))
	}
	if got := logRead(t, before); !reflect.DeepEqual(got["probes"], want["probes"]) {
		t.Fatal("the snapshot before the append changed under its readers")
	}
	grown := logRead(t, after)
	os.Remove(logDiskPath())
	restartLog()
	if whole := logRead(t, readLogSnapshot()); !reflect.DeepEqual(grown["probes"], whole["probes"]) {
		t.Fatalf("the continued read's probes differ from the log's parse: %d vs %d", len(grown["probes"].([]string)), len(whole["probes"].([]string)))
	}
	if c := probeChunk(after.probes, time.Date(2026, 8, 1, 9, 20, 0, 0, time.UTC), provider.Renames{}, nil); c == nil || c.Count != 3*logBlockRows+100-1200 {
		t.Fatalf("probes since: %v", c)
	}
}

// A log of MCP tool calls and probes alone has no gateway block, and was
// never kept: every start parsed all of it again. Its full probe blocks
// are kept, and a start reads them as a parse of the log does (#2278).
func TestStartReadsKeptProbeBlocks(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	t.Cleanup(restartLog)
	loaded := -1
	logDiskLoaded = func(n int) { loaded = n }
	t.Cleanup(func() { logDiskLoaded = nil })
	restartLog()
	probeLines(t, 0, 3*logBlockRows+100)
	readLogSnapshot()
	if _, err := os.Stat(logDiskPath()); err != nil {
		t.Fatalf("a log of probes wasn't kept: %v", err)
	}
	probeLines(t, 3*logBlockRows+100, logBlockRows/2)
	restartLog()
	kept := logRead(t, readLogSnapshot())
	if loaded != 3 {
		t.Fatalf("started again, %d blocks read from the file, want 3", loaded)
	}
	os.Remove(logDiskPath())
	restartLog()
	loaded = -1
	whole := logRead(t, readLogSnapshot())
	if loaded != -1 {
		t.Fatal("read blocks from a file that was removed")
	}
	if !reflect.DeepEqual(kept, whole) {
		t.Fatalf("the kept probes differ from the log's parse: %d vs %d", len(kept["probes"].([]string)), len(whole["probes"].([]string)))
	}
}
