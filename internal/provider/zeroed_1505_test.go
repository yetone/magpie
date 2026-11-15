package provider

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/steady"
)

// #1505: a Windows machine that went down mid-write left logins.json at
// its full length, 10,783 bytes, every one 0x00, and magpie started with
// no accounts. Its last good generation, kept beside it, is read instead,
// and the user told.
func TestZeroedLoginsReadFromBackup(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	googleSandbox(t, &fakeGoogle{})
	lastgood.Reset()
	t.Cleanup(lastgood.Reset)
	for _, u := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		if err := addGoogleLogin("antigravity", u, "", googleAuth{RefreshToken: "1//" + u, Project: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	p := loginsPath()
	if _, err := os.Stat(lastgood.Bak(p)); err != nil {
		t.Fatalf("no last good generation kept: %v", err)
	}
	// the crash: the file as Zhangwier found it, and magpie starting anew
	if err := os.WriteFile(p, make([]byte, 10783), 0o600); err != nil {
		t.Fatal(err)
	}
	lastLoginsMu.Lock()
	lastLogins, loginsKnown = nil, false
	lastLoginsMu.Unlock()

	users := func() string {
		var us []string
		for _, l := range readLogins() {
			us = append(us, l.User)
		}
		return strings.Join(us, ",")
	}
	// the backup lags one write: it holds a and b, the write of c is the
	// one the crash took
	if got := users(); got != "a@x.com,b@x.com" {
		t.Fatalf("a zeroed logins.json read as %q", got)
	}
	if n := lastgood.Recovered(); len(n) != 1 || n[0].File != "logins.json" || !strings.Contains(n[0].Why, "10783 bytes") {
		t.Fatalf("the user isn't told: %+v", n)
	}
	// the next account added keeps them, and the zeroed file aside
	if err := addGoogleLogin("antigravity", "d@x.com", "", googleAuth{RefreshToken: "1//d", Project: "p"}); err != nil {
		t.Fatal(err)
	}
	if got := users(); got != "a@x.com,b@x.com,d@x.com" {
		t.Fatalf("after an account added: %q", got)
	}
	kept, _ := filepath.Glob(p + ".bad-*")
	if len(kept) != 1 {
		t.Fatalf("the zeroed file not kept aside: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); !bytes.Equal(b, make([]byte, 10783)) {
		t.Fatalf("kept %d bytes", len(b))
	}
}

// With no backup, a zeroed logins.json is copied aside before an account
// added writes over it.
func TestZeroedLoginsWithoutBackupKeptAside(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	googleSandbox(t, &fakeGoogle{})
	lastgood.Reset()
	t.Cleanup(lastgood.Reset)
	p := loginsPath()
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, make([]byte, 10783), 0o600)
	lastLoginsMu.Lock()
	lastLogins, loginsKnown = nil, false
	lastLoginsMu.Unlock()
	if err := addGoogleLogin("antigravity", "new@x.com", "", googleAuth{RefreshToken: "1//n", Project: "p"}); err != nil {
		t.Fatal(err)
	}
	if kept, _ := filepath.Glob(p + ".bad-*"); len(kept) != 1 {
		t.Fatalf("the zeroed file not kept aside: %v", kept)
	}
	if _, err := os.Stat(lastgood.Bak(p)); err == nil {
		t.Fatal("a zeroed file kept as the last good generation")
	}
}

// migrations.json says which subscriptions moved onto their plugins; one
// zeroed by the crash is not "nothing moved".
func TestZeroedMigrationsReadFromBackup(t *testing.T) {
	googleSandbox(t, &fakeGoogle{})
	lastgood.Reset()
	t.Cleanup(lastgood.Reset)
	for _, id := range []string{"zed", "kiro"} {
		if err := setMigration(id, func(m *Migration) { m.State = MovePlugin }); err != nil {
			t.Fatal(err)
		}
	}
	p := migrationsPath()
	os.WriteFile(p, make([]byte, 4096), 0o600)
	if !Moved("zed") {
		t.Fatal("a zeroed migrations.json read as zed not moved")
	}
	if len(lastgood.Recovered()) != 1 {
		t.Fatalf("not noted: %+v", lastgood.Recovered())
	}
}

// providers.json zeroed: its providers are its last good generation's.
func TestZeroedProvidersReadFromBackup(t *testing.T) {
	providerFileHome(t)
	lastgood.Reset()
	t.Cleanup(lastgood.Reset)
	writeProviderFile(t, originalProviderFile)
	if err := Save(Provider{ID: "new", Name: "New", Chat: "https://new.example.invalid/v1", Key: "synthetic-new"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(Path(), make([]byte, 2048), 0o600)
	if err := FileError(); err != nil {
		t.Fatalf("a zeroed providers.json with a backup: %v", err)
	}
	ps := load().Providers
	if len(ps) != 1 || ps[0].ID != "original" {
		t.Fatalf("providers = %+v, want the backup's", ps)
	}
}

// writePrivate, which writes logins.json, migrations.json and
// providers.json, flushes the new file before the rename; one that fails
// to flush leaves the old file.
func TestWritePrivateSyncsBeforeRename(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logins.json")
	os.WriteFile(p, []byte(`["old"]`), 0o600)
	defer func() { steady.Sync = (*os.File).Sync }()
	steady.Sync = func(*os.File) error { return errors.New("the disk went away") }
	if err := writePrivate(p, []byte(`["new"]`)); err == nil {
		t.Fatal("written without a flush")
	}
	if b, _ := os.ReadFile(p); string(b) != `["old"]` {
		t.Fatalf("a failed flush left %q", b)
	}
}
