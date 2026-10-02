package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestOTelExportsGatewayUsageWithoutContent(t *testing.T) {
	f := &fake{ctype: "application/json", reply: `{"id":"c1","model":"m1","choices":[{"message":{"role":"assistant","content":"PRIVATE-REPLY"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`}
	setup(t, provider.Chat, f)
	received := make(chan string, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	t.Setenv("MAGPIE_OTEL_ENABLED", "true")
	t.Setenv("MAGPIE_OTEL_ENDPOINT", collector.URL)
	t.Setenv("MAGPIE_OTEL_HEADERS", "")
	t.Setenv("MAGPIE_OTEL_METRICS", "false")
	stop := usage.StartOTel()
	t.Cleanup(stop)
	code, body := post(t, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"PRIVATE-PROMPT"}]}`)
	if code != 200 || !strings.Contains(body, "PRIVATE-REPLY") {
		t.Fatalf("gateway: %d %s", code, body)
	}
	stop()
	select {
	case exported := <-received:
		if strings.Contains(exported, "PRIVATE-") || !strings.Contains(exported, `"magpie.route.id"`) || !strings.Contains(exported, `"intValue":"10"`) {
			t.Fatalf("export: %s", exported)
		}
	case <-time.After(time.Second):
		t.Fatal("gateway usage was not exported")
	}
}

// with bodies on (#538) the span carries the request and the reply as
// Langfuse's observation input and output — a stream's text put together,
// a secret in the request still masked — and with it off neither
func TestOTelExportsBodiesWhenOn(t *testing.T) {
	stream := "data: {\"id\":\"c1\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"PRIVATE-\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"REPLY\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"model\":\"m1\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n"
	for _, c := range []struct {
		name, bodies, ctype, reply, request string
	}{
		{"off", "false", "application/json", `{"id":"c1","model":"m1","choices":[{"message":{"role":"assistant","content":"PRIVATE-REPLY"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, `{"model":"m1","messages":[{"role":"user","content":"PRIVATE-PROMPT"}]}`},
		{"on", "true", "application/json", `{"id":"c1","model":"m1","choices":[{"message":{"role":"assistant","content":"PRIVATE-REPLY"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, `{"model":"m1","messages":[{"role":"user","content":"PRIVATE-PROMPT"}]}`},
		{"on, streamed", "true", "text/event-stream", stream, `{"model":"m1","stream":true,"messages":[{"role":"user","content":"PRIVATE-PROMPT"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{ctype: c.ctype, reply: c.reply}
			setup(t, provider.Chat, f)
			received := make(chan string, 1)
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				received <- string(b)
				io.WriteString(w, `{}`)
			}))
			defer collector.Close()
			t.Setenv("MAGPIE_OTEL_ENABLED", "true")
			t.Setenv("MAGPIE_OTEL_ENDPOINT", collector.URL)
			t.Setenv("MAGPIE_OTEL_HEADERS", "")
			t.Setenv("MAGPIE_OTEL_METRICS", "false")
			t.Setenv("MAGPIE_OTEL_BODIES", c.bodies)
			stop := usage.StartOTel()
			t.Cleanup(stop)
			secret := "sk-ant-api03-" + strings.Repeat("Q7x", 30)
			request := strings.Replace(c.request, `"model":"m1"`, `"model":"m1","metadata":{"api_key":"`+secret+`"}`, 1)
			if code, body := post(t, "/v1/chat/completions", request); code != 200 || !strings.Contains(body, "REPLY") {
				t.Fatalf("gateway: %d %s", code, body)
			}
			stop()
			var exported string
			select {
			case exported = <-received:
			case <-time.After(time.Second):
				t.Fatal("gateway usage was not exported")
			}
			if strings.Contains(exported, secret) {
				t.Fatalf("secret exported: %s", exported)
			}
			attrs := map[string]string{}
			var wire struct {
				ResourceSpans []struct {
					ScopeSpans []struct {
						Spans []struct {
							Attributes []struct {
								Key   string            `json:"key"`
								Value map[string]string `json:"value"`
							} `json:"attributes"`
						} `json:"spans"`
					} `json:"scopeSpans"`
				} `json:"resourceSpans"`
			}
			if err := json.Unmarshal([]byte(exported), &wire); err != nil {
				t.Fatal(err)
			}
			for _, a := range wire.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes {
				attrs[a.Key] = a.Value["stringValue"]
			}
			in, out := attrs["langfuse.observation.input"], attrs["langfuse.observation.output"]
			if c.bodies == "false" {
				if in != "" || out != "" || strings.Contains(exported, "PRIVATE-") {
					t.Fatalf("bodies exported while off: %s", exported)
				}
				return
			}
			if !strings.Contains(in, `"content":"PRIVATE-PROMPT"`) {
				t.Fatalf("input: %q", in)
			}
			if c.ctype == "text/event-stream" {
				if out != "PRIVATE-REPLY" {
					t.Fatalf("streamed output: %q", out)
				}
			} else if !strings.Contains(out, `"content":"PRIVATE-REPLY"`) {
				t.Fatalf("output: %q", out)
			}
		})
	}
}

// with bodies whole (#538) the export carries the request and reply entire,
// not the 256 KiB the Recent-calls page keeps, and no cut mark; with it off
// the first 256 KiB of each goes, cut, as before
func TestOTelExportsWholeBodiesWhenOn(t *testing.T) {
	big := strings.Repeat("A", 300<<10)
	reply := `{"id":"c1","model":"m1","choices":[{"message":{"role":"assistant","content":"` + big + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	request := `{"model":"m1","messages":[{"role":"user","content":"` + big + `"}]}`
	for _, c := range []struct {
		name  string
		whole bool
		cut   bool
	}{
		{"cut", false, true},
		{"whole", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{ctype: "application/json", reply: reply}
			setup(t, provider.Chat, f)
			received := make(chan string, 1)
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				received <- string(b)
				io.WriteString(w, `{}`)
			}))
			defer collector.Close()
			t.Setenv("MAGPIE_OTEL_ENABLED", "true")
			t.Setenv("MAGPIE_OTEL_ENDPOINT", collector.URL)
			t.Setenv("MAGPIE_OTEL_HEADERS", "")
			t.Setenv("MAGPIE_OTEL_METRICS", "false")
			t.Setenv("MAGPIE_OTEL_BODIES", "true")
			t.Setenv("MAGPIE_OTEL_BODIES_WHOLE", fmt.Sprintf("%t", c.whole))
			stop := usage.StartOTel()
			t.Cleanup(stop)
			s := New()
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(request)))
			if response.Code != 200 || !strings.Contains(response.Body.String(), big) {
				t.Fatalf("gateway: %d", response.Code)
			}
			recent := s.Recent()
			if len(recent) != 1 {
				t.Fatalf("recent calls: %d, want 1", len(recent))
			}
			if recent[0].otelIn != nil || recent[0].otelOut != nil {
				t.Fatal("Recent calls retained whole OTel bodies")
			}
			if len(recent[0].RequestBody) != callBodyLimit || len(recent[0].ResponseBody) != callBodyLimit || !recent[0].RequestTruncated || !recent[0].ResponseTruncated {
				t.Fatal("Recent calls must retain only the truncated diagnostic bodies")
			}
			stop()
			var exported string
			select {
			case exported = <-received:
			case <-time.After(3 * time.Second):
				t.Fatal("gateway usage was not exported")
			}
			attrs := otelSpanAttrs(t, exported)
			in, out := attrs["langfuse.observation.input"], attrs["langfuse.observation.output"]
			if c.cut {
				if !strings.Contains(in, usage.BodyCut) || !strings.Contains(out, usage.BodyCut) {
					t.Fatalf("expected cut bodies, got in=%d out=%d", len(in), len(out))
				}
				return
			}
			if strings.Contains(in, usage.BodyCut) || strings.Contains(out, usage.BodyCut) {
				t.Fatalf("whole bodies were cut")
			}
			if len(in) < len(big) || len(out) < len(big) {
				t.Fatalf("short bodies: in=%d out=%d want at least %d", len(in), len(out), len(big))
			}
		})
	}
}

// otelSpanAttrs is the first span's string attributes in one exported batch.
func otelSpanAttrs(t *testing.T, exported string) map[string]string {
	t.Helper()
	var wire struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					Attributes []struct {
						Key   string            `json:"key"`
						Value map[string]string `json:"value"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal([]byte(exported), &wire); err != nil {
		t.Fatal(err)
	}
	attrs := map[string]string{}
	for _, a := range wire.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes {
		attrs[a.Key] = a.Value["stringValue"]
	}
	return attrs
}
