package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// StepFun's safety filter refuses with HTTP 451 and the type
// "censorship_blocked" (#5089): the vendor names the refusal in its error
// object, machine-readable, as the ones policyRefusal reads — the group's
// next member is asked, and nobody rests, as for any refusal of the same
// family. Before, the 451 went to the agent as it came, the next member
// unasked, and the try stood as a plain failure.
func TestCensorshipBlocked451FailsOver(t *testing.T) {
	const blocked = `{"error":{"type":"censorship_blocked","message":"The content you provided or machine outputted is blocked."}}`
	for _, ask := range []struct {
		name string
		body string
	}{
		{"whole", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`},
		{"stream", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(ask.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{451, "", blocked}}}
			b := &scripted{replies: []reply{{200, "", chatOK}}}
			scriptedOn(t, "a", provider.Chat, a)
			scriptedOn(t, "b", provider.Chat, b)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			code, body := sendTo(s, "/v1/chat/completions", ask.body)
			if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "censorship") || a.n != 1 || b.n != 1 {
				t.Fatalf("%s: %d %s (a %d, b %d)", ask.name, code, body, a.n, b.n)
			}
			r := s.trace.routes[len(s.trace.routes)-1]
			if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil || r.Status != 200 {
				t.Fatalf("%s: tries: %+v", ask.name, r.Tries)
			}
			if s.resting("a") {
				t.Fatal("a censorship refusal set the member aside")
			}
		})
	}
}

// The refusal read from the error object itself, as settle reads it.
func TestCensorshipBlockedIsAPolicyRefusal(t *testing.T) {
	note, ok := policyRefusal([]byte(`{"error":{"type":"censorship_blocked","message":"The content you provided or machine outputted is blocked."}}`))
	if !ok || !strings.Contains(note, "censorship_blocked") {
		t.Fatalf("policyRefusal: %q, %v", note, ok)
	}
}
