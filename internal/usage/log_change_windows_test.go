//go:build windows

package usage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Probe the API independently: an implementation returning an empty stamp on
// a supported filesystem must fail, rather than turn the test into a skip.
func requireChangeStamp(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var basic fileBasicInfo
	err = windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic)))
	if err == windows.ERROR_NOT_SUPPORTED || err == windows.ERROR_INVALID_FUNCTION || err == nil && basic.ChangeTime == 0 {
		t.Skip("filesystem does not expose ChangeTime")
	}
	if err != nil {
		t.Fatal(err)
	}
	info, err := statLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stamp := logChangeStamp(info)
	if stamp == "" {
		t.Fatal("ChangeTime is available but the snapshot has no stamp")
	}
	want := time.Unix(basic.ChangeTime/10_000_000-11_644_473_600, basic.ChangeTime%10_000_000*100)
	if got := logChangeTime(info); !got.Equal(want) {
		t.Fatalf("captured ChangeTime %v != FILETIME %v", got, want)
	}
	return stamp
}

func TestWindowsFileBasicInfoLayout(t *testing.T) {
	var basic fileBasicInfo
	if unsafe.Sizeof(basic) != 40 || unsafe.Offsetof(basic.ChangeTime) != 24 || unsafe.Offsetof(basic.FileAttributes) != 32 {
		t.Fatal("FILE_BASIC_INFO ABI mismatch")
	}
}

func TestWindowsChangeTimeFailureUsesFingerprint(t *testing.T) {
	pageHome(t)
	historyLog(t, 3)
	f, err := os.Open(Path())
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	// A closed handle cannot query FILE_BASIC_INFO, but the saved file identity
	// remains valid. No synthetic or stale change time may be returned.
	got := stampLogHandle(f, info)
	if logChangeStamp(got) != "" || !sameLogFile(got, info) {
		t.Fatal("failed query did not preserve fingerprint fallback")
	}
}

func TestLogChangeStatFileHasStamp(t *testing.T) {
	pageHome(t)
	historyLog(t, 3)
	stamp := requireChangeStamp(t, Path())
	if len(stamp) == 0 {
		t.Fatal("empty stamp")
	}
}

func TestLogChangeSnapshotStampImmutable(t *testing.T) {
	pageHome(t)
	historyLog(t, 1034)
	Summarize(All)
	snapshot := readLogSnapshot()
	before := requireChangeStamp(t, Path())
	if got := logChangeStamp(snapshot.info); got != before {
		t.Fatalf("snapshot stamp %q != live stamp %q", got, before)
	}
	statBefore, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(data, []byte(`"in":10`), []byte(`"in":73`), 1)
	if bytes.Equal(edited, data) || len(edited) != len(data) {
		t.Fatal("fixture must change at the same size")
	}
	if err := os.WriteFile(Path(), edited, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(Path(), statBefore.ModTime(), statBefore.ModTime()); err != nil {
		t.Fatal(err)
	}
	// The old snapshot must keep the old stamp even though the file changed.
	if got := logChangeStamp(snapshot.info); got != before {
		t.Fatalf("snapshot stamp mutated: got %q want %q", got, before)
	}
	after := requireChangeStamp(t, Path())
	if after == before {
		t.Fatalf("change time did not move after rewrite: %q", after)
	}
	got, want := Summarize(All), summarize(All, time.Now(), Load(time.Time{}))
	if got.Totals != want.Totals {
		t.Fatalf("summary stale after rewrite: got %+v want %+v", got.Totals, want.Totals)
	}
}

func TestLogChangeSnapshotReuseWithoutFingerprint(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	historyLog(t, 1034)
	first := readLogSnapshot()
	requireChangeStamp(t, Path())
	// Simulate the no-fingerprint cache-hit path: with a live change-time
	// stamp the snapshot must still be reused even though it has no hash.
	logIndex.Lock()
	logIndex.snapshot.hash = ""
	logIndex.Unlock()
	second := readLogSnapshot()
	if second != first {
		t.Fatal("snapshot rebuilt despite a valid change-time stamp")
	}
	if logChangeStamp(second.info) == "" {
		t.Fatal("reused snapshot lost its stamp")
	}
}

func TestLogChangeTrustedAppendReuse(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	historyLog(t, 1034)
	before := readLogSnapshot()
	requireChangeStamp(t, Path())
	Append(Record{Time: time.Now(), Agent: "codex", Provider: "relay", Model: "m", Input: 11})
	// The in-process append recorded both states; the next snapshot must extend
	// the trusted blocks instead of falling back to a hash rebuild.
	logAppends.Lock()
	trusted := logAppends.path == Path() && logAppends.base != nil && logAppends.last != nil && logChangeStamp(logAppends.last) != ""
	logAppends.Unlock()
	if !trusted {
		t.Fatal("append did not record trusted change-time states")
	}
	// The guarantee: the read after a trusted append hashes none of the
	// historical prefix. Since #1357 its snapshot still carries a fingerprint,
	// the old digest continued over the appended lines, so count what goes
	// through logRecordHash instead of looking for an empty hash.
	hash := logRecordHash
	var hashed int64
	logRecordHash = func(path string, n int64) string {
		hashed += n
		return hash(path, n)
	}
	t.Cleanup(func() { logRecordHash = hash })
	after := readLogSnapshot()
	logRecordHash = hash
	if hashed != 0 {
		t.Fatalf("trusted append re-hashed %d bytes of the %d-byte historical prefix", hashed, before.off)
	}
	if after.blocks[0] != before.blocks[0] {
		t.Fatal("in-process append rebuilt the sealed prefix")
	}
	if after.off <= before.off || after.hash != recordHash(Path(), after.off) {
		t.Fatalf("continued fingerprint does not cover the log: off %d -> %d, hash %q", before.off, after.off, after.hash)
	}
	got, want := Summarize(All), summarize(All, time.Now(), Load(time.Time{}))
	if got.Totals != want.Totals {
		t.Fatalf("summary stale after trusted append: got %+v want %+v", got.Totals, want.Totals)
	}
}

func TestLogChangeLongPath(t *testing.T) {
	pageHome(t)
	dir := t.TempDir()
	for len(dir) < 320 {
		dir = filepath.Join(dir, strings.Repeat("nested", 8))
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	historyLog(t, 1034)
	if len(Path()) <= 260 {
		t.Fatal("fixture must exceed the legacy Windows path limit")
	}
	info, err := statLogFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	native, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !sameLogFile(info, native) || info.Size() != native.Size() {
		t.Fatal("long-path stat lost file identity or size")
	}
	requireChangeStamp(t, Path())
	got, want := Summarize(All), summarize(All, time.Now(), Load(time.Time{}))
	if got.Totals != want.Totals || got.Calls != 1034 {
		t.Fatalf("long-path history missing: got %+v want %+v", got.Totals, want.Totals)
	}
}
