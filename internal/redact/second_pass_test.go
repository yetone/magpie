package redact

import "testing"

// A text masked by one magpie and masked again by another process, which
// has none of the first's made-up names in memory, comes out as it came:
// a user name's made-up name, an address's public-ip- name and the
// placeholders are no values to find. (A made-up key looks like a key, so a
// process that doesn't know it masks it again; what keeps that from
// happening between magpies is MaskedAsHeader, maskedBefore.) (Luxzinw on X).
func TestSecondProcessLeavesStandInsAlone(t *testing.T) {
	Forget()
	t.Cleanup(Forget)
	o := Options{Personal: true, Secrets: true, Shape: true, Kinds: map[string]bool{"ip": true, "username": true, "email": true}}
	msg := "ssh root@47.96.123.45 as /Users/eerttreeeert/work, mail jo@quill.dev"
	first, n := Mask(msg, o)
	if n == 0 || first == msg {
		t.Fatalf("nothing masked: %q", first)
	}
	Forget() // another process
	second, n2 := Mask(first, o)
	if second != first || n2 != 0 {
		t.Errorf("masked again (%d): first %q, second %q", n2, first, second)
	}
}
