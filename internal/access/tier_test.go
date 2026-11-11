package access

import (
	"testing"
	"time"
)

// JackyFill on X: a gateway key can be set to a service tier. tier-key sets
// it (from the words people use too), Authenticate tells it, "follow" takes
// it off, and a word it doesn't know is refused with the key left as it was.
func TestKeyServiceTier(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clockAt(t, time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	id, secret := addKey(t, "Team")
	if who, _ := Authenticate(secret); who.ServiceTier != "" {
		t.Fatal("a new key has a tier:", who.ServiceTier)
	}
	for in, want := range map[string]string{"fast": "priority", "Ultra_Fast": "ultrafast", "normal": "default", "priority": "priority", "follow": "", "": ""} {
		if _, err := Update("tier-key", Change{Key: id, ServiceTier: in}); err != nil {
			t.Fatal(in, err)
		}
		if who, ok := Authenticate(secret); !ok || who.ServiceTier != want {
			t.Fatalf("%q: tier %q, want %q", in, who.ServiceTier, want)
		}
	}
	Update("tier-key", Change{Key: id, ServiceTier: "fast"})
	if _, err := Update("tier-key", Change{Key: id, ServiceTier: "turbo"}); err == nil {
		t.Fatal("turbo was taken")
	}
	if who, _ := Authenticate(secret); who.ServiceTier != "priority" {
		t.Fatal("a refused word changed the key:", who.ServiceTier)
	}
}
