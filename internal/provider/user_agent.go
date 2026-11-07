package provider

// PassesUserAgent says a request to p goes with the User-Agent of the agent
// that sent it (#1851): p is a key+URL provider, and the user didn't set
// it to go as magpie (MagpieUserAgent). A signed-in account and a
// plugin's provider sign as their own client; OpenCode's, Cline's and
// Kilo's gateways are sent the client they serve (OwnClient), and another
// magpie is told magpie by it: none of them takes the agent's in its place.
func (p Provider) PassesUserAgent() bool {
	return !p.MagpieUserAgent && p.Account == nil && !p.OwnClient()
}

// OwnClient says magpie's requests to p go as a client the vendor serves,
// whatever agent sent them: OpenCode's gateway is sent OpenCode's
// User-Agent, Cline's Cline's, Kilo's the Kilo CLI's, and another magpie
// magpie's.
func (p Provider) OwnClient() bool {
	return p.IsOpenCode() || p.IsCline() || p.IsKilo() || p.IsRemoteMagpie()
}
