package redact

import (
	"strings"
	"testing"
)

// A value masked once goes as its placeholder wherever it is next, in
// whatever words are around it: what the vendor wrote about it, as the
// agent sends it back.
func TestKnownValueMaskedInOtherWords(t *testing.T) {
	o := Options{Secrets: true, Personal: true}
	const secret, email, phone = "Zq8hunterPass42", "rina.k@acme-labs.io", "13912345670"
	ps, n := Mask("DB_PASSWORD="+secret, o)
	pe, _ := Mask("I am "+email, o)
	pp, _ := Mask("call "+phone, o)
	if n != 1 || strings.Contains(ps+pe+pp, secret) || strings.Contains(pe, email) || strings.Contains(pp, phone) {
		t.Fatalf("not masked to begin with: %q %q %q", ps, pe, pp)
	}
	ps, pe, pp = strings.TrimPrefix(ps, "DB_PASSWORD="), strings.TrimPrefix(pe, "I am "), strings.TrimPrefix(pp, "call ")

	// the vendor's reply as the agent has it back: no rule matches these
	reply := "Your password is " + secret + ", and I will write to " + email + ". Or call " + phone + "!"
	out, n := Mask(reply, o)
	if strings.Contains(out, secret) || strings.Contains(out, email) || strings.Contains(out, phone) {
		t.Fatalf("a known value went out: %q", out)
	}
	if want := "Your password is " + ps + ", and I will write to " + pe + ". Or call " + pp + "!"; out != want || n != 3 {
		t.Fatalf("%d masked: %q, want the same placeholders as before: %q", n, out, want)
	}
	if back := Restore(out, false); back != reply {
		t.Fatalf("round trip: %q", back)
	}

	// in a request body, as the next turn's history has it
	body := `{"messages":[{"role":"assistant","content":"` + reply + `"},{"role":"user","content":"thanks"}]}`
	if got, n := MaskJSON([]byte(body), o); strings.Contains(string(got), secret) || strings.Contains(string(got), email) || strings.Contains(string(got), phone) || n != 3 {
		t.Fatalf("%d masked: %s", n, got)
	}

	// only what the settings mask: personal data off, secrets off
	if out, _ := Mask(reply, Options{Secrets: true}); !strings.Contains(out, email) || !strings.Contains(out, phone) || strings.Contains(out, secret) {
		t.Fatalf("personal data off: %q", out)
	}
	if out, _ := Mask(reply, Options{Personal: true}); !strings.Contains(out, secret) || strings.Contains(out, email) {
		t.Fatalf("secrets off: %q", out)
	}

	// a piece of a longer number or address is another one
	for _, s := range []string{"order 2" + phone, "order " + phone + "9", "mail " + email + ".cn now", "mail x" + email} {
		if out, _ := Mask(s, Options{Personal: true}); strings.Contains(out, pp) || strings.Contains(out, pe) {
			t.Errorf("%q: masked as the known value: %q", s, out)
		}
	}
	// a secret in other punctuation is the secret still; inside a longer
	// word it is another word
	if out, _ := Mask("`"+secret+"`/"+secret+"_v2", Options{Secrets: true}); strings.Contains(out, secret) {
		t.Errorf("secret left: %q", out)
	}
	if out, _ := Mask("pass"+secret+"x", Options{Secrets: true}); out != "pass"+secret+"x" {
		t.Errorf("a longer word masked: %q", out)
	}
}

// A value too short to be told from ordinary text is masked where a rule
// finds it, and nowhere else.
func TestShortValueNotMaskedEverywhere(t *testing.T) {
	o := Options{Secrets: true}
	if out, n := Mask("postgres://app:ab12cd@db/app", o); n != 1 || strings.Contains(out, "ab12cd") {
		t.Fatalf("the rule should mask it: %q", out)
	}
	if out, n := Mask("hex ab12cd0 and ab12cd", o); n != 0 || out != "hex ab12cd0 and ab12cd" {
		t.Fatalf("a short value masked everywhere: %q", out)
	}
}

// What a vendor sealed has a secret masked before as its placeholder, the
// way the vendor wrote it, though nothing else in the request has it; a
// number or address of its own it keeps, though one masked before.
func TestKnownInSigned(t *testing.T) {
	o := Options{Secrets: true, Personal: true}
	const secret, phone = "Wv7otterPass93", "13612345674"
	Mask("DB_PASSWORD="+secret, o)
	Mask("call "+phone, o)
	body := `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"the password is ` + secret + `; order ` + phone + `","signature":"Eqk1"}]}]}`
	out, _ := MaskJSON([]byte(body), o)
	if strings.Contains(string(out), secret) || !strings.Contains(string(out), "order "+phone) {
		t.Fatalf("%s", out)
	}
}

// After values are let go, the ones still held are matched as before.
func TestKnownReindexed(t *testing.T) {
	o := Options{Secrets: true}
	const secret = "Kp3heronPass71"
	p, _ := Mask("API_TOKEN="+secret, o)
	mu.Lock()
	reindexKnown()
	mu.Unlock()
	if out, _ := Mask("it is "+secret, o); out != "it is "+strings.TrimPrefix(p, "API_TOKEN=") {
		t.Fatalf("%q", out)
	}
}
