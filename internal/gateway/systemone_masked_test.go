package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A System One request is the user's text like a chat's: with Settings'
// masking on, a secret in its state or questions never reaches the
// decision model's vendor, asked at /v1/systemone or through /v1/decisions
// translated to it (c7810ea5c), which was masked on its own route before.
// A decision model the user set to go unmasked gets it whole.
func TestSystemOneMasked(t *testing.T) {
	secret := "sk-ant-api03-" + strings.Repeat("Ab3x", 20)
	systemOne := `{"model":"qwen-decision/decision-model-preview","state":{"message":"my key is ` + secret + `"},` +
		`"questions":{"leak":{"type":"noul","instructions":"Is ` + secret + ` a key?"}}}`
	decisions := `{"model":"qwen-decision/decision-model-preview","input":"my key is ` + secret + `",` +
		`"questions":[{"type":"predicate","name":"leak","instructions":"Is ` + secret + ` a key?"}]}`
	reply := `{"model":"decision-model-preview","answers":{"leak":{"type":"noul","noul":0.9}},"usage":{"input_tokens":40,"output_tokens":1}}`
	for _, tc := range []struct {
		name, path, body  string
		unredacted, whole bool
	}{
		{name: "systemone", path: "/v1/systemone", body: systemOne},
		{name: "decisions", path: "/v1/decisions", body: decisions},
		{name: "systemone unmasked", path: "/v1/systemone", body: systemOne, unredacted: true, whole: true},
		{name: "decisions unmasked", path: "/v1/decisions", body: decisions, unredacted: true, whole: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			up := &systemOneUp{reply: reply}
			qwenDecision(t, up)
			if tc.unredacted {
				p, err := provider.Find("qwen-decision")
				if err != nil {
					t.Fatal(err)
				}
				p.Unredacted = true
				if err := provider.Save(*p); err != nil {
					t.Fatal(err)
				}
			}
			if err := settings.Save(settings.Settings{Redact: true}); err != nil {
				t.Fatal(err)
			}
			code, out := sendTo(New(), tc.path, tc.body)
			if code != 200 {
				t.Fatalf("%d %s", code, out)
			}
			got := up.asked()
			if len(got) != 1 {
				t.Fatalf("asked %d times", len(got))
			}
			sent, _ := json.Marshal(got[0])
			if whole := strings.Contains(string(sent), secret); whole != tc.whole {
				t.Fatalf("sent whole %v, want %v: %s", whole, tc.whole, sent)
			}
			if !strings.Contains(string(sent), "my key is") {
				t.Fatalf("the state didn't go: %s", sent)
			}
		})
	}
}
