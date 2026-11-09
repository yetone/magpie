package provider

import "testing"

// The Providers page asks every provider whether it is a Tencent Token
// Plan or OpenCode's gateway; a base of another vendor's is told apart
// without parsing it.
func TestOtherVendorsBasesAreNotParsed(t *testing.T) {
	p := Provider{ID: "x", Chat: "https://api.example.com/v1", Responses: "https://api.example.com/v1", Anthropic: "https://api.example.com/anthropic"}
	if n := testing.AllocsPerRun(50, func() { tencentPlanRegion(p) }); n != 0 {
		t.Errorf("tencentPlanRegion of another vendor's provider allocated %v times a call", n)
	}
	if n := testing.AllocsPerRun(50, func() { openCodeGateway(p.Chat) }); n != 0 {
		t.Errorf("openCodeGateway of another vendor's base allocated %v times a call", n)
	}
	// and still what they are about, whatever its case
	if r, ok := tencentPlanRegion(Provider{Chat: "https://TokenHub.TencentMaaS.com/plan/v3"}); !ok || r != "ap-guangzhou" {
		t.Errorf("Tencent's plan base: %q %v", r, ok)
	}
	if g := openCodeGateway("https://OpenCode.ai/zen/go/v1"); g != "go" {
		t.Errorf("OpenCode Go's base: %q", g)
	}
}
