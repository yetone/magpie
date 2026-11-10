package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
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
  magpie group set <id> k=v…              change one: name, models (the whole list, in order, patterns too),
                                          models+=<m>[,m2…] (append), models-=<m>[,m2…] (drop), routing, stays,
                                          context (how long a request agents are told it takes: 272k; smallest
                                          is its smallest model's; empty is its largest model's), levels (the reasoning levels agents are offered:
                                          levels=none,low,medium,high,xhigh,max; empty is those every model has —
                                          a model without the one asked is sent its nearest),
                                          family (a tag: magpie visible shows agents families, not each group),
                                          id (what agents pick it as: id=gpt-6-astra drops auto-; the groups
                                          it is in follow; an agent set to the old id needs setting again),
                                          effort=auto (the classifier picks each turn's reasoning; needs classifier=),
                                          effort=agent (the agent's again), classifier=<provider/model>|group/<id>,
                                          intent-sure=<0…1> (how sure the classifier must be of an intent for it
                                          to count; 0 takes its likeliest however unsure, "none of these" never
                                          counting; empty is the 0.4 groups have always asked),
                                          pick=<model> (routing=manual, every request to that one of its models),
                                          fast=<m1>[,m2…] (the models sent in their vendor's fast mode; empty for none),
                                          weight=<m1>=<n>[,m2=<n>…] (each model's share under routing=weight,
                                          1 to 1000; a model not named keeps its own, 1 by default; empty for all 1),
                                          listed=no (its models leave the lists agents see, offered through the
                                          group alone; listed=yes lists them too again)
  magpie group pick <id> <model>          route the group manually, every request to that one of its models
                                          (as clicking it on the group's card in the Routing view does)
  magpie group copy <id> [name]           duplicate a group with its models, routing and rules, listed after it
                                          (name: "<its name> copy" when left out; also: magpie group duplicate)
  magpie group template [kind] [name]     a group for a common need, made of your own models in one go; without
                                          a kind, lists them and the models each would take: smart (hard turns
                                          to the strongest model, easy ones to a cheap one, a classifier telling
                                          them apart), thrifty (a cheap model first, the strongest for high
                                          reasoning or when the cheap ones fail), steady (the strongest model of
                                          each of up to three providers, each behind the other). A name or id in
                                          use is numbered, never replaced
  magpie group rm <id>…                   remove groups (one magpie found is hidden instead)
  magpie group restore <id>               bring back a group magpie found that you removed
  magpie group auto [on|off]              whether magpie finds groups on its own (on by default); off, none is
                                          listed or served — yours, and found ones you changed, stay — and an
                                          agent set to one is moved to its model from one provider; on brings
                                          them back
  magpie group auto skip <provider>…      keep providers out of the groups magpie finds (groups you made or
                                          changed keep them); skip none lets every one back in
  magpie group rule add|rm|mv <id> …      rules: which model a turn goes to first, by its length, an image,
                                          the reasoning asked for or the agent (magpie group rule help)

  magpie finds a group for each model two or more providers serve (auto-<model>, never stored;
  magpie group auto off stops it);
  removing one stores {"id":…,"hidden":true} in providers.json, which is what keeps it removed:
  take that record out of the file and the group is back

  models   provider/model ids as magpie models lists them; a bare model id works when one provider serves it;
           group/<id> puts another group in it, routed by its own routing and rules in its place —
           never one the group is in already (that would put it in itself), at most 8 groups deep
           provider/model:<effort> fixes the member's reasoning — none, minimal, low, medium, high, xhigh
           or max — sent whatever the agent asks or effort=auto picks, at the level the model has nearest;
           without one it reasons as the group's effort says. The same model at two efforts is two members
           (a rule can send to either). A model whose own id ends so (a :free, :7b or :0) stays as it is
           provider/model:low+high gives it several levels for the agent to choose from, each sent at the
           level the model has nearest, and the group offers agents those for it
           provider/model[:effort][:fast]#<account> sends it on that one account or key of its provider alone,
           by its id or the name magpie accounts shows: the same model on two accounts is two members, and
           with routing order they are tried top first (codex/gpt-6-astra#work codex/gpt-6-astra#home)
           provider/model[:effort]:fast sends the member in its vendor's fast mode: priority processing on a
           ChatGPT account (Codex's Fast) or an OpenAI key, Cursor's -fast model, Claude's fast mode on an
           Anthropic key for Opus 4.8 and 5; a model without one is refused (codex/gpt-6.1-sol:high:fast)
           a pattern puts in every model it matches, as magpie models lists them now and later: a glob, where
           * is any run of characters, / too, over the provider/model id in any case (openrouter/*:free,
           *-free, opencode-zen/*), or re:<regexp> matching the whole id, in any case too (re:openrouter/.*:(free|beta));
           (?-i) in the regexp makes case matter (re:(?-i)openrouter/.*:FREE).
           Quote it for the shell. The group keeps the pattern, not its models: one the vendor adds joins,
           one it drops leaves. The models you name come first, in your order, then those the patterns
           match, in the order magpie models lists them; one both named and matched keeps its named place.
           A pattern never matches a group. models-=<pattern> drops the pattern; magpie group <id> shows
           how many models each matches now, and says when one matches nothing
  routing  smart   (default) of the subscriptions with quota to spare, the one renewing soonest first
           order   the first model until it can't answer, then the next
           drain   one account or key until it is used up, then the next, and stays there: an
                   earlier one that renews meanwhile doesn't take the requests back
           rotate  each conversation's next turn goes to the next member's account or key
           usage   the account or key with the most of its allowance left first
           pace    the account with the most of its week left per hour until it renews first, so less
                   of a week is lost at its reset
           weight  each model takes its share by its weight (weight=): one weighing 3 three requests for
                   every one a model weighing 1 takes, spread out; one resting leaves the round, the next
                   model is the fallback, a group in it is one model, a model's keys go as its provider routes
           manual  only the model you pick (pick=, or click it on the group's card): the others and
                   the rules wait until you pick another or route it otherwise; no failover to them
  stays    auto    (default) with the account or key that answered, while its cache is worth keeping
           session for the whole session
           turn    within a turn only; routing decides afresh when you speak again
           off     every request routed afresh
           strict  for the whole session and never moved: resting, used up or past its cap, the
                   account that answered is still asked and its error goes to the agent
  effort   agent   (default) each request reasons as much as the agent asked, but at a member's own :<effort>
           auto    as a turn begins, the group's classifier — Jev, from a TypeSafe provider
                   (magpie provider add typesafe key=…), or any model, as Jev Router on OpenRouter,
                   asked in words — rates how hard it is, and the turn's requests
                   reason at low, medium, high or xhigh in place of the agent's level; only those
                   the agent asked to reason, never one it turned off (none). A route's effort
                   is what the agent asked, each try's what its model was sent. A group in the
                   group, effort=auto of its own, is asked only when this one gave no pick:
                   the outermost pick stands

  e.g. magpie group add "Opus anywhere" models=claude/claude-opus-5-5,copilot/claude-opus-5.5 routing=order
       magpie group set opus-anywhere stays=session models+=openrouter/anthropic/claude-opus-5.5
       magpie group set auto-gpt-6-astra id=gpt-6-astra
       magpie group add Everything models=group/opus-anywhere,deepseek/deepseek-v4-flash routing=order
       magpie group set opus-anywhere effort=auto classifier=typesafe/jev-latest
       magpie group add Fast models=codex/gpt-5.6-luna:low,deepseek/deepseek-v4-flash,glm/glm-5.3-flash:high
       magpie group set fast models+=gcloud/gemini-3.8-flash:medium
       magpie group add my-free-pool models='openrouter/*:free,opencode-zen/*-free' routing=order
       magpie group set opus-anywhere routing=weight weight=claude/claude-opus-5-5=3
       magpie group set deepseek-anywhere routing=fast   # the provider that answered it fastest lately first
       magpie group set deepseek-anywhere routing=cheap  # the provider it costs least on now first (DeepSeek's off-peak hours)
       magpie group set free-pool routing=drain          # one account until it is used up, then the next, never back
       magpie group set checkins routing=expiring        # free quota first, the soonest to expire first; paid only when none is left
       magpie group pick opus-anywhere copilot/claude-opus-5.5
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
	{provider.Pace, "pace", []string{"weekly-pace"}},
	{provider.Weighted, "weight", []string{"weighted", "by-weight", "weighted-round-robin", "wrr"}},
	{provider.FreeFirst, "free", []string{"free-first", "freest", "no-cost"}},
	{provider.Fastest, "fast", []string{"fastest", "speed", "speed-first"}},
	{provider.Cheapest, "cheap", []string{"cheapest", "cheapest-first", "price", "cost"}},
	{provider.Drain, "drain", []string{"until-used-up", "use-up", "sticky"}},
	{provider.FreeExpiring, "expiring", []string{"free-expiring", "free-expiry", "free-soonest", "soonest-free"}},
	{provider.Manual, "manual", []string{"pick", "picked", "pinned"}},
}

var staysNames = []struct {
	value, name string
	also        []string
}{
	{"", "auto", []string{"default"}},
	{provider.AffinitySession, "session", nil},
	{provider.AffinityTurn, "turn", []string{"within-a-turn"}},
	{provider.AffinityOff, "off", []string{"none", "never"}},
	{provider.AffinityStrict, "strict", []string{"strict-session", "never-move"}},
}

func parseRouting(v string) (string, error) {
	v = strings.TrimSpace(v)
	if provider.PluginRouting(v) { // a gateway plugin's
		return v, nil
	}
	v = strings.ToLower(v)
	var names []string
	for _, r := range routingNames {
		if v == r.value || v == r.name || slices.Contains(r.also, v) {
			return r.value, nil
		}
		names = append(names, r.name)
	}
	return "", fmt.Errorf("routing %q is not one magpie has: %s, or <plugin>/<name> for a gateway plugin's", v, strings.Join(names, ", "))
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

// routingName is what the CLI calls routing v: a plugin's, or one it
// doesn't know, as it is.
func routingName(v string) string {
	for _, r := range routingNames {
		if r.value == v {
			return r.name
		}
	}
	return v
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

// splitIDs splits a list of model ids at commas, each trimmed: an id can
// hold a space ("Opus 5.5" on a relay, #1976), which splitList would cut in
// two. An item with a space in it that whole doesn't take (no model the
// group can have) is split at its spaces, as such a list was before; with
// whole nil, never.
func splitIDs(v string, whole func(string) bool) []string {
	var out []string
	for _, id := range strings.Split(v, ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if whole != nil && strings.ContainsFunc(id, unicode.IsSpace) && !whole(id) {
			out = append(out, strings.Fields(id)...)
			continue
		}
		out = append(out, id)
	}
	return out
}

// inGroup: whether v names one of members, as pickMember finds it
func inGroup(members []string) func(string) bool {
	return func(v string) bool {
		v, _ = provider.MemberFast(v)
		_, err := pickMember(members, v)
		return err == nil
	}
}

// memberResolver spells a model the user typed as the catalog's
// provider/model id: the id itself, or a bare model id one provider
// serves — with the effort it is fixed at after it, when one was typed
// ("glm/glm-5.3-flash:high"; see provider.MemberEffort). keep are ids the
// group has already, kept though no provider serves them now (the Routing
// view keeps them too, skipped).
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
	var resolve func(string) (string, error)
	resolve = func(in string) (string, error) {
		id := strings.TrimPrefix(strings.TrimSpace(in), "magpie/")
		if slices.Contains(keep, id) {
			return id, nil
		}
		if member, acct := provider.MemberAccount(id); acct != "" {
			// on one account or key of its own (EastonXvii on X): the
			// member as any other; SaveGroup finds the account by its id or
			// name, and refuses one it has none such for
			if provider.IsGroupRef(provider.CanonicalRef(member)) {
				return "", fmt.Errorf("%s is a group: its models are sent on the accounts it says, so it takes no %s%s", member, provider.AccountMark, acct)
			}
			m, err := resolve(member)
			if err != nil {
				return "", err
			}
			return provider.WithMemberAccount(m, acct), nil
		}
		if member, fast := provider.MemberFast(id); fast {
			// sent fast (Group.Fast): the member as any other, SaveGroup
			// takes the :fast off
			m, err := resolve(member)
			if err != nil {
				return "", err
			}
			return m + ":" + provider.FastWord, nil
		}
		if model, effort := provider.MemberEffort(id); effort != "" {
			// a model at an effort of its own: the model as any other is
			if provider.IsGroupRef(model) {
				return "", fmt.Errorf("%s is a group: its models reason as it says, so it takes no :%s", model, effort)
			}
			m, err := resolve(model)
			if err != nil {
				return "", err
			}
			return provider.WithMemberEffort(m, effort), nil
		}
		if gid, ok := strings.CutPrefix(provider.CanonicalRef(id), provider.GroupPrefix); ok {
			// a group in the group: auto/<model> as group/auto-<slug>;: SaveGroup refuses one it would be in itself through
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
		if provider.IsDecider(id) {
			// a decision model, for a group of decision models alone,
			// asked at /v1/systemone (SaveGroup refuses a mixed one)
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
	return resolve
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
	// patterns typed among the models (#766) are kept apart: SaveGroup
	// finds their models each time the group is read
	var patterns []string
	members := func(v string) ([]string, error) {
		var out []string
		patterns = nil
		for _, m := range splitIDs(v, func(m string) bool { _, err := resolve(m); return err == nil }) {
			if provider.IsPattern(m) {
				ps, err := provider.CleanPatterns([]string{m})
				if err != nil {
					return nil, err
				}
				patterns = append(patterns, ps...)
				continue
			}
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
			// the whole list, patterns too: what the old ones matched goes
			g.Members, err = members(v)
			g.Match, g.Matched = patterns, nil
			g.Fast = nil // the list as typed: :fast on those sent fast
		case "models+", "members+", "model+":
			var add []string
			if add, err = members(v); err == nil {
				for _, id := range add {
					if slices.Contains(g.Matched, id) {
						// a pattern's model named: it has the place it is named at
						g.Matched = slices.DeleteFunc(g.Matched, func(x string) bool { return x == id })
						g.Members = slices.DeleteFunc(g.Members, func(x string) bool { return x == id })
					}
					if !slices.Contains(g.Members, id) {
						g.Members = append(g.Members, id)
					}
				}
				for _, p := range patterns {
					if !slices.Contains(g.Match, p) {
						g.Match = append(g.Match, p)
					}
				}
			}
		case "models-", "members-", "model-":
			for _, m := range splitIDs(v, inGroup(g.Members)) {
				if provider.IsPattern(m) {
					m = strings.TrimPrefix(strings.TrimSpace(m), "magpie/")
					i := slices.Index(g.Match, m)
					if i < 0 {
						return fmt.Errorf("%s is not one of the group's patterns (%s)", m, orNone(g.Match))
					}
					g.Match = slices.Delete(g.Match, i, i+1)
					continue
				}
				m, _ = provider.MemberFast(strings.TrimPrefix(m, "magpie/"))
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
				if x := g.Members[i]; slices.Contains(g.Matched, x) {
					// taken out, the pattern would find it again
					var by []string
					for _, p := range g.Match {
						if provider.PatternMatches(p, x) {
							by = append(by, p)
						}
					}
					return fmt.Errorf("%s is in the group by its pattern %s: change the pattern (models-=%s, then models+= what you want), or switch the model off in the Routing view", x, strings.Join(by, ", "), by[0])
				}
				g.Members = slices.Delete(g.Members, i, i+1)
			}
		case "routing", "strategy":
			g.Routing, err = parseRouting(v)
		case "stays", "stay", "affinity":
			g.Affinity, err = parseStays(v)
		case "context":
			// what agents are told the group takes; empty or 0 is its
			// largest model's again
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "", "largest", "max":
				g.Context = 0
			case "smallest", "min":
				g.Context = provider.ContextSmallest
			default:
				g.Context, err = parseTokens(v)
			}
		case "levels", "level":
			// the reasoning levels agents are offered; empty is those every
			// member has again
			g.Levels = nil
			if strings.TrimSpace(v) != "" {
				g.Levels, err = provider.CleanLevels(splitList(v))
			}
		case "family", "tag":
			g.Family = strings.TrimSpace(v)
		case "listed":
			// no: the models in it leave the lists agents see (#1783)
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "no":
				g.Unlists = true
			case "yes":
				g.Unlists = false
			default:
				err = fmt.Errorf("listed=no (its models offered through the group alone) or listed=yes, not %q", v)
			}
		case "effort", "reasoning":
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "auto", "jev":
				g.Effort = provider.EffortAuto
			case "", "agent", "off":
				g.Effort = ""
			default:
				err = fmt.Errorf("effort=auto (the classifier picks each turn's) or effort=agent (the agent's), not %q", v)
			}
		case "classifier", "classify":
			g.Classifier = strings.TrimPrefix(strings.TrimSpace(v), "magpie/")
		case "intent-sure", "intentsure":
			// how sure the classifier must be of an intent for it to count
			// (#5101): 0 takes its likeliest however unsure, "none of
			// these" never counting; empty is 0.4 as groups have always
			// asked
			if strings.TrimSpace(v) == "" {
				g.IntentSure = nil
				break
			}
			sure, perr := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if perr != nil || !(sure >= 0 && sure <= 1) { // NaN too
				err = fmt.Errorf("intent-sure is from 0 to 1, as intent-sure=0.25 (empty is the 0.4 groups have always asked), not %q", v)
				break
			}
			g.IntentSure = &sure
		case "fast":
			// the members sent in their vendor's fast mode, as they are in
			// the group
			g.Fast = nil
			for _, m := range splitIDs(v, inGroup(g.Members)) {
				m, _ = provider.MemberFast(m)
				id, perr := pickMember(g.Members, m)
				if perr != nil {
					return perr
				}
				g.Fast = append(g.Fast, id)
			}
		case "weight", "weights":
			// each member's share when the group routes by weight (#1910):
			// those named, the rest kept; empty for every one 1 again
			if strings.TrimSpace(v) == "" {
				g.Weights = nil
			}
			for _, mw := range splitIDs(v, func(mw string) bool {
				at := strings.LastIndex(mw, "=")
				return at > 0 && inGroup(g.Members)(mw[:at])
			}) {
				at := strings.LastIndex(mw, "=")
				if at < 0 {
					return fmt.Errorf("weight=<model>=<n>, as weight=%s=3, not %q", mw, mw)
				}
				n, aerr := strconv.Atoi(strings.TrimSpace(mw[at+1:]))
				if aerr != nil || n < 1 || n > provider.MaxKeyWeight {
					return fmt.Errorf("%s: a model's weight is 1 to %d", mw, provider.MaxKeyWeight)
				}
				m, _ := provider.MemberFast(mw[:at])
				id, perr := pickMember(g.Members, m)
				if perr != nil {
					return perr
				}
				g.SetMemberWeight(id, n)
			}
		case "pick", "use":
			// the one member a manual group sends to (#317): routing is
			// manual then
			g.Pick, err = pickMember(g.Members, v)
			g.Routing = provider.Manual
		default:
			return fmt.Errorf("unknown field %q (fields: name, models, models+, models-, routing, pick, stays, context, levels, family, effort, classifier, intent-sure, fast, weight, listed; magpie group help)", k)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// pickMember is the group's member the user named: its id, or a bare
// model id one member has.
func pickMember(members []string, v string) (string, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "magpie/")
	if i := slices.IndexFunc(members, func(x string) bool { return strings.EqualFold(x, v) }); i >= 0 {
		return members[i], nil
	}
	var hits []string
	for _, x := range members {
		if _, bare, _ := strings.Cut(x, "/"); strings.EqualFold(bare, v) {
			hits = append(hits, x)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("%s is not in the group (its models: %s)", v, strings.Join(members, ", "))
	}
	return "", fmt.Errorf("%s is %d of the group's models; name one: %s", v, len(hits), strings.Join(hits, ", "))
}

// findGroup looks a group up by its id, its catalog id or its name.
func findGroup(ref string) (provider.Group, error) {
	all := provider.Groups()
	ref = strings.TrimPrefix(provider.CanonicalRef(strings.TrimPrefix(strings.TrimSpace(ref), "magpie/")), provider.GroupPrefix)
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
	base := provider.GroupSlug(name)
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
		fmt.Println(green.Render("✓"), verb, bold.Render(g.Name), muted.Render("· agents pick it as "+g.Ref()))
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
		if slices.ContainsFunc(rest[1:], func(kv string) bool { return strings.HasPrefix(strings.ToLower(kv), "name=") }) {
			// a new name isn't a new id: say what agents still ask for
			fmt.Println(muted.Render("  its id stays " + g.ID + ": agents ask for " + g.Ref() + " (a client given the name may ask by the name too)"))
		}
		return showGroup(g)
	case "rm", "remove", "delete":
		if len(rest) < 1 {
			return fmt.Errorf("magpie group rm <id>…")
		}
		gs, err := removeGroups(rest)
		if err != nil {
			return err
		}
		for _, g := range gs {
			if g.Auto {
				fmt.Println(green.Render("✓"), "removed", bold.Render(g.Name), muted.Render("· magpie found it, so it's hidden: magpie group restore "+g.ID+" brings it back"))
			} else {
				fmt.Println(green.Render("✓"), "removed", bold.Render(g.Name))
			}
		}
		return nil
	case "pick", "use":
		if len(rest) != 2 {
			return fmt.Errorf("magpie group pick <id> <model>")
		}
		g, err := setGroup(rest[0], []string{"pick=" + rest[1]})
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), bold.Render(g.Name), "sends every request to", bold.Render(g.Picked()), muted.Render("· routing manual"))
		return showGroup(g)
	case "copy", "duplicate", "dup", "clone":
		if len(rest) < 1 {
			return fmt.Errorf("magpie group copy <id> [name]")
		}
		src, err := findGroup(rest[0])
		if err != nil {
			return err
		}
		if src.Hidden {
			return fmt.Errorf("%s was removed: magpie group restore %s brings it back first", src.ID, src.ID)
		}
		g, err := provider.CopyGroup(src.ID, strings.Join(rest[1:], " "))
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "copied", bold.Render(src.Name), "to", bold.Render(g.Name), muted.Render("· agents pick it as "+g.Ref()))
		return showGroup(g)
	case "template", "templates", "tpl":
		return templateCmd(rest)
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
	case "auto", "found":
		return autoGroupsCmd(rest)
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
	if len(g.Members) == 0 && len(g.Match) == 0 {
		return g, fmt.Errorf("a group needs a model in it: models=<provider/model>[,…] (magpie models lists them), or a pattern: models='openrouter/*:free'")
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
	if len(g.Members) == 0 && len(g.Match) == 0 {
		return g, fmt.Errorf("a group needs a model in it; magpie group rm %s removes it", from)
	}
	pruneRules(&g)
	to := strings.ToLower(strings.TrimSpace(g.ID))
	if to != from && (to == "" || to != provider.GroupSlug(to)) {
		return g, fmt.Errorf("a group's id must be lowercase letters, digits, dots and dashes, not %q", g.ID)
	}
	g.ID = from
	if err := provider.SaveGroup(g); err != nil { // one magpie found is the user's now
		return g, err
	}
	if to != from {
		moved, err := agent.RenameGroup(from, to)
		if err != nil {
			return g, err
		}
		for _, a := range moved {
			fmt.Println(green.Render("✓"), a+" is on "+provider.GroupPrefix+to+" now")
		}
	}
	return findGroup(to)
}

// removedOnly is a removed found group magpie doesn't find now (its model
// is down to one provider): only its record is left, to keep it removed.
func removedOnly(ref string) (string, bool) {
	ref = strings.TrimPrefix(provider.CanonicalRef(strings.TrimPrefix(strings.TrimSpace(ref), "magpie/")), provider.GroupPrefix)
	for _, id := range provider.RemovedGroups() {
		if strings.EqualFold(id, ref) {
			return id, true
		}
	}
	return "", false
}

// removeGroups removes the groups refs name, all or none (lc on Discord).
func removeGroups(refs []string) ([]provider.Group, error) {
	if len(refs) == 1 {
		g, err := removeGroup(refs[0])
		return []provider.Group{g}, err
	}
	var gs []provider.Group
	var ids []string
	for _, ref := range refs {
		g, err := findGroup(ref)
		if err != nil {
			return nil, err
		}
		if g.Hidden {
			return nil, fmt.Errorf("%s is removed already; magpie group restore %s brings it back", g.ID, g.ID)
		}
		gs, ids = append(gs, g), append(ids, g.ID)
	}
	return gs, provider.DeleteGroups(ids)
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
		// a group an agent is on carries Claude Code's [1m] mark, as a
		// model's does, and these are the agents' own settings: the mark
		// comes off before the id is looked up by it (GroupFinder)
		if id, ok := provider.GroupIDOf(v); ok {
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
	model, effort := provider.MemberEffort(id)
	if e, ok := names[model]; ok {
		n := e.Name
		if n == "" {
			n = e.Model
		}
		if _, acct := provider.MemberAccount(id); acct != "" {
			n += " · on " + memberAccountName(e.Provider.ID, acct) + " alone"
		}
		if _, levels := provider.MemberLevels(effort); len(levels) > 0 {
			n += " · " + strings.Join(levels, "/") + " reasoning, as the agent picks"
		} else if effort != "" {
			n += " · " + effort + " reasoning, whatever the agent asks"
		}
		return e.Provider.Name + " · " + n, true
	}
	return "", false
}

// memberAccountName is how the account or key acct of the provider pid a
// member names is shown: by its id when magpie has no such now.
func memberAccountName(pid, acct string) string {
	if n, ok := provider.AccountNames()[pid+"/"+acct]; ok {
		return strings.TrimPrefix(n, pid+"/")
	}
	return acct
}

// typedMember is the group's member as it is typed: with :fast after it
// when the group sends it fast.
func typedMember(g provider.Group, id string) string {
	if g.IsFast(id) {
		base, acct := provider.MemberAccount(id)
		return provider.WithMemberAccount(base+":"+provider.FastWord, acct)
	}
	return id
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
// autoGroupsCmd says whether magpie finds groups on its own, or turns
// that on or off: off, the agents set to one are moved to its model from
// one provider (agent.Reseat), as the Routing view's switch does.
func autoGroupsCmd(args []string) error {
	if len(args) == 0 {
		if provider.AutoGroupsOn() {
			fmt.Println("found groups are", green.Render("on"), muted.Render("· a model two or more providers serve is a group of them (auto-<model>); magpie group auto off turns them off"))
		} else {
			fmt.Println("found groups are", amber.Render("off"), muted.Render("· only the groups you made or changed; magpie group auto on brings the others back"))
		}
		if skip := provider.AutoGroupSkip(); len(skip) > 0 {
			fmt.Println("left out:", strings.Join(skip, ", "), muted.Render("· magpie group auto skip none lets them back in"))
		}
		return nil
	}
	var on bool
	switch strings.ToLower(args[0]) {
	case "on", "true", "yes", "1":
		on = true
	case "off", "false", "no", "0":
	case "skip":
		return autoGroupSkipCmd(args[1:])
	default:
		return fmt.Errorf("magpie group auto [on|off|skip <provider>…|skip none]")
	}
	if len(args) > 1 {
		return fmt.Errorf("magpie group auto [on|off]")
	}
	moved, err := agent.Reseat(func() error { return provider.SetAutoGroups(on) })
	if err != nil {
		return err
	}
	defer printMoved(moved)
	if on {
		fmt.Println(green.Render("✓"), "found groups are on", muted.Render("· a model two or more providers serve is a group of them again"))
	} else {
		fmt.Println(green.Render("✓"), "found groups are off", muted.Render("· the groups you made or changed stay; a request for an auto- group goes to its model from one provider"))
	}
	return nil
}

// autoGroupSkipCmd keeps the providers named out of the groups magpie
// finds, the others back in (provider.SetAutoGroupSkip; chenxiang0010 on
// Discord); "none" lets every one back in, and no name says which are
// out. A name magpie has no provider of is refused, never kept.
func autoGroupSkipCmd(args []string) error {
	if len(args) == 0 {
		skip := provider.AutoGroupSkip()
		if len(skip) == 0 {
			fmt.Println("no provider is left out of found groups", muted.Render("· magpie group auto skip <provider>… leaves them out"))
		} else {
			fmt.Println("left out of found groups:", strings.Join(skip, ", "))
		}
		return nil
	}
	var ids []string
	if !(len(args) == 1 && strings.EqualFold(args[0], "none")) {
		have := map[string]bool{}
		for _, p := range provider.All() {
			have[p.ID] = true
		}
		for _, a := range args {
			id := strings.ToLower(strings.TrimSpace(a))
			if !have[id] {
				return fmt.Errorf("no provider %q (magpie providers lists their ids)", a)
			}
			ids = append(ids, id)
		}
	}
	moved, err := agent.Reseat(func() error { return provider.SetAutoGroupSkip(ids) })
	if err != nil {
		return err
	}
	defer printMoved(moved)
	if len(ids) == 0 {
		fmt.Println(green.Render("✓"), "no provider is left out of found groups")
	} else {
		fmt.Println(green.Render("✓"), "left out of found groups:", strings.Join(ids, ", "), muted.Render("· the groups you made or changed keep them"))
	}
	return nil
}

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
	if !provider.AutoGroupsOn() {
		defer fmt.Println(" ", muted.Render("found groups are off · magpie group auto on brings them back"))
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
		r := row{name: bold.Render(g.Name), id: muted.Render(g.Ref())}
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
		if g.Routing == provider.Ordered || g.Routing == provider.Drain {
			sep = muted.Render(" → ")
		}
		var ms []string
		for _, id := range g.Members {
			model, _ := provider.MemberEffort(id)
			served := names[model].ID != ""
			picked := g.Routing == provider.Manual && id == g.Picked()
			if served && (picked || g.Routing != provider.Manual) {
				ready = true
			}
			shown := typedMember(g, id)
			switch {
			case !served:
				ms = append(ms, faint.Render(shown+" ("+whyNotServed(model)+")"))
			case picked:
				ms = append(ms, green.Render("● "+shown))
			case g.Routing == provider.Manual:
				ms = append(ms, faint.Render(shown))
			default:
				ms = append(ms, shown)
			}
		}
		for _, h := range provider.PatternHits(g) {
			if h.Models == 0 {
				ms = append(ms, amber.Render(h.Pattern+" (matches nothing)"))
			} else {
				ms = append(ms, muted.Render(fmt.Sprintf("%s (%d)", h.Pattern, h.Models)))
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
	head := bold.Render(g.Name) + muted.Render("  "+g.Ref())
	if g.Auto {
		head += faint.Render("  found by magpie — changing it makes it yours")
	}
	if g.Hidden {
		head += amber.Render("  removed") + muted.Render(" · magpie group restore "+g.ID)
	}
	fmt.Println(" ", head)
	if g.Routing == provider.Manual {
		kv("routing", "manual"+muted.Render("  every request to "+g.Picked()+"; magpie group pick "+g.ID+" <model> picks another"))
	} else {
		kv("routing", routingName(g.Routing))
	}
	kv("stays", staysName(g.Affinity))
	if g.Unlists {
		kv("listed", "no"+muted.Render("  its models are offered through the group alone; listed=yes lists them too"))
	}
	names := catalogByID()
	for i, id := range g.Members {
		k := ""
		if i == 0 {
			k = "models"
		}
		line := fmt.Sprintf("%d %s", i+1, typedMember(g, id))
		if g.Routing == provider.Manual && id == g.Picked() {
			line = green.Render(fmt.Sprintf("%d %s ●", i+1, typedMember(g, id)))
		}
		if l, ok := memberLabel(id, names); ok {
			if g.IsFast(id) {
				l += " · fast"
			}
			if g.Routing == provider.Weighted {
				l += fmt.Sprintf(" · weight %d", g.MemberWeight(id))
			}
			if slices.Contains(g.Matched, id) {
				l += " · by pattern"
			}
			line += muted.Render("  " + l)
		} else {
			model, _ := provider.MemberEffort(id)
			line = faint.Render(line) + amber.Render("  "+whyNotServed(model)+" now, skipped")
		}
		kv(k, line)
	}
	if len(g.Members) == 0 {
		kv("models", faint.Render("none now"))
	}
	for i, h := range provider.PatternHits(g) {
		k := ""
		if i == 0 {
			k = "patterns"
		}
		switch h.Models {
		case 0:
			kv(k, h.Pattern+amber.Render("  matches nothing now"))
		case 1:
			kv(k, h.Pattern+muted.Render("  1 model"))
		default:
			kv(k, h.Pattern+muted.Render(fmt.Sprintf("  %d models", h.Models)))
		}
	}
	for i, r := range g.Rules {
		k := ""
		if i == 0 {
			k = "rules"
		}
		line := fmt.Sprintf("%d %s", i+1, ruleLine(r))
		if g.Routing == provider.Manual {
			line = faint.Render(line + "  (waits: routing is manual)")
		}
		kv(k, line)
	}
	if len(g.Levels) > 0 {
		kv("levels", strings.Join(g.Levels, ", ")+muted.Render("  offered to agents; a model without the one asked is sent its nearest"))
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
	if g.IntentSure != nil {
		kv("intent-sure", fmt.Sprintf("%g", *g.IntentSure)+muted.Render("  how sure the classifier must be of an intent for it to count; 0 takes its likeliest"))
	}
	if u := groupUses()[g.ID]; len(u) > 0 {
		kv("used by", green.Render(strings.Join(u, ", ")))
	}
	return nil
}

// whyNotServed is why a group's member isn't served: most often a model its
// provider's list has but doesn't expose, which `magpie provider models
// <id> +<model>` puts right (MOMO on Discord: opencode-go/deepseek-flash).
func whyNotServed(member string) string {
	pid, model, ok := strings.Cut(member, "/")
	if !ok || provider.IsGroupRef(member) {
		return "not served"
	}
	p, err := provider.Find(pid)
	if err != nil {
		return "not served: no provider " + pid
	}
	if !p.On() {
		return "not served: " + p.Name + " is off"
	}
	if slices.ContainsFunc(p.Available(), func(m catalog.Model) bool { return m.ID == model }) &&
		!slices.ContainsFunc(p.Exposed(), func(m catalog.Model) bool { return m.ID == model }) {
		return "not served: not exposed · magpie provider models " + p.ID + " +" + model
	}
	return "not served"
}

// templateCmd: magpie group template [kind] [name] (provider.Templates):
// each template and the models it would take, or one of them added.
func templateCmd(rest []string) error {
	if len(rest) == 0 {
		for _, tp := range provider.Templates() {
			fmt.Println(bold.Render(tp.Kind), muted.Render("· "+provider.TemplateNames[tp.Kind]))
			if tp.Why != "" {
				fmt.Println("  " + muted.Render("can't be made: "+tp.Why))
				continue
			}
			fmt.Println("  " + strings.Join(tp.Group.Members, " → "))
			if tp.Group.Classifier != "" {
				fmt.Println("  " + muted.Render("classifier "+tp.Group.Classifier))
			}
		}
		fmt.Println(muted.Render("magpie group template <kind> [name] adds one"))
		return nil
	}
	kind := strings.ToLower(rest[0])
	for k, id := range provider.TemplateIDs {
		if kind == id {
			kind = k
		}
	}
	g, err := provider.AddTemplate(kind, strings.Join(rest[1:], " "))
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "added", bold.Render(g.Name), muted.Render("· agents pick it as "+g.Ref()))
	return showGroup(g)
}
