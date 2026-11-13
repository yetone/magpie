package provider

import (
	"context"
	"testing"
	"time"
)

// A switched-off provider's accounts are still listed for their check-in
// (wxkwxk666 on X), each with its key; a press checks one in and leaves
// the provider off.
func TestOffCheckinsListsSwitchedOffAccounts(t *testing.T) {
	keys := []string{"ck-claim", "ck-done"}
	presses := pluginCheckinSandbox(t, keys...)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if qs := OffCheckins(); len(qs) != 0 {
		t.Fatalf("with nothing switched off: %+v", qs)
	}
	id := PluginID("fakeco")
	if err := SetOff(id, true); err != nil {
		t.Fatal(err)
	}
	qs := OffCheckins()
	if len(qs) != len(keys) {
		t.Fatalf("switched off: %d cards, want %d: %+v", len(qs), len(keys), qs)
	}
	if qs[0].CheckinKey == qs[1].CheckinKey {
		t.Fatalf("two accounts, one key: %+v", qs)
	}
	for _, q := range qs {
		if q.Provider != id || q.Name != "FakeCo" || q.User == "" || !q.Checkins || q.CheckinBy != "plugin:fakeco" || q.CheckinKey == "" || q.Windows == nil {
			t.Fatalf("a card: %+v", q)
		}
	}

	done := qs[len(qs)-1]
	rs := CheckInAccount(ctx, done.CheckinBy, done.CheckinKey)
	if len(rs) != 1 || rs[0].Outcome != CheckinDone && rs[0].Outcome != CheckinClaimed {
		t.Fatalf("the press: %+v", rs)
	}
	if p := pressed(t, presses); len(p) != 1 {
		t.Fatalf("pressed %q", p)
	}
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Off {
		t.Fatal("the press switched the provider back on")
	}
	// what came of it is on its card
	for _, q := range OffCheckins() {
		if (q.CheckinKey == done.CheckinKey) != (q.Checkin != nil) {
			t.Fatalf("after the press of %s: %+v", done.CheckinKey, q)
		}
	}

	if err := SetOff(id, false); err != nil {
		t.Fatal(err)
	}
	if qs := OffCheckins(); len(qs) != 0 {
		t.Fatalf("switched on again: %+v", qs)
	}
}
