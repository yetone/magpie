package provider

// A plugin's search API that signs in (SearchVendor.Auth) searches as the
// same plugin's provider: its accounts, as the user switched them on and
// ordered them for the provider's requests.

// SearchSignIn is what a search API that signs in searches as: the name
// of the provider it signs in to, and that provider as All lists it (its
// first account; AlsoOn the others on), nil while it isn't signed in or
// another plugin serves the id (the user picked another plugin for it).
func SearchSignIn(v SearchVendor) (name string, p *Provider) {
	if v.Auth == "" || v.Plugin == "" {
		return v.Auth, nil
	}
	name = v.Auth
	found := false
	for _, pp := range heldPlugins() {
		if pp.ID == v.Auth && pp.Spec == v.Plugin {
			found = true
			if pp.Name != "" {
				name = pp.Name
			}
		}
	}
	if !found {
		return name, nil
	}
	id := PluginID(v.Auth)
	for _, q := range All() {
		if q.ID == id && q.PluginProvider() == v.Auth && q.Account.plugin.Spec == v.Plugin {
			return name, &q
		}
	}
	return name, nil
}

// PluginAccountKey is the account's key in the plugin host's sign-ins
// (provider or provider#slot), "" for a provider not a plugin's.
func (p Provider) PluginAccountKey() string {
	if !p.IsPlugin() {
		return ""
	}
	return p.Account.pluginKey
}
