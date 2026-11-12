package access

import (
	"strings"
	"testing"
	"time"
)

// ljayic on X: a key can be given the management API, but only a key the
// user made for it, and only by the user.
func TestAdminKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	plainID, plain := addKey(t, "plain")
	if who, ok := Authenticate(plain); !ok || who.Admin != "" {
		t.Fatalf("a new key has Admin %q", who.Admin)
	}
	if _, err := Update("admin-key", Change{Key: plainID, Admin: "sometimes"}); err == nil {
		t.Fatal("an unknown level was taken")
	}
	for in, want := range map[string]string{"local": AdminLocal, "any": AdminAny, "off": ""} {
		if _, err := Update("admin-key", Change{Key: plainID, Admin: in}); err != nil {
			t.Fatal(in, err)
		}
		if who, _ := Authenticate(plain); who.Admin != want {
			t.Errorf("admin %s: Admin %q, want %q", in, who.Admin, want)
		}
	}

	// read-only: refused, and read-only takes the level away
	if _, err := Update("admin-key", Change{Key: plainID, Admin: "any"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("access-key", Change{Key: plainID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if who, _ := Authenticate(plain); who.Admin != "" {
		t.Error("a key made read-only kept Admin")
	}
	if _, err := Update("admin-key", Change{Key: plainID, Admin: "local"}); err == nil {
		t.Error("a read-only key was given Admin")
	}

	// the extension's key
	if _, err := Update("add-key", Change{Name: "ext", Browser: true}); err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	if _, err := Update("admin-key", Change{Key: keys[len(keys)-1].ID, Admin: "local"}); err == nil {
		t.Error("the extension's key was given Admin")
	}

	// an issued key, even when the file says otherwise
	issuerID, _ := addKey(t, "issuer")
	if _, err := Update("admin-key", Change{Key: issuerID, Admin: "any"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("issuer-key", Change{Key: issuerID, Issues: true}); err != nil {
		t.Fatal(err)
	}
	k, err := IssueKey(Issue{Name: "sandbox", TTL: time.Hour, Issuer: issuerID})
	if err != nil {
		t.Fatal(err)
	}
	if who, ok := Authenticate(k.Key); !ok || who.Admin != "" {
		t.Errorf("an issued key has Admin %q", who.Admin)
	}
	keys, _ = List()
	for _, key := range keys {
		if key.Issuer != "" {
			if _, err := Update("admin-key", Change{Key: key.ID, Admin: "any"}); err == nil || !strings.Contains(err.Error(), "issued") {
				t.Errorf("an issued key was given Admin: %v", err)
			}
		}
	}
}
