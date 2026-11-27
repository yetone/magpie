package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// askWith runs askRisk(id) with answer typed in, and returns what it
// printed and its error.
func askWith(t *testing.T, id, answer string) (string, error) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	go func() { io.WriteString(inW, answer); inW.Close() }()
	got := make(chan string)
	go func() { b, _ := io.ReadAll(outR); got <- string(b) }()
	err = askRisk(id)
	outW.Close()
	inR.Close()
	return <-got, err
}

// agy-cli's sign-in from the CLI (`magpie accounts add agy-cli`, `magpie
// plugin login agy-cli`) says Antigravity's risk and asks first, as the
// window's risk step does (WdBlink_ on X: an agy-cli account was suspended
// after use from Claude Code).
func TestAgyCLISignInSaysTheRiskFirst(t *testing.T) {
	out, err := askWith(t, "agy-cli", "n\n")
	if err == nil {
		t.Fatal("a no went on to sign in")
	}
	if !strings.Contains(out, provider.AntigravityRisk) {
		t.Fatalf("the risk isn't said: %q", out)
	}
	for _, word := range []string{"magpie", "safe"} {
		if strings.Contains(strings.ToLower(provider.AntigravityRisk), word) {
			t.Fatalf("the risk says %q: %s", word, provider.AntigravityRisk)
		}
	}
	if _, err := askWith(t, "agy-cli", "y\n"); err != nil {
		t.Fatalf("a yes was refused: %v", err)
	}
	// a subscription with no risk is asked nothing
	if out, err := askWith(t, "openai", ""); err != nil || out != "" {
		t.Fatalf("openai asked: %q, %v", out, err)
	}
}
