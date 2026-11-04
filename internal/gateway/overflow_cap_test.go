package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A relay's own cap on one request's input is a refusal over the input's
// size, like a model's window: the next member of a group, another vendor,
// may hold it (#2445, worksyang: B.ai's "Input token exceed the limit" on a
// 380K request, under the model's 1M window, failed the whole group, its
// other three members never asked). It goes the way an overflow goes
// (#700): the next member with room is asked, nobody rests, and another
// account of the same model isn't. A request the vendor can't read still
// isn't asked of anyone else, nor is a rate limit worded with input tokens
// taken for one.
func TestRelayInputCapGoesToTheNextMember(t *testing.T) {
	// the vendor's words as the reporter's routing log has them, after
	// magpie's "B.ai: "; the JSON around them is not the reporter's
	const capped = "Input token exceed the limit (request id: 2026…)"
	if !tooLong(400, "B.ai: "+capped) {
		t.Errorf("%q isn't taken for a request too long", capped)
	}
	if !overflowed(400, []byte(`{"error":{"message":"`+capped+`","type":"invalid_request_error"}}`)) {
		t.Errorf("%q in an error body isn't an overflow", capped)
	}
	for _, msg := range []string{
		"Input token exceed the limit per minute",
		"Input tokens exceeded the limit, please wait",
		"input tokens exceed the TPM limit",
	} {
		if tooLong(400, msg) {
			t.Errorf("a rate limit %q taken for a request too long", msg)
		}
	}

	fresh(t)
	a := &scripted{replies: []reply{{400, "", `{"error":{"message":"` + capped + `"}}`}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "Top", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/top","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("capped: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 400 || r.Tries[0].Fail != failOverflow || r.Tries[0].Rest != nil || r.Tries[1].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}

	// a plain bad request is as bad at the next member: not asked. The
	// overflow above left this conversation's memory of a's refusal
	// behind, and #3453's learning would put a behind b before either is
	// asked; this phase is about how a plain 400 is classed, so the
	// memory starts empty
	overflowLearned.Lock()
	overflowLearned.m = map[string]map[string]refusedLong{}
	overflowLearned.Unlock()
	bad := `{"error":{"message":"Invalid parameter: messages","type":"invalid_request_error"}}`
	a.replies, a.n, b.n = []reply{{400, "", bad}}, 0, 0
	code, body = postAs(t, s, "", `{"model":"group/top","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "Invalid parameter") || a.n != 1 || b.n != 0 {
		t.Fatalf("bad request: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
}

// #4420 (MYCF711 on Discord): a group member's 400 "内容太长了，请精简输入
// 或开启新对话", past ~730K on a model it declares at 1M, names the input
// rather than the context. It was filed as a failure that rested the
// member, and the group's other members were never asked. It goes the way
// an overflow goes: the next member is asked and nobody rests, in the
// vendor's words and \u-escaped alike.
func TestChineseInputTooLongGoesToTheNextMember(t *testing.T) {
	for _, said := range []string{
		`{"error":{"message":"内容太长了，请精简输入或开启新对话","type":"invalid_request_error"}}`,
		`{"error":{"message":"内容太长了，请精简输入或开启新对话"}}`,
	} {
		fresh(t)
		a := &scripted{replies: []reply{{400, "", said}}}
		b := &scripted{replies: []reply{{200, "", chatOK}}}
		scriptedOn(t, "a", provider.Chat, a)
		scriptedOn(t, "b", provider.Chat, b)
		if err := provider.SaveGroup(provider.Group{Name: "Top", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		s := New()
		code, body := postAs(t, s, "", `{"model":"group/top","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
			t.Fatalf("%s: %d %s (a %d, b %d)", said, code, body, a.n, b.n)
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		if len(r.Tries) != 2 || r.Tries[0].Fail != failOverflow || r.Tries[0].Rest != nil || r.Tries[1].Rest != nil {
			t.Fatalf("%s: tries %+v", said, r.Tries)
		}
	}
}

// #4420 (MYCF711): a vendor whose own ceiling is under the window it
// declares says only that the request is long, or to shorten it or start
// over, without naming what is long. Past half the member's window, that
// is the request being too long for it: the next member with room is
// asked, and nobody rests. Under half, or said of a pace or of a part of
// the request (a file), the same words are left as they were.
func TestLengthWordsPastHalfTheWindowGoToTheNextMember(t *testing.T) {
	long := strings.Repeat("长对话 ", 600) // ~1500 tokens by estimate's count: past half a's 2000, under its whole
	ask := func(t *testing.T, said, content string) (*Server, *scripted, *scripted, int, string) {
		t.Helper()
		fresh(t)
		a := &scripted{replies: []reply{{400, "", said}}}
		b := &scripted{replies: []reply{{200, "", chatOK}}}
		roomyMember(t, "a", 2000, a)
		roomyMember(t, "b", 10000, b)
		if err := provider.SaveGroup(provider.Group{Name: "Top", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		s := New()
		code, body := postAs(t, s, "", `{"model":"group/top","messages":[{"role":"user","content":"`+content+`"}]}`)
		return s, a, b, code, body
	}
	for _, said := range []string{
		`{"error":{"message":"请求过长，请精简后开启新对话"}}`,
		`{"error":{"message":"请求过长，请精简后开启新对话"}}`,
		`{"error":{"message":"Request too large. Please shorten it and try again.","type":"invalid_request_error","param":null}}`,
	} {
		// the word list alone doesn't take it: the length words decide
		if overflowed(400, []byte(said)) {
			t.Fatalf("%s: already an overflow by its words; the test needs one that isn't", said)
		}
		s, a, b, code, body := ask(t, said, long)
		if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
			t.Fatalf("%s: %d %s (a %d, b %d)", said, code, body, a.n, b.n)
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		if len(r.Tries) != 2 || r.Tries[0].Fail != failOverflow || r.Tries[0].Rest != nil || r.Tries[1].Rest != nil {
			t.Fatalf("%s: tries %+v", said, r.Tries)
		}

		// a short request isn't too long for anything: the vendor's
		// words reach the agent as they did
		s, a, b, code, body = ask(t, said, "hi")
		if code != 400 || a.n != 1 || b.n != 0 {
			t.Fatalf("%s, short: %d %s (a %d, b %d)", said, code, body, a.n, b.n)
		}
		if r := s.trace.routes[len(s.trace.routes)-1]; r.Tries[0].Fail == failOverflow {
			t.Fatalf("%s, short: taken for an overflow", said)
		}
	}
	for _, said := range []string{
		`{"error":{"message":"请求过长，已达到速率限制"}}`, // a pace
		`{"error":{"message":"文件过长，请精简后重试"}}`,  // a part of the request
		`{"error":{"message":"Image too large"}}`,
	} {
		s, _, _, _, body := ask(t, said, long)
		if r := s.trace.routes[len(s.trace.routes)-1]; r.Tries[0].Fail == failOverflow || strings.Contains(body, "prompt is too long") {
			t.Fatalf("%s: taken for the request's length: %s %+v", said, body, r.Tries)
		}
	}
}
