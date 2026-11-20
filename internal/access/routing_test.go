package access

import (
	"testing"
	"time"
)

// justforlxz on X: a gateway key can set how the routing groups it asks
// for route its requests. routing-key sets it, Authenticate tells it,
// "follow" takes it off, and Manual (a group's own pick) or a word it
// doesn't know is refused with the key left as it was. A key issued under
// it that sets none follows its issuer's, and one that sets its own keeps
// it.
func TestKeyRouting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clockAt(t, time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
	id, secret := addKey(t, "Team")
	if who, _ := Authenticate(secret); who.Routing != "" {
		t.Fatal("a new key has a routing:", who.Routing)
	}
	for in, want := range map[string]string{"rotate": "rotate", "Usage": "usage", "smart": "smart", "free": "free", "fit": "fit", "drain": "drain", "acme/sticky": "acme/sticky", "follow": "", "": ""} {
		if _, err := Update("routing-key", Change{Key: id, Routing: in}); err != nil {
			t.Fatal(in, err)
		}
		if who, ok := Authenticate(secret); !ok || who.Routing != want {
			t.Fatalf("%q: routing %q, want %q", in, who.Routing, want)
		}
	}
	Update("routing-key", Change{Key: id, Routing: "rotate"})
	for _, bad := range []string{"manual", "sideways", "a/b/c"} {
		if _, err := Update("routing-key", Change{Key: id, Routing: bad}); err == nil {
			t.Fatalf("%q was taken", bad)
		}
	}
	if who, _ := Authenticate(secret); who.Routing != "rotate" {
		t.Fatal("a refused word changed the key:", who.Routing)
	}

	if _, err := Update("issuer-key", Change{Key: id, Issues: true}); err != nil {
		t.Fatal(err)
	}
	child, err := IssueKey(Issue{TTL: time.Hour, Issuer: id})
	if err != nil {
		t.Fatal(err)
	}
	if who, _ := Authenticate(child.Key); who.Routing != "rotate" {
		t.Fatal("issued, setting none, doesn't follow its issuer:", who.Routing)
	}
	if _, err := Update("routing-key", Change{Key: child.ID, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	if who, _ := Authenticate(child.Key); who.Routing != "order" {
		t.Fatal("issued, its own lost to its issuer's:", who.Routing)
	}
}
