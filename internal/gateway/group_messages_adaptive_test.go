package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// dsh asks a routing group whose members are each Messages-only Gemini 4s
// (agy-cli's) on its Messages route, adaptively, as it asks such a member
// alone (01huadalang on Discord, #6778). The group's level reaches the
// member it picks as the member alone has it, max as max and xhigh as
// xhigh, thinking still adaptive and the member's own model named.
func TestGroupOnMessagesAsksMemberAdaptively(t *testing.T) {
	up := &adaptiveVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	for _, id := range []string{"relay", "relay2"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Anthropic: srv.URL, Models: []string{"gemini-4-hydrogen"}}); err != nil {
			t.Fatal(err)
		}
		// the levels agy-cli's plugin lists for it
		if err := provider.SetModelEfforts(id+"/gemini-4-hydrogen", []string{"low", "medium", "high", "xhigh", "max"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "h", Name: "H", Members: []string{"relay/gemini-4-hydrogen", "relay2/gemini-4-hydrogen"}}); err != nil {
		t.Fatal(err)
	}
	for _, effort := range []string{"max", "xhigh", "high"} {
		for _, model := range []string{"group/h", "relay/gemini-4-hydrogen"} {
			body := `{"model":"` + model + `","max_tokens":64000,"thinking":{"type":"adaptive"},"output_config":{"effort":"` + effort + `"},"stream":true,"messages":[{"role":"user","content":"hi"}]}`
			rt := postProto(t, New(), "/v1/messages", body)
			got := up.last()
			th, _ := got["thinking"].(map[string]any)
			oc, _ := got["output_config"].(map[string]any)
			if got["model"] != "gemini-4-hydrogen" || th["type"] != "adaptive" || oc["effort"] != effort {
				t.Errorf("%s %s: upstream model %v thinking %v output_config %v", model, effort, got["model"], got["thinking"], got["output_config"])
			}
			if rt.Effort != effort {
				t.Errorf("%s: route effort %q", effort, rt.Effort)
			}
		}
	}
}
