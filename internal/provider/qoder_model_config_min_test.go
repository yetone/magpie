package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// Qoder's plugin before 0.2.54 sent a model's whole model-list entry as the
// request's model_config, which Qoder's own client never does, and Qoder CN
// billed DeepSeek-Flash (dfmodel) calls as Auto at Auto's rate beside it
// (plugins#638, square-235). 0.2.54 sends model_config as Qoder's client
// builds it and refuses a model with no key of its own. The movers keep
// every moved Qoder and Qoder CN user on a version that has it.
func TestQoderMoversSendTheClientsModelConfig(t *testing.T) {
	for _, id := range []string{"qoder", QoderCNID} {
		if update.Newer("0.2.54", movers[id].min) {
			t.Fatalf("the %s mover installs opencode-qoder-auth %s, which sends the model-list entry as model_config", id, movers[id].min)
		}
	}
}
