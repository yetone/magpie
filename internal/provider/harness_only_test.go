package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A model test OpenRouter refuses as not from an agent app says why and
// what to do, not only the 403 (Wade111999 on X), whatever the words are
// wrapped in, and once.
func TestHarnessOnlyTestSaysWhy(t *testing.T) {
	const said = "thinkingmachines/inkling-2-small:free is only available on agentic harnesses. Try plugging it into a coding agent or productivity app listed on https://openrouter.ai/apps"
	for _, b := range []string{
		`{"error":{"message":"` + said + `","code":403}}`,
		said,
	} {
		got := APIError([]byte(b), "403 Forbidden")
		if !strings.Contains(got, said) || !strings.HasSuffix(got, " — "+HarnessOnlyHint) {
			t.Fatalf("%s: %q", b, got)
		}
		if again := APIError([]byte(got), "403 Forbidden"); strings.Count(again, HarnessOnlyHint) > 1 {
			t.Fatalf("hint said twice: %q", again)
		}
		if !HarnessOnly([]byte(b)) {
			t.Fatalf("%s not read as the harness gate", b)
		}
	}
	if HarnessOnly([]byte(`{"error":{"message":"Key disabled","code":403}}`)) || strings.Contains(APIError([]byte(`{"error":{"message":"Key disabled","code":403}}`), ""), HarnessOnlyHint) {
		t.Fatal("another 403 read as the harness gate")
	}

	// the test itself, as the editor's Test models runs it
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"` + said + `","code":403}}`))
	}))
	defer srv.Close()
	p := Provider{ID: "openrouter", Chat: srv.URL + "/api/v1", Key: "sk-or-v1-test"}
	r := p.TestModels(t.Context(), []string{"thinkingmachines/inkling-2-small:free"})[0]
	if r.OK || r.Status != http.StatusForbidden || !strings.Contains(r.Error, HarnessOnlyHint) {
		t.Fatalf("test result %+v", r)
	}
}
