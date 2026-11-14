package gateway

import (
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// openRouterHarness403 is OpenRouter's refusal of a free model to a
// request not from an agent app it lists, the reporter's words
// (Wade111999 on X) with the advice other clients quote after them.
const openRouterHarness403 = `{"error":{"message":"thinkingmachines/inkling-2-small:free is only available on agentic harnesses. Try plugging it into a coding agent or productivity app listed on https://openrouter.ai/apps","code":403}}`

// OpenRouter turning a free model away from a request's app sets nothing
// aside: not the key, whose other models answer as before, nor the model,
// which an agent app OpenRouter lists is served. A key's own 403 still
// rests the key.
func TestOpenRouterHarnessOnlyRestsNothing(t *testing.T) {
	clineRestsCleared(t)
	s := &Server{}
	or := provider.Provider{ID: "openrouter", Preset: "openrouter", Chat: "https://openrouter.ai/api/v1", Key: "sk-or-v1-test"}
	at := func(model string) candidate { return candidate{p: or, model: model, rest: or.ID} }
	gated := at("thinkingmachines/inkling-2-small:free")
	r := s.restAfter(gated, http.StatusForbidden, http.Header{}, []byte(openRouterHarness403))
	if r.By != "off" {
		t.Fatalf("the harness gate rests %+v", r)
	}
	for _, m := range []string{"thinkingmachines/inkling-2-small:free", "thinkingmachines/inkling-2-small", "deepseek/deepseek-v4-flash:free"} {
		if resting(at(m)) {
			t.Fatalf("%s rests after the harness gate: %v", m, rests())
		}
	}
	if !retryable(http.StatusForbidden, []byte(openRouterHarness403)) {
		t.Fatal("a group's next member isn't asked after the harness gate")
	}
	// the boundary: a 403 about the key rests it
	s.restAfter(gated, http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"Key disabled","code":403}}`))
	if !resting(at("deepseek/deepseek-v4-flash:free")) {
		t.Fatal("a disabled key's 403 no longer rests the key")
	}
}
