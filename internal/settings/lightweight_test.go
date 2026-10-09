package settings

import "testing"

// Lightweight mode (#580) is saved and read back, off by default, and is
// this computer's own: a sync or a restored backup keeps it as it is here.
func TestLightweightKeptAndOwn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Load().Lightweight {
		t.Fatal("on by default")
	}
	s := Load()
	s.Lightweight = true
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if !Load().Lightweight {
		t.Fatal("not kept")
	}
	from := Settings{Theme: "dark"}
	from.KeepOwn(Load())
	if !from.Lightweight || from.Theme != "dark" {
		t.Fatalf("KeepOwn: %+v", from)
	}
	from = Settings{Lightweight: true}
	from.KeepOwn(Settings{})
	if from.Lightweight {
		t.Fatal("another computer's turned on here")
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
