package gateway

// An account's plan may not take every reasoning level its model has: a
// Free ChatGPT account turns gpt-6-luna at high and above away with a 400,
// which a Plus one beside it answers (#520). Such an account is asked
// after its provider's others that may take the level — by its own model
// list's levels, or what it answered last — and one that refuses the level
// is not the request's fault: the next is asked, and it doesn't rest. When
// none that is left takes it, the agent is told so, not the level lowered
// behind its back.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// effortRemember is how long an account's refusal of a level is kept: a
// plan upgraded is asked again after it.
const effortRemember = time.Hour

var effortRefusals = struct {
	sync.Mutex
	m map[string]effortRefusal // who|model → what it said
}{m: map[string]effortRefusal{}}

type effortRefusal struct {
	refused []string // levels it turned away
	takes   []string // the levels it said it takes, when it said
	at      time.Time
}

func effortKey(c candidate) string { return c.who() + "|" + c.model }

// noteEffortRefused remembers c turning e away, with the levels it said
// it takes.
func noteEffortRefused(c candidate, e string, takes []string) {
	effortRefusals.Lock()
	defer effortRefusals.Unlock()
	k := effortKey(c)
	n := effortRefusals.m[k]
	if time.Since(n.at) > effortRemember {
		n = effortRefusal{}
	}
	if !slices.Contains(n.refused, e) {
		n.refused = append(n.refused, e)
	}
	if len(takes) > 0 {
		n.takes = takes
	}
	n.at = time.Now()
	effortRefusals.m[k] = n
}

// takesEffort says whether c's account may be asked for reasoning e on its
// model, as far as magpie knows: not when it turned e away within the hour,
// or its own model list gives the model levels without e.
func takesEffort(c candidate, e string) bool {
	if e == "" || c.p.Account == nil {
		return true
	}
	effortRefusals.Lock()
	n, ok := effortRefusals.m[effortKey(c)]
	effortRefusals.Unlock()
	if ok && time.Since(n.at) <= effortRemember {
		if slices.Contains(n.refused, e) || len(n.takes) > 0 && !slices.Contains(n.takes, e) {
			return false
		}
		if slices.Contains(n.takes, e) {
			return true
		}
	}
	if levels, ok := c.p.Account.Levels(c.model); ok && !slices.Contains(levels, e) {
		return false
	}
	return true
}

// effortMate is the first of left that is another account of c's member —
// its provider and model, at its fixed effort — that may take e, or -1.
func effortMate(left []candidate, c candidate, e string) int {
	return slices.IndexFunc(left, func(x candidate) bool {
		return x.p.Account != nil && x.p.ID == c.p.ID && x.model == c.model && x.effort == c.effort &&
			x.who() != c.who() && takesEffort(x, e)
	})
}

var (
	// unsupportedEffort is OpenAI's "Unsupported value: 'high' is not
	// supported with the 'gpt-6-luna' model. Supported values are: …"
	unsupportedEffort = regexp.MustCompile(`(?i)unsupported value:?\s*['"\x60]?([a-z]+)['"\x60]?\s+is not supported`)
	// effortNamed is a refusal naming the reasoning effort itself
	effortNamed  = regexp.MustCompile(`(?i)reasoning[._ ]?effort`)
	effortDenied = regexp.MustCompile(`(?i)not supported|unsupported|not available|isn'?t available|not allowed|invalid value|upgrade|your plan`)
	supportedAre = regexp.MustCompile(`(?i)supported values are:?\s*([^.]*)`)
	quotedLevel  = regexp.MustCompile(`['"\x60]([a-z]+)['"\x60]`)
)

// effortRefused reads a vendor's 400 as the reasoning level sent turned
// away, and the levels it says it takes, if it says. A refusal of some
// other field's value — text.verbosity's 'high' — isn't one.
func effortRefused(body []byte, sent string) (takes []string, ok bool) {
	if sent == "" || len(body) == 0 {
		return nil, false
	}
	s := string(body)
	var e struct {
		Param string `json:"param"`
		Error struct {
			Param string `json:"param"`
		} `json:"error"`
	}
	if i := strings.IndexByte(s, '{'); i >= 0 && json.Unmarshal([]byte(s[i:]), &e) == nil {
		param := e.Error.Param
		if param == "" {
			param = e.Param
		}
		if param != "" && !effortNamed.MatchString(param) {
			return nil, false
		}
	}
	m := unsupportedEffort.FindStringSubmatch(s)
	switch {
	case m != nil && strings.EqualFold(m[1], sent):
	case m == nil && effortNamed.MatchString(s) && effortDenied.MatchString(s):
	default:
		return nil, false
	}
	if sm := supportedAre.FindStringSubmatch(s); sm != nil {
		for _, q := range quotedLevel.FindAllStringSubmatch(sm[1], -1) {
			if l := strings.ToLower(q[1]); slices.Contains(effortRank, l) && !slices.Contains(takes, l) {
				takes = append(takes, l)
			}
		}
	}
	return takes, true
}

// modelTakes says whether c's model is known to have level e, or its
// levels aren't known: a level the model has, turned away, is the
// account's plan, not the request.
func modelTakes(c candidate, e string) bool {
	levels := c.p.Efforts(c.model)
	return len(levels) == 0 || slices.Contains(levels, e)
}

// effortError is what the agent is told when the account left turned the
// level away and none that takes it could answer.
func effortError(c candidate, e string, takes []string, vendor string) string {
	msg := fmt.Sprintf("%s doesn't take reasoning effort %q on %s", c.label(), e, c.model)
	if len(takes) > 0 {
		msg += " (it takes " + strings.Join(takes, ", ") + ")"
	}
	msg += ", and no other account that does could answer; pick a lower effort or another account"
	if vendor != "" {
		msg += ": " + vendor
	}
	return msg
}

// askedTier is the service tier a Responses request asks for.
func askedTier(body []byte) string {
	var q struct {
		Tier string `json:"service_tier"`
	}
	json.Unmarshal(body, &q)
	return q.Tier
}

// takesTier says whether c's account may be asked for service tier t on
// its model: not when its own model list says which tiers the model has
// and t isn't one.
func takesTier(c candidate, t string) bool {
	if c.p.Account == nil {
		return true
	}
	tiers, ok := c.p.Account.Tiers(c.model)
	return !ok || slices.Contains(tiers, t)
}

// tierMate is the first of left that is another account of c's member
// whose list offers tier t on the model, or -1.
func tierMate(left []candidate, c candidate, t string) int {
	return slices.IndexFunc(left, func(x candidate) bool {
		if x.p.Account == nil || x.p.ID != c.p.ID || x.model != c.model || x.effort != c.effort || x.who() == c.who() {
			return false
		}
		tiers, ok := x.p.Account.Tiers(x.model)
		return ok && slices.Contains(tiers, t)
	})
}
