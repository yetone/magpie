package gateway

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// codexFastAsk is a turn as Codex sends it in Fast mode (or Ultrafast), web
// search offered as on every turn, which has magpie translate it for a
// provider that doesn't search by itself.
func codexFastAsk(tier string) string {
	return fmt.Sprintf(`{"model":"m1","instructions":"You are Codex.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"web_search","external_web_access":true}],"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"high","summary":"auto"},"store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"019a0c1e-thread","service_tier":%q}`, tier)
}

// A provider the user added by its address is sent the tier Codex asked
// for, Fast's and Ultrafast's, on its Responses API and on its Chat API
// (hsiangron on X: both were left out).
func TestOwnProviderGetsTier(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Responses, provider.Chat} {
		for _, tier := range []string{"priority", "ultrafast", "flex"} {
			t.Run(string(proto)+"/"+tier, func(t *testing.T) {
				f := &fake{t: t, reply: optionalFieldReply(proto)}
				setup(t, proto, f)
				code, reply := postTo(t, New(), "/v1/responses", codexFastAsk(tier))
				if code != 200 {
					t.Fatalf("%d: %s", code, reply)
				}
				var sent map[string]any
				json.Unmarshal(f.got, &sent)
				if sent["service_tier"] != tier {
					t.Errorf("tier %v sent, want %s: %s", sent["service_tier"], tier, f.got)
				}
			})
		}
	}
}

// One of a preset is sent no tier: a relay magpie knows may refuse it.
func TestPresetProviderGetsNoTier(t *testing.T) {
	f := &fake{t: t, reply: optionalFieldReply(provider.Chat)}
	setup(t, provider.Chat, f)
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	p.Preset = "openrouter"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if code, reply := postTo(t, New(), "/v1/responses", codexFastAsk("priority")); code != 200 {
		t.Fatalf("%d: %s", code, reply)
	}
	var sent map[string]any
	json.Unmarshal(f.got, &sent)
	if _, ok := sent["service_tier"]; ok {
		t.Errorf("tier sent to a preset: %s", f.got)
	}
}

// One that turns the field away is asked again without it, and not sent
// it again.
func TestOwnProviderRefusingTier(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Responses, provider.Chat} {
		t.Run(string(proto), func(t *testing.T) {
			f := &fake{t: t, reply: optionalFieldReply(proto)}
			refused := 0
			f.refuse = func(b []byte) (int, string) {
				var v map[string]json.RawMessage
				json.Unmarshal(b, &v)
				if _, ok := v["service_tier"]; ok {
					refused++
					return 400, `{"error":{"message":"Unsupported parameter: 'service_tier' is not supported with this model.","param":"service_tier","code":"unsupported_parameter"}}`
				}
				return 0, ""
			}
			setup(t, proto, f)
			srv := New()
			for i := range 2 {
				if code, reply := postTo(t, srv, "/v1/responses", codexFastAsk("priority")); code != 200 {
					t.Fatalf("turn %d: %d: %s", i, code, reply)
				}
			}
			if refused != 1 || f.calls != 3 {
				t.Errorf("refused %d times in %d calls, want once in 3", refused, f.calls)
			}
		})
	}
}
