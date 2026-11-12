package provider

import (
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// An unchanged save leaves logins.json alone (#6289): its mtime stays and
// what filememo parsed of it is not parsed again.
func TestUnchangedLoginsSaveKeepsFileAndParse(t *testing.T) {
	signIn(t)
	if err := writeLogins([]savedLogin{{Agent: "codex", User: "me@example.com", Plan: "pro"}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(loginsPath(), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := filememo.ReadNow("logins", loginsPath(), parseLogins); err != nil {
		t.Fatal(err)
	}
	if err := writeLogins(readLogins()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(loginsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(old) {
		t.Fatalf("an unchanged save moved logins.json's time to %v", fi.ModTime())
	}
	parses := 0
	if _, err := filememo.ReadNow("logins", loginsPath(), func(b []byte) ([]savedLogin, error) {
		parses++
		return parseLogins(b)
	}); err != nil {
		t.Fatal(err)
	}
	if parses != 0 {
		t.Fatalf("an unchanged save made logins.json parse again: %d parses", parses)
	}
	// a real change is still written
	ls := readLogins()
	ls[0].Order = 7
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(loginsPath()); fi.ModTime().Equal(old) {
		t.Fatal("a changed account was not written")
	}
}

// The siblings written through writePrivate hold still too.
func TestWritePrivateSameBytesLeavesFile(t *testing.T) {
	p := t.TempDir() + "/x.json"
	if err := writePrivate(p, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.Chtimes(p, old, old)
	if err := writePrivate(p, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); !fi.ModTime().Equal(old) {
		t.Fatal("the same bytes moved the file's time")
	}
	if err := writePrivate(p, []byte("{\"a\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "{\"a\":1}\n" {
		t.Fatalf("changed bytes not written: %q", b)
	}
}
