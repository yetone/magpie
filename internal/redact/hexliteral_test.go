package redact

import (
	"strings"
	"testing"
)

// A number written in hex, 0x and up to 16 hex digits (64 bits), goes as
// written whatever the local model takes it for: an address in reverse-
// engineering notes went as {{SECRET_…}} and the vendor's model lost it
// (TakaNaShi_ShiKi on X), and as an account number it went as
// {{ACCOUNT_…}}. No rule takes one either. A key written in hex is
// longer, and is masked still. Each case starts with nothing known, so
// one label's mask isn't the next one's known value.
func TestHexNumberGoesAsWritten(t *testing.T) {
	all := map[string]bool{}
	for _, c := range Categories {
		all[c.ID] = true
	}
	for _, c := range []struct{ text, value string }{
		{"| `sub_4EA5E0` | `0x004EA5E0` | 解密函数入口 |", "0x004EA5E0"},
		{"DecryptKey: 0x004EA5E0", "0x004EA5E0"},
		{"the token is 0X7FFE0300", "0X7FFE0300"},
		{"mask = 0xFFFFFFFFFFFFFFFF;", "0xFFFFFFFFFFFFFFFF"},
		{"base 0x00007ff6a1b20000 in x64dbg", "0x00007ff6a1b20000"},
	} {
		Forget()
		if out, n := Mask(c.text, Options{Secrets: true, Personal: true, Kinds: all}); n != 0 {
			t.Errorf("%q by the rules: masked %q", c.text, out)
		}
		for _, label := range []string{"secret", "account_number", "private_phone"} {
			Forget()
			f := &fakeModel{values: map[string]string{c.value: label}}
			if out, n := Mask(c.text, Options{Secrets: true, Personal: true, Kinds: all, Model: f.find}); n != 0 {
				t.Errorf("%q as %s: masked %q", c.text, label, out)
			}
		}
	}
	const pk = "0x9b2e1f4c7a3d5e8f0b6c2a4d9e1f3b5c7d0a2e4f6b8c1d3e5f7a9b0c2d4e6f81"
	const k128 = "0x5e8f0b6c2a4d9e1f3b5c7d0a2e4f6b8c"
	for _, c := range []struct{ text, value string }{
		{"私钥 " + pk + " 别外传", pk},
		{"deploy with " + pk, pk},
		{"seed " + k128, k128},
	} {
		Forget()
		f := &fakeModel{values: map[string]string{c.value: "secret"}}
		if out, _ := Mask(c.text, Options{Secrets: true, Personal: true, Model: f.find}); strings.Contains(out, c.value) {
			t.Errorf("%q: left in %q", c.value, out)
		}
	}
}
