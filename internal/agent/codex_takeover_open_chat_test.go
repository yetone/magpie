package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filelock"
)

// milianxinqi on X, the Codex app: "Model provider mismatch. This chat
// uses magpie, but your current provider is OpenAI. Switch to magpie to
// continue." ("用的好好的，codex 突然就弹这个了"). The ChatGPT account ran
// out, magpie became Codex's provider (the takeover) and the chat was
// started on magpie. At the account's reset Sync put Codex back beside
// its sign-in, on OpenAI, while the Codex app kept the app-server the
// takeover started (Restart the Codex app to follow is off by default)
// with the chat loaded and its lock held, so bringBack couldn't move it
// and every send in it was refused. The takeover now waits while a Codex
// holds a chat on magpie, and ends once the app lets it go.
func TestCodexTakeoverWaitsForTheOpenChat(t *testing.T) {
	const id = "019a0000-0000-7000-8000-0000000000c1"
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	dir := filepath.Join(home, ".codex")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	asked := watchCodexThreads(t)
	t.Cleanup(func() { codexUsedUp = func() bool { return false } })

	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("used up, magpie not Codex's provider:\n%s", cfg)
	}
	// the chat started during the takeover, quiet, that the Codex app has
	// loaded (codex-rs rollout writer_lock.rs)
	path := codexThreadOn(t, dir, id, "magpie")
	old := time.Now().Add(-10 * time.Minute)
	os.Chtimes(path, old, old)
	letGo := holdCodexThreadLock(t, dir, id)

	// the reset: the account has room again
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("the account back with a chat on magpie open in the Codex app, Codex was put on OpenAI under it (\"This chat uses magpie, but your current provider is OpenAI\"):\n%s", cfg)
	}
	if p := codexRowOn(t, dir, id); p != "magpie" {
		t.Fatalf("the open chat is on %q, want magpie, the provider Codex is on", p)
	}
	select {
	case <-asked:
		t.Fatal("a Sync asked for while the app still holds the chat")
	case <-time.After(100 * time.Millisecond):
	}

	// the app lets it go: restarted, quit, or the chat closed
	letGo()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the chat let go, no Sync asked for to end the takeover")
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") {
		t.Fatalf("the chat let go, magpie still Codex's provider:\n%s", cfg)
	}
	if p := codexRowOn(t, dir, id); p != "openai" {
		t.Fatalf("the chat is on %q after the takeover ended, want openai, where Codex resumes it", p)
	}
}

// With Restart the Codex app to follow on, magpie restarts the app-server
// for the takeover's end and moves the chats it has open first
// (codexRetagOpenThreads): nothing waits.
func TestCodexTakeoverEndsAtOnceWhenTheAppFollows(t *testing.T) {
	const id = "019a0000-0000-7000-8000-0000000000c2"
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	dir := filepath.Join(home, ".codex")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	watchCodexThreads(t)
	oldFollows := codexAppFollows
	codexAppFollows = func() bool { return true }
	t.Cleanup(func() { codexUsedUp = func() bool { return false }; codexAppFollows = oldFollows })

	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	path := codexThreadOn(t, dir, id, "magpie")
	old := time.Now().Add(-10 * time.Minute)
	os.Chtimes(path, old, old)
	holdCodexThreadLock(t, dir, id)

	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") {
		t.Fatalf("the app restarted for the change, the takeover still waited:\n%s", cfg)
	}
}

// holdCodexThreadLock holds a thread's writer lock as a Codex that has it
// loaded does, till the returned func or the test's end.
func holdCodexThreadLock(t *testing.T, dir, id string) (letGo func()) {
	t.Helper()
	locks := filepath.Join(dir, "thread-writer-locks")
	os.MkdirAll(locks, 0o755)
	lf, err := os.OpenFile(filepath.Join(locks, id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := filelock.TryLock(lf); !got {
		t.Fatal("lock:", err)
	}
	done := false
	letGo = func() {
		if !done {
			done = true
			filelock.Unlock(lf)
			lf.Close()
		}
	}
	t.Cleanup(letGo)
	return letGo
}
