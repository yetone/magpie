package provider

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// What a group sets wins; what it leaves unset follows the default for
// every group in settings (jiezhengj on Discord), and with that unset too,
// magpie's own: wait for a first token, keep one's place, a minute's
// patience.
func TestGroupDefaultsUnlessItsOwn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	none := Group{}
	if none.Waits() != PatienceDefault*time.Second || none.FirstTokenOr() != 0 || none.Sinks() {
		t.Fatalf("magpie's own: %s %d %v", none.Waits(), none.FirstTokenOr(), none.Sinks())
	}
	if err := settings.SetGroupDefaults(true, 45, 200); err != nil {
		t.Fatal(err)
	}
	if none.Waits() != 200*time.Second || none.FirstTokenOr() != 45 || !none.Sinks() {
		t.Fatalf("default: %s %d %v", none.Waits(), none.FirstTokenOr(), none.Sinks())
	}
	own := Group{Patience: PatienceDefault, FirstToken: FirstTokenWait, NoSink: true}
	if own.Waits() != PatienceDefault*time.Second || own.FirstTokenOr() > 0 || own.Sinks() {
		t.Fatalf("own: %s %d %v", own.Waits(), own.FirstTokenOr(), own.Sinks())
	}
	if err := settings.SetGroupDefaults(false, 0, PatienceOff); err != nil {
		t.Fatal(err)
	}
	if none.Waits() != 0 {
		t.Fatalf("default off: %s", none.Waits())
	}
	if g := (Group{Patience: 120, Sink: true, FirstToken: 30}); g.Waits() != 120*time.Second || !g.Sinks() || g.FirstTokenOr() != 30 {
		t.Fatalf("own over off: %s %v %d", g.Waits(), g.Sinks(), g.FirstTokenOr())
	}
	// a value no setting can hold is refused, and the file kept
	for _, bad := range [][2]int{{-2, 0}, {0, -2}, {0, PatienceLongest + 1}} {
		if err := settings.SetGroupDefaults(false, bad[0], bad[1]); err == nil {
			t.Fatalf("%v saved", bad)
		}
	}
	if none.Waits() != 0 {
		t.Fatalf("after refusals: %s", none.Waits())
	}

	// Wait, a minute and Keeps its place are kept as the group's own
	if err := Save(Provider{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "Own", Members: []string{"a/x"}, Routing: Ordered, FirstToken: FirstTokenWait, Patience: PatienceDefault, NoSink: true}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := FindGroup("group/own")
	if g.FirstToken != FirstTokenWait || g.Patience != PatienceDefault || !g.NoSink {
		t.Fatalf("saved: %+v", g)
	}
	// Sink and NoSink both: Sink
	if err := SaveGroup(Group{ID: "own", Name: "Own", Members: []string{"a/x"}, Routing: Ordered, Sink: true, NoSink: true}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("group/own"); !g.Sink || g.NoSink {
		t.Fatalf("both: %+v", g)
	}
}
