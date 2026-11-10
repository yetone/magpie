package redact

import (
	"strings"
	"testing"
)

// Never mask holds from the next text on (NicoleS76448271 on X): a
// conversation's earlier turns, sent masked, go masked again with the
// same placeholder, so the vendor's cache of them still matches, and the
// turn after the value was let go has it as written. Another route, or a
// request with none, has it as written at once.
func TestAllowKeepsSentTextMasked(t *testing.T) {
	resetSent()
	t.Cleanup(resetSent)
	const mail = "nicole.ops@kelvarn.io"
	o := Options{Secrets: true, Personal: true, Route: "anthropic"}
	turn1 := `{"role":"user","content":"mail ` + mail + ` the report"}`
	body1 := `{"messages":[` + turn1 + `]}`
	first, _ := MaskJSON([]byte(body1), o)
	if strings.Contains(string(first), mail) {
		t.Fatalf("not masked before it was let go: %s", first)
	}
	sent1 := strings.TrimSuffix(strings.TrimPrefix(string(first), `{"messages":[`), `]}`)

	o.Allow = []string{"Nicole.Ops@kelvarn.io"}
	turn2 := `{"role":"user","content":"and cc ` + mail + ` too"}`
	second, _ := MaskJSON([]byte(`{"messages":[`+turn1+`,{"role":"assistant","content":"ok"},`+turn2+`]}`), o)
	if !strings.HasPrefix(string(second), `{"messages":[`+sent1+`,`) {
		t.Fatalf("the turn sent before changed, the cache misses:\nbefore %s\nnow    %s", first, second)
	}
	if !strings.Contains(string(second), `"and cc `+mail+` too"`) {
		t.Fatalf("the turn after Never mask isn't as written: %s", second)
	}

	// another route never had it: as written there
	other := o
	other.Route = "openai"
	if out, _ := MaskJSON([]byte(body1), other); !strings.Contains(string(out), mail) {
		t.Fatalf("masked on a route it never went: %s", out)
	}
	// nor does a request with no route keep it (a search's query)
	if out, _ := Mask("mail "+mail+" the report", Options{Secrets: true, Personal: true, Allow: o.Allow}); !strings.Contains(out, mail) {
		t.Fatalf("masked with no route: %q", out)
	}
	// taken off Never mask, it is masked again everywhere
	o.Allow = nil
	if out, _ := MaskJSON([]byte(`{"messages":[`+turn2+`]}`), o); strings.Contains(string(out), mail) {
		t.Fatalf("masked no more after leaving Never mask: %s", out)
	}
}
