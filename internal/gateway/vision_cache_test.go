package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFailedDescriptionIsNotSaved(t *testing.T) {
	s, u := eyed(t)
	u.fail = true
	if code, got := postAs(t, s, "", imagesBody(1)); code != 502 {
		t.Fatalf("failed description: %d %s, want 502", code, got)
	}
	if _, err := os.Stat(s.sightFile); !os.IsNotExist(err) {
		t.Fatalf("a failed description was written: %v", err)
	}

	before := len(u.sent("eye"))
	u.mu.Lock()
	u.fail = false
	u.mu.Unlock()
	restarted := New()
	if code, got := postAs(t, restarted, "", imagesBody(1)); code != 200 {
		t.Fatalf("after the describer recovers: %d %s", code, got)
	}
	if n := len(u.sent("eye")); n != before+1 {
		t.Fatalf("the failure was cached: %d descriptions, want one fresh one after the original %d", n, before)
	}
}

func TestRestartReadsCacheBeforeATruncatedRecord(t *testing.T) {
	s, u := eyed(t)
	if code, got := postAs(t, s, "", imagesBody(1)); code != 200 {
		t.Fatalf("before restart: %d %s", code, got)
	}
	f, err := os.OpenFile(s.sightFile, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"version":1,"key":"truncated`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := New()
	if code, got := postAs(t, restarted, "", imagesBody(1)); code != 200 {
		t.Fatalf("after restart: %d %s", code, got)
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("a truncated tail hid the good records: %d descriptions, want 1", n)
	}
}

func TestTruncatedRecordDoesNotBlockCompaction(t *testing.T) {
	fresh(t)
	s := New()
	key1 := fmt.Sprintf("%064x", 31)
	key2 := fmt.Sprintf("%064x", 32)
	s.saveSight(key1, "before the crash", "probe/eye")
	f, err := os.OpenFile(s.sightFile, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"version":2,"kind":"inline","key":"truncated`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	s.sightDiskLines = sightsCompactAfter
	s.saveSight(key2, "after the crash", "probe/eye")

	records, _, malformed, err := readSavedSights(s.sightFile)
	if err != nil || malformed || len(records) != 2 {
		t.Fatalf("compaction kept a malformed line: %d records, malformed=%v, err=%v", len(records), malformed, err)
	}
	entries, err := os.ReadDir(filepath.Dir(s.sightFile))
	if err != nil {
		t.Fatal(err)
	}
	backedUp := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), filepath.Base(s.sightFile)+".broken-") {
			backedUp = true
		}
	}
	if !backedUp {
		t.Fatal("the malformed file wasn't preserved before compaction")
	}
	restarted := New()
	restarted.sightMu.Lock()
	defer restarted.sightMu.Unlock()
	if restarted.sights[key1] == nil || restarted.sights[key2] == nil {
		t.Fatalf("valid records were lost: key1=%v key2=%v", restarted.sights[key1], restarted.sights[key2])
	}
}

func TestSavedSightsReloadWithinCacheLimit(t *testing.T) {
	fresh(t)
	s := New()
	for i := 1; i <= sightsKept+10; i++ {
		s.saveSight(fmt.Sprintf("%064x", i), fmt.Sprintf("description %d", i), "probe/eye")
	}

	restarted := New()
	restarted.sightMu.Lock()
	defer restarted.sightMu.Unlock()
	if len(restarted.sights) != sightsKept || len(restarted.sightOrder) != sightsKept {
		t.Fatalf("restored %d descriptions and %d order entries, want %d", len(restarted.sights), len(restarted.sightOrder), sightsKept)
	}
	for i := 1; i <= 10; i++ {
		if restarted.sights[fmt.Sprintf("%064x", i)] != nil {
			t.Fatalf("least recently used description %d survived the limit", i)
		}
	}
	last := restarted.sights[fmt.Sprintf("%064x", sightsKept+10)]
	if last == nil || last.text != fmt.Sprintf("description %d", sightsKept+10) || last.by != "probe/eye" {
		t.Fatalf("the newest description wasn't restored: %+v", last)
	}
}

func TestSightFileCompactsWithoutDroppingNewest(t *testing.T) {
	fresh(t)
	s := New()
	for i := 1; i <= sightsKept; i++ {
		s.saveSight(fmt.Sprintf("%064x", i), fmt.Sprintf("description %d", i), "probe/eye")
	}
	s.sightDiskLines = sightsCompactAfter
	extra := fmt.Sprintf("%064x", sightsKept+1)
	s.saveSight(extra, "newest", "probe/eye")

	b, err := os.ReadFile(s.sightFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(string(b), "\n")
	if lines > sightsKept+1 {
		t.Fatalf("compacted file has %d records, want at most %d", lines, sightsKept+1)
	}
	restarted := New()
	restarted.sightMu.Lock()
	defer restarted.sightMu.Unlock()
	if len(restarted.sights) != sightsKept || restarted.sights[extra] == nil || restarted.sights[extra].text != "newest" {
		t.Fatalf("compaction lost the newest description: %d restored, newest=%+v", len(restarted.sights), restarted.sights[extra])
	}
}

func TestURLDescriptionStaysProcessLocal(t *testing.T) {
	s, u := eyed(t)
	body := `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/current.png"}}]}]}`
	if code, got := postAs(t, s, "", body); code != 200 {
		t.Fatalf("before restart: %d %s", code, got)
	}
	if _, err := os.Stat(s.sightFile); !os.IsNotExist(err) {
		t.Fatalf("a URL-keyed description was persisted: %v", err)
	}
	if code, got := postAs(t, New(), "", body); code != 200 {
		t.Fatalf("after restart: %d %s", code, got)
	}
	if n := len(u.sent("eye")); n != 2 {
		t.Fatalf("a mutable URL reused an old description: %d descriptions, want 2", n)
	}
}

func TestURLDescriptionDoesNotLeakThroughCompaction(t *testing.T) {
	s, u := eyed(t)
	body := `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/changing.png"}}]}]}`
	if code, got := postAs(t, s, "", body); code != 200 {
		t.Fatalf("before compaction: %d %s", code, got)
	}
	s.sightDiskLines = sightsCompactAfter
	s.saveSight(fmt.Sprintf("%064x", 42), "inline", "probe/eye")

	restarted := New()
	if code, got := postAs(t, restarted, "", body); code != 200 {
		t.Fatalf("after compaction and restart: %d %s", code, got)
	}
	if n := len(u.sent("eye")); n != 2 {
		t.Fatalf("compaction persisted a URL: %d descriptions, want 2", n)
	}
}

func TestSightCompactionKeepsAnotherServersRecord(t *testing.T) {
	fresh(t)
	a, b := New(), New()
	a.sightDiskLines = sightsCompactAfter
	b.sightDiskLines = sightsCompactAfter
	keyB := fmt.Sprintf("%064x", 51)
	keyA := fmt.Sprintf("%064x", 52)
	b.saveSight(keyB, "from B", "probe/eye")
	a.saveSight(keyA, "from A", "probe/eye")

	restarted := New()
	restarted.sightMu.Lock()
	defer restarted.sightMu.Unlock()
	if got := restarted.sights[keyB]; got == nil || got.text != "from B" {
		t.Fatalf("A's compaction dropped B's completed description: %+v", got)
	}
	if got := restarted.sights[keyA]; got == nil || got.text != "from A" {
		t.Fatalf("the compacting writer's own description is missing: %+v", got)
	}
}

func TestSightWriteFailureKeepsMemoryCache(t *testing.T) {
	s, u := eyed(t)
	if err := os.Mkdir(s.sightFile, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if code, got := postAs(t, s, "", imagesBody(1)); code != 200 {
			t.Fatalf("request %d with a broken cache file: %d %s", i+1, code, got)
		}
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("a disk failure also broke the memory cache: %d descriptions, want 1", n)
	}
}

func TestDuplicateSavedSightUsesNewest(t *testing.T) {
	fresh(t)
	s := New()
	key := fmt.Sprintf("%064x", 77)
	s.saveSight(key, "old", "probe/eye")
	s.saveSight(key, "new", "other/bigeye")
	restarted := New()
	restarted.sightMu.Lock()
	defer restarted.sightMu.Unlock()
	if len(restarted.sights) != 1 || len(restarted.sightOrder) != 1 {
		t.Fatalf("duplicate key restored %d entries and %d order rows", len(restarted.sights), len(restarted.sightOrder))
	}
	if got := restarted.sights[key]; got == nil || got.text != "new" || got.by != "other/bigeye" {
		t.Fatalf("newest duplicate didn't win: %+v", got)
	}
}

func TestCancelWhileSavingKeepsCompletedDescription(t *testing.T) {
	s, u := eyed(t)
	// Hold the writer before the model answers, then cancel after see has
	// published its answered state but before the disk save can proceed.
	sightsSaving.Lock()
	locked := true
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		if locked {
			sightsSaving.Unlock()
		}
	}()
	done := servedOn(s, ctx, "/v1/chat/completions", imagesBody(1))
	key := sightKey("data:image/png;base64,cGljdHVyZSAw")
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.sightMu.Lock()
		e := s.sights[key]
		answered := e != nil && e.answered
		s.sightMu.Unlock()
		if answered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the model never completed its description")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case rec := <-done:
		t.Fatalf("the request returned while its save was blocked: %d %s", rec.Code, rec.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	waitCtx, stopWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	idle := s.waitSightSaves(waitCtx)
	stopWait()
	if idle {
		t.Fatal("the pending-save barrier disappeared while the save was still blocked")
	}
	sightsSaving.Unlock()
	locked = false
	if rec := recorded(t, done); rec.Code < 400 {
		t.Fatalf("canceled request: %d %s, want an error", rec.Code, rec.Body.String())
	}
	if !s.waitSightSaves(context.Background()) {
		t.Fatal("the pending-save barrier didn't clear after the save")
	}
	s.sightMu.Lock()
	e := s.sights[key]
	finished := false
	if e != nil {
		select {
		case <-e.done:
			finished = true
		default:
		}
	}
	s.sightMu.Unlock()
	if !finished {
		t.Fatal("the canceled request returned before its completed description was saved")
	}

	if code, got := postAs(t, New(), "", imagesBody(1)); code != 200 {
		t.Fatalf("after restart: %d %s", code, got)
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("the completed description was lost on cancellation: %d descriptions, want 1", n)
	}
}
