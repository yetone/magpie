package masking

import (
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Fingerprint is the same for settings that mask alike and differs for
// any that change what is masked, and names nothing when nothing is.
func TestFingerprint(t *testing.T) {
	base := settings.Settings{Redact: true, RedactWords: []string{"acme"}}
	if Fingerprint(settings.Settings{}) != "" {
		t.Error("a fingerprint with masking off")
	}
	fp := Fingerprint(base)
	if fp == "" || Fingerprint(settings.Settings{Redact: true, RedactWords: []string{"acme"}}) != fp {
		t.Fatalf("settings alike, fingerprints %q and another", fp)
	}
	for name, st := range map[string]settings.Settings{
		"another word":   {Redact: true, RedactWords: []string{"globex"}},
		"personal too":   {Redact: true, RedactPersonal: true, RedactWords: []string{"acme"}},
		"whole words":    {Redact: true, RedactWords: []string{"acme"}, RedactWordsWhole: true},
		"a value let by": {Redact: true, RedactWords: []string{"acme"}, RedactAllow: []string{"x@example.com"}},
	} {
		if Fingerprint(st) == fp {
			t.Errorf("%s: the same fingerprint as the settings it differs from", name)
		}
	}
	if len(fp) != 32 {
		t.Errorf("fingerprint %q, want 32 hex", fp)
	}
}
