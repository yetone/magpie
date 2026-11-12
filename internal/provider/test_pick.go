package provider

import (
	"context"
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

// TestPick is one account or key a test can be pinned to.
type TestPick struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TestPicks are the accounts or keys of p that are on, to pick one from
// for a test; none when p has fewer than two.
func (p Provider) TestPicks() []TestPick {
	var out []TestPick
	if p.Account != nil {
		for _, a := range p.AccountsOn() {
			out = append(out, TestPick{ID: a.Account.User, Name: p.AccountShown(a.Account.User)})
		}
	} else {
		for _, k := range p.KeyList() {
			if !k.On {
				continue
			}
			name := k.Name
			if name == "" {
				name = k.Masked
			}
			out = append(out, TestPick{ID: k.ID, Name: name})
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// accountNamed is p as its account named ref (a pool's user), on or not
// served by the user's say-so: the user asked for this one.
func (p Provider) accountNamed(ref string) (Provider, bool) {
	for _, a := range p.AccountsOn() {
		if strings.EqualFold(a.Account.User, ref) {
			return a.Provider, true
		}
	}
	return p, false
}

// keyNamed is p using its key on with id ref, whichever models the user
// let it serve: the user asked for this one.
func (p Provider) keyNamed(ref string) (Provider, bool) {
	for _, k := range p.KeysOn() {
		if keyID(k.Key) == ref {
			q := p.WithKey(k)
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
