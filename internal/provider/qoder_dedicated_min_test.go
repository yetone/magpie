package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// Qoder's plugin before 0.2.53 took a Qoder CN enterprise's own domain only
// as a VPC instance (<instance>.vpc.qoder.com.cn), so an enterprise whose
// Qoder IDE signs in to a dedicated domain such as <corp>.rdc.aliyuncs.com
// (企业版专属域) had no way in. 0.2.53 adds that sign-in. The mover keeps
// every moved Qoder CN user on a version that has it.
func TestQoderCNMoverHasTheDedicatedDomainSignIn(t *testing.T) {
	if update.Newer("0.2.53", movers[QoderCNID].min) {
		t.Fatalf("the %s mover installs opencode-qoder-auth %s, which has no enterprise dedicated domain sign-in", QoderCNID, movers[QoderCNID].min)
	}
}
