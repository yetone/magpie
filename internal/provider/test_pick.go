package provider

import (
	"context"
	"slices"
	"strings"
)

// A test can be pinned to one of a provider's accounts or keys (tmr11235
// on X): a provider of several is tested with the one routing would send
// the model to, so a model that failed said nothing of the other accounts.
// TestOn asks the one named: a pool account by its user, a key by its id
// (KeyInfo.ID). The result names the account or key that was asked.

type testOnKey struct{}

// TestOn is ctx with Test and TestModels asking ref's account or key
// ("" as routing picks one).
func TestOn(ctx context.Context, ref string) context.Context {
	return context.WithValue(ctx, testOnKey{}, strings.TrimSpace(ref))
}

func testOnRef(ctx context.Context) string {
	r, _ := ctx.Value(testOnKey{}).(string)
	return r
}

// TestPick is one account or key a test can be pinned to; Off, one the
// user turned off, which only such a test is sent to. Note is what the
// window's menu says beside it, "Off" for such a one (a word the window
// translates), so the menu takes a pick as it comes.
type TestPick struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Off  bool   `json:"-"`
	Note string `json:"note,omitempty"`
}

// TestPicks are every account or key of p, on or off, to pick one from
// for a test: those on in routing's order, then those off. An account
// is turned on to take requests at once, so it is tested off first
// (tmr11235 on X: "我想先测试能不能用再开"). None when p has only one,
// and it on.
func (p Provider) TestPicks() []TestPick {
	var out []TestPick
	if p.Account != nil {
		for _, a := range p.testAccounts() {
			out = append(out, TestPick{ID: a.Account.User, Name: p.AccountShown(a.Account.User), Off: a.Off})
		}
	} else {
		for _, k := range p.KeyList() {
			name := k.Name
			if name == "" {
				name = k.Masked
			}
			out = append(out, TestPick{ID: k.ID, Name: name, Off: !k.On})
		}
		// those on first, as routing asks them
		slices.SortStableFunc(out, func(a, b TestPick) int { return cmpBool(a.Off, b.Off) })
	}
	if len(out) < 2 && !slices.ContainsFunc(out, func(k TestPick) bool { return k.Off }) {
		return nil
	}
	for i := range out {
		if out[i].Off {
			out[i].Note = "Off"
		}
	}
	return out
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// testAccount is one of a signed-in provider's accounts a test can be
// sent to: Off when the user turned it off (or paused the agent's own).
type testAccount struct {
	Provider
	Off bool
}

// testAccounts are every account of p (signed in: p.Account), those on
// in routing's order (AccountsOn), then those off. One that is off is
// built here for its test alone: routing reads AlsoOn, which leaves it
// out, so it takes no request, and nothing is written of it.
func (p Provider) testAccounts() []testAccount {
	all := p.alsoOn(true)
	var on []Provider
	for i, q := range all {
		all[i] = withPicks(q, p)
		if !q.Account.off {
			on = append(on, all[i])
		}
	}
	var out []testAccount
	for _, a := range p.accountsOrdered(on) {
		out = append(out, testAccount{Provider: a.Provider})
	}
	listed := func(user string) bool {
		return slices.ContainsFunc(out, func(a testAccount) bool { return strings.EqualFold(a.Account.User, user) })
	}
	if !listed(p.Account.User) {
		// the agent's own, paused while others are on (#263)
		out = append(out, testAccount{Provider: p, Off: true})
	}
	for _, q := range all {
		if q.Account.off && !listed(q.Account.User) {
			out = append(out, testAccount{Provider: q, Off: true})
		}
	}
	return out
}

// accountNamed is p as its account named ref (a pool's user), on or off,
// served by the user's say-so: the user asked for this one.
func (p Provider) accountNamed(ref string) (Provider, bool) {
	for _, a := range p.testAccounts() {
		if strings.EqualFold(a.Account.User, ref) {
			return a.Provider, true
		}
	}
	return p, false
}

// keyNamed is p using its key with id ref, on or off, alone and
// whichever models the user let it serve: the user asked for this one,
// and the test is never moved to another key.
func (p Provider) keyNamed(ref string) (Provider, bool) {
	if p.Key == "" {
		return p, false
	}
	for _, k := range append([]KeyAccount{p.first()}, p.Keys...) {
		if k.Key != "" && keyID(k.Key) == ref {
			k.Off = false
			q := p.WithKey(k)
			// the one key, still named as one of several (testedAccount)
			q.Keys = []KeyAccount{k}
			q.AccountModels = nil
			return q, true
		}
	}
	return p, false
}

// testedAccount is who a request sent with p goes as, for a result to
// name: a pool's account, or the key of a provider that has several.
func (p Provider) testedAccount() string {
	if p.Account != nil {
		return p.Account.User
	}
	if len(p.Keys) == 0 || p.Key == "" {
		return ""
	}
	if p.KeyName != "" {
		return p.KeyName
	}
	return Mask(p.Key)
}
