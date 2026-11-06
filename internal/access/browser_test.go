package access

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The key a magpie from before made for the Magpie Browser extension had
// full access. ScopeBrowser holds it to the extension: the secret is the
// same, so the extension keeps connecting, read-only for a magpie from
// before this one, and it can neither be given full access back nor issue
// keys. A key made for the extension now is so from the start. The sharing
// key is never held so, and a key that isn't there says so.
func TestScopeBrowser(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret, err := Update("add-key", Change{Name: "Magpie Browser"})
	if err != nil {
		t.Fatal(err)
	}
	who, ok := Authenticate(secret)
	if !ok || who.Browser || who.ReadOnly {
		t.Fatal("a full key as made before:", who, ok)
	}
	if _, err := Update("issuer-key", Change{Key: who.KeyID, Issues: true}); err != nil {
		t.Fatal(err)
	}
	if err := ScopeBrowser(who.KeyID); err != nil {
		t.Fatal(err)
	}
	after, ok := Authenticate(secret)
	if !ok || !after.Browser || !after.ReadOnly || after.Issues || after.KeyID != who.KeyID {
		t.Fatal("held to the browser:", after, ok)
	}
	// what a magpie from before reads: read-only
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var old []struct {
		ID       string
		ReadOnly bool `json:"readOnly"`
		Issues   bool `json:"issues"`
	}
	if json.Unmarshal(b, &old) != nil || len(old) != 1 || !old[0].ReadOnly || old[0].Issues {
		t.Fatalf("a magpie from before reads %s", b)
	}
	if err := ScopeBrowser(who.KeyID); err != nil {
		t.Fatal("twice:", err)
	}
	for _, c := range []struct {
		action string
		in     Change
	}{
		{"access-key", Change{Key: who.KeyID}},
		{"access-key", Change{Key: who.KeyID, ReadOnly: true}},
		{"issuer-key", Change{Key: who.KeyID, Issues: true}},
	} {
		if _, err := Update(c.action, c.in); err == nil {
			t.Errorf("%s %+v on the browser key", c.action, c.in)
		}
	}
	if k, _ := Authenticate(secret); !k.Browser || !k.ReadOnly {
		t.Fatal("the browser key let go:", k)
	}

	made, err := Update("add-key", Change{Name: "Magpie Browser 2", Browser: true})
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := Authenticate(made); !ok || !k.Browser || !k.ReadOnly || k.Issues {
		t.Fatal("a browser key made now:", k, ok)
	}

	if err := ScopeBrowser("no-such-key"); !errors.Is(err, ErrNoKey) {
		t.Fatal("a key that isn't there:", err)
	}
	if err := ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	lan := settings.Load().LANKeyID
	if lan == "" {
		t.Fatal("no sharing key")
	}
	if err := ScopeBrowser(lan); err == nil {
		t.Fatal("the sharing key was held to the browser")
	}
	if k, ok := Authenticate(LANSecret()); !ok || k.Browser || k.ReadOnly {
		t.Fatal("the sharing key changed:", k, ok)
	}
}

// Current says whether the key an identity was read from still lets it in:
// not once it is turned off, removed or expired, or once its secret is
// rotated (the old secret no longer does).
func TestCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var secrets []string
	for _, name := range []string{"Off", "Removed", "Rotated", "Kept"} {
		s, err := Update("add-key", Change{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, s)
	}
	var who []Identity
	for _, s := range secrets {
		w, err := Check(s)
		if err != nil {
			t.Fatal(err)
		}
		if !Current(w) {
			t.Fatal("a key that is on isn't current:", w.KeyName)
		}
		who = append(who, w)
	}
	if Current(Identity{KeyID: who[0].KeyID}) {
		t.Fatal("an identity read from no secret is current")
	}
	if _, err := Update("off-key", Change{Key: who[0].KeyID}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("remove-key", Change{Key: who[1].KeyID}); err != nil {
		t.Fatal(err)
	}
	rotated, err := Update("rotate-key", Change{Key: who[2].KeyID})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{false, false, false, true} {
		if got := Current(who[i]); got != want {
			t.Errorf("%s: Current = %v, want %v", who[i].KeyName, got, want)
		}
	}
	if w, err := Check(rotated); err != nil || !Current(w) {
		t.Fatal("the rotated secret:", err)
	}
	if _, err := Update("on-key", Change{Key: who[0].KeyID}); err != nil {
		t.Fatal(err)
	}
	if !Current(who[0]) {
		t.Fatal("a key turned on again isn't current")
	}
}
