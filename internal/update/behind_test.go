package update

import (
	"os"
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

// Releases come every few minutes, so the header's pill (users on X,
// 11-12: "always lit") waits until this version has been behind for Quiet.
func TestPillWaitsForTheQuiet(t *testing.T) {
	behindHome(t)
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	b := NoteBehind("0.1.100", "0.1.101", false, t0)
	if b.Lit("0.1.100", t0.Add(time.Minute)) {
		t.Error("lit a minute after the first newer version")
	}
	// ten newer versions later, the clock still runs from the first
	for i := 2; i < 12; i++ {
		b = NoteBehind("0.1.100", "0.1.1"+string(rune('0'+i%10))+"0", false, t0.Add(time.Duration(i)*time.Hour))
	}
	if b.Lit("0.1.100", t0.Add(Quiet-time.Minute)) {
		t.Error("lit before the quiet passed")
	}
	if !b.Lit("0.1.100", t0.Add(Quiet)) {
		t.Error("not lit once the quiet passed")
	}
	// the clock is kept across a restart without an update
	if !ReadBehind("0.1.100").Lit("0.1.100", t0.Add(Quiet)) {
		t.Error("the note was not kept")
	}
	// updated: the note is for the version it was
	if ReadBehind("0.1.200").Lit("0.1.200", t0.Add(10*Quiet)) {
		t.Error("a note for another version counted")
	}
}

// A security fix is never held back (the pill lights at once), and stays
// lit until this version has caught up with it.
func TestUrgentLightsThePillAtOnce(t *testing.T) {
	behindHome(t)
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	b := NoteBehind("0.1.100", "0.1.105", true, t0)
	if !b.Lit("0.1.100", t0) {
		t.Fatal("an urgent release did not light the pill")
	}
	b = NoteBehind("0.1.100", "0.1.106", false, t0.Add(time.Hour))
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
	if (Behind{Base: "0.1.105", Urgent: "0.1.105", Since: t0}).Lit("0.1.105", t0) {
		t.Error("lit for an urgent release already running")
	}
}

// A dismissal holds for the quiet, not only until the next release.
func TestDismissalStartsTheQuietOver(t *testing.T) {
	behindHome(t)
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	NoteBehind("0.1.100", "0.1.101", false, t0)
	later := t0.Add(Quiet + time.Hour)
	if !ReadBehind("0.1.100").Lit("0.1.100", later) {
		t.Fatal("not lit after the quiet")
	}
	b := QuietAgain("0.1.100", later)
	if b.Lit("0.1.100", later.Add(Quiet-time.Minute)) {
		t.Error("lit again before the quiet passed after a dismissal")
	}
	if !b.Lit("0.1.100", later.Add(Quiet)) {
		t.Error("never lit again")
	}
}

// An unreadable note is unknown: it starts the quiet, it doesn't light.
func TestUnreadableBehindNoteIsNone(t *testing.T) {
	behindHome(t)
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	writeBehind(Behind{Base: "0.1.100", Since: t0})
	if err := writeFileForTest(behindFile(), "{half"); err != nil {
		t.Fatal(err)
	}
	if ReadBehind("0.1.100").Lit("0.1.100", t0.Add(10*Quiet)) {
		t.Error("a half-written note lit the pill")
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
