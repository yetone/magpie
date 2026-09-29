package provider

// Several Kiro subscriptions. kiro-cli and the Kiro IDE keep one account,
// which magpie only ever reads. Each account magpie signs in gets a home of
// its own holding its kiro-auth-token.json, refreshed there, so each refresh
// token has one holder. logins.json names those homes; Kiro's own account is
// remembered there too, with no home, so it can stand behind another or be
// turned off.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// kiroTokenFile is what an account's home holds, in the IDE's shape.
const kiroTokenFile = "kiro-auth-token.json"

// kiroAccountsDir holds the homes of the Kiro accounts magpie signed in.
func kiroAccountsDir() string { return filepath.Join(filepath.Dir(Path()), "kiro") }

// kiroLegacyPath is where magpie kept the one Kiro account it signed in
// before it kept several (v0.1.374).
func kiroLegacyPath() string { return filepath.Join(filepath.Dir(Path()), kiroTokenFile) }

// kiroLogin is a Kiro account and the home it is signed in in, "" for
// kiro-cli's or the IDE's.
type kiroLogin struct {
	Login
	Home string
}

// kiroOwnUser is who kiro-cli's or the IDE's sign-in is, "" for none; until
// Kiro has said, it is named for what it is.
func kiroOwnUser() string {
	if _, ok := readKiroAt("", ""); !ok {
		return ""
	}
	user, _ := kiroIdentity("", "")
	return firstNonEmpty(user, "Kiro account")
}

// kiroLogins lists the Kiro accounts that are signed in, the first in use
// first, then the rest as they were added.
func kiroLogins() []kiroLogin {
	kiroAdoptLegacy()
	var out []kiroLogin
	for _, l := range sideLogins("kiro", kiroOwnUser(), func(l savedLogin) bool {
		_, ok := readKiroAt("", l.Home)
		return l.Home != "" && ok
	}) {
		out = append(out, kiroLogin{l.Login, l.saved.Home})
	}
	return out
}

// kiroAdoptLegacy moves the account magpie kept alone into a home of its
// own, among the rest, once Kiro says whose it is; until it can, the file
// stays where it was and is tried again a few minutes on.
func kiroAdoptLegacy() {
	kiroAdopt.Lock()
	defer kiroAdopt.Unlock()
	old := kiroLegacyPath()
	if time.Since(kiroAdopt.tried) < 5*time.Minute {
		return
	}
	if _, err := os.Stat(old); err != nil {
		return
	}
	kiroAdopt.tried = time.Now()
	home, err := newKiroHome()
	if err != nil {
		return
	}
	file := filepath.Join(home, kiroTokenFile)
	if err := os.Rename(old, file); err != nil {
		removeKiroHome(home)
		return
	}
	if _, err := addKiroLogin(home); err != nil {
		_ = os.Rename(file, old)
		removeKiroHome(home)
	}
}

var kiroAdopt struct {
	sync.Mutex
	tried time.Time
}

func kiroSide() []sideLogin {
	var out []sideLogin
	for _, k := range kiroLogins() {
		out = append(out, sideLogin{Login: k.Login})
	}
	return out
}

// kiroLoginList is the Kiro accounts as Logins lists them.
func kiroLoginList() []Login { return loginsOf(kiroSide()) }

// switchKiroLogin puts a Kiro account first. kiro-cli's and the IDE's
// sign-in stay as they are: magpie only changes which account its gateway
// uses first.
func switchKiroLogin(user string) error { return switchSideLogin("kiro", user, kiroSide()) }

func setKiroLoginOn(user string, on bool) error {
	return setSideLoginOn("kiro", user, on, kiroSide())
}

// forgetKiroLogin drops an account magpie signed in, with its home.
// kiro-cli's or the IDE's own is signed out there.
func forgetKiroLogin(user string) error {
	return forgetSideLogin("kiro", user, "kiro-cli's or the Kiro IDE's own sign-in; run `kiro-cli logout` or sign out in the IDE", kiroSide(),
		func(l savedLogin) { removeKiroHome(l.Home) })
}

// newKiroHome makes the home a further Kiro account is signed in in.
func newKiroHome() (string, error) {
	home := filepath.Join(kiroAccountsDir(), strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(randomToken(9))))
	return home, os.MkdirAll(home, 0o700)
}

// removeKiroHome deletes a home magpie made, and nothing else.
func removeKiroHome(home string) {
	if rel, err := filepath.Rel(kiroAccountsDir(), home); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		_ = os.RemoveAll(home)
	}
}

// addKiroLogin keeps the account just signed in in home, in use beside the
// others, once Kiro says whose it is. Signed in again, an account keeps
// the newer home.
func addKiroLogin(home string) (string, error) {
	user, plan := askKiroIdentity("", home)
	if user == "" {
		return "", errors.New("signed in, but Kiro didn't say whose account it is; try again")
	}
	kiroSaidNow("", home, user, plan)
	return user, addSideLogin(savedLogin{Agent: "kiro", User: user, Plan: plan, Home: home}, kiroOwnUser(),
		func(l savedLogin) { removeKiroHome(l.Home) })
}

// kiroAlsoOn is the Kiro accounts in use behind the first; with a key
// saved on the provider, that key is all there is.
func kiroAlsoOn(p Provider) []Provider {
	if p.Key != "" {
		return nil
	}
	var out []Provider
	for _, k := range kiroLogins() {
		if k.Active || !k.On {
			continue
		}
		out = append(out, kiroProvider("", kiroAcct(k)))
	}
	return out
}

// kiroLoginQuota is the allowance of one of the Kiro accounts.
func kiroLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	for _, k := range kiroLogins() {
		if strings.EqualFold(k.User, l.User) {
			return kiroQuotaAt(ctx, "", k.Home)
		}
	}
	return SubscriptionQuota{Provider: "kiro", Plan: l.Plan, Windows: []QuotaWindow{}, Error: "not signed in"}
}
