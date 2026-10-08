package provider

import "strings"

// The plan and access SKU are distinct: education can share the individual
// plan without granting the model choices of a paid Pro subscription.
type copilotEntitlement struct {
	Plan      string `json:"copilot_plan"`
	AccessSKU string `json:"access_type_sku"`
}

func (e copilotEntitlement) label() string {
	if e.AccessSKU == "free_educational_quota" {
		return "Education"
	}
	if e.AccessSKU == "free_limited_copilot" {
		return "Free"
	}
	if name := copilotPlans[strings.ToLower(e.Plan)]; name != "" {
		return name
	}
	if e.Plan != "" {
		return strings.ToUpper(e.Plan[:1]) + e.Plan[1:]
	}
	return ""
}

// Refresh only an existing account still using the credential queried. A
// delayed response must not update a replacement login or create an account.
func refreshCopilotEntitlement(app copilotApp, plan, sku string) {
	if plan == "" && sku == "" {
		return
	}
	// Resolve external editor/CLI credentials before taking the shared login lock.
	own, ownOK := copilotLogin(copilotConfigDir())
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		l := &ls[i]
		if l.Agent != "copilot" || !strings.EqualFold(l.User, app.User) {
			continue
		}
		current, valid := copilotSaved(*l)
		if l.own() {
			current, valid = own, ownOK
			valid = valid && strings.EqualFold(CopilotAccountName(firstNonEmpty(current.User, "GitHub"), current.Host), app.User)
		}
		if !valid || current.Token != app.Token {
			continue
		}
		if plan == "" {
			plan = l.Plan
		}
		if sku == "" {
			sku = l.AccessSKU
		}
		if l.Plan == plan && l.AccessSKU == sku {
			return
		}
		// Missing fields do not erase the last successful entitlement reading.
		if plan != "" {
			l.Plan = plan
		}
		if sku != "" {
			l.AccessSKU = sku
		}
		_ = writeLogins(ls)
		return
	}
}
