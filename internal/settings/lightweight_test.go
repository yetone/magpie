package settings

import "testing"

// Lightweight mode (#580) is off for a user who never chose (the owner,
// 10-21: too slow to have by default), on for one who turned it on, and
// kept off for one who turned it off; it is saved and read back, and is
// this computer's own: a sync or a restored backup keeps it as it is here.
func TestLightweightKeptAndOwn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Load().LightweightOn() {
		t.Fatal("on for a user who never chose")
	}
	off, on := false, true
	s := Load()
	s.Lightweight = &on
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if !Load().LightweightOn() {
		t.Fatal("turned on, but read back off")
	}
	s.Lightweight = &off
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if Load().LightweightOn() {
		t.Fatal("turned off, but read back on")
	}
	s.Theme = "dark" // another setting saved keeps it off
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if Load().LightweightOn() {
		t.Fatal("another setting's save turned it on")
	}
	from := Settings{Theme: "dark", Lightweight: &on}
	from.KeepOwn(Load())
	if from.LightweightOn() || from.Theme != "dark" {
		t.Fatalf("KeepOwn: %+v", from)
	}
	from = Settings{Lightweight: &off}
	from.KeepOwn(Settings{})
	if from.Lightweight != nil {
		t.Fatal("another computer's choice taken here")
	}
}

// Keep awake (xiao_wang24004 on X) is off by default, saved, and this
// computer's own.
func TestKeepAwakeKeptAndOwn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Load().KeepAwake {
		t.Fatal("on by default")
	}
	s := Load()
	s.KeepAwake, s.KeepAwakeDisplay = true, true
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if l := Load(); !l.KeepAwake || !l.KeepAwakeDisplay {
		t.Fatal("not kept")
	}
	from := Settings{}
	from.KeepOwn(Load())
	if !from.KeepAwake || !from.KeepAwakeDisplay {
		t.Fatal("KeepOwn dropped it")
	}
	from = Settings{KeepAwake: true, KeepAwakeDisplay: true}
	from.KeepOwn(Settings{})
	if from.KeepAwake || from.KeepAwakeDisplay {
		t.Fatal("another computer's turned on here")
	}
}

// Whether the main window was maximised is this computer's, like its size:
// a synced or restored bundle doesn't bring another's.
func TestWindowMaximisedKeptAndOwn(t *testing.T) {
	from := Settings{}
	from.KeepOwn(Settings{Window: []int{900, 700}, WindowMaximised: true})
	if !from.WindowMaximised || len(from.Window) != 2 {
		t.Fatalf("KeepOwn dropped it: %v %v", from.Window, from.WindowMaximised)
	}
	from = Settings{Window: []int{1200, 800}, WindowMaximised: true}
	from.KeepOwn(Settings{})
	if from.WindowMaximised || from.Window != nil {
		t.Fatalf("another computer's window here: %v %v", from.Window, from.WindowMaximised)
	}
}

// Detect agents in WSL (#1264) is this computer's own: a sync from a Mac or
// a restored backup leaves it as it is on this Windows box.
func TestNoWSLAgentsOwn(t *testing.T) {
	from := Settings{Theme: "dark"}
	from.KeepOwn(Settings{NoWSLAgents: true})
	if !from.NoWSLAgents || from.Theme != "dark" {
		t.Fatalf("KeepOwn: %+v", from)
	}
	from = Settings{NoWSLAgents: true}
	from.KeepOwn(Settings{})
	if from.NoWSLAgents {
		t.Fatal("another computer's turned off here")
	}
}

// Microsoft 365 HTTPS is this computer's own: its certificate trust and
// loopback listener cannot be carried to another computer by sync or backup.
func TestM365Own(t *testing.T) {
	from := Settings{Theme: "dark"}
	from.KeepOwn(Settings{M365: true, M365KeyID: "local-key"})
	if !from.M365 || from.M365KeyID != "local-key" || from.Theme != "dark" {
		t.Fatalf("KeepOwn: %+v", from)
	}
	from = Settings{M365: true, M365KeyID: "other-key"}
	from.KeepOwn(Settings{})
	if from.M365 || from.M365KeyID != "" {
		t.Fatalf("another computer's Microsoft 365 gateway here: %+v", from)
	}
}
