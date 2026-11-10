package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's auto-review of an approval, as codex-rs's guardian sends it: on
// its reviewer model, codex-auto-review, whatever the conversation is on,
// under x-openai-subagent: guardian and x-codex-guardian: reviewer.
const reviewTurn = `{"model":"codex-auto-review","stream":true,"input":"review this approval"}`

// reviewOn posts body to s's Codex route as Codex's guardian does (guardian
// true) or as one of the conversation's own turns.
func reviewOn(s *Server, body string, guardian bool) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	if guardian {
		req.Header.Set("x-openai-subagent", "guardian")
		req.Header.Set("x-codex-guardian", "reviewer")
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Codex's one ChatGPT account out of its allowance, and Codex routed on
// through a routing group of the user's with Codex's model first and an
// API after it: the conversation's turns go on to the API, and so does
// its auto-review, which asks codex-auto-review, a model no group has
// (riverleaf88 on X: "在codex耗尽额度时，即使路由到别的模型，在codex中自动审查会失败").
// Before, the review was handed the backend's refusal, and Codex failed it.
func TestCodexReviewSpentFollowsTheGroup(t *testing.T) {
	for _, x := range codexSpent {
		t.Run(x.name, func(t *testing.T) {
			b := &spentBackend{refuse: x.refuse}
			spentAccount(t, b, false)
			a := &scripted{replies: []reply{{200, "", chatSSEReply("from the group")}}}
			scriptedOn(t, "a", provider.Chat, a)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5", "a/m"}}); err != nil {
				t.Fatal(err)
			}
			s := New()
			if rec := reviewOn(s, oneAccountTurn, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from the group") {
				t.Fatalf("turn: %d %q", rec.Code, rec.Body.String())
			}
			rec := reviewOn(s, reviewTurn, true)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from the group") || strings.Contains(rec.Body.String(), "usage_limit_reached") {
				t.Fatalf("review: %d %q", rec.Code, rec.Body.String())
			}
			if a.n != 2 {
				t.Fatalf("the group's API asked %d times, ChatGPT %d", a.n, b.n.Load())
			}
			if b.n.Load() > 2 {
				// the spent account isn't asked again for the review on
				// the conversation's model, the group's first member
				t.Fatalf("ChatGPT asked %d times", b.n.Load())
			}
		})
	}
}

// The conversation on one of magpie's models, picked in the Codex app
// beside the spent ChatGPT sign-in: its auto-review, on codex-auto-review
// through the sign-in, goes on to that model when the account is out.
func TestCodexReviewSpentFollowsMagpieModel(t *testing.T) {
	for _, x := range codexSpent {
		t.Run(x.name, func(t *testing.T) {
			b := &spentBackend{refuse: x.refuse}
			spentAccount(t, b, false)
			a := &scripted{replies: []reply{{200, "", chatSSEReply("from magpie's model")}}}
			scriptedOn(t, "a", provider.Chat, a)
			s := New()
			if rec := reviewOn(s, `{"model":"a/m","stream":true,"input":"ping"}`, false); rec.Code != 200 || b.n.Load() != 0 {
				t.Fatalf("turn: %d %q, ChatGPT asked %d", rec.Code, rec.Body.String(), b.n.Load())
			}
			rec := reviewOn(s, reviewTurn, true)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from magpie's model") || b.n.Load() != 1 || a.n != 2 {
				t.Fatalf("review: %d %q, ChatGPT asked %d, a %d", rec.Code, rec.Body.String(), b.n.Load(), a.n)
			}
		})
	}
}

// With nowhere else for the conversation to go (no fallback, no group, no
// turn of magpie's to follow), the review still has the backend's own
// refusal, as it had.
func TestCodexReviewSpentNowhereElseRelayed(t *testing.T) {
	b := &spentBackend{refuse: codexSpent[0].refuse}
	spentAccount(t, b, false)
	a := &scripted{replies: []reply{{200, "", chatSSEReply("never")}}}
	scriptedOn(t, "a", provider.Chat, a)
	rec := reviewOn(New(), reviewTurn, true)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "usage_limit_reached") || b.n.Load() != 1 || a.n != 0 {
		t.Fatalf("%d %q, ChatGPT asked %d, a %d", rec.Code, rec.Body.String(), b.n.Load(), a.n)
	}
}

// The account held after ChatGPT's 429 named when it is back
// (codexHolds): the next turn, and the review after it, go on to the
// group as the turn that held it did, never handed the refusal again from
// the hold while it lasts.
func TestCodexHeldTurnFollowsTheGroup(t *testing.T) {
	for _, x := range codexSpent[:2] {
		t.Run(x.name, func(t *testing.T) {
			b := &spentBackend{refuse: x.refuse}
			spentAccount(t, b, false)
			a := &scripted{replies: []reply{{200, "", chatSSEReply("from the group")}}}
			scriptedOn(t, "a", provider.Chat, a)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5", "a/m"}}); err != nil {
				t.Fatal(err)
			}
			s := New()
			for i, body := range []string{oneAccountTurn, oneAccountTurn, reviewTurn} {
				rec := reviewOn(s, body, body == reviewTurn)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from the group") {
					t.Fatalf("request %d: %d %q", i, rec.Code, rec.Body.String())
				}
			}
			if b.n.Load() != 1 || a.n != 3 {
				t.Fatalf("ChatGPT asked %d times, the group's API %d", b.n.Load(), a.n)
			}
		})
	}
}
