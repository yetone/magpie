package provider

// TakesPassErrors says PassErrors can be set for p: a provider of an API
// key, a preset vendor's or one the user added. A signed-in account and a
// plugin's provider never take it: magpie reads their errors to keep the
// account well (a sign-in to renew, a plan's window to wait out).
func (p Provider) TakesPassErrors() bool {
	return p.Account == nil && hasEndpoint(p) && !subscriptionID(p.ID)
}

// PassesErrors says the gateway hands p's errors to the agent as they came
// and asks it nothing again (PassErrors).
func (p Provider) PassesErrors() bool {
	return p.PassErrors && p.TakesPassErrors()
}
