package gateway

import (
	"github.com/yetone/magpie/internal/provider"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// agDrawer is the image model the tests' Antigravity accounts list.
const agDrawer = "gemini-3.1-flash-image"

// drawnPNG is Code Assist's reply with one image drawn.
const drawnPNG = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}}`

// antigravityDrawers is two Antigravity accounts listing agDrawer, the
// first answering spent as spent says; the second draws. asked is who was
// asked, in order.
func antigravityDrawers(t *testing.T, spent func(w http.ResponseWriter)) (*Server, *[]string) {
	t.Helper()
	asked := new([]string)
	s, _ := antigravityGroupFor(t, []string{"u1@example.com", "u2@example.com"}, agDrawer, func(w http.ResponseWriter, r *http.Request) {
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		*asked = append(*asked, who)
		if who == "u1@example.com" {
			spent(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(drawnPNG))
	})
	return s, asked
}

func drawOn(t *testing.T, s *Server) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/images/generations",
		strings.NewReader(`{"model":"antigravity/`+agDrawer+`","prompt":"a cat"}`)))
	return rec.Code, rec.Body.String()
}

// Antigravity tells a model's quota used up inside the reply too: a 200
// whose stream is the error. Told as a 502, the drawing ended on the
// spent account while the next had its quota (01huadalang on Discord:
// several accounts, one's image quota spent, no move to the next).
func TestAntigravityDrawingOutOfQuotaInsideTheReplyGoesOn(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	s, asked := antigravityDrawers(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse("data: "+antigravityQuota))
	})
	if code, out := drawOn(t, s); code != 200 || !strings.Contains(out, "b64_json") {
		t.Fatalf("the drawing: %d %s (asked %v)", code, out, *asked)
	}
	if got := strings.Join(*asked, " "); got != "u1@example.com u2@example.com" {
		t.Fatalf("asked %s", got)
	}
}

// Any other error said inside the reply is no account's quota, and the
// drawing still ends there: the next account isn't asked, nothing rests.
func TestAntigravityDrawingOtherErrorInsideTheReplyStays(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	s, asked := antigravityDrawers(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`))
	})
	if code, out := drawOn(t, s); code != http.StatusBadGateway {
		t.Fatalf("the drawing: %d %s", code, out)
	}
	if len(*asked) != 1 {
		t.Fatalf("asked %v", *asked)
	}
	if r := rests(); len(r) != 0 {
		t.Fatalf("rests %v", r)
	}
}

// A drawing refused for the image model's quota rests that model on the
// account until the reset Antigravity names, not the account: the next
// drawing asks the account that still has its quota first, where before
// every drawing asked the spent one again, and its text models stay in.
func TestAntigravityDrawingQuotaRestsTheImageModel(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	for _, tc := range []struct {
		name  string
		spent func(w http.ResponseWriter)
	}{
		{"as a 429", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, antigravityQuota)
		}},
		{"inside the reply", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse("data: "+antigravityQuota))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, asked := antigravityDrawers(t, tc.spent)
			for range 2 {
				if code, out := drawOn(t, s); code != 200 {
					t.Fatalf("the drawing: %d %s", code, out)
				}
			}
			if got := strings.Join(*asked, " "); got != "u1@example.com u2@example.com u2@example.com" {
				t.Fatalf("asked %s: the spent account was asked again first", got)
			}
			r, ok := restOf("antigravity@u1@example.com/" + agDrawer)
			if !ok || r.Why != failQuota || r.By != "resets" || r.Model != agDrawer {
				t.Fatalf("the image model's rest: %+v %v (rests %v)", r, ok, rests())
			}
			if d := time.Until(r.Until); d < 3*time.Hour+19*time.Minute || d > 3*time.Hour+20*time.Minute {
				t.Fatalf("rests %v, not until the reset Antigravity named (3h19m30s)", d)
			}
			if _, ok := restOf("antigravity@u1@example.com"); ok {
				t.Fatalf("the whole account rests for its image model's quota: %v", rests())
			}
		})
	}
}
