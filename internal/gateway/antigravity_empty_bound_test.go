package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// A reply that said nothing on two accounts running is about the request,
// not the account: the third, fourth and fifth aren't asked, as each ask
// (an in-place retry or two, then the next account) took minutes on a long
// conversation (vhjjkkhg on X: 8 to 9 tries, 10+ minutes, from Alma on
// Responses). No account rests for it.
func TestEmptyReplyOnTwoAccountsEndsTheRequest(t *testing.T) {
	users := []string{"u1@example.com", "u2@example.com", "u3@example.com", "u4@example.com", "u5@example.com"}
	for _, path := range []string{"/v1/responses", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			var asked atomic.Int32
			s, _ := antigravityGroupFor(t, users, "gemini-3.8-flash", func(w http.ResponseWriter, r *http.Request) {
				asked.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(
					`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking...","thought":true}]}}]}}`,
					`data: {"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":5}}}`))
			})
			body := `{"model":"antigravity/gemini-3.8-flash","stream":true,"input":"hi","messages":[{"role":"user","content":"hi"}]}`
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
			if !strings.Contains(rec.Body.String(), "an empty reply") {
				t.Fatalf("not told the empty reply: %d %s", rec.Code, rec.Body.String())
			}
			// two accounts, each asked again in place a few times (the old
			// code asked all five: 12 asks)
			if n := asked.Load(); n > 8 {
				t.Errorf("an empty reply went round %d asks, over two accounts' worth", n)
			}
			r := lastRoute(s)
			accounts := map[string]bool{}
			var ids []string
			for _, x := range r.Tries {
				accounts[x.ID] = true
				ids = append(ids, x.ID+"/"+strconv.Itoa(x.Status))
			}
			if len(accounts) > 3 { // the group's own entry, and two accounts
				t.Errorf("%d tries: %v", len(r.Tries), ids)
			}
			for _, u := range users {
				if _, ok := restOf("antigravity@" + u); ok {
					t.Errorf("antigravity@%s rested for an empty reply", u)
				}
			}
		})
	}
}
