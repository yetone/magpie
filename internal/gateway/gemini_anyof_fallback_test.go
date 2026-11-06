package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// tokenFreeAnyOf400 is a relay's (sub2api, TokenFree) answer passing Google's
// own Gemini on, which turned a tool's parameters away over a description
// beside an anyOf, as worksyang's gateway log has it (#5374): Google's Schema
// takes an anyOf alone. A relay saying Google's words on writes them
// camel-case, the field in backticks, no path — not the
// function_declarations[N].parameters shape shapeWords' Google branch reads.
const tokenFreeAnyOf400 = `{"error":{"message":"TokenFree: Unable to submit request because ` + "`paseo_respond_to_permission`" + ` functionDeclaration ` + "`parameters.response`" + ` schema specified other fields alongside any_of. When using any_of, it must be the only field set. Learn more: https://ai.google.dev/gemini-api/docs","type":"api_error"}}`

// googleAnyOf400 is Google saying it on a request sent straight: the same
// fixed wording, no relay in front of it.
const googleAnyOf400 = `{"error":{"code":400,"message":"Unable to submit request because ` + "`paseo_respond_to_permission`" + ` functionDeclaration ` + "`parameters.response`" + ` schema specified other fields alongside any_of. When using any_of, it must be the only field set. Learn more: https://ai.google.dev/gemini-api/docs","status":"INVALID_ARGUMENT"}}`

// A relay whose Gemini turns a tool's parameters away over a description
// beside an anyOf is a member's limit, not a bad request: the group's next
// member is asked and the relay doesn't rest (#5374). Before, the route ended
// on the 400 with the later members never asked — an agent run of minutes
// died whole on the first seat's word.
func TestGroupFailsOverAnAnyOfSchemaRefused(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{400, "", tokenFreeAnyOf400}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "tokenfree", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"tokenfree/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 400 || r.Tries[0].Fail != failShape || r.Tries[0].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}

	for _, x := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, tokenFreeAnyOf400, true},
		{422, tokenFreeAnyOf400, true},
		{400, googleAnyOf400, true},
		// as the reporter ran it, a plain bad request is still the
		// request's, and another Google 400 naming a declaration's name
		// is neither
		{400, `{"error":{"message":"messages: field required"}}`, false},
		{400, `{"error":{"message":"* GenerateContentRequest.tools[0].function_declarations[2].name: Invalid function name.","status":"INVALID_ARGUMENT"}}`, false},
	} {
		if got := shapeRefused(x.status, []byte(x.body)); got != x.want {
			t.Errorf("shapeRefused(%d, %s) = %v", x.status, x.body, got)
		}
	}
}
