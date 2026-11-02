package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// beamReasoning400 is Command Code's reflection/beam:free turning a second
// turn away (EDwinpzy on Discord, magpie 0.1.2717): the reporter's words,
// "Missing required parameter: 'messages[N].reasoning_content'", in the
// OpenAI error shape the Provider API answers in.
const beamReasoning400 = `{"error":{"message":"Missing required parameter: 'messages[1].reasoning_content'.","type":"invalid_request_error","param":"messages[1].reasoning_content","code":"missing_required_parameter"}}`

// A model that wants an earlier reply's reasoning back the history doesn't
// have is the model's demand, not a bad request: a group of Beam then
// Glyph goes on to Glyph, on the Chat route and on Responses alike, and
// Beam doesn't rest for it. Before, the 400 wasn't retryable and the route
// ended at Beam.
func TestGroupFailsOverAMissingReasoning(t *testing.T) {
	second := []struct {
		route string
		post  func(*Server) (int, string)
	}{
		{"chat", func(s *Server) (int, string) {
			return postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"Hello! How can I help?"},{"role":"user","content":"what model are you?"}]}`)
		}},
		{"responses", func(s *Server) (int, string) {
			return postResponses(t, s, `{"model":"group/g","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello! How can I help?"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"what model are you?"}]}]}`)
		}},
	}
	for _, x := range second {
		t.Run(x.route, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{400, "", beamReasoning400}}}
			b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
			scriptedOn(t, "beam", provider.Chat, a)
			scriptedOn(t, "glyph", provider.Chat, b)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"beam/m", "glyph/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			code, body := x.post(s)
			if code != 200 || !strings.Contains(body, "from b") || a.n != 1 || b.n != 1 {
				t.Fatalf("%d %s (beam %d, glyph %d)", code, body, a.n, b.n)
			}
			r := s.trace.routes[len(s.trace.routes)-1]
			if len(r.Tries) != 2 || r.Tries[0].Status != 400 || r.Tries[0].Fail != failShape || r.Tries[0].Rest != nil {
				t.Fatalf("tries %+v", r.Tries)
			}
		})
	}
}

// A request that is itself bad still stops at the first member, and the
// words are read as a demand for reasoning only where they name it.
func TestMissingReasoningIsNotABadRequest(t *testing.T) {
	fresh(t)
	bad := `{"error":{"message":"Missing required parameter: 'messages'.","type":"invalid_request_error","param":"messages","code":"missing_required_parameter"}}`
	a := &scripted{replies: []reply{{400, "", bad}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code != 400 || a.n != 1 || b.n != 0 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}

	for _, x := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, beamReasoning400, true},
		{400, `Command Code: Missing required parameter: 'messages[3].reasoning_content'.`, true},
		// DeepSeek's and Kimi's words for their thinking on a tool turn
		{400, "{\"error\":{\"message\":\"The `reasoning_content` in the thinking mode must be passed back to the API.\",\"type\":\"invalid_request_error\"}}", true},
		{400, "{\"error\":{\"message\":\"Missing `reasoning_content` field in the assistant message at message index 1.\",\"type\":\"invalid_request_error\"}}", true},
		{400, `{"error":{"message":"thinking is enabled but reasoning_content is missing in assistant tool call message at index 2","type":"invalid_request_error"}}`, true},
		{422, beamReasoning400, true},
		{200, beamReasoning400, false},
		{500, beamReasoning400, false},
		// the request's own fault
		{400, bad, false},
		{400, `{"error":{"message":"Missing required parameter: 'model'.","type":"invalid_request_error"}}`, false},
		{400, `{"error":{"message":"messages[1].content: field required"}}`, false},
	} {
		if got := shapeRefused(x.status, []byte(x.body)); got != x.want {
			t.Errorf("shapeRefused(%d, %s) = %v", x.status, x.body, got)
		}
		// (a bare 'model' was retryable before, read as a model unserved)
		if got := retryable(x.status, []byte(x.body)); x.want && !got {
			t.Errorf("retryable(%d, %s) = %v", x.status, x.body, got)
		}
	}
}
