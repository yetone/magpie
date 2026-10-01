package provider

// Several Devin subscriptions. The devin CLI keeps one account, in its
// credentials.toml; magpie signs the CLI in when it has none, and reads it
// after. Each further account gets a home of magpie's own, laid out as the
// CLI's data folder, so the CLI itself answers for it (`devin auth status`
// and `devin models list` run with that home as their data folder) and the
// CLI's own sign-in is never touched. logins.json names those homes; the
// CLI's own account is remembered there too, with no home, so it can stand
// behind another or be turned off.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
)

// devinAccountsDir holds the homes of the Devin accounts magpie signed in.
func devinAccountsDir() string { return filepath.Join(filepath.Dir(Path()), "devin") }

// devinCredentialsAt is where an account's credentials.toml is: the CLI's
// own for home "", else the one in magpie's home.
func devinCredentialsAt(home string) string {
	if home == "" {
		return DevinCredentialsPath()
	}
	return filepath.Join(home, "devin", "credentials.toml")
}

// devinCommand runs the devin CLI for an account: the CLI's own for home
// "", else with magpie's home as its data folder.
func devinCommand(ctx context.Context, home, path string, args ...string) *exec.Cmd {
	if home == "" {
		return agentCommand(ctx, path, args...)
	}
	cmd := proc.CommandContext(ctx, path, args...)
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch strings.ToUpper(k) {
		case "XDG_DATA_HOME", "APPDATA":
			continue
		}
		env = append(env, e)
	}
	cmd.Env = netproxy.Env(append(env, "XDG_DATA_HOME="+home, "APPDATA="+home))
	return cmd
}

// devinLogin is a Devin account and the home it is signed in in, "" for
// the CLI's own.
type devinLogin struct {
	Login
	Home string
}

// devinLogins lists the Devin accounts that are signed in, the first in
// use first, then the rest as they were added.
func devinLogins() []devinLogin {
	own, plan, ok := devinIdentity()
	if !ok {
		own = ""
	}
	var out []devinLogin
	for _, l := range sideLogins("devin", own, func(l savedLogin) bool {
		_, _, err := DevinAuthAt(l.Home)
		return l.Home != "" && err == nil
	}) {
		if l.saved.own() && l.Plan == "" {
			l.Plan = plan // the CLI's own, by the tier `devin auth status` says, as the plugin names it
		}
		out = append(out, devinLogin{l.Login, l.saved.Home})
	}
	return out
}

func devinSide() []sideLogin {
	var out []sideLogin
	for _, d := range devinLogins() {
		out = append(out, sideLogin{Login: d.Login})
	}
	return out
}

// devinLoginList is the Devin accounts as Logins lists them.
func devinLoginList() []Login { return loginsOf(devinSide()) }

// switchDevinLogin puts a Devin account first. The CLI's own sign-in stays
// as it is: magpie only changes which account its gateway uses first.
func switchDevinLogin(user string) error { return switchSideLogin("devin", user, devinSide()) }

func setDevinLoginOn(user string, on bool) error {
	return setSideLoginOn("devin", user, on, devinSide())
}

// forgetDevinLogin drops an account magpie signed in, with its home. The
// CLI's own is only hidden (forgetSideLogin).
func forgetDevinLogin(user string) error {
	return forgetSideLogin("devin", user, devinSide(),
		func(l savedLogin) { removeDevinHome(l.Home) })
}

// newDevinHome makes the home a further Devin account is signed in in.
func newDevinHome() (string, error) {
	home := filepath.Join(devinAccountsDir(), strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(randomToken(9))))
	return home, os.MkdirAll(filepath.Join(home, "devin"), 0o700)
}

// removeDevinHome deletes a home magpie made, and nothing else.
func removeDevinHome(home string) {
	if rel, err := filepath.Rel(devinAccountsDir(), home); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		_ = os.RemoveAll(home)
	}
}

// addDevinLogin keeps the account just signed in in home, in use beside
// the others, once the CLI says whose it is. Signed in again, an account
// keeps the newer home.
func addDevinLogin(home string) (user, plan string, err error) {
	user, plan, ok := askDevinIdentityAt(home)
	if !ok || user == "" {
		return "", "", errors.New("signed in, but `devin auth status` doesn't show the account; run it in a terminal to see why")
	}
	own, _, ownOK := devinIdentity()
	if !ownOK {
		own = ""
	}
	return user, plan, addSideLogin(savedLogin{Agent: "devin", User: user, Plan: plan, Home: home}, own,
		func(l savedLogin) { removeDevinHome(l.Home) })
}

// devinAlsoOn is the Devin accounts in use behind the first.
func devinAlsoOn() []Provider {
	var out []Provider
	for _, d := range devinLogins() {
		if !d.Active && d.On {
			out = append(out, devinProvider(d))
		}
	}
	return out
}
