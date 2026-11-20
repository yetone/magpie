package provider

// The cards another magpie is told (CachedCards) as fresh as this magpie
// knows them, not only as the Usage page last read them (jorben, #1313).
// A magpie that serves only other magpies (`magpie serve` in a container)
// has nobody on its Usage page, so the page's cards were never read
// there, while the same accounts' allowances are read all the time
// behind the requests: the account switching and the Codex warm-up
// (LoginUsage), routing (Allowances, KeyAllowance). Those readings now
// stand on the cards in place of an older one, an account or key with no
// card yet gets one from them, and after a restart, before anything is
// read, the cards are the ones kept on disk (quotas.json, as of when they
// were read). Nothing here asks a vendor.

import (
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// cardFace is what a card is named and drawn with.
type cardFace struct{ name, icon string }

// accountCard is the face of the card the Usage page gives each account
// of agent, as its usage is read (LoginUsage): a plugin's ("plugin:<id>")
// as the plugin's cards are.
func accountCard(agent string) cardFace {
	if pp, ok := pluginOfAgent(agent); ok && strings.HasPrefix(agent, "plugin:") {
		return pluginCardFace(pp)
	}
	switch agent {
	case "claude":
		return cardFace{"Claude Code", "claude-color"}
	case "codex":
		return cardFace{"Codex", "codex-color"}
	case "copilot":
		return cardFace{"Copilot", "githubcopilot"}
	case QoderCNID:
		return cardFace{"Qoder CN", "qoder"}
	case "gemini", "antigravity":
		app, _ := googleAppOf(agent)
		return cardFace{app.name, app.icon}
	}
	if c, ok := movedCards[agent]; ok {
		return cardFace{c.name, c.icon}
	}
	return cardFace{}
}

// KnownFace is the name and icon magpie knows the provider id by while it
// isn't set up: a built-in subscription signed out of, or with account
// discovery off ("claude" is Claude Code), or a preset's removed since.
// The usage log keeps the id of every provider a call went to, and the
// Usage page names those by this rather than by the bare id (@ByronVon1
// on X: "claude" in the Provider filter). "" for an id magpie doesn't
// know, such as a custom provider removed since.
func KnownFace(id string) (name, icon string) {
	if !strings.HasPrefix(id, "plugin:") {
		if f := accountCard(id); f.name != "" {
			return f.name, f.icon
		}
	}
	if pr := Preset(id); pr != nil {
		return pr.Name, pr.Icon
	}
	return "", ""
}

func pluginCardFace(pp plugin.Provider) cardFace {
	name, icon := pluginCard(pp)
	return cardFace{name, icon}
}

// cardAgents are the agents whose accounts each have a card of their own
// on the Usage page, read as LoginUsage reads them. Cursor's account is
// known by asking its CLI, Grok's are read by home, and a Kiro key has
// one card for itself: their cards stay as the page read them.
var cardAgents = []string{"claude", "codex", "copilot", "kiro", "zcode", "workbuddy", WorkBuddyAIID,
	CommandCodePlanID, "qoder", QoderCNID, "zed", "devin", "factory", MiMoID, "gemini", "antigravity"}

// cardLogins are the accounts the Usage page has a card of its own for,
// as LoginUsage reads them, a built-in moved onto its plugin by the
// plugin's.
func cardLogins() []Login {
	agents := []string{}
	for _, id := range cardAgents {
		if PluginServes(id) || id == "kiro" && kiroKey() != "" {
			continue
		}
		agents = append(agents, id)
	}
	for _, pp := range plugin.Cached() {
		agents = append(agents, pluginAgent(pp))
	}
	seen := map[string]bool{}
	var out []Login
	for _, agent := range agents {
		ls, _ := usageLogins(agent)
		for _, l := range ls {
			if k := l.Agent + "/" + strings.ToLower(l.User); l.User != "" && !seen[k] {
				seen[k] = true
				out = append(out, l)
			}
		}
	}
	return out
}

// withAccountReadings is cards with each account's latest reading, made
// for the Usage page or behind it (LoginUsage), in place of an older
// one. An account without a card gets one, from its reading, else from
// the one kept on disk (quotas.json) as of when it was made.
func withAccountReadings(cards []SubscriptionQuota) []SubscriptionQuota {
	ls := cardLogins()
	if len(ls) == 0 {
		return cards
	}
	read := map[string]SubscriptionQuota{}
	c := &loginUsageCache
	now := time.Now()
	c.Lock()
	for _, l := range ls {
		k := l.Agent + "/" + strings.ToLower(l.User)
		if e, ok := c.m[k]; ok {
			read[k] = e.asOf(now).read
		}
	}
	c.Unlock()
	out := slices.Clone(cards)
	for _, l := range ls {
		id := loginProvider(l)
		q, ok := read[l.Agent+"/"+strings.ToLower(l.User)]
		i := slices.IndexFunc(out, func(x SubscriptionQuota) bool {
			return x.From == "" && x.Provider == id && strings.EqualFold(x.User, l.User)
		})
		if i >= 0 {
			// the same reading (perLogin's) or a later one
			if ok && q.readSeq >= out[i].readSeq {
				q.Provider, q.Name, q.Icon, q.User = id, out[i].Name, out[i].Icon, out[i].User
				out[i] = q
			}
			out[i] = withHeardClaude(out[i], l)
			continue
		}
		if !ok {
			if q, ok = lastReading(id + "/" + strings.ToLower(l.User)); !ok {
				q = SubscriptionQuota{Plan: l.Plan, Windows: []QuotaWindow{}}
			}
		}
		if q = withHeardClaude(q, l); !ok && q.readSeq == 0 {
			continue
		}
		f := accountCard(l.Agent)
		q.Provider, q.Name, q.Icon, q.User = id, f.name, f.icon, l.User
		out = append(out, q)
	}
	return out
}

// withHeardClaude is q, the card of the Claude account l, with what
// Claude Code last told of its allowance as it answered (NoteClaudeLimits)
// when it told it after q was read: the windows a request's answer gave
// are kept apart from the readings (claudeUsage), and telling them drops
// the account's reading (StaleAllowance), so a magpie only requests reach
// (`magpie serve` for a Remote magpie) had a card for nobody (#2206,
// SuInk). A reading started after it stands, as it is newer, unless it
// failed: a card that stands in for a failed read (AsOf, Error) is the
// snapshot from before, and what was heard since it was made, while it
// stands (claudeHeardStands), is newer than that (mayne_wong on X: a
// setup-token account, which /usage never reads, showed a two-day-old
// snapshot after every refresh while Routing showed its last reply's).
func withHeardClaude(q SubscriptionQuota, l Login) SubscriptionQuota {
	if l.Agent != "claude" {
		return q
	}
	claudeUsage.Lock()
	e, ok := claudeUsage.m[strings.ToLower(l.User)]
	claudeUsage.Unlock()
	if !ok || len(e.ws) == 0 {
		return q
	}
	now := time.Now()
	failed := q.Error != "" || q.AsOf != nil
	switch {
	case e.heardSeq > q.readSeq && e.heardSeq != 0:
	case failed && claudeHeardStands(e, l.User, now) && (q.AsOf == nil || e.heard.After(*q.AsOf)) &&
		!claudeDenied(q.Error) && !claudeDenied(q.ReadError):
	default:
		return q
	}
	at := e.heard
	q.Windows, q.ReadAt, q.AsOf, q.Error, q.ReadError, q.ReadDeferred = e.windows(now), &at, nil, "", "", false
	q.readSeq = max(q.readSeq, e.heardSeq)
	return q
}

// withHeardClaudeCards is cards with each Claude account's card as
// withHeardClaude has it: the Usage page's, kept from the read before
// (SubscriptionUsage holds them a minute, or till asked), showed a
// snapshot from before while Routing, which reads what was heard, showed
// the account's last reply (mayne_wong on X).
func withHeardClaudeCards(cards []SubscriptionQuota) []SubscriptionQuota {
	for i, q := range cards {
		if q.Provider == "claude" && q.From == "" && q.User != "" {
			cards[i] = withHeardClaude(q, Login{Agent: "claude", User: q.User})
		}
	}
	return cards
}

// lastReading is the reading kept on disk under key (keepLast), with its
// date (AsOf).
func lastReading(key string) (SubscriptionQuota, bool) {
	c := &lastQuotas
	c.Lock()
	defer c.Unlock()
	c.load()
	return c.reading(key)
}

// cardKey is one of p's keys as its plan and balance cards are read with
// it: the key, and the account the cards name it by ("" for a provider's
// one card), as PlanQuotas and KeyBalances name them.
type cardKey struct {
	user string
	key  string
}

// cardKeys are the keys p's plan and balance cards are read with, in the
// order requests try them: the key in use first, then each other one on.
// One entry per key, even two of one name: each card is read and kept by
// its key (keyTag), and a map by the name made the second of one name
// drop out — one card told another magpie where the Usage page has two.
func cardKeys(p Provider) []cardKey {
	others := 0
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" && k.Key != p.Key {
			others++
		}
	}
	name := func(n, key string) string {
		if n == "" {
			return Mask(key)
		}
		return n
	}
	first := cardKey{key: p.Key}
	if others > 0 {
		first.user = name(p.KeyName, p.Key)
	}
	out := []cardKey{first}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" && k.Key != p.Key {
			out = append(out, cardKey{user: name(k.Name, k.Key), key: k.Key})
		}
	}
	return out
}

// keyCardProviders are the providers whose keys can have a plan or
// balance card.
func keyCardProviders() []Provider {
	var out []Provider
	for _, p := range All() {
		if !p.Hidden && !p.Off && p.Account == nil && p.Key != "" {
			out = append(out, p)
		}
	}
	return out
}

// keptKeyCards are the plan and balance cards of the keys magpie has, as
// they were last read and kept on disk (quotas.json): what another magpie
// is shown before this one reads them again after a restart.
func keptKeyCards(ps []Provider) (plans, balances []SubscriptionQuota) {
	plans, balances = []SubscriptionQuota{}, []SubscriptionQuota{}
	for _, p := range ps {
		for _, ck := range cardKeys(p) {
			for _, kind := range []string{"plan", "balance"} {
				q, ok := lastReading(p.ID + "/" + keyTag(kind, ck.key))
				if !ok || !strings.EqualFold(q.User, ck.user) {
					continue // kept under the name it had then
				}
				q.Provider, q.Name, q.Icon = p.ID, p.Name, p.Icon
				if kind == "plan" {
					plans = append(plans, q)
				} else {
					balances = append(balances, q)
				}
			}
		}
	}
	return plans, balances
}

// withKeyReadings is the plan or balance cards (kind) with the windows
// routing read of their keys since (KeyAllowance), and what was read with
// them, in place of the card's own, which only the Usage page reads. A key
// routing has read that has no card yet gets one.
func withKeyReadings(cards []SubscriptionQuota, ps []Provider, kind string) []SubscriptionQuota {
	out := slices.Clone(cards)
	for _, p := range ps {
		if !strings.HasPrefix(keyCardTag(p), kind+" ") {
			continue // the key's windows are its other card's
		}
		// each card goes to one key: two keys of one name have a card
		// each, in the order cardKeys gives them (PlanQuotas and
		// KeyBalances make them in that order)
		claimed := map[int]bool{}
		for _, ck := range cardKeys(p) {
			i := -1
			for j, q := range out {
				if !claimed[j] && q.From == "" && q.Provider == p.ID && strings.EqualFold(q.User, ck.user) {
					i = j
					break
				}
			}
			if i >= 0 {
				claimed[i] = true
			}
			r := keyRead(p.ID + "#" + keyID(ck.key))
			if r.seq == 0 {
				continue
			}
			var q SubscriptionQuota
			switch {
			case i >= 0 && r.seq <= out[i].readSeq:
				continue // the card was read since
			case i >= 0:
				q = out[i]
			case len(r.ws) == 0 && r.balance == "":
				continue // a key without a plan, nor a balance read
			default:
				q = SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, User: ck.user}
			}
			at := r.at
			q.Windows, q.ReadAt, q.AsOf, q.Error, q.readSeq = slices.Clone(r.ws), &at, nil, "", r.seq
			if q.Windows == nil {
				q.Windows = []QuotaWindow{}
			}
			if r.plan != "" {
				q.Plan = r.plan
			}
			if r.balance != "" {
				q.Balance, q.BalanceParts, q.BalanceSplit = r.balance, r.parts, r.split
			}
			if i >= 0 {
				out[i] = q
			} else {
				claimed[len(out)] = true
				out = append(out, q)
			}
		}
	}
	return out
}

// keyRead is what routing last read of the key id (provider id#key id).
func keyRead(id string) keyReading {
	c := &keyAllowances
	c.Lock()
	defer c.Unlock()
	if e := c.m[id]; e != nil {
		return e.read
	}
	return keyReading{}
}

// keyReading is a key's windows as routing read them, with the plan or
// balance read with them, for its card.
type keyReading struct {
	ws      []QuotaWindow
	plan    string
	balance string
	parts   []BalancePart
	split   []BalancePart
	at      time.Time
	seq     uint64 // quotaReading's, begun as it was asked
}
