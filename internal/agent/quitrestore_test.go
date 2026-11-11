package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const quitUserSettings = "{\n  \"theme\": \"dark\",\n  \"defaultProvider\": \"xai\",\n  \"defaultModel\": \"grok-4.6\"\n}\n"
const quitUserModels = "{\n  \"providers\": {\n    \"mine\": {\n      \"baseUrl\": \"http://127.0.0.1:9/v1\",\n      \"api\": \"openai-completions\",\n      \"apiKey\": \"MINE_KEY\",\n      \"models\": [{\"id\": \"m1\"}]\n    }\n  }\n}\n"

// quitHome is a sandbox with Prime Agent and OmO, each with the user's own
// settings.json and models.json.
func quitHome(t *testing.T) (prime, omoA *Agent, files map[string][]byte) {
	t.Helper()
	home := syncHome(t)
	prime, omoA = primeAgent(home), omo(home)
	files = map[string][]byte{}
	for _, a := range []*Agent{prime, omoA} {
		writeFile(t, a.Path, quitUserSettings)
		writeFile(t, filepath.Join(a.Dir, "models.json"), quitUserModels)
		files[a.Path] = []byte(quitUserSettings)
		files[filepath.Join(a.Dir, "models.json")] = []byte(quitUserModels)
		if err := a.Connect(); err != nil || !a.Wired() {
			t.Fatalf("%s: connect: %v", a.ID, err)
		}
	}
	return
}

func sameBytes(t *testing.T, files map[string][]byte) {
	t.Helper()
	for p, want := range files {
		if b, _ := os.ReadFile(p); !bytes.Equal(b, want) {
			t.Fatalf("%s not as it was:\n%s", p, b)
		}
	}
}

// Quitting with the setting on puts every connected agent's files back as
// they were before magpie, byte for byte, leaves no disconnected mark, and
// the next start connects them again.
func TestRestoreOnQuitAndRewireOnStart(t *testing.T) {
	prime, omoA, files := quitHome(t)
	all := []*Agent{prime, omoA}
	ids, err := RestoreOnQuit(all)
	if err != nil || len(ids) != 2 {
		t.Fatalf("restored %v: %v", ids, err)
	}
	for _, a := range all {
		if a.Wired() || a.Disconnected() {
			t.Fatalf("%s: wired %v, disconnected mark %v", a.ID, a.Wired(), a.Disconnected())
		}
	}
	sameBytes(t, files)
	got, err := RewireOnStart(all)
	if err != nil || len(got) != 2 {
		t.Fatalf("rewired %v: %v", got, err)
	}
	for _, a := range all {
		if !a.Wired() || a.Check() != "" {
			t.Fatalf("%s not connected again:\n%s", a.ID, readFile(a.Path))
		}
	}
	// the record is used once
	if again, _ := RewireOnStart(all); len(again) != 0 {
		t.Fatalf("rewired twice: %v", again)
	}
}

// An agent the user disconnected stays disconnected: a quit doesn't touch
// it, and a start doesn't connect it, nor one they disconnected after the
// quit.
func TestRestoreOnQuitLeavesUserDisconnected(t *testing.T) {
	prime, omoA, files := quitHome(t)
	all := []*Agent{prime, omoA}
	if err := omoA.Disconnect(); err != nil {
		t.Fatal(err)
	}
	ids, err := RestoreOnQuit(all)
	if err != nil || len(ids) != 1 || ids[0] != "prime-agent" {
		t.Fatalf("restored %v: %v", ids, err)
	}
	sameBytes(t, files)
	if got, _ := RewireOnStart(all); len(got) != 1 || got[0] != "prime-agent" {
		t.Fatalf("rewired %v", got)
	}
	if omoA.Wired() || !omoA.Disconnected() {
		t.Fatalf("OmO came back: wired %v, mark %v", omoA.Wired(), omoA.Disconnected())
	}
	// disconnected by hand while magpie was closed
	if _, err := RestoreOnQuit(all); err != nil {
		t.Fatal(err)
	}
	stashMark(prime)
	if got, _ := RewireOnStart(all); len(got) != 0 || prime.Wired() {
		t.Fatalf("rewired %v, wired %v", got, prime.Wired())
	}
}

func stashMark(a *Agent) { stash(map[string]string{a.ID + disconnectedKey: "1"}) }

// With nothing restored on quit, a start connects nothing: the setting's
// off behaviour is that a connected agent keeps magpie.
func TestRewireOnStartWithoutRestore(t *testing.T) {
	prime, omoA, _ := quitHome(t)
	if err := omoA.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if got, _ := RewireOnStart([]*Agent{prime, omoA}); len(got) != 0 || omoA.Wired() || !prime.Wired() {
		t.Fatalf("rewired %v", got)
	}
}
