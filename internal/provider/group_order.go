package provider

import (
	"fmt"
	"slices"
)

// orderedGroups is gs in the order the user put them in on the Routing page
// (#779): those order names first, as it has them, then the rest as they
// came (the user's, then the found ones). Every list of the groups is made
// from groupsIn, so the gateway's /v1/models, which agents' pickers show
// and some clients probe the first model of, follows it too.
func orderedGroups(gs []Group, order []string) []Group {
	if len(order) == 0 {
		return gs
	}
	at := map[string]int{}
	for i, id := range order {
		if _, dup := at[id]; !dup {
			at[id] = i
		}
	}
	rank := func(g Group) int {
		if i, ok := at[g.ID]; ok {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(gs, func(a, b Group) int { return rank(a) - rank(b) })
	return gs
}

// SetGroupOrder saves the order the routing groups are listed in, by id.
// Every id must be a group's, once; the ones left out follow those named,
// as they are now, and the whole order is kept, so a group made or found
// later goes after them all.
func SetGroupOrder(ids []string) error {
	f, err := read()
	if err != nil {
		return err
	}
	all := groupsIn(providerEntries())
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("%s is in the order twice", id)
		}
		seen[id] = true
		if !slices.ContainsFunc(all, func(g Group) bool { return g.ID == id }) {
			return fmt.Errorf("no group %q", id)
		}
	}
	order := slices.Clone(ids)
	for _, g := range all {
		if !seen[g.ID] {
			order = append(order, g.ID)
		}
	}
	f.GroupOrder = order
	return store(f)
}

// StoredGroupOrder is the order the user put the groups in, as saved; as
// with Stored, a file that can't be read is an error.
func StoredGroupOrder() ([]string, error) {
	f, err := read()
	return f.GroupOrder, err
}

// MirrorGroupOrder makes the groups' order the one another computer has,
// as sync brings it, the way MirrorOrder does the providers': its ids
// first, then the ones named here alone. None leaves the order here as it
// is.
func MirrorGroupOrder(order []string) error {
	if len(order) == 0 {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	out := []string{}
	seen := map[string]bool{}
	for _, id := range slices.Concat(order, f.GroupOrder) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if slices.Equal(out, f.GroupOrder) {
		return nil
	}
	f.GroupOrder = out
	return store(f)
}
