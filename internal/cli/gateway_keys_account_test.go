package cli

import (
	"errors"
	"testing"
)

// `gateway-key limit <id> account --account … --window …` is a limit that
// follows that account's window (ljayic on X); one without either, or
// either on another period, is refused.
func TestParseAccountWindowLimit(t *testing.T) {
	usage := errors.New("usage")
	l, err := parseKeyLimit("account", []string{"--account", "codex/me@example.com", "--window=5h", "--tokens", "2m"}, usage)
	if err != nil || l.Period != "account" || l.Account != "codex/me@example.com" || l.AccountWindow != "5h" || l.Tokens != 2000000 {
		t.Fatalf("%+v %v", l, err)
	}
	for _, args := range [][]string{
		{"--account", "codex/me@example.com", "--tokens", "2m"},
		{"--window", "5h", "--tokens", "2m"},
	} {
		if l, err := parseKeyLimit("account", args, usage); err == nil {
			t.Errorf("%v taken: %+v", args, l)
		}
	}
	if l, err := parseKeyLimit("week", []string{"--window", "5h", "--tokens", "2m"}, usage); err == nil {
		t.Errorf("--window on a week taken: %+v", l)
	}
}
