package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

// movedTwo records "zed" moved with two accounts, as forgetting one of
// them finds it.
func movedTwo(t *testing.T) {
	t.Helper()
	claudeHome(t)
	if err := setMigration("zed", func(m *Migration) {
		*m = Migration{State: MovePlugin, Package: "@magpie-community/opencode-zed-auth", At: time.Now().UTC().Truncate(time.Second),
			Accounts: []movedAccount{
				{Key: "zed:a", User: "a@example.com", Was: map[string]any{"type": "oauth", "refresh": "r-a"}},
				{Key: "zed:b", User: "b@example.com"},
			},
			Backup: []savedLogin{
				{Agent: "zed", User: "a@example.com", Auth: json.RawMessage(`{"token":"t-a"}`)},
				{Agent: "zed", User: "b@example.com", Auth: json.RawMessage(`{"token":"t-b"}`)},
			}}
	}); err != nil {
		t.Fatal(err)
	}
}

// forgetA is what forgetPluginAccounts does to the record when a@ is
// signed out.
func forgetA(m *Migration) {
	m.Accounts = slices.DeleteFunc(m.Accounts, func(a movedAccount) bool { return a.User == "a@example.com" })
	m.Backup = slices.DeleteFunc(m.Backup, func(b savedLogin) bool { return b.User == "a@example.com" })
}

// A forget whose write fails changed nothing: what the record is read as
// afterwards is still what migrations.json holds. It was read as b@'s
// account twice over, the second with no key, so moving back handed back
// an account with no sign-in.
func TestFailedMigrationWriteLeavesItAsRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user can't write to")
	}
	movedTwo(t)
	before, _ := MigrationOf("zed")
	if len(before.Accounts) != 2 || len(before.Backup) != 2 {
		t.Fatalf("recorded: %+v", before)
	}
	dir := filepath.Dir(migrationsPath())
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := setMigration("zed", forgetA); err == nil {
		t.Fatal("wrote into a folder it can't write to")
	}
	m, _ := MigrationOf("zed")
	var keys, users []string
	for _, a := range m.Accounts {
		keys = append(keys, a.Key)
	}
	for _, b := range m.Backup {
		users = append(users, b.User)
	}
	if !slices.Equal(keys, []string{"zed:a", "zed:b"}) || !slices.Equal(users, []string{"a@example.com", "b@example.com"}) {
		t.Fatalf("after a forget that wasn't written, read as accounts %q, backup %q", keys, users)
	}
	if m.Accounts[0].Was["refresh"] != "r-a" {
		t.Fatalf("a@'s earlier sign-in read as %v", m.Accounts[0].Was)
	}
}

// Moving back and putting back read the record without the lock a forget
// changes it under: what they read is never changed by it (go test -race).
func TestMigrationReadWhileForgotten(t *testing.T) {
	movedTwo(t)
	MigrationOf("zed")
	var wg sync.WaitGroup
	stop, reading := make(chan struct{}), make(chan struct{})
	read := 0
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			m, _ := MigrationOf("zed")
			if i == 0 {
				close(reading)
			}
			for _, a := range m.Accounts {
				read += len(a.Key)
			}
			for _, b := range m.Backup {
				read += len(b.User)
			}
		}
	}()
	<-reading
	err := setMigration("zed", forgetA)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := MigrationOf("zed"); len(m.Accounts) != 1 || m.Accounts[0].Key != "zed:b" || len(m.Backup) != 1 || m.Backup[0].User != "b@example.com" {
		t.Fatalf("after forgetting a@: %+v", m)
	}
}
