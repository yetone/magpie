package redact

import (
	"strings"
	"testing"
)

// tianyk, #4087: 白名单, a common word, was taken for a name by the local
// model once, and from then on went as its placeholder everywhere, request
// after request (821 replacements in three days, "之前见过"): a value
// masked once is masked again wherever it stands (known.go). The reader
// now knows 白名单 for a word (zhWordNotName, asked of every find), so the
// model's find goes as written and never becomes known. A name masked
// once, where a role said a person was meant, is masked again wherever it
// stands, word-shaped or not: that is what known values are for.
func TestKnownWordTakenForANameGoesAsWritten(t *testing.T) {
	// what this test makes known is let go after it, so no other test
	// meets 白名单 as a known name
	Forget()
	t.Cleanup(Forget)
	f := &fakeModel{values: map[string]string{"白名单": "private_person"}}
	o := Options{Personal: true, Model: f.find}
	if out, n := Mask("开启白名单之后，把要放行的域名填进去。", o); n != 0 {
		t.Fatalf("not left as written to begin with: %q", out)
	}
	// the next request, with the model finding nothing in it
	if out, n := Mask("白名单在设置里，加一行就行。", Options{Personal: true}); n != 0 {
		t.Errorf("as known: %q", out)
	}
	if out, n := Mask("设置里的白名单要加一行。", o); n != 0 {
		t.Errorf("as found again: %q", out)
	}

	// masked where a role says a person is meant, it is masked again
	// in the next request, with no role beside it
	if out, _ := Mask("联系人：白名单，电话见附件", o); strings.Contains(out, "白名单") {
		t.Fatalf("not masked after a role: %q", out)
	}
	if out, n := Mask("白名单说明天到", Options{Personal: true}); n == 0 {
		t.Errorf("a name masked once left as written: %q", out)
	}

	// a name of its own, once masked, is masked again where no word stands
	// in its place: 欧阳娜娜 the reader takes for one (欧阳 a surname's
	// two characters, no word begins with them)
	fr := &fakeModel{values: map[string]string{"欧阳娜娜": "private_person"}}
	or := Options{Personal: true, Model: fr.find}
	if out, _ := Mask("客户欧阳娜娜来电话", or); strings.Contains(out, "欧阳娜娜") {
		t.Fatalf("not masked to begin with: %q", out)
	}
	if out, n := Mask("欧阳娜娜今天到了", Options{Personal: true}); n == 0 {
		t.Errorf("a known name left as written: %q", out)
	}
}
