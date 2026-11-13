package provider

import (
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Which of a Remote magpie's cards are shown, and which of several copies
// of one account's card (X on 0.1.3678: three magpies each other's Remote,
// A → B → C → A, and A's "only one local model" Remote still told Kiro and
// Codex, by remote-B/kiro and remote-B/remote-C/remote-A/remote-B/…/kiro,
// the same account at different hours).
//
//   - A card is shown for a Remote only when the Remote is enabled for a
//     model of the provider the card is of (remoteCardWanted): the
//     card's id is the provider path of one of those models' ids, or a
//     parent of it, whole parts only (provider/model, as that remote lists
//     them; relay/strata enabled does not tell relay/kiro).
//   - A card whose way names a magpie twice, or this one and another, is
//     no card (catalog.Loops): it isn't kept (keepRemoteCards) nor told.
//   - One account's copies are one card, the newest complete reading
//     (oneCardPerAccount), whatever the way each came by.

// remoteCardWanted says whether a card of provider id (as the remote p
// tells it: "kiro", "remote-C/kiro") is of a provider p is enabled for.
// It is unknown, and so wanted, while p's list isn't read, or a model of
// it is listed with no provider before its name; and all are wanted when p
// is set to serve no model (Select none), which still reads usage.
func remoteCardWanted(p Provider) func(id string) bool {
	// Select none serves no model, "while its balance and usage are still
	// read and shown" (Provider.NoModels): a Remote kept for its quotas
	// alone tells every card it has (a user on X, 0.1.3929)
	if p.ServesNone() {
		return func(string) bool { return true }
	}
	// only the user's picks narrow the cards. With none picked, what is
	// exposed is whatever the remote lists, and a provider the remote set to
	// usage only lists no model, so its cards would be dropped (fcooji on X,
	// 0.1.3959: Claude Code usage only there, its usage gone here)
	if len(p.Models) == 0 {
		return func(string) bool { return true }
	}
	ms := p.Exposed()
	if len(ms) == 0 {
		return func(string) bool { return true }
	}
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return cardWantedFor(ids)
}

// cardWantedFor is remoteCardWanted for the enabled model ids as the remote
// lists them: a model's provider path is its id less the last part
// ("relay/strata/m" is of relay/strata). A card is wanted when its id is
// such a path or one of its parents, whole parts only: relay/strata
// enabled tells relay/strata and relay, never relay/kiro or relay/strat.
// A model with no provider before its name leaves it unknown, so wanted.
func cardWantedFor(ids []string) func(id string) bool {
	paths := map[string]bool{}
	for _, mid := range ids {
		i := strings.LastIndex(mid, "/")
		if i <= 0 {
			return func(string) bool { return true }
		}
		path := mid[:i]
		for {
			paths[path] = true
			j := strings.LastIndex(path, "/")
			if j < 0 {
				break
			}
			path = path[:j]
		}
	}
	return func(id string) bool { return paths[id] }
}

// withoutLoops is cards less those whose way comes back to this magpie.
func withoutLoops(cards []SubscriptionQuota) []SubscriptionQuota {
	if !slices.ContainsFunc(cards, func(q SubscriptionQuota) bool { return catalog.Loops(q.Via) }) {
		return cards
	}
	out := make([]SubscriptionQuota, 0, len(cards))
	for _, q := range cards {
		if !catalog.Loops(q.Via) {
			out = append(out, q)
		}
	}
	return out
}

// cardAccountKey is what one account's copies share, whatever the way they
// came by; "" for a card that is no account's (a remote's own stand-in,
// a gateway key's limit, a key).
func cardAccountKey(q SubscriptionQuota) string {
	// a key's User is its name, which two keys may share
	if q.GatewayKey != nil || q.Provider == "" || q.Kind != "subscription" {
		return ""
	}
	leaf := q.Provider[strings.LastIndex(q.Provider, "/")+1:]
	if q.User != "" {
		return q.Kind + "\x00" + leaf + "\x00" + strings.ToLower(strings.TrimSpace(q.User))
	}
	// a key has no one's name: two keys of one name stay two cards
	return ""
}

func cardComplete(q SubscriptionQuota) bool {
	return q.Error == "" && (len(q.Windows) > 0 || q.Balance != "")
}

func cardReadAt(q SubscriptionQuota) time.Time {
	switch {
	case q.ReadAt != nil:
		return *q.ReadAt
	case q.AsOf != nil:
		return *q.AsOf
	case q.From == "":
		// this computer's own, with no time said, is just read
		return time.Now()
	}
	return time.Time{}
}

// newerCard says whether a is to be shown in place of b, copies of one
// account: a complete reading over an incomplete one, the later read,
// then the shorter way.
func newerCard(a, b SubscriptionQuota) bool {
	if ca, cb := cardComplete(a), cardComplete(b); ca != cb {
		return ca
	}
	if ta, tb := cardReadAt(a), cardReadAt(b); !ta.Equal(tb) {
		return ta.After(tb)
	}
	return len(a.Via) < len(b.Via)
}

// oneCardPerAccount is cards with one card of each account: the newest
// complete reading of its copies, in the place of the first copy.
func oneCardPerAccount(cards []SubscriptionQuota) []SubscriptionQuota {
	best := map[string]int{}
	drop := map[int]bool{}
	for i, q := range cards {
		k := cardAccountKey(q)
		if k == "" {
			continue
		}
		j, ok := best[k]
		if !ok {
			best[k] = i
			continue
		}
		if newerCard(q, cards[j]) {
			drop[j] = true
			best[k] = i
		} else {
			drop[i] = true
		}
	}
	if len(drop) == 0 {
		return cards
	}
	out := make([]SubscriptionQuota, 0, len(cards)-len(drop))
	for i, q := range cards {
		if !drop[i] {
			out = append(out, q)
		}
	}
	return out
}
