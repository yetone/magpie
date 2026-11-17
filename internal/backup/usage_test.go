package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// callLine is one call as usage.jsonl has it.
func callLine(t *testing.T, at time.Time, rid string, in int) string {
	t.Helper()
	b, err := json.Marshal(usage.Record{Time: at, Agent: "codex", Provider: "openai", Model: "gpt-5", Input: in, Output: 7, Status: 200, RequestID: rid})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeLog(t *testing.T, lines ...string) {
	t.Helper()
	p := usage.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func logLines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func added(r Result) string {
	if r.Usage == nil {
		return "none"
	}
	return fmt.Sprint(*r.Usage)
}

func count(lines []string, line string) int {
	n := 0
	for _, l := range lines {
		if l == line {
			n++
		}
	}
	return n
}

// The usage history goes in a backup, and putting it back on a new
// computer adds the calls it hasn't got, once, however often it is put
// back (JasonLeeForOnly on Discord: the history was lost moving servers).
func TestBackupCarriesUsageHistory(t *testing.T) {
	home(t)
	now := time.Now().UTC().Truncate(time.Second)
	old1 := callLine(t, now.Add(-72*time.Hour), "r1", 100)
	old2 := callLine(t, now.Add(-48*time.Hour), "r2", 200)
	both := callLine(t, now.Add(-24*time.Hour), "r3", 300) // the new computer has it too
	writeLog(t, old1, old2, both, old2)                    // r2 twice, as written twice
	// an account's quota history
	at := now.Add(-time.Hour)
	q, _ := json.Marshal(map[string]map[string][]provider.QuotaPoint{"codex/me@example.com": {"5h": {{At: at, Left: 42}}}})
	if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "quota-history.json"), q, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Usage != nil {
		t.Fatal("a whole collect (a sync's, an automatic backup's) carries no usage")
	}
	if err := WithUsage(&b); err != nil {
		t.Fatal(err)
	}
	if b.Usage == nil || b.Usage.Calls != 4 || len(b.Usage.Log) == 0 || len(b.Usage.Quotas) == 0 {
		t.Fatalf("usage in the backup: %+v", b.Usage)
	}
	sealed, err := Seal(b, "pass")
	if err != nil {
		t.Fatal(err)
	}

	home(t) // the new server
	mine := callLine(t, now, "n1", 9)
	writeLog(t, both, mine)
	opened, err := Open(sealed, "pass")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(opened, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage == nil || *r.Usage != 3 {
		t.Fatalf("calls added: %s, want 3 (r1, r2 twice; r3 is here)", added(r))
	}
	lines := logLines(t)
	for line, want := range map[string]int{old1: 1, old2: 2, both: 1, mine: 1} {
		if got := count(lines, line); got != want {
			t.Errorf("%s: %d times, want %d\n%s", line, got, want, strings.Join(lines, "\n"))
		}
	}
	if len(lines) != 5 {
		t.Fatalf("log has %d lines, want 5", len(lines))
	}
	if got := len(usage.Load(time.Time{})); got != 5 {
		t.Fatalf("Load reads %d calls, want 5", got)
	}
	hist, err := os.ReadFile(filepath.Join(filepath.Dir(provider.Path()), "quota-history.json"))
	if err != nil || !bytes.Contains(hist, []byte("me@example.com")) {
		t.Fatalf("quota history not merged: %s, %v", hist, err)
	}

	// put back again: nothing counted twice
	r, err = Restore(opened, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage == nil || *r.Usage != 0 {
		t.Fatalf("second restore added %s calls", added(r))
	}
	if again := logLines(t); len(again) != 5 {
		t.Fatalf("after a second restore the log has %d lines, want 5", len(again))
	}

	// --no-usage
	writeLog(t, mine)
	if r, err = Restore(opened, Parts{Usage: false}); err != nil || r.Usage != nil {
		t.Fatalf("usage left out: %+v, %v", r, err)
	}
	if got := logLines(t); len(got) != 1 {
		t.Fatalf("usage left out, yet the log has %d lines", len(got))
	}
}

// A backup made before usage went in it is put back as before: the log
// here is left as it is.
func TestOldBackupWithoutUsage(t *testing.T) {
	home(t)
	b, err := Collect(true, "old")
	if err != nil {
		t.Fatal(err)
	}
	// as a magpie before it wrote the file: no "usage" field at all
	sealed, err := Seal(b, "pass")
	if err != nil {
		t.Fatal(err)
	}
	home(t)
	mine := callLine(t, time.Now().UTC(), "n1", 9)
	writeLog(t, mine)
	opened, err := Open(sealed, "pass")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(opened, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage != nil {
		t.Fatalf("an old backup said it added %d calls", *r.Usage)
	}
	if got := logLines(t); len(got) != 1 || got[0] != mine {
		t.Fatalf("log changed: %q", got)
	}
}

// A log here that ends in a line cut short, and lines in the backup that
// aren't calls: each call goes on a line of its own, and nothing else goes.
func TestUsageMergeKeepsLinesWhole(t *testing.T) {
	home(t)
	a := callLine(t, time.Now().UTC().Add(-time.Hour), "a", 1)
	writeLog(t, a, "not json", `{"cut":`)
	b := Bundle{}
	if err := WithUsage(&b); err != nil {
		t.Fatal(err)
	}
	home(t)
	p := usage.Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	mine := callLine(t, time.Now().UTC(), "n", 2)
	if err := os.WriteFile(p, []byte(mine+"\n"+`{"half`), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := restoreUsage(b.Usage)
	if err != nil || n != 1 {
		t.Fatalf("added %d, %v; want 1", n, err)
	}
	lines := logLines(t)
	want := []string{mine, `{"half`, a}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Fatalf("log:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
