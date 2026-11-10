package guard

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/clipperhouse/uax29/v2/graphemes"
)

// The tokenizer is the model's tokenizer.json as Hugging Face's
// tokenizers library runs it (XLM-RoBERTa's): special tokens split out
// first, then SentencePiece's precompiled normalization (NFKC and the
// like), a split at whitespace, Metaspace's "▁", and a Unigram model
// over its pieces. Each token says which bytes of the text it came
// from, as the library's offsets do. AstrLink Guard's has 40,003 pieces
// (vocabSize), the embedder's 250,002 (embedder.go).

const vocabSize = 40003

type tokenizer struct {
	pieces  map[string]int
	scores  []float64
	maxLen  int // the longest piece, in bytes
	unk     int
	unkCost float64
	special []special
	trie    []uint32 // the charsmap's double array
	norm    []byte   // and the strings it points at
	// dropSpace is a pre-tokenizer that starts with WhitespaceSplit (the
	// embedder's): every run of whitespace is dropped, after a digit too
	dropSpace bool
}

type special struct {
	text   string
	id     int
	lstrip bool
}

// token is a piece's id, and the bytes of the text [start, end) it is
// for.
type token struct {
	id         int
	start, end int
}

func loadTokenizer(path string, size int) (*tokenizer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		AddedTokens []struct {
			ID      int    `json:"id"`
			Content string `json:"content"`
			Special bool   `json:"special"`
			Lstrip  bool   `json:"lstrip"`
		} `json:"added_tokens"`
		Normalizer struct {
			Type        string `json:"type"`
			Normalizers []struct {
				Type    string `json:"type"`
				Charmap string `json:"precompiled_charsmap"`
			} `json:"normalizers"`
			Charmap string `json:"precompiled_charsmap"`
		} `json:"normalizer"`
		PreTokenizer struct {
			Pretokenizers []struct {
				Type string `json:"type"`
			} `json:"pretokenizers"`
		} `json:"pre_tokenizer"`
		Model struct {
			Type  string               `json:"type"`
			UnkID int                  `json:"unk_id"`
			Vocab [][2]json.RawMessage `json:"vocab"`
		} `json:"model"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Model.Type != "Unigram" || len(f.Model.Vocab) != size {
		return nil, fmt.Errorf("a %s model of %d pieces", f.Model.Type, len(f.Model.Vocab))
	}
	t := &tokenizer{pieces: make(map[string]int, size), scores: make([]float64, size), unk: f.Model.UnkID}
	least := 0.0
	for i, v := range f.Model.Vocab {
		var p string
		if err := json.Unmarshal(v[0], &p); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(v[1], &t.scores[i]); err != nil {
			return nil, err
		}
		if _, ok := t.pieces[p]; !ok {
			t.pieces[p] = i
		}
		t.maxLen = max(t.maxLen, len(p))
		least = min(least, t.scores[i])
	}
	t.unkCost = least - 10
	for _, a := range f.AddedTokens {
		if a.Special {
			t.special = append(t.special, special{a.Content, a.ID, a.Lstrip})
		}
	}
	cm := f.Normalizer.Charmap
	if f.Normalizer.Type == "Sequence" && len(f.Normalizer.Normalizers) == 1 {
		cm = f.Normalizer.Normalizers[0].Charmap
	}
	pre := f.PreTokenizer.Pretokenizers
	t.dropSpace = len(pre) > 0 && pre[0].Type == "WhitespaceSplit"
	raw, err := base64.StdEncoding.DecodeString(cm)
	if err != nil || len(raw) < 4 {
		return nil, errors.New("no precompiled charsmap")
	}
	n := int(binary.LittleEndian.Uint32(raw))
	if n%4 != 0 || 4+n > len(raw) {
		return nil, errors.New("bad precompiled charsmap")
	}
	t.trie = make([]uint32, n/4)
	for i := range t.trie {
		t.trie[i] = binary.LittleEndian.Uint32(raw[4+4*i:])
	}
	t.norm = raw[4+n:]
	return t, nil
}

// encode is text's tokens, without <s> and </s>.
func (t *tokenizer) encode(text string) []token {
	var out []token
	at := 0
	for at < len(text) {
		s, e, id := t.nextSpecial(text, at)
		if s < 0 {
			s, e = len(text), len(text)
		}
		out = t.encodePlain(out, text[at:s], at)
		if id >= 0 {
			out = append(out, token{id, s, e})
		}
		at = e
	}
	return out
}

// nextSpecial is the first special token in text from at on (the longest
// there), with the whitespace before it for one that strips it.
func (t *tokenizer) nextSpecial(text string, at int) (int, int, int) {
	best, end, id := -1, -1, -1
	for _, sp := range t.special {
		i := strings.Index(text[at:], sp.text)
		if i < 0 {
			continue
		}
		s, e := at+i, at+i+len(sp.text)
		if sp.lstrip {
			for s > at {
				r, n := utf8.DecodeLastRuneInString(text[at:s])
				if !unicode.IsSpace(r) {
					break
				}
				s -= n
			}
		}
		if best < 0 || s < best || s == best && e > end {
			best, end, id = s, e, sp.id
		}
	}
	return best, end, id
}

// nchar is a character of the normalized text, and the bytes of the
// text it came from.
type nchar struct {
	r    rune
	s, e int
}

func (t *tokenizer) encodePlain(out []token, text string, base int) []token {
	if text == "" {
		return out
	}
	norm := t.normalize(text, base)
	for _, piece := range pretokenize(norm, t.dropSpace) {
		// Metaspace: spaces become ▁, the piece starts with one, and each
		// ▁ starts a word of its own
		for i := range piece {
			if piece[i].r == ' ' {
				piece[i].r = '▁'
			}
		}
		if piece[0].r != '▁' {
			piece = append([]nchar{{'▁', piece[0].s, piece[0].e}}, piece...)
		}
		w := 0
		for i := 1; i <= len(piece); i++ {
			if i == len(piece) || piece[i].r == '▁' {
				out = t.unigram(out, piece[w:i])
				w = i
			}
		}
	}
	return out
}

// pretokenize splits at whitespace: a run of it is dropped, unless it
// follows a digit and not drop, when each of its characters is a piece
// of its own.
func pretokenize(cs []nchar, drop bool) [][]nchar {
	var out [][]nchar
	w := -1
	flush := func(i int) {
		if w >= 0 {
			out = append(out, cs[w:i])
			w = -1
		}
	}
	for i := 0; i < len(cs); {
		if !unicode.IsSpace(cs[i].r) {
			if w < 0 {
				w = i
			}
			i++
			continue
		}
		flush(i)
		j := i
		for j < len(cs) && unicode.IsSpace(cs[j].r) {
			j++
		}
		if !drop && i > 0 && cs[i-1].r >= '0' && cs[i-1].r <= '9' {
			for k := i; k < j; k++ {
				out = append(out, cs[k:k+1:k+1])
			}
		}
		i = j
	}
	flush(len(cs))
	return out
}

// normalize is SentencePiece's precompiled normalization of text, as the
// tokenizers library does it: each grapheme of under 6 bytes looked up
// whole, and else each of its characters.
func (t *tokenizer) normalize(text string, base int) []nchar {
	out := make([]nchar, 0, len(text))
	add := func(part string, at int, to string) {
		// the first characters of to are the first of part's, in turn;
		// any more are put after the last. Step by the decoded width: a
		// malformed byte comes out as RuneError but takes up one byte,
		// not utf8.RuneLen(RuneError)'s three.
		var orig [][2]int
		for i := 0; i < len(part); {
			_, size := utf8.DecodeRuneInString(part[i:])
			orig = append(orig, [2]int{base + at + i, base + at + i + size})
			i += size
		}
		k := 0
		for _, r := range to {
			o := orig[min(k, len(orig)-1)]
			out = append(out, nchar{r, o[0], o[1]})
			k++
		}
	}
	g := graphemes.FromString(text)
	for g.Next() {
		gr, at := g.Value(), g.Start()
		if len(gr) < 6 {
			if to, ok := t.transform(gr); ok {
				add(gr, at, to)
				continue
			}
		}
		for i := 0; i < len(gr); {
			r, size := utf8.DecodeRuneInString(gr[i:])
			part := gr[i : i+size]
			if to, ok := t.transform(part); ok {
				add(part, at+i, to)
			} else {
				out = append(out, nchar{r, base + at + i, base + at + i + len(part)})
			}
			i += size
		}
	}
	return out
}

// transform is what the charsmap maps the shortest key at the start of s
// to.
func (t *tokenizer) transform(s string) (string, bool) {
	unit := func(i uint32) (uint32, bool) {
		if int(i) >= len(t.trie) {
			return 0, false
		}
		return t.trie[i], true
	}
	offset := func(u uint32) uint32 { return (u >> 10) << ((u & (1 << 9)) >> 6) }
	pos := uint32(0)
	u, ok := unit(pos)
	if !ok {
		return "", false
	}
	pos ^= offset(u)
	for i := 0; i < len(s); i++ {
		c := uint32(s[i])
		if c == 0 {
			break
		}
		pos ^= c
		u, ok = unit(pos)
		if !ok || u&((1<<31)|0xFF) != c {
			return "", false
		}
		pos ^= offset(u)
		if (u>>8)&1 == 1 {
			v, ok := unit(pos)
			if !ok {
				return "", false
			}
			at := int(v & ((1 << 31) - 1))
			if at > len(t.norm) {
				return "", false
			}
			end := at
			for end < len(t.norm) && t.norm[end] != 0 {
				end++
			}
			return string(t.norm[at:end]), true
		}
	}
	return "", false
}

// unigram is the Viterbi best split of word into pieces, as the
// tokenizers library finds it (on a tie the split found first), with a
// run of characters no piece has as one <unk>.
func (t *tokenizer) unigram(out []token, word []nchar) []token {
	var sb strings.Builder
	at := make([]int, 0, len(word)+1) // byte offset of each character
	for _, c := range word {
		at = append(at, sb.Len())
		sb.WriteRune(c.r)
	}
	s := sb.String()
	at = append(at, len(s))
	charAt := make(map[int]int, len(at))
	for i, b := range at {
		charAt[b] = i
	}
	type node struct {
		score float64
		from  int
		id    int
		set   bool
	}
	best := make([]node, len(s)+1)
	best[0].set = true
	for ci := 0; ci < len(word); ci++ {
		from := at[ci]
		if !best[from].set {
			continue
		}
		base := best[from].score
		single := false
		mb := at[ci+1] - from
		for cj := ci + 1; cj <= len(word) && at[cj]-from <= t.maxLen; cj++ {
			to := at[cj]
			id, ok := t.pieces[s[from:to]]
			if !ok {
				continue
			}
			c := base + t.scores[id]
			if n := &best[to]; !n.set || c > n.score {
				*n = node{c, from, id, true}
			}
			if to-from == mb {
				single = true
			}
		}
		if !single {
			to := from + mb
			c := base + t.unkCost
			if n := &best[to]; !n.set || c > n.score {
				*n = node{c, from, t.unk, true}
			}
		}
	}
	var rev []token
	for to := len(s); to > 0; {
		n := best[to]
		a, b := charAt[n.from], charAt[to]
		rev = append(rev, token{n.id, word[a].s, word[b-1].e})
		to = n.from
	}
	for i := len(rev) - 1; i >= 0; i-- {
		tk := rev[i]
		if tk.id == t.unk && len(out) > 0 && out[len(out)-1].id == t.unk && i < len(rev)-1 {
			out[len(out)-1].end = tk.end
			continue
		}
		out = append(out, tk)
	}
	return out
}
