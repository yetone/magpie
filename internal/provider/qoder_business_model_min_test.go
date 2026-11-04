package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// Qoder's plugin before 0.2.61 left the model out of a request's business
// record, which Qoder's own CLI (qodercli 1.1.82) fills with business.model_key,
// and Qoder and Qoder CN booked every call as Auto whatever model_config named
// (plugins#692, chansee97; plugins#638 after 0.2.54). The movers keep every
// moved Qoder and Qoder CN user on a version that sends it.
func TestQoderMoversNameTheModelInTheBusinessRecord(t *testing.T) {
	for _, id := range []string{"qoder", QoderCNID} {
		if update.Newer("0.2.61", movers[id].min) {
			t.Fatalf("the %s mover installs opencode-qoder-auth %s, which leaves business.model_key out", id, movers[id].min)
		}
	}
}
