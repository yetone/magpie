package redact

import (
	"strings"
	"testing"
)

// A value on Never mask is one written out in full, or a pattern with *
// in it for a kind of host or route name met as it comes (*-singapore-*,
// binedge-*; StringKe, #7973), which the list, at most 200 values, can't
// hold one by one. A value the pattern doesn't match is masked still, and
// case is no matter, as it was.
func TestAllowWildcards(t *testing.T) {
	Forget()
	defer Forget()
	const route, other = "binedge-7b0pfgcdddd3", "edgebin-7b0pfgcdddd3"
	f := &fakeModel{values: map[string]string{route: "secret", other: "secret"}}
	o := Options{Secrets: true, Personal: true, Model: f.find, Allow: []string{"*-singapore-*", "BinEdge-*", "*@kelvarn.io"}}
	s := "route " + route + " and " + other + ", mail team.inbox@kelvarn.io or rina.k@fastmail.com"
	out, _ := Mask(s, o)
	for _, v := range []string{route, "team.inbox@kelvarn.io"} {
		if !strings.Contains(out, v) {
			t.Errorf("%q matches a pattern on Never mask, yet it was masked: %q", v, out)
		}
	}
	for _, v := range []string{other, "rina.k@fastmail.com"} {
		if strings.Contains(out, v) {
			t.Errorf("%q matches no pattern, yet it went out: %q", v, out)
		}
	}

	e := Options{Allow: []string{"growth-*", "*-workspace", "*.internal", "a*b*c-d"}}
	for _, v := range []string{"growth-2", "GROWTH-x", "billing-workspace", "db.internal", "aXbYc-d", "abc-d"} {
		if !e.allows("SECRET", v) {
			t.Errorf("%q matches a pattern, yet it isn't let go", v)
		}
	}
	for _, v := range []string{"growth", "growth2", "workspace", "internal", "acb-d", "abc-dx"} {
		if e.allows("SECRET", v) {
			t.Errorf("%q matches no pattern, yet it is let go", v)
		}
	}
}

// A pattern with too few characters besides its stars is only the value it
// is written as: * alone would let every key go. Nor does a pattern let a
// vendor's key go, which a pattern for host names can match by chance;
// only the key written out in full does.
func TestAllowWildcardLetsNoKeyGo(t *testing.T) {
	defer Forget()
	const key = "sk-proj-Al1oWq8Wm3Rt6YxN2vLpZk4Hs7Dc"
	const gh = "ghp_Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78"
	const route = "binedge-7b0pfgcdddd3"
	f := &fakeModel{values: map[string]string{route: "secret"}}
	s := "use " + key + " and " + gh + " on " + route + ", mail rina.k@fastmail.com"
	for _, allow := range [][]string{{"*"}, {"**"}, {"***-*"}, {"b*"}, {"sk-*"}, {"*om"}, {"sk-proj-*", "ghp_*", "*Wm3Rt6*"}} {
		Forget()
		out, _ := Mask(s, Options{Secrets: true, Personal: true, Model: f.find, Allow: allow})
		for _, v := range []string{key, gh} {
			if strings.Contains(out, v) {
				t.Errorf("Never mask %q let a key go: %q", allow, out)
			}
		}
		if allow[0] != "sk-proj-*" && (strings.Contains(out, route) || strings.Contains(out, "rina.k@fastmail.com")) {
			t.Errorf("Never mask %q, under %d characters besides its stars, let a value go: %q", allow, minLiteral, out)
		}
	}
	Forget()
	out, _ := Mask(s, Options{Secrets: true, Model: f.find, Allow: []string{key, "*"}})
	if !strings.Contains(out, key) || strings.Contains(out, gh) {
		t.Errorf("a key written out in full is the one let go: %q", out)
	}
}

// A rule of the user's own lets its value go only written out in full,
// even one whose kind the user named SECRET or EMAIL.
func TestAllowWildcardLetsNoOwnRuleGo(t *testing.T) {
	Forget()
	defer Forget()
	for _, kind := range []string{"SECRET", "EMAIL", "billing"} {
		o := Options{Secrets: true, Personal: true, Rules: []Rule{{Kind: kind, Regex: `binedge-[a-z0-9]{12}`}}, Allow: []string{"binedge-*"}}
		if out, _ := Mask("route binedge-7b0pfgcdddd3", o); strings.Contains(out, "binedge-7b0pfgcdddd3") {
			t.Errorf("a pattern let a value of the user's own %s rule go: %q", kind, out)
		}
	}
}
