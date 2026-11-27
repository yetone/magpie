package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// addAntigravityWith runs `magpie accounts add <name>` with answer typed
// at the risk question, agy-cli's plugin installed or not, and returns
// what was added, what was signed in to and what it printed (#7828).
func addAntigravityWith(t *testing.T, name, answer string, installed bool) (added bool, login string, out string, err error) {
	t.Helper()
	old := agyCLI
	t.Cleanup(func() { agyCLI = old })
	agyCLI.installed = func() bool { return installed }
	agyCLI.add = func(context.Context) error { added = true; return nil }
	agyCLI.login = func(_ context.Context, id, method string) error {
		login = id + ":" + method
		return nil
	}
	inR, inW, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	outR, outW, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	go func() { io.WriteString(inW, answer); inW.Close() }()
	got := make(chan string)
	go func() { b, _ := io.ReadAll(outR); got <- string(b) }()
	err = accountsCmd([]string{"accounts", "add", name})
	outW.Close()
	inR.Close()
	return added, login, <-got, err
}

// The account card sends a personal Google account to `magpie accounts add
// antigravity`, which looked for a plugin of the withdrawn id and failed
// (#7828). It signs in through agy-cli's plugin, adding it first.
func TestAccountsAddAntigravityAddsAgyCLI(t *testing.T) {
	added, login, out, err := addAntigravityWith(t, "antigravity", "y\n", false)
	if err != nil {
		t.Fatal(err)
	}
	if !added || login != provider.AgyCLIID+":" {
		t.Fatalf("added %v, signed in to %q", added, login)
	}
	if !strings.Contains(out, provider.AntigravityRisk) || !strings.Contains(out, provider.AgyCLIPackage) {
		t.Fatalf("printed %q", out)
	}
	// installed already, it is signed in to as it is; "ag" names it too
	added, login, _, err = addAntigravityWith(t, "ag", "y\n", true)
	if err != nil || added || login != provider.AgyCLIID+":" {
		t.Fatalf("installed: added %v, signed in to %q, %v", added, login, err)
	}
}

// A no to the risk adds nothing and signs in to nothing.
func TestAccountsAddAntigravityAsksFirst(t *testing.T) {
	added, login, _, err := addAntigravityWith(t, "antigravity", "n\n", false)
	if err == nil || added || login != "" {
		t.Fatalf("a no: added %v, signed in to %q, %v", added, login, err)
	}
}
