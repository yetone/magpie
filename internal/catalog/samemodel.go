package catalog

import (
	"strings"
	"unicode"

	"github.com/yetone/magpie/internal/lazyre"
)

// SameModel is a model's name as vendors agree on it: lowercase, without
// the vendor's own prefix ("anthropic/claude-sonnet-5" is claude-sonnet-5,
// "accounts/fireworks/models/…" too), with a version's dot as Anthropic
// writes it ("claude-opus-5.5" is claude-opus-5-5, and so is Fireworks'
// "p" for the dot, "deepseek-v4p1"), a run of spaces, "_" and "-" as one
// "-" (a relay that lists its display name as the id, "GPT-6 Sol", is
// gpt-6-sol: Chill on Discord), and without the snapshot
// date some add: "claude-opus-5-5-20260801", Vertex's "…@20260801", and
// Volcengine Ark's six digits ("deepseek-v4-1-flash-260910" is
// deepseek-v4-1-flash, #583). Anything else stays: a variant after ":"
// (":batch"), -flash, -thinking, and a four-digit release (qwen's -2507,
// kimi-k2-0905) that is a model of its own. So does OpenAI's dashed date
// ("gpt-4o-2024-08-06"): a pinned snapshot apart from the alias that
// moves, and an id that has had a group of its own (auto-gpt-4o-2024-08-06)
// agents and saved groups name. An id spelt with spaces merges with its
// dashed spelling, whose group id (Slug's) it had already.
//
// The provider package groups models by it, and the catalog finds a model
// models.dev lists under another spelling by it (normalFor).
func SameModel(id string) string {
	k := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(k, "/"); i >= 0 {
		k = k[i+1:]
	}
	// most ids have no gap but single dashes and no version dot: the
	// Providers page asks this of every model of every list, and each was
	// copied twice and matched against modelGaps all the same
	if !singleDashes(k) {
		k = modelGaps.ReplaceAllString(k, "-")
	}
	k = strings.Trim(k, "-")
	if i := versionDot(k, 1); i > 0 {
		b := []byte(k)
		for ; i+1 < len(b); i++ {
			if (b[i] == '.' || b[i] == 'p') && digit(b[i-1]) && digit(b[i+1]) {
				b[i] = '-'
			}
		}
		k = string(b)
	}
	if i := strings.LastIndexAny(k, "-@"); i > 0 && snapshotDate(k[i+1:]) {
		k = k[:i]
	}
	return k
}

// snapshotDate is whether s is the date a vendor dates a snapshot of a
// model by: YYYYMMDD from 2000 on, or Ark's YYMMDD from 2023 on, with a
// month and a day that are one.
func snapshotDate(s string) bool {
	if strings.Trim(s, "0123456789") != "" {
		return false
	}
	switch {
	case len(s) == 8 && strings.HasPrefix(s, "20"):
		return true
	case len(s) == 6:
		yy, mm, dd := atoi2(s[0:2]), atoi2(s[2:4]), atoi2(s[4:6])
		return yy >= 23 && yy <= 39 && mm >= 1 && mm <= 12 && dd >= 1 && dd <= 31
	}
	return false
}

// modelGaps are what SameModel spells as one "-": a run of spaces (a
// no-break one too), "_" and "-".
var modelGaps = lazyre.New(`[\s\p{Zs}_-]+`)

// singleDashes is whether modelGaps leaves k as it is: no gap in it but
// single "-"s.
func singleDashes(k string) bool {
	for i, r := range k {
		switch {
		case r == '_' || r == '\t' || r == '\n' || r == '\f' || r == '\r' || unicode.Is(unicode.Zs, r):
			return false
		case r == '-' && i+1 < len(k) && k[i+1] == '-':
			return false
		}
	}
	return true
}

// versionDot is the first index from i of a version's dot (or "p") in k,
// between two digits, 0 with none.
func versionDot(k string, i int) int {
	for ; i+1 < len(k); i++ {
		if (k[i] == '.' || k[i] == 'p') && digit(k[i-1]) && digit(k[i+1]) {
			return i
		}
	}
	return 0
}

func atoi2(s string) int { return int(s[0]-'0')*10 + int(s[1]-'0') }

func digit(c byte) bool { return c >= '0' && c <= '9' }
