package gateway

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/maskrec"
)

// Masked is what masking swapped in a request, for the Routing page and
// Recent calls to show: how many places, how many different values, of
// which kinds and found by which finder; Key is its record (maskrec).
type Masked struct {
	N        int            `json:"n"`
	Distinct int            `json:"distinct"`
	Warned   int            `json:"warned,omitempty"` // places a value went as written, the user told
	Kinds    map[string]int `json:"kinds,omitempty"`  // different values by kind
	By       map[string]int `json:"by,omitempty"`     // different values by finder
	// Key is a string in JSON: nanoseconds are past what a JS number
	// holds exactly, and a rounded key finds no record
	Key int64 `json:"key,omitempty,string"`
}

// OnMasked is told of each request's record as masking makes it, for the
// notification that a value was masked (Must2333 on X); set by the app,
// nil (magpie serve, the CLI) for none. It is on the request's way, so it
// only takes the record: which of its values are new is asked later
// (maskrec.Fresh). The record holds no value and no text around one.
var OnMasked func(maskrec.Record)

// maskHits are what a masker swapped: each value from a gwmw.Teller, and
// how many places in all.
type maskHits struct {
	n    int
	hits []gwmw.Hit
	took time.Duration // how long masking took
	// key is where noteMasked puts the record's key, for the answer's
	// writer to tell it what it put back once it is done (redactedHits)
	key *int64
}

func (h *maskHits) add(o maskHits) {
	h.n += o.n
	h.hits = append(h.hits, o.hits...)
	h.took += o.took
}

// sentOf is a masked body as noteMasked takes it: copied only for a
// record that keeps it. With nothing masked no record is made, and one
// over maskrec.SentMax keeps none of it, so a 45 MiB Codex turn was
// copied whole on every request for nothing (#4004).
func sentOf(h maskHits, body []byte) string {
	if h.n == 0 && len(h.hits) == 0 || len(body) > maskrec.SentMax {
		return ""
	}
	return string(body)
}

// noteMasked records what masking swapped in a request that went to a
// vendor as sent: what it was (chat, search, image…), who sent it, the
// route it is in the trace (0 for none), and the provider and model it
// went to. It returns its summary, nil when nothing was masked.
func noteMasked(r *http.Request, what string, h maskHits, sent string, route int64, prov, model string) *Masked {
	m, done := noteMaskedUntil(r, what, h, sent, route, prov, model)
	done("", "")
	return m
}

// noteMaskedUntil is noteMasked for a request with more than one provider
// to try: prov and model are the first planned, and done, called once the
// request is over, names the provider and model that had it last, which
// the record keeps (maskrec.Hold).
func noteMaskedUntil(r *http.Request, what string, h maskHits, sent string, route int64, prov, model string) (*Masked, func(prov, model string)) {
	if h.n == 0 && len(h.hits) == 0 {
		return nil, func(string, string) {}
	}
	rec := maskrec.Record{What: what, Route: route, Provider: prov, Model: model, N: h.n}
	if r != nil {
		rec.Agent, rec.Session = callerOf(r).agent, sessionOf(r.Header)
	}
	rec.Values = valuesOf(h.hits)
	rec.Snips = snipsOf(sent, h.hits)
	rec.Took = h.took.Microseconds()
	rec.Sent = sent
	rec, done := maskrec.Hold(rec)
	if h.key != nil {
		*h.key = rec.Key
	}
	if f := OnMasked; f != nil {
		told := rec
		told.Snips, told.Back = nil, nil // the text around a value stays here
		f(told)
	}
	m := &Masked{N: rec.N, Key: rec.Key}
	for _, v := range rec.Values {
		if v.Action == "warn" {
			m.Warned += v.N
			continue
		}
		m.Distinct++
		if m.Kinds == nil {
			m.Kinds, m.By = map[string]int{}, map[string]int{}
		}
		m.Kinds[v.Kind]++
		m.By[v.By]++
	}
	return m, done
}

// noteChecked counts r's turn (a chat turn, or a native API call) for
// the Masking page while masking is on for it (maskrec.Checked): looked
// over, and whether it changed something (h, which noteMasked records),
// or sent as written (whole) to a model the user set to go unmasked. A turn with nothing to mask has no
// record, and without the count an agent's page read the same as one
// never masked (eiyuuzaki2333 on X).
func noteChecked(r *http.Request, whole bool, h maskHits) {
	if !redactionWantedFor(r.Context()) {
		return
	}
	maskrec.Checked(callerOf(r).agent, !whole, h.n > 0 || len(h.hits) > 0)
}

// noteBlocked records a request held back for a value the user set to
// block: what it had, nothing of what it would have sent.
func noteBlocked(h maskHits) {
	maskrec.Add(maskrec.Record{What: "blocked", N: h.n, Values: valuesOf(h.hits), Blocked: true})
}

func valuesOf(hits []gwmw.Hit) []maskrec.Value {
	var out []maskrec.Value
	for _, x := range hits {
		out = append(out, maskrec.Value{Placeholder: x.Placeholder, Kind: x.Kind, By: x.By, Head: x.Head, Len: x.Len, Action: x.Action})
	}
	return out
}

// snipRunes is how much of the text on each side of a value its snip has.
const snipRunes = 56

// snipsOf are, by placeholder, the text around the first place sent has
// each of hits, as the vendor saw it: what the record shows of a value,
// in memory only.
func snipsOf(sent string, hits []gwmw.Hit) map[string]string {
	if sent == "" || len(hits) == 0 {
		return nil
	}
	// in a JSON body a snip stays inside the string the value is in,
	// so it reads as the text, not as the body around it
	t := strings.TrimSpace(sent)
	inJSON := t != "" && (t[0] == '{' || t[0] == '[')
	out := map[string]string{}
	for _, x := range hits {
		if _, ok := out[x.Placeholder]; ok || len(out) >= 200 {
			continue
		}
		i := strings.Index(sent, x.Placeholder)
		if i < 0 {
			continue
		}
		// a value an earlier snip shows already has that one: the
		// record shows a line once, not once per value in it
		if s, ok := snipWith(out, x.Placeholder); ok {
			out[x.Placeholder] = s
			continue
		}
		a, b := i, i+len(x.Placeholder)
		start, end := 0, len(sent)
		for k := 0; k < snipRunes && a > start; k++ {
			if inJSON && sent[a-1] == '"' && !quoteEscaped(sent, a-1) {
				start = a
				break
			}
			_, n := utf8.DecodeLastRuneInString(sent[:a])
			a -= n
		}
		for k := 0; k < snipRunes && b < end; k++ {
			if inJSON && sent[b] == '"' && !quoteEscaped(sent, b) {
				end = b
				break
			}
			_, n := utf8.DecodeRuneInString(sent[b:])
			b += n
		}
		s := unescapeSnip(sent[a:b])
		if a > start {
			s = "…" + s
		}
		if b < end {
			s += "…"
		}
		// a value that is a whole string of the body is a snip cut from
		// it: copied, or the record kept the whole request (up to
		// SentMax) for as long as memory keeps its snips (#2278)
		out[x.Placeholder] = strings.Clone(s)
	}
	return out
}

// snipWith is a snip in out that shows p, the first one in sorted order.
func snipWith(out map[string]string, p string) (string, bool) {
	var found []string
	for _, s := range out {
		if strings.Contains(s, p) {
			found = append(found, s)
		}
	}
	if len(found) == 0 {
		return "", false
	}
	slices.Sort(found)
	return found[0], true
}

// quoteEscaped says whether the byte at i is behind an odd run of
// backslashes.
func quoteEscaped(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// unescapeSnip is a piece of a JSON body read as text: its escapes
// undone where they are whole, a line break a space.
func unescapeSnip(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n', 'r', 't':
			b.WriteByte(' ')
		case 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			b.WriteString(`\u`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
