package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func behindHome(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", h+"/.config")
	t.Setenv("APPDATA", h+"/AppData")
}

// "右上角的更新按钮不见了" (visamzh, normalcoder_ on X, 11-14): the pill
// waited for this version to be behind for Quiet, counted from the first
// newer version seen. With a release every few minutes that clock started
// over on each update, so a downloaded update waited with no pill for days.
// Being behind lights it at once; only a dismissal holds it, for Quiet.
func TestPillLightsWhenANewerVersionIsOut(t *testing.T) {
	behindHome(t)
	t0 := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	b := NoteBehind("0.1.4174", "0.1.4176", false)
	if !b.Lit("0.1.4174", t0) {
		t.Fatal("not lit when a newer version is out")
	}
	for i := 0; i < 10; i++ {
		b = NoteBehind("0.1.4174", "0.1.4180", false)
	}
	if !b.Lit("0.1.4174", t0.Add(time.Minute)) || !ReadBehind("0.1.4174").Lit("0.1.4174", t0.Add(time.Minute)) {
		t.Error("newer versions seen again put the pill out")
	}
	// a note an older magpie (0.1.3879 to 0.1.4181) kept, "since" the first
	// newer version: it was never a dismissal
	if err := os.MkdirAll(filepath.Dir(behindFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileForTest(behindFile(), `{"base":"0.1.4174","since":"`+t0.Format(time.RFC3339)+`"}`); err != nil {
		t.Fatal(err)
	}
	if !ReadBehind("0.1.4174").Lit("0.1.4174", t0.Add(time.Hour)) {
		t.Error("an old first-seen note held the pill")
	}
}

// A security fix is never held back (the pill lights at once), and stays
// lit until this version has caught up with it.
func TestUrgentLightsThePillAtOnce(t *testing.T) {
	behindHome(t)
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	b := NoteBehind("0.1.100", "0.1.105", true)
	if !b.Lit("0.1.100", t0) {
		t.Fatal("an urgent release did not light the pill")
	}
	b = NoteBehind("0.1.100", "0.1.106", false)
	if !b.Lit("0.1.100", t0.Add(time.Hour)) {
		t.Error("a later plain release put out the urgent one's pill")
	}
	// a dismissal does not hide it either
	if !QuietAgain("0.1.100", t0.Add(2*time.Hour)).Lit("0.1.100", t0.Add(2*time.Hour)) {
		t.Error("a dismissal hid an urgent release")
	}
	if b.Lit("0.1.105", t0) && b.Urgent != "0.1.105" {
		t.Error("urgent not kept")
	}
	if (Behind{Base: "0.1.105", Urgent: "0.1.105", Hushed: t0}).Lit("0.1.105", t0) {
		t.Error("lit for an urgent release already running")
	}
}

// A dismissal holds for the quiet, not only until the next release, and is
// kept across a restart; Show again lifts it; an update starts afresh.
func TestDismissalHoldsForTheQuiet(t *testing.T) {
	behindHome(t)
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	NoteBehind("0.1.100", "0.1.101", false)
	b := QuietAgain("0.1.100", t0)
	NoteBehind("0.1.100", "0.1.102", false)
	if b.Lit("0.1.100", t0.Add(Quiet-time.Minute)) || ReadBehind("0.1.100").Lit("0.1.100", t0.Add(Quiet-time.Minute)) {
		t.Error("lit again before the quiet passed after a dismissal")
	}
	if !ReadBehind("0.1.100").Lit("0.1.100", t0.Add(Quiet)) {
		t.Error("never lit again")
	}
	if !Unhush("0.1.100").Lit("0.1.100", t0.Add(time.Minute)) || !ReadBehind("0.1.100").Lit("0.1.100", t0.Add(time.Minute)) {
		t.Error("Show again did not bring it back")
	}
	QuietAgain("0.1.100", t0)
	if !ReadBehind("0.1.102").Lit("0.1.102", t0.Add(time.Minute)) {
		t.Error("a dismissal on another version counted")
	}
}

// An unreadable note is unknown: a dismissal it held is lost, and the pill
// shows (Settings > Version offers the update either way).
func TestUnreadableBehindNoteIsNone(t *testing.T) {
	behindHome(t)
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	writeBehind(Behind{Base: "0.1.100", Hushed: t0})
	if err := writeFileForTest(behindFile(), "{half"); err != nil {
		t.Fatal(err)
	}
	if b := ReadBehind("0.1.100"); !b.Hushed.IsZero() || b.Urgent != "" {
		t.Errorf("a half-written note read as %+v", b)
	}
}

func TestUrgentIn(t *testing.T) {
	for md, want := range map[string]bool{
		"**Urgent release:** keys leaked":              true,
		"fixes\n\n> **urgent release:** x":             true,
		"  **Urgent release:** x":                      true,
		"an urgent release: not at the line's start":   false,
		"fixed the **Urgent release:** label in notes": false,
		"": false,
	} {
		if UrgentIn(md) != want {
			t.Errorf("UrgentIn(%q) = %v", md, !want)
		}
	}
}

func writeFileForTest(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }
