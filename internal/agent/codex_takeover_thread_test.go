package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nb5p on Discord: with the ChatGPT account Codex is signed in to used
// up, magpie became Codex's provider (the takeover), and the turn the
// user sent then started a thread on magpie. When the account came back
// Codex went back to OpenAI, but the thread, written to that minute, was
// left on magpie for a later Sync, and none came: the Codex app couldn't
// go on with it on OpenAI, Retry failed the same way every time and its
// model read "model unavailable". bringBack asks for that Sync once the
// thread is quiet, and it moves the thread where Codex is.
func TestCodexTakeoverThreadComesBackOnceQuiet(t *testing.T) {
	const id = "019a0000-0000-7000-8000-0000000000b1"
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	dir := filepath.Join(home, ".codex")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	asked := make(chan struct{}, 4)
	oldAgain, oldWait := codexThreadsAgain, codexThreadsWait
	codexThreadsAgain = func() { asked <- struct{}{} }
	codexThreadsWait = 10 * time.Millisecond
	t.Cleanup(func() {
		codexThreadsAgain, codexThreadsWait = oldAgain, oldWait
		codexUsedUp = func() bool { return false }
	})

	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("used up, magpie not Codex's provider:\n%s", cfg)
	}
	// the turn sent during the takeover, its rollout written just now
	path := codexThreadOn(t, dir, id, "magpie")
	now := time.Now()
	os.Chtimes(path, now, now)

	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") {
		t.Fatalf("allowance back, magpie still Codex's provider:\n%s", cfg)
	}
	if p := codexRowOn(t, dir, id); p != "magpie" {
		t.Fatalf("a thread being written to was moved: %q", p)
	}
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the thread left on magpie as it was written to: no later Sync asked for")
	}
	// that Sync, the thread quiet by then
	old := now.Add(-2 * time.Minute)
	os.Chtimes(path, old, old)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := codexRowOn(t, dir, id); p != "openai" {
		t.Fatalf("thread on %q after the later Sync, want openai, where Codex lists it", p)
	}
	select {
	case <-asked:
		t.Fatal("another Sync asked for with nothing left on magpie")
	case <-time.After(50 * time.Millisecond):
	}
}
