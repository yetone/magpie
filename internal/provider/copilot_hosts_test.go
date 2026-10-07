package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copilotUsers lists the Copilot accounts as "user[*][+]": * first, + on.
func copilotUsers() string {
	var s []string
	for _, l := range Logins("copilot") {
		s = append(s, l.User+map[bool]string{true: "*", false: ""}[l.Active]+map[bool]string{true: "+", false: ""}[l.On])
	}
	return strings.Join(s, " ")
}

// One GitHub login on two enterprises' hosts, and on github.com, is three
// Copilot accounts, each kept, listed and addressed apart (#1220). Before,
// the one signed in last took the place of the one before, and the
// editors' own login signed in at an enterprise was let go, listed nowhere.
func TestCopilotSameLoginOnSeveralHosts(t *testing.T) {
	home := signIn(t)
	// the editors' own: mona on github.com
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "mona", "oauth_token": "gho_dot"},
	})
	for _, a := range []struct{ host, token string }{{"acme.ghe.com", "ghu_acme"}, {"octo.ghe.com", "ghu_octo"}} {
		if err := addCopilotLogin("mona", "Enterprise", a.token, a.host); err != nil {
			t.Fatal(err)
		}
	}
	if got := copilotUsers(); got != "mona*+ mona@acme.ghe.com+ mona@octo.ghe.com+" {
		t.Fatalf("logins: %s", got)
	}
	apps := map[string]copilotApp{}
	for _, c := range copilotLogins(copilotConfigDir()) {
		apps[c.User] = c.app
	}
	for user, want := range map[string]copilotApp{
		"mona":              {User: "mona", Token: "gho_dot"},
		"mona@acme.ghe.com": {User: "mona@acme.ghe.com", Token: "ghu_acme", Host: "acme.ghe.com"},
		"mona@octo.ghe.com": {User: "mona@octo.ghe.com", Token: "ghu_octo", Host: "octo.ghe.com"},
	} {
		if got := apps[user]; got != want {
			t.Errorf("%s: %+v, want %+v", user, got, want)
		}
	}

	// each is addressed by its name: put first, forgotten
	if err := SwitchLogin("copilot", "mona@octo.ghe.com"); err != nil {
		t.Fatal(err)
	}
	if got := copilotUsers(); got != "mona@octo.ghe.com*+ mona+ mona@acme.ghe.com+" {
		t.Fatalf("switched: %s", got)
	}
	if err := ForgetLogin("copilot", "mona@acme.ghe.com"); err != nil {
		t.Fatal(err)
	}
	if got := copilotUsers(); got != "mona@octo.ghe.com*+ mona+" {
		t.Fatalf("forgotten: %s", got)
	}

	// signed in again at its host, an account keeps the newer sign-in, as before
	if err := addCopilotLogin("mona", "Enterprise", "ghu_octo2", "octo.ghe.com"); err != nil {
		t.Fatal(err)
	}
	if got := copilotUsers(); got != "mona@octo.ghe.com*+ mona+" {
		t.Fatalf("signed in again: %s", got)
	}
	for _, c := range copilotLogins(copilotConfigDir()) {
		if c.User == "mona@octo.ghe.com" && c.app.Token != "ghu_octo2" {
			t.Fatalf("signed in again: %+v", c.app)
		}
	}
}

// An enterprise account kept before its name carried the host is listed
// by the name it gets now, with the id it had, and a github.com sign-in
// of the same login no longer takes its place (#1220).
func TestCopilotEnterpriseAccountNamedByHost(t *testing.T) {
	signIn(t) // the editors' own: octocat
	os.MkdirAll(filepath.Dir(loginsPath()), 0o700)
	if err := os.WriteFile(loginsPath(), []byte(`[
  {
    "id": "0123456789abcdef",
    "agent": "copilot",
    "user": "mona",
    "plan": "Enterprise",
    "seen": "2026-10-06T08:00:00Z",
    "on": true,
    "auth": {"host":"acme.ghe.com","oauth_token":"ghu_acme"}
  }
]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := copilotUsers(); got != "octocat*+ mona@acme.ghe.com+" {
		t.Fatalf("logins: %s", got)
	}
	if id := LoginID("copilot", "mona@acme.ghe.com"); id != "0123456789abcdef" {
		t.Fatalf("id: %s", id)
	}
	if err := addCopilotLogin("mona", "Pro", "gho_dot", ""); err != nil {
		t.Fatal(err)
	}
	if got := copilotUsers(); got != "octocat*+ mona+ mona@acme.ghe.com+" {
		t.Fatalf("logins: %s", got)
	}
	for _, c := range copilotLogins(copilotConfigDir()) {
		if c.User == "mona@acme.ghe.com" && (c.app.Token != "ghu_acme" || c.app.Host != "acme.ghe.com") {
			t.Fatalf("the enterprise account: %+v", c.app)
		}
	}
}
