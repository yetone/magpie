package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ChangQiu on Discord: a ChatGPT account's gpt-image-2.5, typed in by
// hand, failed its Test, since it was asked as a chat on /responses, which
// the backend refuses for an image model (its real 400, below). It is
// asked as the gateway draws it: on the Codex backend's images API, in
// JSON, with Codex CLI's image ids, and the backend's real answer (its
// fields as chatgpt.com sent them, the picture cut short) is a pass. Any
// gpt-image-* name goes, as the backend takes any (drawsFamily).
func TestChatGPTAccountTestsGPTImageByDrawing(t *testing.T) {
	azureHome(t)
	type asked struct {
		path, accept, imagegen, turn, auth, account string
		body                                        map[string]any
	}
	var mu sync.Mutex
	var got []asked
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a := asked{path: r.URL.Path, accept: r.Header.Get("Accept"), imagegen: r.Header.Get("x-codex-imagegen-request-id"), turn: r.Header.Get("x-codex-image-turn-id"), auth: r.Header.Get("Authorization"), account: r.Header.Get("chatgpt-account-id")}
		_ = json.Unmarshal(b, &a.body)
		mu.Lock()
		got = append(got, a)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/images/generations") {
			w.Write([]byte(`{"created": 1793873496, "background": "opaque", "data": [{"b64_json": "iVBORw0KGgoAAAANSUhEUgAA", "generation_id": "bfe49c3e-1703-47eb-a9e0-6ee5d86ac69e"}], "output_format": "png", "quality": "low", "safety_identifier": null, "size": "1536x1024", "usage": {"input_tokens": 12, "input_tokens_details": {"image_tokens": 0, "text_tokens": 12}, "output_tokens": 343, "output_tokens_details": {"image_tokens": 343, "text_tokens": 0}, "total_tokens": 355}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"detail":"The 'gpt-image-2.5' model is not supported when using Codex with a ChatGPT account.","error":{"type":"invalid_request_error","code":"model_not_found","message":"The 'gpt-image-2.5' model is not supported when using Codex with a ChatGPT account."}}`))
	}))
	defer srv.Close()
	p := Provider{ID: "codex", Name: "Codex", Responses: srv.URL + "/backend-api/codex", Account: &Account{
		Agent: "codex", User: "me@example.com",
		sign: codexSign(func(context.Context) (string, string, error) { return "tok", "acct-1", nil }),
	}}
	rs := p.TestModels(context.Background(), []string{"gpt-image-2.5", "gpt-image-2"})
	for _, r := range rs {
		if !r.OK {
			t.Errorf("%s: %+v", r.Model, r)
		}
	}
	if len(got) != 2 {
		t.Fatalf("asked %d times: %+v", len(got), got)
	}
	for _, a := range got {
		if a.path != "/backend-api/codex/images/generations" {
			t.Errorf("asked at %s, not the images API", a.path)
		}
		if m, _ := a.body["model"].(string); !strings.HasPrefix(m, "gpt-image-2") || a.body["prompt"] == nil {
			t.Errorf("body %v", a.body)
		}
		if a.accept != "application/json" || a.imagegen == "" || a.turn == "" || a.imagegen == a.turn {
			t.Errorf("Accept %q, x-codex-imagegen-request-id %q, x-codex-image-turn-id %q", a.accept, a.imagegen, a.turn)
		}
		if a.auth != "Bearer tok" || a.account != "acct-1" {
			t.Errorf("signed as %q for %q", a.auth, a.account)
		}
	}
}
