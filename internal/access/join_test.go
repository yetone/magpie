package access

import (
	"os"
	"strings"
	"testing"
)

func init() { joinIter = 1000 }

func TestJoinPassword(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if JoinOn() || JoinPasswordOK("") || JoinPasswordOK("anything") {
		t.Fatal("open with no password")
	}
	if err := SetJoinPassword("short"); err == nil {
		t.Fatal("a 5-character password was taken")
	}
	if err := SetJoinPassword("long enough pw"); err != nil {
		t.Fatal(err)
	}
	if !JoinOn() || !JoinPasswordOK("long enough pw") || JoinPasswordOK("long enough pW") || JoinPasswordOK("") {
		t.Fatal("password check")
	}
	b, err := os.ReadFile(JoinPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || strings.Contains(string(b), "long enough") {
		t.Fatalf("join.json holds the password: %s", b)
	}
	if fi, _ := os.Stat(JoinPath()); fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("mode %v", fi.Mode())
	}
	// a file that can't be parsed shuts the door
	if err := os.WriteFile(JoinPath(), []byte(`{"salt":"","hash":"","iter":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if JoinOn() || JoinPasswordOK("long enough pw") || JoinPasswordOK("") {
		t.Fatal("a broken join.json let a password in")
	}
	if err := SetJoinPassword("long enough pw"); err != nil {
		t.Fatal(err)
	}
	if err := SetJoinPassword(""); err != nil {
		t.Fatal(err)
	}
	if JoinOn() || JoinPasswordOK("long enough pw") {
		t.Fatal("still open after clearing")
	}
}
