package gateway

import (
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
