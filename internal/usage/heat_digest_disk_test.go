package usage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"
)

// A start takes a session file's chunk's digest from the last run's, kept
// by its serial, and hashes its rows only when the file or the row code
// changed (#2278).
func TestSourceDigestsKeptAcrossStarts(t *testing.T) {
	pageHome(t)
	cut := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	d := heatDiskRef{root: "r", meta: "m"}
	chunk := func(serial uint64, input int) *rowChunk {
		c := &rowChunk{serial: serial}
		c.Source.Agent, c.Source.Key, c.Source.Path = "codex", "k", fmt.Sprint("/s/", serial)
		for i := 1; i <= 3; i++ {
			c.add(Row{Record: Record{Time: cut.Add(-time.Duration(i) * time.Hour), Agent: "codex", Model: "m", Input: i * input}}, fmt.Sprint("msg", i), int64(i), false)
		}
		return c
	}
	restart := func() {
		sourceDigests.Lock()
		sourceDigests.by, sourceDigests.last, sourceDigests.made = nil, nil, nil
		sourceDigestsDisk.path, sourceDigestsDisk.kept = "", nil
		sourceDigests.Unlock()
	}
	key := func(cs ...*rowChunk) (string, int) {
		sourceDigests.Lock()
		was := sourceDigestsHashed
		sourceDigests.Unlock()
		k, ok := heatSettledKey(d, cut, time.Time{}, Filter{}, nil, [3][]*rowChunk{cs}, heatSum{})
		if !ok {
			t.Fatal("not named")
		}
		sourceDigests.Lock()
		defer sourceDigests.Unlock()
		return k, sourceDigestsHashed - was
	}
	restart()
	first, n := key(chunk(1<<63|1, 10), chunk(1<<63|2, 20))
	if n != 2 {
		t.Fatalf("a first start hashed %d chunks, want 2", n)
	}
	// the same run again: what was made is kept, not hashed again
	if again, n := key(chunk(1<<63|1, 10), chunk(1<<63|2, 20)); n != 0 || again != first {
		t.Fatalf("same run, hashed %d chunks, key %s, was %s", n, again, first)
	}
	sourceDigests.Lock()
	if len(sourceDigests.made) != 0 || sourceDigestsDisk.kept.len() != 2 {
		t.Fatalf("after a save: %d made, %d kept", len(sourceDigests.made), sourceDigestsDisk.kept.len())
	}
	sourceDigests.Unlock()
	restart()
	if again, n := key(chunk(1<<63|1, 10), chunk(1<<63|2, 20)); n != 0 || again != first {
		t.Fatalf("started again, hashed %d chunks, key %s, was %s", n, again, first)
	}
	// a start that met one file keeps the other's for the next
	restart()
	if _, n := key(chunk(1<<63|1, 10)); n != 0 {
		t.Fatalf("hashed %d", n)
	}
	restart()
	if _, n := key(chunk(1<<63|3, 30)); n != 1 {
		t.Fatalf("a new file hashed %d, want 1", n)
	}
	restart()
	if again, n := key(chunk(1<<63|1, 10), chunk(1<<63|2, 20)); n != 0 || again != first {
		t.Fatalf("after a start of other files, hashed %d chunks, key %s, was %s", n, again, first)
	}

	// another build's row code: hashed again
	b, err := os.ReadFile(sourceDigestsPath())
	if err != nil {
		t.Fatal(err)
	}
	other := encodeSourceDigests(requestDiskBinary()+"x", 10, decodeSourceDigests(b, requestDiskBinary()), nil)
	if decodeSourceDigests(b, requestDiskBinary()).len() != 3 {
		t.Fatalf("kept %d, want 3", decodeSourceDigests(b, requestDiskBinary()).len())
	}
	if err := os.WriteFile(sourceDigestsPath(), other, 0o600); err != nil {
		t.Fatal(err)
	}
	restart()
	if _, n := key(chunk(1<<63|1, 10), chunk(1<<63|2, 20)); n != 2 {
		t.Fatalf("other row code: hashed %d chunks, want 2", n)
	}
	// a file cut short is nothing kept
	b, _ = os.ReadFile(sourceDigestsPath())
	os.WriteFile(sourceDigestsPath(), b[:len(b)-1], 0o600)
	restart()
	if _, n := key(chunk(1<<63|1, 10)); n != 1 {
		t.Fatalf("a cut file: hashed %d, want 1", n)
	}
	if got := decodeSourceDigests([]byte(sourceDigestsMagic), requestDiskBinary()); got != nil {
		t.Fatalf("no code: %v", got)
	}
	var sum [sha256.Size]byte
	made := map[uint64][sha256.Size]byte{1: sum, 2: sum}
	if got := decodeSourceDigests(encodeSourceDigests("c", 1, nil, made, map[uint64]struct{}{1: {}, 2: {}}), "c"); got.len() != 1 {
		t.Fatalf("limit 1 kept %d", got.len())
	}
	// a serial used with no digest made or kept is left out
	if got := decodeSourceDigests(encodeSourceDigests("c", 9, nil, map[uint64][sha256.Size]byte{1: sum}, map[uint64]struct{}{1: {}, 2: {}}), "c"); got.len() != 1 {
		t.Fatalf("kept %d, want 1", got.len())
	}
}
