package access

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// young02036 on Discord: a gateway key gets a cap on its requests in
// flight, as a provider has. concurrency-key sets it, Authenticate tells
// it, 0 takes it off, a value outside 0..1000 is refused and the key is
// left as it was, and an issued key is told its issuer's cap too.
func TestKeyConcurrency(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clockAt(t, time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	id, secret := addKey(t, "Team")
	if who, _ := Authenticate(secret); who.MaxConcurrency != 0 {
		t.Fatal("a new key has a cap:", who.MaxConcurrency)
	}
	if _, err := Update("concurrency-key", Change{Key: id, MaxConcurrency: 3}); err != nil {
		t.Fatal(err)
	}
	if who, ok := Authenticate(secret); !ok || who.MaxConcurrency != 3 {
		t.Fatal("set 3:", who.MaxConcurrency, ok)
	}
	keys, _ := List()
	if keys[0].MaxConcurrency != 3 {
		t.Fatal("listed:", keys[0].MaxConcurrency)
	}
	for _, bad := range []int{-1, 1001} {
		if _, err := Update("concurrency-key", Change{Key: id, MaxConcurrency: bad}); err == nil || !strings.Contains(err.Error(), "0 to 1000") {
			t.Fatal(bad, err)
		}
	}
	if who, _ := Authenticate(secret); who.MaxConcurrency != 3 {
		t.Fatal("a refused value changed the key:", who.MaxConcurrency)
	}

	// issued under it: its own cap, and its issuer's
	if _, err := Update("issuer-key", Change{Key: id, Issues: true}); err != nil {
		t.Fatal(err)
	}
	child, err := IssueKey(Issue{TTL: time.Hour, Issuer: id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update("concurrency-key", Change{Key: child.ID, MaxConcurrency: 5}); err != nil {
		t.Fatal(err)
	}
	who, ok := Authenticate(child.Key)
	if !ok || who.MaxConcurrency != 5 || who.Issuer == nil || who.Issuer.MaxConcurrency != 3 {
		t.Fatal("issued:", who, ok)
	}

	if _, err := Update("concurrency-key", Change{Key: id}); err != nil {
		t.Fatal(err)
	}
	if who, _ := Authenticate(secret); who.MaxConcurrency != 0 {
		t.Fatal("0 left a cap:", who.MaxConcurrency)
	}

	// the sharing key takes a cap without its settings mirror being
	// written again
	s := settings.Load()
	s.LANKeyID = id
	s.LANKey = secret
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("concurrency-key", Change{Key: id, MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.LANKey != secret || got.LANKeyID != id {
		t.Fatal("the mirror was rewritten:", got.LANKeyID)
	}
	if who, _ := Authenticate(secret); who.MaxConcurrency != 2 || !who.Default {
		t.Fatal("the sharing key:", who.MaxConcurrency, who.Default)
	}
}

// y0ngb1n on X: a key's masking setting is "", on or off; redact-key sets
// it, Authenticate tells it, a word it doesn't know is refused and leaves
// it as it was, and an issued key is held on by its issuer's on.
func TestKeyRedact(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clockAt(t, time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC))
	id, secret := addKey(t, "Team")
	if who, _ := Authenticate(secret); who.Redact != "" {
		t.Fatal("a new key has a setting:", who.Redact)
	}
	for in, want := range map[string]string{"off": RedactOff, "ON": RedactOn, "follow": "", "off ": RedactOff} {
		if _, err := Update("redact-key", Change{Key: id, Redact: in}); err != nil {
			t.Fatal(in, err)
		}
		if who, ok := Authenticate(secret); !ok || who.Redact != want {
			t.Fatalf("set %q: %q %v", in, who.Redact, ok)
		}
	}
	if _, err := Update("redact-key", Change{Key: id, Redact: "off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("redact-key", Change{Key: id, Redact: "sometimes"}); err == nil {
		t.Fatal("accepted sometimes")
	}
	if keys, _ := List(); keys[0].Redact != RedactOff {
		t.Fatal("a refused word changed it:", keys[0].Redact)
	}

	// issued: its own off is told, but its issuer's on holds
	if _, err := Update("issuer-key", Change{Key: id, Issues: true}); err != nil {
		t.Fatal(err)
	}
	child, err := IssueKey(Issue{TTL: time.Hour, Issuer: id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update("redact-key", Change{Key: child.ID, Redact: "off"}); err != nil {
		t.Fatal(err)
	}
	if who, _ := Authenticate(child.Key); who.Redact != RedactOff {
		t.Fatal("issued under off:", who.Redact)
	}
	if _, err := Update("redact-key", Change{Key: id, Redact: "on"}); err != nil {
		t.Fatal(err)
	}
	if who, _ := Authenticate(child.Key); who.Redact != RedactOn {
		t.Fatal("issued under on, its own off:", who.Redact)
	}
}
