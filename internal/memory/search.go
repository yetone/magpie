package memory

import (
	"math"
	"slices"
	"strings"
	"unicode"
)

// Query is what a search asks for. Scopes are the scopes searched (none:
// every scope), Kinds and Tags narrow it (a memory has one of the kinds
// and every tag), Status is the state looked in ("": active).
type Query struct {
	Text   string
	Scopes []string
	Kinds  []string
	Tags   []string
	Status string
	Limit  int
}

// Hit is a memory a search found, with how well it matched.
type Hit struct {
	*Memory
	Score float64 `json:"score"`
}

// Search finds memories matching q, best first: every word of the text
// counted by how rare it is among the memories, a memory holding the whole
// text ahead, pinned ones and a project's or agent's own a little ahead of
// the rest. With no text, it lists them by kind and newest change. The
// hits are noted as used.
func Search(q Query) ([]Hit, error) {
	f, err := view()
	if err != nil {
		return nil, err
	}
	hits := search(f.Memories, q)
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	if strings.TrimSpace(q.Text) != "" {
		touch(ids)
	}
	return hits, nil
}

func search(all []*Memory, q Query) []Hit {
	status := q.Status
	if status == "" {
		status = Active
	}
	tags := CleanTags(q.Tags)
	pool := []*Memory{}
	for _, m := range all {
		if m.Status != status ||
			(len(q.Scopes) > 0 && !slices.Contains(q.Scopes, m.Scope)) ||
			(len(q.Kinds) > 0 && !slices.Contains(q.Kinds, m.Kind)) ||
			slices.ContainsFunc(tags, func(t string) bool { return !slices.Contains(m.Tags, t) }) {
			continue
		}
		pool = append(pool, m)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	text := strings.TrimSpace(q.Text)
	if text == "" {
		slices.SortStableFunc(pool, func(a, b *Memory) int {
			if a.Pinned != b.Pinned {
				if a.Pinned {
					return -1
				}
				return 1
			}
			if d := kindRank(a.Kind) - kindRank(b.Kind); d != 0 {
				return d
			}
			return b.Updated.Compare(a.Updated)
		})
		out := []Hit{}
		for _, m := range pool[:min(limit, len(pool))] {
			out = append(out, Hit{Memory: m})
		}
		return out
	}
	words := Words(text)
	if len(words) == 0 {
		return []Hit{}
	}
	docs := make([][]string, len(pool))
	df := map[string]int{}
	for i, m := range pool {
		docs[i] = Words(m.Text + " " + m.Why + " " + strings.Join(m.Tags, " ") + " " + m.Kind)
		seen := map[string]bool{}
		for _, w := range docs[i] {
			if !seen[w] {
				seen[w] = true
				df[w]++
			}
		}
	}
	whole := norm(text)
	out := []Hit{}
	for i, m := range pool {
		has := map[string]bool{}
		for _, w := range docs[i] {
			has[w] = true
		}
		score, matched := 0.0, 0
		for _, w := range words {
			if has[w] {
				matched++
				score += 1 + math.Log(float64(len(pool)+1)/float64(df[w]+1))
			}
		}
		if matched == 0 {
			continue
		}
		// most of the words, not one common one, make a match
		score *= float64(matched) / float64(len(words))
		if strings.Contains(norm(m.Text), whole) {
			score += 2
		}
		if m.Pinned {
			score *= 1.2
		}
		if m.Scope != Global {
			score *= 1.1
		}
		out = append(out, Hit{Memory: m, Score: math.Round(score*100) / 100})
	}
	slices.SortStableFunc(out, func(a, b Hit) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return b.Updated.Compare(a.Updated)
	})
	return out[:min(limit, len(out))]
}

func kindRank(k string) int {
	if i := slices.Index(Kinds, k); i >= 0 {
		return i
	}
	return len(Kinds)
}

// Words are the words of s as searched: letters and digits in lower case,
// and a run of Chinese, Japanese or Korean characters as each character
// and each pair of them, since those are written without spaces.
func Words(s string) []string {
	out := []string{}
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			out = append(out, string(word))
			word = word[:0]
		}
	}
	flushCJK := func() {
		for i := range cjk {
			out = append(out, string(cjk[i]))
			if i+1 < len(cjk) {
				out = append(out, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			word = append(word, unicode.ToLower(r))
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return out
}
