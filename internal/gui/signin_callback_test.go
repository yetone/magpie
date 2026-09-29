package gui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dimagent"
	"github.com/yetone/magpie/internal/provider"
)

// The paste route must reach the pending OAuth flow, reject invalid input
// without ending it, and accept the vendor's error without contacting it.
func TestDimAgentSignInCallbackRoute(t *testing.T) {
	sandboxHome(t)
	st, err := provider.StartSignIn("dimagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/signin/"+st.ID+"/callback", strings.NewReader(body)))
		return w
	}
	for _, body := range []string{`{`, `{"url":"not a callback"}`, `{"url":"http://localhost:54321/auth/callback?code=x&state=wrong"}`} {
		if w := post(body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid callback: %d %s", w.Code, w.Body)
		}
	}
	if current, _ := provider.SignInStatus(st.ID); current.State != "waiting" {
		t.Fatal("invalid input ended the sign-in")
	}
	auth, _ := url.Parse(st.URL)
	callback := dimagent.RedirectURI + "?error=access_denied&state=" + auth.Query().Get("state")
	if w := post(`{"url":"` + callback + `"}`); w.Code != http.StatusNoContent {
		t.Fatalf("callback was not handed to the flow: %d %s", w.Code, w.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	finished, err := provider.WaitSignIn(ctx, st.ID)
	if err != nil || finished.State != "failed" || !strings.Contains(finished.Error, "access_denied") {
		t.Fatalf("callback result: %+v %v", finished, err)
	}
}
