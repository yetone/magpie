package redact

import (
	"strings"
	"testing"
)

// A kind the user set to warn of goes as written: told, with a
// placeholder no one remembers, and not counted as masked. The rest is
// masked as before (#1977).
func TestActionWarnSendsAsWritten(t *testing.T) {
	const key, mail = "sk-proj-Wn4rT7yQ2mLx9VbK3cZp", "warned.only@kelvarn.io"
	var hits []Hit
	o := Options{Secrets: true, Personal: true, Actions: map[string]string{"EMAIL": Warn},
		Seen: func(h Hit) { hits = append(hits, h) }}
	out, n := Mask("key "+key+" mail "+mail, o)
	if !strings.Contains(out, mail) || strings.Contains(out, key) || n != 1 {
		t.Fatalf("out %q n %d", out, n)
	}
	var warned *Hit
	for i, h := range hits {
		if h.Kind == "EMAIL" {
			warned = &hits[i]
		}
	}
	if len(hits) != 2 || warned == nil || warned.Action != Warn || warned.Head != "w" || warned.Len != len(mail) {
		t.Fatalf("hits %+v", hits)
	}
	if _, ok := Value(warned.Placeholder); ok || strings.Contains(out, warned.Placeholder) {
		t.Fatalf("a value sent as written is remembered or placed: %+v in %q", warned, out)
	}

	// in a JSON body the same
	hits = nil
	b, n := MaskJSON([]byte(`{"messages":[{"role":"user","content":"mail `+mail+`"}]}`), o)
	if !strings.Contains(string(b), mail) || n != 0 || len(hits) != 1 || hits[0].Action != Warn {
		t.Fatalf("json %s n %d hits %+v", b, n, hits)
	}
}

// A kind the user set to block is masked all the same, and told as
// blocked, so the gateway holds the request back; a request that goes
// anyway goes masked.
func TestActionBlockMasks(t *testing.T) {
	const key = "sk-proj-Bl0cKq8Wm3Rt6YxN2vLp"
	var hits []Hit
	out, n := Mask("key "+key, Options{Secrets: true, Actions: map[string]string{"API_KEY": Block},
		Seen: func(h Hit) { hits = append(hits, h) }})
	if strings.Contains(out, key) || n != 1 || len(hits) != 1 || hits[0].Action != Block || hits[0].Kind != "API_KEY" {
		t.Fatalf("out %q n %d hits %+v", out, n, hits)
	}
	if v, ok := Value(hits[0].Placeholder); !ok || v != key {
		t.Fatalf("a blocked value isn't masked as any other: %q %v", v, ok)
	}
}

// A value on the allow list goes as written, whole, in any case, and
// isn't told; a value it is only part of is masked.
func TestAllowLetsAValueGo(t *testing.T) {
	const mail, key = "Team.Inbox@kelvarn.io", "sk-proj-Al1oWq8Wm3Rt6YxN2vLp"
	var hits []Hit
	o := Options{Secrets: true, Personal: true, Allow: []string{"  team.inbox@KELVARN.io ", key},
		Seen: func(h Hit) { hits = append(hits, h) }}
	out, n := Mask("write "+mail+" with "+key+" or other.person@kelvarn.io", o)
	if !strings.Contains(out, mail) || !strings.Contains(out, key) || strings.Contains(out, "other.person") || n != 1 || len(hits) != 1 {
		t.Fatalf("out %q n %d hits %+v", out, n, hits)
	}
	out, _ = Mask("cc x"+mail, o)
	if strings.Contains(out, "x"+mail) {
		t.Fatalf("a value the allowed one is part of went as written: %q", out)
	}
}

// The user's Masked words win over Never mask, the safe side of two
// things the user asked for: a value on both lists is masked, and so is a
// word inside a value Never mask lets go. Never mask wins over the
// user's own rules: the value it names is the exception to the pattern.
func TestMaskedWordsWinOverAllow(t *testing.T) {
	const mail = "team.inbox@kelvarn.io"
	o := Options{Secrets: true, Personal: true, Words: []string{"build-01", "kelvarn"},
		Allow: []string{"build-01", mail, "PROJ-0001"},
		Rules: []Rule{{Kind: "TICKET", Regex: `PROJ-\d{4}`}}}
	out, _ := Mask("deploy on build-01, mail "+mail+", tickets PROJ-0001 and PROJ-0002", o)
	if strings.Contains(out, "build-01") {
		t.Errorf("a value on both lists went as written: %q", out)
	}
	if strings.Contains(out, "kelvarn") || !strings.Contains(out, "team.inbox@{{TERM_") {
		t.Errorf("a masked word inside an allowed value: %q", out)
	}
	if !strings.Contains(out, "PROJ-0001") || strings.Contains(out, "PROJ-0002") {
		t.Errorf("Never mask against the user's rule: %q", out)
	}
}

// The preview masks as Mask does and remembers nothing: its placeholders
// stand for nothing afterwards, and its values aren't masked later as
// values seen before.
func TestPreviewRemembersNothing(t *testing.T) {
	const key = "sk-proj-Pr3vIewQ8m3Rt6YxN2vL"
	var hits []Hit
	out := Preview("key "+key, Options{Secrets: true, Seen: func(h Hit) { hits = append(hits, h) }})
	if strings.Contains(out, key) || len(hits) != 1 || !strings.Contains(out, hits[0].Placeholder) {
		t.Fatalf("out %q hits %+v", out, hits)
	}
	if _, ok := Value(hits[0].Placeholder); ok {
		t.Fatal("the preview's placeholder is remembered")
	}
	if spans := knownSpans("again "+key, Options{Secrets: true}); len(spans) != 0 {
		t.Fatalf("the preview's value is known: %v", spans)
	}
	// masked for real, the same value goes as the same placeholder
	if real, _ := Mask("key "+key, Options{Secrets: true}); real != out {
		t.Fatalf("preview %q, sent %q", out, real)
	}
}
