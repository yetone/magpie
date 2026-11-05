package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const verifyLinked = `{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://accounts.google.com/signin/continue?sarp=1&x=2"}}]}}`

// An account Google wants verified (#152) is refused until someone does:
// the agent is told how, with the link; its reconnects for the next moment
// are told the same without asking again; the app can lift the rest.
func TestVerifyRefusalRestsTheLastOne(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{403, "application/json", verifyLinked}}}
	scriptedOn(t, "a", provider.Chat, a)
	s := New()
	req := `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`
	code, body := postAs(t, s, "", req)
	msg := provider.APIError([]byte(body), "")
	if code != 403 || a.n != 1 || !strings.Contains(msg, "Verify your account to continue.") ||
		!strings.Contains(msg, "open https://accounts.google.com/signin/continue?sarp=1&x=2 in a browser") {
		t.Fatalf("%d after %d tries: %s", code, a.n, msg)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	try := r.Tries[len(r.Tries)-1]
	if try.Fail != failVerify || try.Rest == nil || try.Rest.By != "verify" || try.Rest.Key != "a" ||
		try.Rest.Link != "https://accounts.google.com/signin/continue?sarp=1&x=2" {
		t.Fatalf("try %+v rest %+v", try, try.Rest)
	}

	// the agent reconnects at once: held, not sent on
	for range 3 {
		if code, again := postAs(t, s, "", req); code != 403 || provider.APIError([]byte(again), "") != msg {
			t.Fatalf("%d %s", code, again)
		}
	}
	if a.n != 1 {
		t.Fatalf("asked again %d times while held", a.n-1)
	}
	if rest, ok := restOf("a"); !ok || rest.Why != failVerify {
		t.Fatalf("%+v %v", rest, ok)
	}

	// verified, and said so in the app: asked again
	if !s.Unrest("a") || s.resting("a") {
		t.Fatal("rest not lifted")
	}
	a.replies = []reply{{200, "", chatOK}}
	if code, body := postAs(t, s, "", req); code != 200 || a.n != 2 {
		t.Fatalf("%d after %d: %s", code, a.n, body)
	}
}

// Without a link, the agent is told to verify it in the vendor's app; with
// another to ask, the request goes on to it and the refused one rests.
func TestVerifyRefusalFallsBack(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{403, "application/json", `{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED"}}`}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered})
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || a.n != 1 || b.n != 1 {
		t.Fatalf("%d (a %d, b %d): %s", code, a.n, b.n, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if rest := r.Tries[0].Rest; r.Tries[0].Fail != failVerify || rest == nil || rest.By != "verify" || rest.Link != "" ||
		!strings.Contains(rest.said, "open the Antigravity app") {
		t.Fatalf("tries %+v", r.Tries)
	}
	if failure(403, []byte(`{"error":{"message":"The caller does not have permission"}}`)) == failVerify {
		t.Fatal("any 403 read as a verification")
	}
}

// Google's link reaches the agent and the Routing page's Verify whole: every
// query parameter, its & unescaped, whether the body wrote & or &.
func TestVerifyLinkWhole(t *testing.T) {
	const link = "https://accounts.google.com/signin/continue?sarp=1&scc=1&continue=https://developers.google.com/gemini-code-assist/auth/auth_success_gemini%3Fsrc%3Dag&plt=AKgnsbvK-3x_Yq9Zt0lPq2vN7m8w&flowName=GlifWebSignIn&authuser=0"
	for _, amp := range []string{"&", `&`} {
		fresh(t)
		u := strings.ReplaceAll(link, "&", amp)
		body := `{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","domain":"cloudcode-pa.googleapis.com","metadata":{"validation_error_message":"Verify your account to continue.","validation_url_link_text":"Verify your account","validation_url":"` + u + `"}},{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Further action is required to use this service. Navigate to the following URL to complete verification:\n\n` + u + `\n\nLearn more:\n\nhttps://support.google.com/accounts?p=al_alert\n","url":"` + u + `"},{"description":"Learn more","url":"https://support.google.com/accounts?p=al_alert"}]}]}}`
		a := &scripted{replies: []reply{{403, "application/json", body}}}
		scriptedOn(t, "a", provider.Chat, a)
		s := New()
		_, got := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
		if msg := provider.APIError([]byte(got), ""); !strings.Contains(msg, " open "+link+" in a browser") {
			t.Fatalf("%s: %s", amp, msg)
		}
		if l, _ := provider.Verification([]byte(got)); l != link {
			t.Fatalf("%s: read back %q", amp, l)
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		if rest := r.Tries[len(r.Tries)-1].Rest; rest == nil || rest.Link != link {
			t.Fatalf("%s: rest %+v", amp, rest)
		}
	}
}
