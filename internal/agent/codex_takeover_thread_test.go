package agent

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filelock"
)

// watchCodexThreads stands in for codexThreadsLater's Sync, looking every
// 10ms, and ends every wait it started when the test ends.
func watchCodexThreads(t *testing.T) <-chan struct{} {
	t.Helper()
	asked := make(chan struct{}, 4)
	oldAgain, oldWait := codexThreadsAgain, codexThreadsWait
	codexThreadsAgain = func() { asked <- struct{}{} }
	codexThreadsWait = 10 * time.Millisecond
	codexThreadsFor.Store(int64(time.Minute))
	t.Cleanup(func() {
		codexThreadsFor.Store(0)
		for end := time.Now().Add(5 * time.Second); ; {
			codexThreadsMu.Lock()
			n := len(codexThreadsPending)
			codexThreadsMu.Unlock()
			if n == 0 {
				break
			}
			if time.Now().After(end) {
				t.Error("a wait for Codex's threads outlived its test")
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		codexThreadsAgain, codexThreadsWait = oldAgain, oldWait
	})
	return asked
}

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
	asked := watchCodexThreads(t)
	t.Cleanup(func() { codexUsedUp = func() bool { return false } })

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
		t.Fatal("a Sync asked for while the thread is still written to")
	case <-time.After(100 * time.Millisecond):
	}
	// quiet now
	old := now.Add(-2 * time.Minute)
	os.Chtimes(path, old, old)
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the thread left on magpie as it was written to: no later Sync asked for")
	}
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

// crypto0xfish on Discord, the Codex app: "Model provider mismatch. This
// chat uses OpenAI, but your current provider is magpie. Switch to OpenAI
// to continue." The app holds the writer lock of every thread it has
// loaded, the chat on screen among them, and a thread's row moves to
// magpie (bringOver) only when no Codex holds it, so the chat open as
// magpie became Codex's provider stayed on OpenAI. Nothing asked for
// another Sync, so it stayed there after the app let it go, and the app
// refused it on every reopen until something else synced, which can be
// hours. bringOver now asks for that Sync once the thread is let go, and
// it moves the thread to magpie, where the app resumes it.
func TestCodexOpenThreadFollowsMagpieOnceLetGo(t *testing.T) {
	const id = "019a0000-0000-7000-8000-0000000000b2"
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	dir := filepath.Join(home, ".codex")
	// a paginated thread of OpenAI's, quiet for a day and packed by
	// Codex, that the Codex app has open (codex-rs rollout writer_lock.rs)
	packThread(t, codexThreadOn(t, dir, id, "openai"))
	db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`ALTER TABLE threads ADD COLUMN history_mode TEXT NOT NULL DEFAULT 'legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE threads SET history_mode = 'paginated' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	locks := filepath.Join(dir, "thread-writer-locks")
	os.MkdirAll(locks, 0o755)
	lf, err := os.OpenFile(filepath.Join(locks, id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := filelock.TryLock(lf); !got {
		t.Fatal("lock:", err)
	}
	letGo := func() { filelock.Unlock(lf); lf.Close() }
	defer func() {
		if lf != nil {
			letGo()
		}
	}()

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
	if p := codexRowOn(t, dir, id); p != "openai" {
		t.Fatalf("a thread a Codex holds open was moved: %q", p)
	}
	select {
	case <-asked:
		t.Fatal("a Sync asked for while the app still holds the thread")
	case <-time.After(100 * time.Millisecond):
	}
	// the app lets it go: the chat closed, or the app quit
	letGo()
	lf = nil
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the thread the app had open stayed on OpenAI: no later Sync asked for")
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := codexRowOn(t, dir, id); p != "magpie" {
		t.Fatalf("thread on %q after the later Sync, want magpie, where the app resumes it", p)
	}
	select {
	case <-asked:
		t.Fatal("another Sync asked for with nothing left on OpenAI")
	case <-time.After(50 * time.Millisecond):
	}
}
