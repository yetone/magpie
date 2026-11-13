package davsync

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A sync setup copied as text (WJaunita45604 on X) opens on another computer
// with the same passphrase, into the same server, password and choices. The
// text holds neither the password nor the passphrase readable: base64 alone
// would hand the server to anyone who saw it.
func TestSetupTextOpensWithItsPassphrase(t *testing.T) {
	for _, c := range []Config{
		{URL: "https://dav.jianguoyun.com/dav/", User: "me@example.com", Password: "dav-app-password-9z", Passphrase: "correct horse", Keys: true, Agents: true, Usage: true},
		{URL: "s3://bucket/team", User: "AKIAEXAMPLE", Password: "s3-secret-Qx7", Passphrase: "correct horse", Endpoint: "https://acc.r2.cloudflarestorage.com", Region: "auto", PathStyle: true, Library: new(bool), SignIns: true},
		{URL: "github://me/backup/magpie", Password: "github_pat_11ABCDEF", Passphrase: "correct horse", Branch: "sync", Memory: new(bool)},
	} {
		newComputer(t).use(t)
		writeSetup(t, c)
		text, err := SetupText()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(text, SetupPrefix) || strings.ContainsAny(text, " \n") {
			t.Fatalf("not one line with its prefix: %q", text)
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(text, SetupPrefix))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{c.Password, c.Passphrase, c.User, "jianguoyun", "bucket", "github"} {
			if secret != "" && (strings.Contains(text, secret) || strings.Contains(string(raw), secret)) {
				t.Fatalf("%q is readable in the text", secret)
			}
		}

		// another computer: a chat wrapped the line, and text came before it
		newComputer(t).use(t)
		wrapped := "here it is: " + text[:40] + "\n" + text[40:90] + " \r\n" + text[90:] + "\n"
		if _, err := OpenSetupText(wrapped, "wrong horse"); !errors.Is(err, ErrSetupPassphrase) {
			t.Fatalf("a wrong passphrase: %v", err)
		}
		s, err := OpenSetupText(wrapped, c.Passphrase)
		if err != nil {
			t.Fatal(err)
		}
		want := Setup{URL: c.URL, User: c.User, Password: c.Password, Endpoint: c.Endpoint, Region: c.Region, PathStyle: c.PathStyle,
			Branch: c.Branch, Keys: c.Keys, Agents: c.Agents, Library: c.library(), Memory: c.memoryOn(), Usage: c.Usage, SignIns: c.SignIns}
		if s != want {
			t.Fatalf("opened %+v, want %+v", s, want)
		}
	}
}

func TestSetupTextNeedsSyncAndItsOwnText(t *testing.T) {
	newComputer(t).use(t)
	if _, err := SetupText(); !errors.Is(err, ErrSetupOff) {
		t.Fatalf("sync off: %v", err)
	}
	for _, s := range []string{"", "magpie-sync:", "magpie-sync:!!!", "magpie-sync:aGVsbG8", "https://dav.example.com"} {
		if _, err := OpenSetupText(s, "x"); !errors.Is(err, ErrNotSetup) {
			t.Fatalf("%q: %v", s, err)
		}
	}
}

func writeSetup(t *testing.T, c Config) {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path("sync.json")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path("sync.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}
