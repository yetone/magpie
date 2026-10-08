package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// TestDevinSignedInOnlyInMagpie is 蓝猫's Devin on Discord: the CLI kept a
// sign-in Devin no longer took, so magpie signed the account in in a home
// of its own. Every request failed until `devin auth login`: the model
// list, which turns a family (claude-sonnet-5-5) into the variant Devin's
// API takes, was asked of the CLI's own sign-in only. And once the CLI
// signed in to the same account, it was listed twice.
func TestDevinSignedInOnlyInMagpie(t *testing.T) {
	home := claudeHome(t)
	data := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", data)
	os.MkdirAll(filepath.Join(data, "devin"), 0o700)
	cliSignIn := func(key string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(data, "devin", "credentials.toml"), devinCredentials(key, "", "", ""), 0o600); err != nil {
			t.Fatal(err)
		}
		forgetDevinStatus()
	}

	// a CLI that answers for the key in its data folder's credentials.toml:
	// an old account's, one Devin refused, or the account signed in (by
	// magpie, or by `devin auth login` after)
	exe := filepath.Join(home, "devin")
	testenv.Program(t, exe, `#!/bin/sh
f="$XDG_DATA_HOME/devin/credentials.toml"
[ -f "$f" ] || { echo "Not logged in"; exit 1; }
if grep -q refused "$f"; then
  if [ "$1" = models ]; then echo "Authentication required: Invalid token" >&2; exit 1; fi
  `+devinKeyRefused+`
  exit 0
fi
if [ "$1" = models ]; then
  echo '{"families":[{"family_uid":"claude-sonnet-5-5","family_label":"Claude Sonnet 5.5","variants":[{"model_uid":"claude-sonnet-5-5-medium"},{"model_uid":"claude-sonnet-5-5-high"}]}]}'
  exit 0
fi
if grep -q old "$f"; then who=old@example.com; else who=dev@example.com; fi
printf 'Logged in (via Devin).\n\nUser:\n  Email:             %s\n\nAccount:\n  Tier:              Devin Pro\n' "$who"
`)
	fakeDevin(t, exe)
	devinFamiliesCached(nil)
	t.Cleanup(func() { devinFamiliesCached(nil) })

	// the CLI was signed in to another account once, remembered as its own
	cliSignIn("devin-session-token$old")
	if ls := devinLogins(); len(ls) != 1 || ls[0].User != "old@example.com" || ls[0].Home != "" {
		t.Fatalf("CLI's own %+v", ls)
	}
	// then Devin refused its key: nobody is signed in there
	cliSignIn("devin-session-token$refused")
	if ls := devinLogins(); len(ls) != 0 {
		t.Fatalf("refused %+v", ls)
	}

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"sessionToken": "devin-session-token$magpie"})
	}))
	defer fake.Close()
	oldTok := devinExchangeURL
	devinExchangeURL = fake.URL
	t.Cleanup(func() { devinExchangeURL = oldTok })
	st, err := StartSignIn("devin")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "dv-code")
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "dev@example.com" || !st.Using {
		t.Fatalf("state %+v", st)
	}
	ls := devinLogins()
	if len(ls) != 1 || ls[0].User != "dev@example.com" || ls[0].Home == "" {
		t.Fatalf("signed in in magpie %+v", ls)
	}

	// a request for the family goes to Devin as its variant at the effort,
	// read from the account magpie signed in
	if got := DevinVariant(context.Background(), "claude-sonnet-5-5", "high"); got != "claude-sonnet-5-5-high" {
		t.Fatalf("variant %q, want claude-sonnet-5-5-high", got)
	}

	// `devin auth login` to the same account: still one account
	cliSignIn("devin-session-token$cli")
	if ls := Logins("devin"); len(ls) != 1 || ls[0].User != "dev@example.com" || !ls[0].Active {
		t.Fatalf("after the CLI signed in %+v", ls)
	}
	if p, ok := devinAccount(); !ok || p.Account.User != "dev@example.com" || p.AlsoOn() != nil {
		t.Fatalf("account %+v also %+v", p.Account, p.AlsoOn())
	}
}
