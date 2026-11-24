package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sigv4"
)

// signedBedrock is the fake runtime behind a check Bedrock makes of an
// access key's requests (#7687): each is signed again from what arrived
// (host, path, the X-Amz-* headers and the bytes of the body) at the time
// it names, and one whose signature differs is refused with 403, as AWS
// answers it.
type signedBedrock struct {
	bedrock
	id, secret, token string
	mu2               sync.Mutex
	refused           []string
}

func (b *signedBedrock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	why := ""
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	switch {
	case err != nil:
		why = "no X-Amz-Date"
	case r.Header.Get("X-Amz-Security-Token") != b.token:
		why = "token " + r.Header.Get("X-Amz-Security-Token")
	case r.Header.Get("x-api-key") != "":
		why = "an API key"
	default:
		again, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
		again.Header.Set("X-Amz-Security-Token", b.token)
		sigv4.Signer{ID: b.id, Secret: b.secret, Token: b.token, Region: "us-east-1", Service: "bedrock", Few: true, Twice: true}.
			Sign(again, sigv4.Hash(body), at)
		if got, want := r.Header.Get("Authorization"), again.Header.Get("Authorization"); got != want {
			why = "signature " + got + ", want " + want
		}
	}
	if why != "" {
		b.mu2.Lock()
		b.refused = append(b.refused, r.URL.Path+": "+why)
		b.mu2.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"The request signature we calculated does not match the signature you provided."}`)
		return
	}
	b.bedrock.ServeHTTP(w, r)
}

// The Bedrock preset with an AWS access key and no API key: each request,
// streamed messages and chat completions, from either API an agent speaks,
// reaches the runtime signed over the body it carries, with the session
// token and no key; the agent's anthropic-beta, which the gateway fits
// after signing, doesn't break it.
func TestBedrockAccessKeySigned(t *testing.T) {
	fresh(t)
	up := &signedBedrock{id: "AKIDEXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", token: "FQoGZXIvYXdzEXAMPLETOKEN"}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset("bedrock")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = ""
	p.AccessKeyID, p.SecretAccessKey, p.SessionToken = up.id, up.secret, up.token
	p.Anthropic, p.Chat, p.Responses = srv.URL+"/anthropic", srv.URL+"/openai/v1", srv.URL+"/openai/v1"
	p.Models = []string{"global.anthropic.claude-opus-5-5", "openai.gpt-oss-120b-1:0"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, body, path2, reply string
		head                           map[string]string
	}{
		{"claude on messages", "/v1/messages",
			`{"model":"bedrock/global.anthropic.claude-opus-5-5","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"/anthropic/v1/messages", "from claude", map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,claude-code-20250219"}},
		{"claude on chat", "/v1/chat/completions",
			`{"model":"bedrock/global.anthropic.claude-opus-5-5","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"/anthropic/v1/messages", "from claude", nil},
		{"gpt-oss on chat", "/v1/chat/completions",
			`{"model":"bedrock/openai.gpt-oss-120b-1:0","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"/openai/v1/chat/completions", "from chat", nil},
		{"gpt-oss on messages", "/v1/messages",
			`{"model":"bedrock/openai.gpt-oss-120b-1:0","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"/openai/v1/chat/completions", "from chat", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postWith(t, tc.path, tc.body, tc.head)
			up.mu2.Lock()
			refused := append([]string(nil), up.refused...)
			up.mu2.Unlock()
			if code != 200 || !strings.Contains(body, tc.reply) || len(refused) > 0 {
				t.Fatalf("status %d: %s\nrefused: %v", code, body, refused)
			}
			c := up.last()
			if c.path != tc.path2 || !strings.HasPrefix(c.head.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") {
				t.Fatalf("upstream %s: %v", c.path, c.head)
			}
		})
	}
}
