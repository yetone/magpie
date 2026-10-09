package redact

import "strings"

// A value masked once is masked again wherever it is in a request, as it
// is, whatever is around it. The rules find a value by what is around it
// (DB_PASSWORD=, a bound an email may not touch), and the vendor answers
// about a placeholder in words of its own: "the password is {{SECRET_…}}",
// "write to {{EMAIL_…}}.". The agent reads the value there (Restore), and
// on the next turn that text comes back in the history, where no rule
// matches it. Matched as known, it goes as the same placeholder again.
//
// What is known is what values holds (in memory only, bounded by
// maxValues): the values of the placeholders made since magpie started,
// for every request alike, since a placeholder is the same for the same
// value whichever conversation it is in.

// minKnown is the shortest value matched as known. Shorter ones (a
// password of three letters in a URL, which the rule takes from its
// user:password@) would turn up all over ordinary text; 8 is what the rule
// for a password in an assignment asks of its value.
const minKnown = 8

type knownValue struct{ v, p, kind string }

var (
	// known are the values matched as known, by their first minKnown
	// bytes; guarded by mu, as values is
	known = map[string][]knownValue{}
	// knownBits has a bit for the first four bytes of each known value, so
	// most places in a text are passed over without a map lookup
	knownBits [1 << 14]uint64
)

func gram(s string, i int) uint32 {
	g := uint32(s[i]) | uint32(s[i+1])<<8 | uint32(s[i+2])<<16 | uint32(s[i+3])<<24
	return (g * 2654435761) >> 12 // 20 bits
}

// addKnown adds the value of placeholder p; called with mu held. The
// user's own words are left to Options.Words, which match them anywhere
// already, and are only as long as two letters.
func addKnown(p, v string) {
	kind := kindOf(p)
	if kind == "TERM" || len(v) < minKnown {
		return
	}
	head := v[:minKnown]
	for i, k := range known[head] {
		if k.v == v {
			// one value under two kinds: the same one wins whichever came
			// first, so it is the same after reindexKnown
			if p < k.p {
				known[head][i] = knownValue{v, p, kind}
			}
			return
		}
	}
	known[head] = append(known[head], knownValue{v, p, kind})
	g := gram(v, 0)
	knownBits[g/64] |= 1 << (g % 64)
}

// reindexKnown makes known again from values, after some were let go;
// called with mu held.
func reindexKnown() {
	known = map[string][]knownValue{}
	knownBits = [1 << 14]uint64{}
	for p, v := range values {
		addKnown(p, v)
	}
}

// kindOf is the kind in a placeholder: SECRET of {{SECRET_abcdefgh}}.
func kindOf(p string) string {
	if i := strings.LastIndexByte(p, '_'); i > 2 {
		return p[2:i]
	}
	return ""
}

var personalKinds = map[string]bool{"EMAIL": true, "ID_CARD": true, "PHONE": true, "BANK_CARD": true}

// knownSpans are where s has a known value o masks: a personal one when o
// masks personal data, any other when it masks secrets. Where two start
// at the same place, the longer.
func knownSpans(s string, o Options) []span {
	if !o.Secrets && !o.Personal || len(s) < minKnown {
		return nil
	}
	mu.RLock()
	defer mu.RUnlock()
	if len(known) == 0 {
		return nil
	}
	var out []span
	for i := 0; i+minKnown <= len(s); i++ {
		if g := gram(s, i); knownBits[g/64]&(1<<(g%64)) == 0 {
			continue
		}
		var best *knownValue
		for j, k := range known[s[i:i+minKnown]] {
			if personalKinds[k.kind] && !o.Personal || !personalKinds[k.kind] && !o.Secrets {
				continue
			}
			if best != nil && len(k.v) <= len(best.v) || !strings.HasPrefix(s[i:], k.v) || !standsAlone(k.kind, s, i, i+len(k.v)) {
				continue
			}
			best = &known[s[i:i+minKnown]][j]
		}
		if best != nil {
			out = append(out, span{i, i + len(best.v), best.kind})
			i += len(best.v) - 1
		}
	}
	return out
}

// standsAlone says s[a:b] is the known value itself, not a piece of a
// longer one: a number with no digit beside it, an email that is not part
// of a longer address (though a full stop may end the sentence after it),
// a secret with no letter or digit against it (twelve hex digits a rule of
// the user's took are also in the middle of a hash).
func standsAlone(kind, s string, a, b int) bool {
	before, after := byte(0), byte(0)
	if a > 0 {
		before = s[a-1]
	}
	if b < len(s) {
		after = s[b]
	}
	is := func(c byte, set string) bool { return c != 0 && strings.IndexByte(set, c) >= 0 }
	switch kind {
	case "PHONE", "BANK_CARD":
		return !is(before, digits) && !is(after, digits)
	case "ID_CARD":
		return !is(before, alnum) && !is(after, alnum)
	case "EMAIL":
		if is(before, alnum+"._%+-") || is(after, alnum+"_%+") {
			return false
		}
		// a.b@c.com.cn is another address; a.b@c.com. ends a sentence
		return !is(after, ".-") || b+1 >= len(s) || !is(s[b+1], alnum)
	}
	return !(is(before, alnum) && is(s[a], alnum)) && !(is(after, alnum) && is(s[b-1], alnum))
}
