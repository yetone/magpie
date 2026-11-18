package access

import (
	"strings"
	"testing"
	"time"
)

// Niapyaya on X: a key can be let use Magpie Memory from another
// computer, but only a key the user made for it, and only by the user:
// never the sharing key, a read-only or Browser key, nor an issued one.
func TestMemoryKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	plainID, plain := addKey(t, "plain")
	if who, ok := Authenticate(plain); !ok || who.Memory {
		t.Fatal("a new key has Memory")
	}
	for _, on := range []bool{true, false, true} {
		if _, err := Update("memory-key", Change{Key: plainID, Memory: on}); err != nil {
			t.Fatal(on, err)
		}
		if who, _ := Authenticate(plain); who.Memory != on {
			t.Errorf("memory %v: Memory %v", on, who.Memory)
		}
	}

	// read-only takes it away, and is refused it
	if _, err := Update("access-key", Change{Key: plainID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if who, _ := Authenticate(plain); who.Memory {
		t.Error("a key made read-only kept Memory")
	}
	if _, err := Update("memory-key", Change{Key: plainID, Memory: true}); err == nil {
		t.Error("a read-only key was given Memory")
	}

	// the extension's key
	if _, err := Update("add-key", Change{Name: "ext", Browser: true}); err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	if _, err := Update("memory-key", Change{Key: keys[len(keys)-1].ID, Memory: true}); err == nil {
		t.Error("the extension's key was given Memory")
	}

	// the sharing key, given to every agent that reaches magpie
	if err := ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	keys, _ = List()
	for _, k := range keys {
		if k.LAN {
			if _, err := Update("memory-key", Change{Key: k.ID, Memory: true}); err == nil || !strings.Contains(err.Error(), "sharing key") {
				t.Errorf("the sharing key was given Memory: %v", err)
			}
		}
	}
	if who, _ := Authenticate(LANSecret()); who.Memory {
		t.Error("the sharing key has Memory")
	}

	// an issued key, even when the file says otherwise
	issuerID, _ := addKey(t, "issuer")
	if _, err := Update("memory-key", Change{Key: issuerID, Memory: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("issuer-key", Change{Key: issuerID, Issues: true}); err != nil {
		t.Fatal(err)
	}
	k, err := IssueKey(Issue{Name: "sandbox", TTL: time.Hour, Issuer: issuerID})
	if err != nil {
		t.Fatal(err)
	}
	if who, ok := Authenticate(k.Key); !ok || who.Memory {
		t.Error("an issued key has Memory")
	}
	keys, _ = List()
	for _, key := range keys {
		if key.Issuer != "" {
			if _, err := Update("memory-key", Change{Key: key.ID, Memory: true}); err == nil || !strings.Contains(err.Error(), "issued") {
				t.Errorf("an issued key was given Memory: %v", err)
			}
		}
	}
}
