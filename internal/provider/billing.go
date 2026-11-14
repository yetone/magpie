package provider

import "strings"

// How a provider bills (EV_John_Liu on X: mark pay as you go apart from a
// token plan on Providers and Routing): on a plan, a subscription or
// token package paid ahead, or pay as you go, each call billed as used.
// The Providers list and a routing group's card say it. The user's word
// (Provider.Billing) stands; else it is read from what magpie knows of
// the provider: a signed-in account is a plan, a preset's plan or pay as
// you go endpoints (its Regions) say theirs, a key of a vendor or relay
// is pay as you go. A provider added by its address, a local server or
// another magpie says nothing until the user sets it.
const (
	BillingPlan  = "plan"
	BillingPAYG  = "payg"
	BillingMixed = "mixed" // its keys are on a plan and on pay as you go
)

// BillingOK reports whether s is a billing the user may set: plan, pay
// as you go, or "" for magpie to read it.
func BillingOK(s string) bool { return s == "" || s == BillingPlan || s == BillingPAYG }

// planPresets are presets whose keys are a plan's alone, with no pay as
// you go endpoints of their own to tell them apart by.
var planPresets = map[string]bool{
	"kimi-code": true, "kimi-code-cn": true,
	"qwen-token-plan": true, "bailian-token-plan": true, "alibaba-token-plan": true, "qwencloud-token-plan": true,
	"opencode-go": true, "clinepass": true,
}

// unsaidPresets are vendors and relays whose one endpoint takes a plan's
// keys and pay as you go's alike, or that are free, so magpie can't say
// which a key is (MiniMax's Coding Plan keys, ModelScope's daily quota,
// an AIGoCode key spending its plan and then its balance),
// and decision models, which bill as their own provider does.
var unsaidPresets = map[string]bool{
	"minimax": true, "minimax-cn": true, "ollama-cloud": true, "modelscope": true,
	"nvidia": true, "longcat": true, "commandcode": true,
	"typesafe": true, "vercel-jev": true, "cloudflare-jev": true, "aigocode": true,
	"bailian-decision": true, "qwen-decision": true, "volcengine-decision": true,
}

// regionBilling is what a preset's region's name says of its billing:
// "Pay as you go …" or "… Plan …"; "" for one named by place.
func regionBilling(r *Region) string {
	if r == nil {
		return ""
	}
	n := strings.ToLower(r.Name)
	switch {
	case strings.HasPrefix(n, "pay as you go"):
		return BillingPAYG
	case strings.Contains(n, "plan"):
		return BillingPlan
	}
	return ""
}

// BillingKind is how p bills: the user's word, else what magpie reads of
// it (BillingPlan, BillingPAYG, BillingMixed), "" when it can't say.
func (p Provider) BillingKind() string {
	if p.Billing != "" {
		return p.Billing
	}
	if p.Account != nil {
		return BillingPlan
	}
	pr := Preset(p.Preset)
	if pr == nil || unsaidPresets[pr.ID] || strings.HasSuffix(pr.ID, "-decision") || (pr.Kind != KindVendor && pr.Kind != KindRelay) {
		return ""
	}
	if planPresets[pr.ID] {
		return BillingPlan
	}
	// the region the provider sits at: a plan or pay as you go by its
	// name, the vendor's API (billed as used) when named by place; one
	// asked at none of its regions' endpoints can't be told
	own := BillingPAYG
	if len(pr.Regions) > 0 {
		r := p.regionOf(pr)
		if r == nil {
			return ""
		}
		if b := regionBilling(r); b != "" {
			own = b
		}
	}
	// keys on another plan than the provider's (KeyAccount.Plan)
	if p.plansMixed() {
		for _, plan := range append([]string{p.KeyPlan}, keyPlans(p)...) {
			if _, r, _ := p.keyRegion(plan); r != nil {
				if b := regionBilling(r); b != "" && b != own {
					return BillingMixed
				}
			}
		}
	}
	return own
}

// keyPlans are the plans p's other keys in use say they are for.
func keyPlans(p Provider) []string {
	var out []string
	for _, k := range p.Keys {
		if k.Off || k.Key == "" || k.Key == p.Key || k.Plan == "" {
			continue
		}
		out = append(out, k.Plan)
	}
	return out
}
