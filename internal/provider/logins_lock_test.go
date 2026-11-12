package provider

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// newProcess is what a magpie run from a terminal starts with: nothing
// remembered of when the accounts were last looked at.
func newProcess() {
	loginsMu.Lock()
	loginsSeenAt.Store(0)
	loginsMu.Unlock()
}

var seenAgoCalls int

// seenAgo puts every account's Seen in logins.json back by d, and the
// file's time back an hour, so a write after it shows.
func seenAgo(t *testing.T, d time.Duration) (raw []byte, mod time.Time) {
	t.Helper()
	var ls []map[string]any
	b, err := os.ReadFile(loginsPath())
	if err != nil || json.Unmarshal(b, &ls) != nil || len(ls) == 0 {
		t.Fatalf("logins.json: %v %s", err, b)
	}
	for _, l := range ls {
		l["seen"] = time.Now().Add(-d).UTC().Truncate(time.Second)
	}
	writeFile(t, loginsPath(), ls)
	// a time of its own each call, as another magpie's write has: the
	// same size at the same time would read as the file unchanged
	seenAgoCalls++
	mod = time.Now().Add(-time.Hour - time.Duration(seenAgoCalls)*time.Minute).Truncate(time.Second)
	if err := os.Chtimes(loginsPath(), mod, mod); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(loginsPath())
	return raw, mod
}

func loginsUnchanged(t *testing.T, raw []byte, mod time.Time) bool {
	t.Helper()
	b, err := os.ReadFile(loginsPath())
	fi, ferr := os.Stat(loginsPath())
	if err != nil || ferr != nil {
		t.Fatal(err, ferr)
	}
	return bytes.Equal(b, raw) && fi.ModTime().Equal(mod)
}

// A magpie run from a terminal that only lists accounts (magpie ls,
// providers, sessions, usage) writes nothing to logins.json (#1674,
// FaintGhost): each is a new process, so the 30 seconds between looks
// never held, and every run wrote the file back with Seen moved on, over
// whatever the gateway had written meanwhile.
func TestReadOnlyRunLeavesLogins(t *testing.T) {
	home := signIn(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	noAnthropic(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "own@example.com"}})
	rememberLogins(true)
	if ls := Logins(""); len(ls) < 2 {
		t.Fatalf("listed: %+v", ls)
	}

	raw, mod := seenAgo(t, time.Minute)
	for range 3 {
		newProcess()
		Logins("")
		Logins("codex")
		Logins("claude")
		All()
	}
	if !loginsUnchanged(t, raw, mod) {
		b, _ := os.ReadFile(loginsPath())
		t.Fatalf("a run that only listed the accounts wrote logins.json:\nwas %s\nnow %s", raw, b)
	}

	// seen long enough ago, it is written: the time still moves on
	raw, mod = seenAgo(t, time.Hour)
	newProcess()
	Logins("")
	if loginsUnchanged(t, raw, mod) {
		t.Fatal("an account last seen an hour ago wasn't written as seen now")
	}

	// a change besides the time is written at once: Codex refreshed its own
	raw, mod = seenAgo(t, time.Minute)
	codexSignIn(t, home, "me@example.com", "r-refreshed")
	newProcess()
	Logins("")
	if loginsUnchanged(t, raw, mod) {
		t.Fatal("Codex's refreshed sign-in wasn't saved")
	}
	if b, _ := os.ReadFile(loginsPath()); !bytes.Contains(b, []byte("r-refreshed")) {
		t.Fatalf("logins.json: %s", b)
	}
}

// TestLoginsOtherMagpie is the gateway of TestLoginsKeepAnotherMagpiesWrite:
// it renews the saved account's sign-in, as renewSavedLogin writes it.
func TestLoginsOtherMagpie(t *testing.T) {
	if os.Getenv("MAGPIE_LOGINS_WRITER") == "" {
		t.Skip("run by TestLoginsKeepAnotherMagpiesWrite")
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	found := false
	for i := range ls {
		if ls[i].User == "work@example.com" {
			ls[i].Auth = []byte(strings.ReplaceAll(string(ls[i].Auth), "r-work", "r-rotated"))
			ls[i].Renewed = time.Now().UTC().Truncate(time.Second)
			found = true
		}
	}
	if !found {
		t.Fatalf("no saved work@example.com in %+v", ls)
	}
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
}

// The gateway renews a saved account's sign-in while a magpie run from a
// terminal is between reading logins.json and writing it back (#1674): the
// renewed sign-in stays. Codex's refresh tokens are spent by use, so the
// old one written back would sign the account out.
func TestLoginsKeepAnotherMagpiesWrite(t *testing.T) {
	home := signIn(t)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	codexSignIn(t, home, "me@example.com", "r-me")
	rememberLogins(true)
	if b, _ := os.ReadFile(loginsPath()); !bytes.Contains(b, []byte("r-work")) {
		t.Fatalf("work@example.com not saved: %s", b)
	}

	// Codex refreshed its own sign-in: this magpie has a change to write
	codexSignIn(t, home, "me@example.com", "r-me-2")
	newProcess()
	var other *exec.Cmd
	otherDone := make(chan error, 1)
	beforeLoginsWrite = func() {
		beforeLoginsWrite = func() {}
		other = exec.Command(os.Args[0], "-test.run=^TestLoginsOtherMagpie$", "-test.count=1")
		other.Env = append(os.Environ(), "MAGPIE_LOGINS_WRITER=1")
		var out bytes.Buffer
		other.Stdout, other.Stderr = &out, &out
		if err := other.Start(); err != nil {
			t.Error(err)
			otherDone <- err
			return
		}
		go func() {
			err := other.Wait()
			if err != nil {
				t.Logf("the other magpie: %v\n%s", err, out.Bytes())
			}
			otherDone <- err
		}()
		// the other magpie writes now if it can; with the lock held here
		// it waits for this one's write instead
		select {
		case err := <-otherDone:
			otherDone <- err
		case <-time.After(2 * time.Second):
		}
	}
	t.Cleanup(func() { beforeLoginsWrite = func() {} })
	rememberLogins(false)
	if other == nil {
		t.Fatal("this magpie wrote nothing")
	}
	if err := <-otherDone; err != nil {
		t.Fatal("the other magpie failed")
	}

	b, _ := os.ReadFile(loginsPath())
	if !bytes.Contains(b, []byte("r-rotated")) || bytes.Contains(b, []byte(`r-work"`)) {
		t.Fatalf("the other magpie's renewed sign-in was written over:\n%s", b)
	}
	if !bytes.Contains(b, []byte("r-me-2")) {
		t.Fatalf("this magpie's change was lost:\n%s", b)
	}
}

// A change is made from logins.json as it is on the disk, not as this
// magpie parsed it before another wrote it: the GUI's state holds what
// was read for a second (filememo.Hold), and taking loginsMu looks again.
func TestLoginsReadAfreshUnderLock(t *testing.T) {
	home := signIn(t)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(loginsPath(), old, old)
	release := filememo.Hold()
	defer release()
	readLogins()

	// another magpie's write, which this one's filememo.Forget never hears of
	b, _ := os.ReadFile(loginsPath())
	if err := os.WriteFile(loginsPath(), bytes.ReplaceAll(b, []byte("r-work"), []byte("r-rotated")), 0o600); err != nil {
		t.Fatal(err)
	}
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	if len(ls) != 1 || !bytes.Contains(ls[0].Auth, []byte("r-rotated")) {
		t.Fatalf("read under the lock: %+v", ls)
	}
}

// Writing logins.json only to move when the accounts were last seen
// builds no catalog anew: an idle magpie did each seenEvery, and dsh's
// round alone built ~42 MB of models again for it. A change besides the
// time still has them built anew.
func TestSeenOnlyWriteBuildsNothingAnew(t *testing.T) {
	home := signIn(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	noAnthropic(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "own@example.com"}})
	rememberLogins(true)
	Logins("") // the side accounts (Copilot) found once, written as new

	raw, mod := seenAgo(t, time.Hour)
	stamp := StampNow()
	newProcess()
	Logins("")
	if loginsUnchanged(t, raw, mod) {
		t.Fatal("an account last seen an hour ago wasn't written as seen now")
	}
	if StampNow() != stamp {
		t.Fatalf("a write that moved only Seen had every catalog built anew: %+v %+v", stamp, StampNow())
	}
	if b, _ := os.ReadFile(loginsPath()); bytes.Equal(b, raw) {
		t.Fatal("logins.json kept the old Seen")
	}

	raw, mod = seenAgo(t, time.Minute)
	stamp = StampNow()
	codexSignIn(t, home, "me@example.com", "r-refreshed")
	newProcess()
	Logins("")
	if loginsUnchanged(t, raw, mod) {
		t.Fatal("Codex's refreshed sign-in wasn't saved")
	}
	if StampNow() == stamp {
		t.Fatal("a refreshed sign-in left the catalogs as they were")
	}
}
