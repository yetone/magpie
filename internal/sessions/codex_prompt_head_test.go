package sessions

import (
	"encoding/json"
	"math/rand"
	"runtime"
	"strings"
	"testing"
)

// oldTextPrompt is what a prompt's title was read as before: its whole
// text decoded, then cxPrompt.
func oldTextPrompt(q []byte) string {
	return cxPrompt(strAt(append([]byte(`,"text":`), q...), cxText))
}

// TestCxTextPromptAsTheWholeText holds the head-only read of a prompt
// to the whole text's decoding, title for title, around and past the
// head's length, with escapes, surrogates, bytes not UTF-8, runs of
// space, Codex's own texts and what json.Unmarshal refuses late in it.
func TestCxTextPromptAsTheWholeText(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	pieces := []string{"a", "word ", " ", "\n", "\t\t", " ", "　", "é", "中文", "😀", "<", "#", "\"", "\\", "/", "…"}
	raws := []string{`\n`, ` `, ` `, `😀`, `\ud83d`, `\ude00`, `\ud83dx`, "\xff", "\xe4\xb8", `\/`, `\"`, `\\`, `\t`, `A`}
	bads := []string{`\x`, "\x01", `\u12`, `\uzzzz`, `\`}
	make1 := func() []byte {
		var b strings.Builder
		switch r.Intn(6) {
		case 0:
			b.WriteString(strings.Repeat(" ", r.Intn(9000)))
		case 1:
			b.WriteString("# AGENTS.md")
		case 2:
			b.WriteString("<environment_context>")
		case 3:
			b.WriteString(strings.Repeat(`\n`, r.Intn(3000)))
		}
		n := []int{50, 1500, 2100, 4000, 9000, 20000}[r.Intn(6)]
		q := []byte{'"'}
		q = append(q, b.String()...)
		for len(q) < n {
			if r.Intn(3) == 0 {
				q = append(q, raws[r.Intn(len(raws))]...)
				continue
			}
			s, _ := json.Marshal(pieces[r.Intn(len(pieces))])
			if r.Intn(40) == 0 {
				s, _ = json.Marshal(strings.Repeat(" ", r.Intn(5000)))
			}
			q = append(q, s[1:len(s)-1]...)
		}
		if r.Intn(8) == 0 {
			at := 1 + r.Intn(len(q)-1)
			q = append(q[:at], append([]byte(bads[r.Intn(len(bads))]), q[at:]...)...)
		}
		if r.Intn(20) > 0 {
			q = append(q, '"')
		}
		return q
	}
	words := strings.Repeat("word ", 200)
	short := strings.Repeat("word ", 20) // the title ends in what follows
	hand := []string{
		words + strings.Repeat(`\u3000`, 3000),
		words + strings.Repeat(`\u3000`, 3000) + "x",
		words + strings.Repeat("　", 3000),
		short + strings.Repeat("　", 3000),
		short + strings.Repeat(" ", 3000),
		short + strings.Repeat(`\u3000`, 3000),
		words + strings.Repeat("　", 3000) + " ",
		words + strings.Repeat("　", 3000) + "x",
		words + strings.Repeat(" ", 9000),
		words + strings.Repeat(" ", 9000) + `\x`,
		words + strings.Repeat(" ", 9000) + `\`,
		words + strings.Repeat(" ", 9000) + `\q`,
		words + strings.Repeat(`\ud83d\ude00`, 2000),
		words + strings.Repeat(" ", 9000) + `\ud83d`,
		strings.Repeat(" ", 9000) + words,
		strings.Repeat(`\n`, 9000) + "<x>" + words,
	}
	cut := 0
	for i := range 4000 + len(hand) {
		var q []byte
		if i < len(hand) {
			q = []byte(`"` + hand[i] + `"`)
		} else {
			q = make1()
		}
		// as the caller has it: up to its first closing quote
		q = q[:1+strEnd(q[1:])]
		want, got := oldTextPrompt(q), cxTextPrompt(q)
		if got != want {
			t.Fatalf("case %d (%d bytes): got %q, want %q", i, len(q), got, want)
		}
		if strings.HasSuffix(want, "…") {
			cut++
		}
	}
	if cut < 500 {
		t.Fatalf("only %d of the cases were titles cut short", cut)
	}
}

// TestCxTextPromptDecodesItsHead: a 5 MB prompt's title is read from
// its head, not from the whole of it decoded (~10 MB each time), and a
// short one with nothing to unescape is read where it is.
func TestCxTextPromptDecodesItsHead(t *testing.T) {
	q, _ := json.Marshal("Fix the build: " + strings.Repeat("log line \"x\" é\n", 280000))
	if got, want := cxTextPrompt(q), oldTextPrompt(q); got != want || !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q, want %q", got, want)
	}
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	before := ms.TotalAlloc
	for range 10 {
		cxTextPrompt(q)
	}
	runtime.ReadMemStats(&ms)
	per := (ms.TotalAlloc - before) / 10
	t.Logf("%d bytes a read of %d", per, len(q))
	if per > 8<<10 {
		t.Fatalf("a %d-byte prompt's title made %d bytes, want at most 8 KB", len(q), per)
	}
	short := []byte(`"` + strings.Repeat("plain words é ", 100) + `"`)
	words := string(short[1 : len(short)-1])
	own := testing.AllocsPerRun(20, func() { title(words) })
	if n := testing.AllocsPerRun(20, func() { cxTextPrompt(short) }); n > own {
		t.Fatalf("a plain prompt's title made %v allocations, want %v, its title's own", n, own)
	}
}
