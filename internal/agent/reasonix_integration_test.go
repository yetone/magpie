package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// An opt-in contract check against the released client. Everything it reads and
// writes is isolated; the upstream is a fixture, with no vendor credentials.
func TestReasonixStudioCLIIntegration(t *testing.T) {
	bin := os.Getenv("MAGPIE_TEST_REASONIX_CLI")
	if bin == "" {
		t.Skip("set MAGPIE_TEST_REASONIX_CLI to a Studio 2.x native CLI")
	}
	if !reasonixCLI(bin) {
		t.Fatal("integration client must be a native Reasonix 2.x CLI")
	}
	a, _ := reasonixFixture(t, strings.Split(reasonixNativeConfig, "[plugins]")[0])
	t.Setenv(reasonixKey, "unused-shell-credential")
	t.Setenv("REASONIX_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	work := t.TempDir()
	const sentinel = "MAGPIE_REASONIX_TOOL_RESULT"
	// low is also the SDK's fallback for this declared list, so it cannot
	// prove that the model-specific setting survived SDK override resolution.
	const expectedEffort = "xhigh"
	if err := os.WriteFile(filepath.Join(work, "sentinel.txt"), []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests int
	var toolResult bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Effort   string `json:"reasoning_effort"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode client request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		if req.Model != "pro" || !req.Stream || req.Effort != expectedEffort || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("wire contract: model=%q stream=%v effort=%q", req.Model, req.Stream, req.Effort)
		}
		for _, msg := range req.Messages {
			if msg.Role == "tool" && strings.Contains(string(msg.Content), sentinel) {
				toolResult = true
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(delta any, finish any) {
			chunk := map[string]any{"id": "reasonix-contract", "object": "chat.completion.chunk", "model": "pro", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		if toolResult {
			send(map[string]any{"content": "REASONIX_"}, nil)
			send(map[string]any{"content": "INTEGRATION_OK"}, nil)
			send(map[string]any{}, "stop")
		} else {
			found := false
			for _, tool := range req.Tools {
				found = found || tool.Function.Name == "read_file"
			}
			if !found {
				t.Error("client did not expose read_file")
			}
			send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "read_sentinel", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":`}}}}, nil)
			send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": `"sentinel.txt"}`}}}}, nil)
			send(map[string]any{}, "tool_calls")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	if err := provider.Save(provider.Provider{ID: "a", Chat: upstream.URL + "/v1", Key: "upstream-secret", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))
	if err := a.Field("model").Set("magpie/group/code"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("effort").Set(expectedEffort); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, "run", "--print", "--preset", "light", "--max-steps", "4", "--permission-mode", "read-only", "--dir", work, "Read sentinel.txt using read_file, then report the verification result.")
	cmd.Dir = work
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("released CLI: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "REASONIX_INTEGRATION_OK") {
		t.Fatalf("streamed reply was not assembled: %s", out)
	}
	mu.Lock()
	if requests < 2 || !toolResult {
		t.Errorf("stream/tool round trip: requests=%d result=%v", requests, toolResult)
	}
	mu.Unlock()
	for _, call := range s.Recent() {
		if call.Agent != "reasonix" || call.Provider != "a" || call.Status != http.StatusOK {
			t.Errorf("gateway attribution: agent=%q provider=%q status=%d error=%q", call.Agent, call.Provider, call.Status, call.Error)
		}
	}
}
