package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Agent Space refuses Responses probes with anthropic-version, while
// Anthropic endpoints require it. Every probe entry point follows that rule.
func TestProviderProbesUseProtocolHeaders(t *testing.T) {
	for _, entry := range []string{"endpoints", "models", "detect", "detect-models"} {
		t.Run(entry, func(t *testing.T) {
			detectHome(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6-sol"},{"id":"claude-opus-5"}]}`))
					return
				}
				_, version := r.Header["Anthropic-Version"]
				if r.URL.Path == "/v1/messages" {
					if r.Header.Get("anthropic-version") != "2023-06-01" {
						http.Error(w, `{"error":{"message":"anthropic-version is required"}}`, http.StatusBadRequest)
						return
					}
				} else if version {
					http.Error(w, `{"error":{"message":"The request headers are invalid.","type":"invalid_request_error","param":null,"code":"invalid_headers"}}`, http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			p := Provider{ID: "relay", Key: "sk-test", Chat: srv.URL + "/v1",
				Responses: srv.URL + "/v1", Anthropic: srv.URL,
				Models: []string{"gpt-6-sol", "claude-opus-5"}}
			ctx := context.Background()
			var results []Result
			want := 3
			switch entry {
			case "endpoints":
				results = p.Test(ctx)
			case "models":
				p.Chat, p.Anthropic = "", ""
				results = p.TestModels(ctx, []string{"gpt-6-sol"})
				want = 1
			case "detect":
				want = len(detectProtocols) // Gemini's too, without anthropic-version
				detected, err := p.Detect(ctx, srv.URL, "gpt-6-sol")
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range detected {
					results = append(results, d.Result)
				}
			case "detect-models":
				want = len(detectProtocols)
				models, _, err := p.DetectModels(ctx, srv.URL, []string{"gpt-6-sol"})
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range models {
					for _, d := range m.Results {
						results = append(results, d.Result)
					}
				}
			}
			if len(results) != want {
				t.Fatalf("got %d probe results, want %d: %+v", len(results), want, results)
			}
			for _, r := range results {
				if !r.OK || r.Status != http.StatusOK {
					t.Errorf("%s probe failed: %+v", r.Protocol, r)
				}
			}
		})
	}
}
