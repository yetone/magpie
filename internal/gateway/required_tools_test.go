package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Required tool choices must never turn into a plain-text request when the
// allowlist does not match a declared callable function.
func TestRequiredEmptyAllowlistTranslation(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"responses unknown", "/v1/responses", `{"model":"probe/m","input":"hi","tools":[{"type":"function","name":"known"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"unknown"}]}}`},
		{"responses empty", "/v1/responses", `{"model":"probe/m","input":"hi","tools":[{"type":"function","name":"known"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[]}}`},
		{"responses only unsupported", "/v1/responses", `{"model":"probe/m","input":"hi","tools":[{"type":"function","name":"known"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"mcp","name":"external"}]}}`},
		{"gemini unknown", "/v1beta/models/probe/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"known"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["unknown"]}}}`},
		{"gemini no declarations", "/v1beta/models/probe/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			sent := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent++
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(`data: {"id":"x","choices":[{"delta":{"content":"wrong"},"finish_reason":"stop"}]}`, `data: [DONE]`))
			}))
			t.Cleanup(upstream.Close)
			if err := provider.Save(provider.Provider{ID: "probe", Name: "Probe", Key: "k", Chat: upstream.URL + "/v1", Models: []string{"m"}}); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			if rec.Code != 400 || sent != 0 || !strings.Contains(rec.Body.String(), "callable") {
				t.Fatalf("status=%d sent=%d body=%s", rec.Code, sent, rec.Body.String())
			}
		})
	}
}

func TestRequiredAllowlistStillPermitsSearchAndPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		native           bool
	}{
		{"responses allowed search", "/v1/responses", `{"model":"probe/m","input":"hi","tools":[{"type":"function","name":"known"},{"type":"web_search_preview"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"web_search_preview"}]}}`, false},
		{"gemini allowed search", "/v1beta/models/probe/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"googleSearch":{}},{"functionDeclarations":[{"name":"known"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["unknown"]}}}`, false},
		{"responses native unknown", "/v1/responses", `{"model":"probe/m","input":"hi","tools":[{"type":"function","name":"known"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"unknown"}]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			sent := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent++
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"x","output":[],"candidates":[]}`)
			}))
			t.Cleanup(upstream.Close)
			p := provider.Provider{ID: "probe", Name: "Probe", Key: "k", Models: []string{"m"}}
			// In a translated request, a nonempty allowed web-search offer remains
			// callable even if no function declaration survived filtering.
			if strings.HasPrefix(tc.path, "/v1/responses") {
				if tc.native {
					p.Responses = upstream.URL + "/v1"
				} else {
					p.Anthropic = upstream.URL
				}
			} else {
				p.Anthropic = upstream.URL
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			// A web-search request may ask the fake provider more than once;
			// its own fake reply is not a valid streamed search response. The
			// important contract is that the request wasn't rejected as an empty
			// callable set before any provider could be contacted.
			if sent == 0 || rec.Code == 400 {
				t.Fatalf("code=%d sent=%d body=%s", rec.Code, sent, rec.Body.String())
			}
		})
	}
}
