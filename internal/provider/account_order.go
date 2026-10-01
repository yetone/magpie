package provider

import "fmt"

// SetAccountOrder changes the order used by routing, not just its presentation.
// The first enabled account becomes first through the same switch used by Make
// first. Disabled accounts can be moved, but never silently enabled by a drag.
func SetAccountOrder(id string, order []string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.IsPlugin() {
		// a plugin's accounts are listed and switched by the provider's id,
		// and kept in logins.json as plugin:<its id>
		return arrangeLoginsAs(p.ID, pluginAgent(*p.Account.plugin), order)
	}
	if p.Account != nil {
		return arrangeLogins(p.Account.Agent, order)
	}
	var available []string
	keys := map[string]KeyAccount{}
	if p.Key != "" {
		keys[keyID(p.Key)] = p.first()
	}
	for _, k := range p.Keys {
		keys[keyID(k.Key)] = k
	}
	for _, k := range p.KeyList() {
		available = append(available, k.ID)
	}
	if err := validateAccountOrder(available, order); err != nil {
		return err
	}
	if keys[order[0]].Off {
		return fmt.Errorf("turn this account on before moving it first")
	}
	p.setFirst(keys[order[0]])
	p.Keys = nil
	for _, ref := range order[1:] {
		p.Keys = append(p.Keys, keys[ref])
	}
	return Save(*p)
}

func arrangeLogins(agent string, order []string) error {
	return arrangeLoginsAs(agent, agent, order)
}

// arrangeLoginsAs arranges the accounts agent lists and switches, saved in
// logins.json under stored.
func arrangeLoginsAs(agent, stored string, order []string) error {
	var available []string
	var first Login
	for _, l := range Logins(agent) {
		available = append(available, l.User)
		if len(order) > 0 && l.User == order[0] {
			first = l
		}
	}
	if err := validateAccountOrder(available, order); err != nil {
		return err
	}
	if !first.Active && !first.On {
		return fmt.Errorf("turn this account on before moving it first")
	}
	// Save ranks under the login lock, without holding it across SwitchLogin
	// (which takes that lock itself). A failed switch restores only the ranks,
	// never an old copy of tokens another request may have just refreshed.
	loginsMu.Lock()
	ls := readLogins()
	before := map[string]int{}
	rank := map[string]int{}
	for i, user := range order {
		rank[user] = i + 1
	}
	for i := range ls {
		if ls[i].Agent == stored {
			before[ls[i].User] = ls[i].Order
			ls[i].Order = rank[ls[i].User]
		}
	}
	for _, user := range order {
		if _, ok := before[user]; !ok {
			loginsMu.Unlock()
			return fmt.Errorf("accounts changed; reopen the provider and try again")
		}
	}
	err := writeLogins(ls)
	loginsMu.Unlock()
	if err != nil || first.Active {
		return err
	}
	if err = SwitchLogin(agent, order[0]); err == nil {
		return nil
	}
	loginsMu.Lock()
	ls = readLogins()
	for i := range ls {
		if ls[i].Agent == stored {
			ls[i].Order = before[ls[i].User]
		}
	}
	restoreErr := writeLogins(ls)
	loginsMu.Unlock()
	if restoreErr != nil {
		return fmt.Errorf("%w; restoring account order: %v", err, restoreErr)
	}
	return err
}

func validateAccountOrder(available, order []string) error {
	if len(available) != len(order) || len(order) == 0 {
		return fmt.Errorf("accounts changed; reopen the provider and try again")
	}
	ids := make(map[string]bool, len(available))
	for _, id := range available {
		ids[id] = true
	}
	for _, id := range order {
		if !ids[id] {
			return fmt.Errorf("unknown or repeated account; reopen the provider and try again")
		}
		delete(ids, id)
	}
	return nil
}
