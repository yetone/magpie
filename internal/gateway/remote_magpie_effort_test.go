package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A budget that reads as max beside a turn that asked high: the first
// magpie must not write the budget's level as a top-level effort, which the
// next magpie reads before the turn's (DIYgod on X).
func TestRemoteMagpieKeepsTurnEffortOverBudget(t *testing.T) {
	fresh(t)
	got := make(chan []byte, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case got <- b:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(relay.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Key: "relay-key", Models: []string{"claude-opus-5-5"}, Anthropic: relay.URL}); err != nil {
		t.Fatal(err)
	}
	hop := make(chan []byte, 1)
	rh := New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			select {
			case hop <- b:
			default:
			}
		}
		rh.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Chat: remote.URL, Key: "remote-key"}); err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find("office")
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"office/relay/claude-opus-5-5","max_tokens":40000,"thinking":{"type":"enabled","budget_tokens":31999},"messages":[{"role":"user","content":"hi"},{"role":"system","content":[],"output_config":{"effort":"high"}}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("User-Agent", "claude-cli/2.1.0 (external, cli)")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	select {
	case b := <-hop:
		if e := gjson.GetBytes(b, "output_config.effort").String(); e != "" {
			t.Errorf("the remote magpie was sent top-level effort %q: %s", e, b)
		}
	default:
		t.Fatal("the remote magpie received no /v1/messages request")
	}
	select {
	case b := <-got:
		if e := requestEffort(provider.Anthropic, b); e != "high" {
			t.Errorf("upstream effort = %q, want high: %s", e, b)
		}
	default:
		t.Fatal("relay received no request")
	}
}

func TestAdaptiveThinkingLeavesTurnEffortTheLevel(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":31999},"messages":[{"role":"user","content":"x"},{"role":"system","content":[],"output_config":{"effort":"high"}}]}`)
	out := adaptiveThinking(body)
	if e := gjson.GetBytes(out, "output_config.effort").String(); e != "" {
		t.Errorf("top-level effort = %q, want none", e)
	}
	if e := requestEffort(provider.Anthropic, out); e != "high" {
		t.Errorf("requestEffort = %q, want high", e)
	}
}
