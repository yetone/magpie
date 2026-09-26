package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// Routing groups from the terminal: what the Routing view does, through
// the same provider.SaveGroup, DeleteGroup and ShowGroup.

const groupUsage = `usage:
  magpie groups                           list routing groups: yours, then those magpie found
  magpie group <id>                       show one group and its models (also: magpie group show <id>)
  magpie group add <name> models=<m1>[,m2…] [routing=…] [stays=…]
                                          make a group; agents pick it as group/<id>, the id made from the name;
                                          a name in use replaces that group
  magpie group set <id> k=v…              change one: name, models (the whole list, in order),
                                          models+=<m> (append), models-=<m> (drop), routing, stays,
                                          context (how long a request agents are told it takes: 272k; empty is
                                          its shortest model's), family (a tag: magpie visible shows agents
                                          families, not each group),
                                          id (what agents pick it as: id=gpt-6-astra drops auto-; the groups
                                          it is in follow; an agent set to the old id needs setting again),
                                          effort=auto (Jev picks each turn's reasoning; needs classifier=),
                                          effort=agent (the agent's again), classifier=<provider/model>
  magpie group rm <id>                    remove a group (one magpie found is hidden instead)
  magpie group restore <id>               bring back a group magpie found that you removed
  magpie group rule add|rm|mv <id> …      rules: which model a turn goes to first, by its length, an image,
                                          the reasoning asked for or the agent (magpie group rule help)

  magpie finds a group for each model two or more providers serve (auto-<model>, never stored);
  removing one stores {"id":…,"hidden":true} in providers.json, which is what keeps it removed:
  take that record out of the file and the group is back

  models   provider/model ids as magpie models lists them; a bare model id works when one provider serves it;
           group/<id> puts another group in it, routed by its own routing and rules in its place —
           never one the group is in already (that would put it in itself), at most 8 groups deep
  routing  smart   (default) of the subscriptions with quota to spare, the one renewing soonest first
           order   the first model until it can't answer, then the next
           rotate  each conversation's next turn goes to the next member's account or key
           usage   the account or key with the most of its allowance left first
  stays    auto    (default) with the account or key that answered, while its cache is worth keeping
           session for the whole session
           turn    within a turn only; routing decides afresh when you speak again
           off     every request routed afresh
  effort   agent   (default) each request reasons as much as the agent asked
           auto    as a turn begins, the group's classifier — Jev, from a TypeSafe provider
                   (magpie provider add typesafe key=…) — rates how hard it is, and the turn's requests
                   reason at low, medium, high or xhigh; only those the agent asked to reason

  e.g. magpie group add "Opus anywhere" models=claude/claude-opus-5-5,copilot/claude-opus-5.5 routing=order
       magpie group set opus-anywhere stays=session models+=openrouter/anthropic/claude-opus-5.5
       magpie group set auto-gpt-6-astra id=gpt-6-astra
       magpie group add Everything models=group/opus-anywhere,deepseek/deepseek-v4-flash routing=order
       magpie group set opus-anywhere effort=auto classifier=typesafe/jev-latest
       magpie claude group/opus-anywhere`

// routingNames: each routing's value in the file, what the CLI calls it,
// and the other spellings it takes.
var routingNames = []struct {
	value, name string
	also        []string
}{
	{"", "smart", []string{"default", "auto"}},
	{provider.Ordered, "order", []string{"ordered", "in-order"}},
	{provider.Rotate, "rotate", []string{"in-turn", "round-robin"}},
	{provider.LeastUsed, "usage", []string{"least-used"}},
}

var staysNames = []struct {
	value, name string
	also        []string
}{
	{"", "auto", []string{"default"}},
	{provider.AffinitySession, "session", nil},
	{provider.AffinityTurn, "turn", []string{"within-a-turn"}},
	{provider.AffinityOff, "off", []string{"none", "never"}},
}

func parseRouting(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	var names []string
	for _, r := range routingNames {
		if v == r.value || v == r.name || slices.Contains(r.also, v) {
			return r.value, nil
		}
		names = append(names, r.name)
	}
	return "", fmt.Errorf("routing %q is not one magpie has: %s", v, strings.Join(names, ", "))
}

func parseStays(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	var names []string
	for _, s := range staysNames {
		if v == s.value || v == s.name || slices.Contains(s.also, v) {
			return s.value, nil
		}
		names = append(names, s.name)
	}
	return "", fmt.Errorf("stays %q is not one magpie has: %s", v, strings.Join(names, ", "))
}

func routingName(v string) string {
	for _, r := range routingNames {
		if r.value == v {
			return r.name
		}
	}
	return "smart"
}

func staysName(v string) string {
	for _, s := range staysNames {
		if s.value == v {
			return s.name
		}
	}
	return "auto"
}

func splitList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
}

// memberResolver spells a model the user typed as the catalog's
// provider/model id: the id itself, or a bare model id one provider
// serves. keep are ids the group has already, kept though no provider
// serves them now (the Routing view keeps them too, skipped).
func memberResolver(keep []string) func(string) (string, error) {
	var ids []string
	byModel := map[string][]string{}
	for _, e := range provider.Served() {
		if e.Group != "" {
			continue
		}
		ids = append(ids, e.ID)
		byModel[strings.ToLower(e.Model)] = append(byModel[strings.ToLower(e.Model)], e.ID)
	}
	return func(in string) (string, error) {
		id := strings.TrimPrefix(strings.TrimSpace(in), "magpie/")
		if gid, ok := strings.CutPrefix(id, provider.GroupPrefix); ok {
			// a group in the group: SaveGroup refuses one it would be in itself through
			for _, g := range provider.Groups() {
				if strings.EqualFold(g.ID, gid) && !g.Hidden {
					return provider.GroupPrefix + g.ID, nil
				}
			}
			return "", fmt.Errorf("magpie has no group %q (magpie groups lists them)", gid)
		}
		if slices.Contains(ids, id) || slices.Contains(keep, id) {
			return id, nil
		}
		for _, x := range ids { // a provider/model id in another case
			if strings.EqualFold(x, id) {
				return x, nil
			}
		}
		switch hits := byModel[strings.ToLower(id)]; len(hits) {
		case 0:
		case 1:
			return hits[0], nil
		default:
			return "", fmt.Errorf("%s is served by %d providers; name one: %s", id, len(hits), strings.Join(hits, ", "))
		}
		msg := fmt.Sprintf("magpie knows no model %q", id)
		if near := closeMatches(id, ids, 6); len(near) > 0 {
			msg += "; did you mean " + strings.Join(near, ", ") + "?"
		}
		return "", fmt.Errorf("%s (magpie models lists them)", msg)
	}
}

// closeMatches are the ids most like what was typed: those containing it,
// then those a few edits away.
func closeMatches(in string, ids []string, n int) []string {
	q := strings.ToLower(in)
	if _, m, ok := strings.Cut(q, "/"); ok && m != "" {
		q = m // the provider may be the part that's wrong
	}
	type hit struct {
		id string
		d  int
	}
	var hits []hit
	for _, id := range ids {
		l := strings.ToLower(id)
		d := editDistance(q, l[strings.Index(l, "/")+1:])
		switch {
		case strings.Contains(l, q):
			d = -1
		case d > max(2, len(q)/3):
			continue
		}
		hits = append(hits, hit{id, d})
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return a.d - b.d })
	var out []string
	for _, h := range hits {
		if len(out) == n {
			break
		}
		out = append(out, h.id)
	}
	return out
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

// applyGroupPairs sets a group's fields from k=v pairs. id is taken only
// where allowID (a new group's).
func applyGroupPairs(g *provider.Group, pairs []string, resolve func(string) (string, error), allowID bool) error {
	members := func(v string) ([]string, error) {
		var out []string
		for _, m := range splitList(v) {
			id, err := resolve(m)
			if err != nil {
				return nil, err
			}
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		return out, nil
	}
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("expected key=value, got %q (magpie group help)", kv)
		}
		var err error
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "id":
			if !allowID {
				return fmt.Errorf("a group's id stays as it is; add a new group for another")
			}
			g.ID = v
		case "name":
			g.Name = v
		case "models", "members", "model":
			g.Members, err = members(v)
		case "models+", "members+", "model+":
			var add []string
			if add, err = members(v); err == nil {
				for _, id := range add {
					if !slices.Contains(g.Members, id) {
						g.Members = append(g.Members, id)
					}
				}
			}
		case "models-", "members-", "model-":
			for _, m := range splitList(v) {
				m = strings.TrimPrefix(m, "magpie/")
				i := slices.IndexFunc(g.Members, func(x string) bool { return strings.EqualFold(x, m) })
				if i < 0 { // a bare model id, as models= takes it
					i = slices.IndexFunc(g.Members, func(x string) bool {
						_, bare, _ := strings.Cut(x, "/")
						return strings.EqualFold(bare, m)
					})
				}
				if i < 0 {
					return fmt.Errorf("%s is not in the group (its models: %s)", m, strings.Join(g.Members, ", "))
				}
				g.Members = slices.Delete(g.Members, i, i+1)
			}
		case "routing", "strategy":
			g.Routing, err = parseRouting(v)
		case "stays", "stay", "affinity":
			g.Affinity, err = parseStays(v)
		case "context":
			// what agents are told the group takes; empty or 0 is its
			// shortest model's again
			g.Context = 0
			if strings.TrimSpace(v) != "" {
				g.Context, err = parseTokens(v)
			}
		case "family", "tag":
			g.Family = strings.TrimSpace(v)
		case "effort", "reasoning":
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "auto", "jev":
				g.Effort = provider.EffortAuto
			case "", "agent", "off":
				g.Effort = ""
			default:
				err = fmt.Errorf("effort=auto (Jev picks each turn's) or effort=agent (the agent's), not %q", v)
			}
		case "classifier", "classify":
			g.Classifier = strings.TrimPrefix(strings.TrimSpace(v), "magpie/")
		default:
			return fmt.Errorf("unknown field %q (fields: name, models, models+, models-, routing, stays, context, family, effort, classifier; magpie group help)", k)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// findGroup looks a group up by its id, its catalog id or its name.
func findGroup(ref string) (provider.Group, error) {
	all := provider.Groups()
	ref = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(ref), "magpie/"), provider.GroupPrefix)
	for _, match := range []func(provider.Group) bool{
		func(g provider.Group) bool { return g.ID == ref },
		func(g provider.Group) bool { return strings.EqualFold(g.ID, ref) },
		func(g provider.Group) bool { return strings.EqualFold(g.Name, ref) && !g.Hidden },
	} {
		if i := slices.IndexFunc(all, match); i >= 0 {
			return all[i], nil
		}
	}
	var ids []string
	for _, g := range all {
		if !g.Hidden {
			ids = append(ids, g.ID)
		}
	}
	if len(ids) == 0 {
		return provider.Group{}, fmt.Errorf("no group %q; there are none yet (magpie group add <name> models=…)", ref)
	}
	return provider.Group{}, fmt.Errorf("no group %q (groups: %s)", ref, strings.Join(ids, ", "))
}

// newGroupID is the id a new group gets, as the Routing view makes it:
// its name's, else "group", numbered past one taken.
func newGroupID(name string) string {
	base := provider.Slug(name)
	if base == "" {
		base = "group"
	}
	taken := map[string]bool{}
	for _, g := range provider.Groups() {
		taken[g.ID] = true
	}
	id := base
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

// groupCmd: `magpie group <verb> …`
func groupCmd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", groupUsage)
	}
	verb, rest := args[1], args[2:]
	switch verb {
	case "-h", "--help", "help":
		fmt.Println(groupUsage)
		return nil
	case "add", "new":
		if len(rest) == 0 || strings.Contains(rest[0], "=") {
			return fmt.Errorf("magpie group add <name> models=<m1>[,m2…] [routing=…] [stays=…]\n\n%s", groupUsage)
		}
		verb := "added"
		if slices.ContainsFunc(provider.Groups(), func(o provider.Group) bool {
			return !o.Hidden && strings.EqualFold(o.Name, strings.TrimSpace(rest[0])) && !slices.ContainsFunc(rest[1:], func(kv string) bool { return strings.HasPrefix(kv, "id=") })
		}) {
			verb = "replaced"
		}
		g, err := addGroup(rest[0], rest[1:])
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), verb, bold.Render(g.Name), muted.Render("· agents pick it as "+provider.GroupPrefix+g.ID))
		return showGroup(g)
	case "set", "edit":
		if len(rest) < 2 {
			return fmt.Errorf("magpie group set <id> k=v…\n\n%s", groupUsage)
		}
		g, err := setGroup(rest[0], rest[1:])
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "saved", bold.Render(g.Name))
		return showGroup(g)
	case "rm", "remove", "delete":
		if len(rest) != 1 {
			return fmt.Errorf("magpie group rm <id>")
		}
		g, err := removeGroup(rest[0])
		if err != nil {
			return err
		}
		if g.Auto {
			fmt.Println(green.Render("✓"), "removed", bold.Render(g.Name), muted.Render("· magpie found it, so it's hidden: magpie group restore "+g.ID+" brings it back"))
		} else {
			fmt.Println(green.Render("✓"), "removed", bold.Render(g.Name))
		}
		return nil
	case "rule", "rules":
		return ruleCmd(rest)
	case "show":
		if len(rest) != 1 {
			return fmt.Errorf("magpie group show <id>")
		}
		g, err := findGroup(rest[0])
		if err != nil {
			return err
		}
		return showGroup(g)
	case "restore", "unhide":
		if len(rest) != 1 {
			return fmt.Errorf("magpie group restore <id>")
		}
		g, err := restoreGroup(rest[0])
		if err != nil {
			return err
		}
		if len(g.Members) == 0 {
			fmt.Println(green.Render("✓"), bold.Render(g.ID), "is no longer removed", muted.Render("· it shows once two providers serve its model"))
			return nil
		}
		fmt.Println(green.Render("✓"), bold.Render(g.Name), "is back")
		return showGroup(g)
	}
	if len(rest) > 0 {
		return fmt.Errorf("magpie group has no %q\n\n%s", verb, groupUsage)
	}
	g, err := findGroup(verb)
	if err != nil {
		return err
	}
	return showGroup(g)
}

func addGroup(name string, pairs []string) (provider.Group, error) {
	g := provider.Group{Name: strings.TrimSpace(name)}
	if err := applyGroupPairs(&g, pairs, memberResolver(nil), true); err != nil {
		return g, err
	}
	if g.ID = strings.ToLower(strings.TrimSpace(g.ID)); g.ID != "" {
		if old, err := findGroup(g.ID); err == nil && old.ID == g.ID {
			return g, fmt.Errorf("there is a group %s already: magpie group set %s k=v… changes it", g.ID, g.ID)
		}
	} else {
		// a group is known by its name: adding one under a name taken
		// replaces that group (its id stays), rather than making name-2
		g.ID = newGroupID(g.Name)
		for _, o := range provider.Groups() {
			if !o.Hidden && strings.EqualFold(o.Name, g.Name) {
				g.ID = o.ID
				break
			}
		}
	}
	if strings.TrimSpace(g.Name) == "" {
		g.Name = g.ID
	}
	if len(g.Members) == 0 {
		return g, fmt.Errorf("a group needs a model in it: models=<provider/model>[,…] (magpie models lists them)")
	}
	if err := provider.SaveGroup(g); err != nil {
		return g, err
	}
	return findGroup(g.ID)
}

func setGroup(ref string, pairs []string) (provider.Group, error) {
	g, err := findGroup(ref)
	if err != nil {
		return g, err
	}
	if g.Hidden {
		return g, fmt.Errorf("%s was removed: magpie group restore %s brings it back first", g.ID, g.ID)
	}
	from := g.ID
	if err := applyGroupPairs(&g, pairs, memberResolver(g.Members), true); err != nil {
		return g, err
	}
	if len(g.Members) == 0 {
		return g, fmt.Errorf("a group needs a model in it; magpie group rm %s removes it", from)
	}
	pruneRules(&g)
	to := strings.ToLower(strings.TrimSpace(g.ID))
	if to != from && (to == "" || to != provider.Slug(to)) {
		return g, fmt.Errorf("a group's id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	g.ID = from
	if err := provider.SaveGroup(g); err != nil { // one magpie found is the user's now
		return g, err
	}
	if to != from {
		if err := provider.RenameGroup(from, to); err != nil {
			return g, err
		}
		fmt.Println(amber.Render("!"), "agents set to "+provider.GroupPrefix+from+" need "+provider.GroupPrefix+to+" now")
	}
	return findGroup(to)
}

// removedOnly is a removed found group magpie doesn't find now (its model
// is down to one provider): only its record is left, to keep it removed.
func removedOnly(ref string) (string, bool) {
	ref = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(ref), "magpie/"), provider.GroupPrefix)
	for _, id := range provider.RemovedGroups() {
		if strings.EqualFold(id, ref) {
			return id, true
		}
	}
	return "", false
}

func removeGroup(ref string) (provider.Group, error) {
	g, err := findGroup(ref)
	if err != nil {
		if id, ok := removedOnly(ref); ok {
			return g, fmt.Errorf("%s is removed already (and no two providers serve its model now); magpie group restore %s brings it back", id, id)
		}
		return g, err
	}
	if g.Hidden {
		return g, fmt.Errorf("%s is removed already; magpie group restore %s brings it back", g.ID, g.ID)
	}
	return g, provider.DeleteGroup(g.ID)
}

func restoreGroup(ref string) (provider.Group, error) {
	g, err := findGroup(ref)
	if err != nil {
		if id, ok := removedOnly(ref); ok {
			// its record goes; the group comes back once two providers serve its model
			return provider.Group{ID: id, Name: id}, provider.ShowGroup(id)
		}
		return g, err
	}
	if !g.Hidden {
		return g, fmt.Errorf("%s isn't removed", g.ID)
	}
	if err := provider.ShowGroup(g.ID); err != nil {
		return g, err
	}
	return findGroup(g.ID)
}

// groupUses: the agents set to each group, by its id.
func groupUses() map[string][]string {
	out := map[string][]string{}
	for _, a := range agent.Detected() {
		if len(a.Fields) == 0 {
			continue
		}
		v := strings.TrimPrefix(a.Fields[0].Get(), "magpie/")
		if id, ok := strings.CutPrefix(v, provider.GroupPrefix); ok {
			out[id] = append(out[id], a.Name)
		}
	}
	return out
}

// memberLabel: a member as the Routing view shows it, its provider and
// the model's name; ok is false for one no provider serves now.
func memberLabel(id string, names map[string]provider.Entry) (string, bool) {
	if gid, ok := strings.CutPrefix(id, provider.GroupPrefix); ok {
		for _, g := range provider.Groups() {
			if g.ID == gid && !g.Hidden {
				return "routing group " + g.Name + " · " + routingName(g.Routing) + " · " + strings.Join(g.Members, ", "), true
			}
		}
		return "", false
	}
	if e, ok := names[id]; ok {
		n := e.Name
		if n == "" {
			n = e.Model
		}
		return e.Provider.Name + " · " + n, true
	}
	return "", false
}

func catalogByID() map[string]provider.Entry {
	out := map[string]provider.Entry{}
	for _, e := range provider.Served() {
		if e.Group == "" {
			out[e.ID] = e
		}
	}
	return out
}

// groups: `magpie groups`
func groups() error {
	all := provider.Groups()
	var shown, hidden []provider.Group
	for _, g := range all {
		if g.Hidden {
			hidden = append(hidden, g)
		} else {
			shown = append(shown, g)
		}
	}
	if len(shown) == 0 {
		fmt.Println(muted.Render("no routing groups yet ·"), "magpie group add <name> models=<m1>,<m2>", muted.Render("· magpie group help"))
	}
	names, uses := catalogByID(), groupUses()
	for _, e := range provider.Served() {
		if e.Group != "" {
			names[e.ID] = e // a group in a group is served when it is
		}
	}
	type row struct{ name, id, how, members, uses string }
	var rows []row
	w := [3]int{}
	for _, g := range shown {
		r := row{name: bold.Render(g.Name), id: muted.Render(provider.GroupPrefix + g.ID)}
		if g.Auto {
			r.name += " " + faint.Render("found")
		}
		r.how = routingName(g.Routing)
		if g.Affinity != "" {
			r.how += muted.Render(" · stays " + staysName(g.Affinity))
		}
		if n := len(g.Rules); n > 0 {
			r.how += muted.Render(fmt.Sprintf(" · %d rule%s", n, map[bool]string{true: "", false: "s"}[n == 1]))
		}
		sep, ready := muted.Render(" · "), false
		if g.Routing == provider.Ordered {
			sep = muted.Render(" → ")
		}
		var ms []string
		for _, id := range g.Members {
			if _, ok := names[id]; ok {
				ready = true
				ms = append(ms, id)
			} else {
				ms = append(ms, faint.Render(id+" (not served)"))
			}
		}
		r.members = strings.Join(ms, sep)
		if !ready {
			r.how += " " + amber.Render("no member ready")
		}
		if u := uses[g.ID]; len(u) > 0 {
			r.uses = green.Render("  ← " + strings.Join(u, ", "))
		}
		for i, s := range []string{r.name, r.id, r.how} {
			w[i] = max(w[i], lipgloss.Width(s))
		}
		rows = append(rows, r)
	}
	for _, r := range rows {
		fmt.Printf("  %s  %s  %s  %s%s\n", pad(r.name, w[0]), pad(r.id, w[1]), pad(r.how, w[2]), r.members, r.uses)
	}
	var ids []string
	for _, g := range hidden {
		ids = append(ids, g.ID)
	}
	for _, id := range provider.RemovedGroups() {
		if !slices.Contains(ids, id) {
			ids = append(ids, id) // its model is down to one provider now
		}
	}
	if len(ids) > 0 {
		fmt.Println()
		fmt.Println(" ", muted.Render("removed: "+strings.Join(ids, ", ")+" · magpie group restore <id> brings one back"))
		fmt.Println(" ", muted.Render("  (each is a {\"hidden\": true} record in providers.json that keeps it removed; deleting the record brings it back)"))
	}
	return nil
}

func showGroup(g provider.Group) error {
	kv := func(k, v string) { fmt.Printf("  %s %s\n", muted.Render(pad(k, 9)), v) }
	head := bold.Render(g.Name) + muted.Render("  "+provider.GroupPrefix+g.ID)
	if g.Auto {
		head += faint.Render("  found by magpie — changing it makes it yours")
	}
	if g.Hidden {
		head += amber.Render("  removed") + muted.Render(" · magpie group restore "+g.ID)
	}
	fmt.Println(" ", head)
	kv("routing", routingName(g.Routing))
	kv("stays", staysName(g.Affinity))
	names := catalogByID()
	for i, id := range g.Members {
		k := ""
		if i == 0 {
			k = "models"
		}
		line := fmt.Sprintf("%d %s", i+1, id)
		if l, ok := memberLabel(id, names); ok {
			line += muted.Render("  " + l)
		} else {
			line = faint.Render(line) + amber.Render("  not served now, skipped")
		}
		kv(k, line)
	}
	for i, r := range g.Rules {
		k := ""
		if i == 0 {
			k = "rules"
		}
		kv(k, fmt.Sprintf("%d %s", i+1, ruleLine(r)))
	}
	if g.Effort == provider.EffortAuto {
		kv("effort", "auto"+muted.Render("  the classifier picks each turn's reasoning"))
	}
	if g.Classifier != "" {
		what := "  tells which intent a message is"
		if !slices.ContainsFunc(g.Rules, func(r provider.Rule) bool { return r.Intent != "" }) {
			what = "  rates how hard each turn is"
		}
		kv("classifier", g.Classifier+muted.Render(what))
	}
	if u := groupUses()[g.ID]; len(u) > 0 {
		kv("used by", green.Render(strings.Join(u, ", ")))
	}
	return nil
}
