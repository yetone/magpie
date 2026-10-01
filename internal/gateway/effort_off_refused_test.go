package gateway

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code's auto mode classifier on Command Code's DeepSeek V4.1 Flash
// Fast through its plugin (#394): its levels borrowed from those listing it
// (none, low, high, max), the classifier asked for none, which Command Code
// turns away naming the levels it takes. Asked again at low, and at low
// from then on.
func TestTranslatedEffortNoneRefused(t *testing.T) {
	var sent []any
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"<block>no"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	f.refuse = func(b []byte) (int, string) {
		var v map[string]any
		json.Unmarshal(b, &v)
		sent = append(sent, v["reasoning_effort"])
		if e, ok := v["reasoning_effort"].(string); ok && !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, e) {
			return 400, `{"error":{"message":"Invalid option: expected one of \"low\"|\"medium\"|\"high\"|\"xhigh\"|\"max\"","type":"invalid_request_error"}}`
		}
		return 0, ""
	}
	up := setup(t, provider.Chat, f)
	if err := catalog.SaveLive("fake", up.URL+"/v1", []catalog.Model{{ID: "m1", Context: 128000, Efforts: []string{"none", "low", "high", "max"}}}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	for turn, want := range [][]any{{"none", "low"}, {"low"}} {
		sent = nil
		code, body := postTo(t, srv, "/v1/messages", autoModeAsk("m1"))
		if code != 200 || !saidNo(body) {
			t.Fatalf("turn %d: %d %s", turn+1, code, body)
		}
		if !slices.Equal(sent, want) {
			t.Errorf("turn %d: efforts sent %v, want %v", turn+1, sent, want)
		}
	}
}

// Asking for low can fit back to minimal when the borrowed catalog offers
// minimal and high. Once off is refused, both the retry and later requests
// must pick a level that enables reasoning.
func TestTranslatedEffortMinimalRefused(t *testing.T) {
	for _, tt := range []struct {
		name          string
		levels        []string
		first, retry  string
		rejectEnabled bool
	}{
		{"minimal-high", []string{"minimal", "high"}, "minimal", "high", false},
		{"minimal-only", []string{"minimal"}, "minimal", "low", false},
		{"none-minimal-high", []string{"none", "minimal", "high"}, "none", "high", false},
		{"enabled-also-refused", []string{"minimal", "high"}, "minimal", "high", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sent []string
			f := &fake{t: t, reply: sse(
				`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"<block>no"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`)}
			f.refuse = func(b []byte) (int, string) {
				var v struct {
					Effort string `json:"reasoning_effort"`
				}
				json.Unmarshal(b, &v)
				sent = append(sent, v.Effort)
				if len(sent) > 2 {
					// Bound the test if a regression keeps retrying off.
					return 400, `{"error":{"message":"retry did not make progress"}}`
				}
				if v.Effort == "none" || v.Effort == "minimal" || tt.rejectEnabled {
					return 400, `{"error":{"message":"Invalid option: expected one of \"low\"|\"medium\"|\"high\"|\"max\"","type":"invalid_request_error"}}`
				}
				return 0, ""
			}
			up := setup(t, provider.Chat, f)
			if err := catalog.SaveLive("fake", up.URL+"/v1", []catalog.Model{{ID: "m1", Context: 128000, Efforts: tt.levels}}); err != nil {
				t.Fatal(err)
			}
			srv := New()
			for turn, want := range [][]string{{tt.first, tt.retry}, {tt.retry}} {
				sent = nil
				code, body := postTo(t, srv, "/v1/messages", autoModeAsk("m1"))
				if tt.rejectEnabled {
					if code != 400 {
						t.Errorf("turn %d: %d %s", turn+1, code, body)
					}
				} else if code != 200 || !saidNo(body) {
					t.Errorf("turn %d: %d %s", turn+1, code, body)
				}
				if !slices.Equal(sent, want) {
					t.Errorf("turn %d: efforts sent %v, want %v", turn+1, sent, want)
				}
			}
		})
	}
}

func TestTranslatedEffortMinimalAccepted(t *testing.T) {
	var sent []string
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"<block>no"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	f.refuse = func(b []byte) (int, string) {
		var v struct {
			Effort string `json:"reasoning_effort"`
		}
		json.Unmarshal(b, &v)
		sent = append(sent, v.Effort)
		return 0, ""
	}
	up := setup(t, provider.Chat, f)
	if err := catalog.SaveLive("fake", up.URL+"/v1", []catalog.Model{{ID: "m1", Context: 128000, Efforts: []string{"minimal", "high"}}}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	for turn := range 2 {
		code, body := postTo(t, srv, "/v1/messages", autoModeAsk("m1"))
		if code != 200 || !saidNo(body) {
			t.Fatalf("turn %d: %d %s", turn+1, code, body)
		}
	}
	if !slices.Equal(sent, []string{"minimal", "minimal"}) {
		t.Errorf("efforts sent %v, want minimal on each request", sent)
	}
}
