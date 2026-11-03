package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// WorkBuddy AI, the international build, keeps its sessions as the China
// build does, in ~/.workbuddy-ai: someone signed in to both apps finds each
// one's sessions under its own agent on the Sessions page, its calls on the
// Usage page and its folder among those the reading consent lists
// (maxleebeta on Discord: the international build was missing from the
// agents). The fixture is the China build's own files, as the two builds
// are one app writing to its own folder.
func TestWorkBuddyAI(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	copyTree(t, "testdata/workbuddy", filepath.Join(home, ".workbuddy-ai"))
	Reset()
	ss := List(0)
	if n := count(ss, "workbuddy-ai"); n != 3 {
		t.Fatalf("want 3 WorkBuddy AI sessions, got %d", n)
	}
	// the China build's stay its own, each listed once
	if n := count(ss, "workbuddy"); n != 3 {
		t.Fatalf("want 3 WorkBuddy sessions, got %d", n)
	}
	w := find(t, ss, "workbuddy-ai", wbID)
	if w.Cwd != "/work/wb" || w.Title != "Launch notes draft" || w.Resume != "" {
		t.Fatalf("workbuddy-ai: %+v", w)
	}
	if m := model(w, "hy3"); m.Tokens != (Tokens{2422, 968, 8777, 0, 0}) {
		t.Fatalf("hy3: %+v", m)
	}
	// brought over from its older history: the cwd from its meta file
	if o := find(t, ss, "workbuddy-ai", wbOldID); o.Cwd != "/work/wb-old" {
		t.Fatalf("old: %+v", o)
	}
	if !Deletable("workbuddy-ai") {
		t.Fatal("WorkBuddy AI's sessions are not deletable")
	}
	if !slices.Contains(Found(), "workbuddy-ai") {
		t.Fatalf("the reading consent doesn't list WorkBuddy AI: %v", Found())
	}

	// its calls reach the usage ledger, the one its own models.json names
	// as magpie's gateway left to the gateway, as WorkBuddy's are (its own
	// calls alone: the fixture's message ids are WorkBuddy's, which the
	// ledger counts once over every agent)
	cs := map[string]Call{}
	for _, c := range AgentCalls(time.Time{}, "workbuddy-ai") {
		if c.Agent == "workbuddy-ai" && c.Msg != "" {
			cs[c.Msg] = c
		}
	}
	if m := cs["m1"]; m.Model != "hy3" || m.Tokens != (Tokens{200, 80, 1000, 0, 0}) {
		t.Fatalf("WorkBuddy AI's reply: %+v (%v)", m, cs)
	}
	if _, dup := cs["wm1"]; dup {
		t.Fatalf("a call made to magpie's gateway was read from the file too: %+v", cs["wm1"])
	}
	if _, ok := cs["wm2"]; !ok {
		t.Fatalf("no call for the user's own custom provider: %+v", cs)
	}

	// a ~/.workbuddy-ai that is a link to ~/.workbuddy is read once, as
	// WorkBuddy's
	if err := os.RemoveAll(filepath.Join(home, ".workbuddy-ai")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".workbuddy"), filepath.Join(home, ".workbuddy-ai")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	Reset()
	ss = List(0)
	if n, m := count(ss, "workbuddy-ai"), count(ss, "workbuddy"); n != 0 || m != 3 {
		t.Fatalf("one folder under two names: %d WorkBuddy AI and %d WorkBuddy sessions, want 0 and 3", n, m)
	}
}

// WorkBuddy AI's own models.json says which of its calls went through
// magpie's gateway, not WorkBuddy's: with the China build's model pointed at
// magpie and the international build's same id at the user's own provider,
// the international build's call is its own, read from its file.
func TestWorkBuddyAIGatewayIsItsOwn(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	copyTree(t, "testdata/workbuddy", filepath.Join(home, ".workbuddy-ai"))
	// WorkBuddy's own-proxy/glm-5 is magpie's; WorkBuddy AI's keeps the
	// fixture's models.json, where it is the user's
	viaMagpie := `[{"id": "own-proxy/glm-5", "name": "GLM-5", "vendor": "magpie",
  "apiKey": "magpie-workbuddy", "url": "http://127.0.0.1:3425/v1/chat/completions"}]
`
	if err := os.WriteFile(filepath.Join(home, ".workbuddy", "models.json"), []byte(viaMagpie), 0o600); err != nil {
		t.Fatal(err)
	}
	Reset()
	if !workbuddyGateway("custom-local:own-proxy/glm-5") {
		t.Fatal("WorkBuddy's models.json is not read as naming magpie's gateway")
	}
	if buddyGateway(WorkBuddyAIDir(), "custom-local:own-proxy/glm-5") {
		t.Fatal("WorkBuddy AI's own provider is read as magpie's gateway")
	}
	var ai, wb bool
	for _, c := range Calls(time.Time{}) {
		if c.Msg == "wm2" {
			ai = ai || c.Agent == "workbuddy-ai"
			wb = wb || c.Agent == "workbuddy"
		}
	}
	if !ai || wb {
		t.Fatalf("the call to the user's own provider: read as WorkBuddy AI's %v, as WorkBuddy's %v; want true, false", ai, wb)
	}
}
