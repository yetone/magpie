package edit

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2/unstable"
)

// tomlShaper finds every key and comment where the parser's own Shape does.
func TestTOMLShaperMatchesParser(t *testing.T) {
	doc := "# top\na = 1 # one\n\n[t]\n  b.c = \"x#y\"  # two\n\"q\" = [\n  1,\n]\n[[arr]]\nz = { k = 1 }\n"
	lines := splitLines(doc)
	shapeOf := tomlShaper(lines)
	p := unstable.Parser{KeepComments: true}
	p.Reset([]byte(strings.Join(lines, "\n")))
	n := 0
	check := func(it *unstable.Node) {
		if it.Raw.Length == 0 {
			return
		}
		if got, want := shapeOf(it.Raw), p.Shape(it.Raw); got != want {
			t.Errorf("%v %q: %+v, the parser says %+v", it.Kind, p.Raw(it.Raw), got, want)
		}
		n++
	}
	for p.NextExpression() {
		e := p.Expression()
		check(e)
		if e.Kind != unstable.Comment {
			for k := e.Key(); k.Next(); {
				check(k.Node())
			}
		}
		if c := e.Next(); c != nil {
			check(c)
		}
	}
	if err := p.Error(); err != nil || n < 8 {
		t.Fatalf("parsed %d nodes: %v", n, err)
	}
}

// tomlBigFile is a config.toml of about size bytes: the table shape a Codex
// one reached, 1.5 MB of keys, each on its own header.
func tomlBigFile(size int) []string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "[projects.\"/Users/me/p%d\"]\ntrust_level = \"trusted\" # note\n\n", i)
	}
	b.WriteString("model_provider = \"magpie\"\n")
	return splitLines("model_provider = \"x\"\n" + b.String())
}

// parseTOMLRatio times one 8 MB parse against eight 1 MB parses on the same
// CPU budget: each round parses the small file eight times (the sizes differ
// 8×, so that costs about what one large parse does) and the large file
// once, and the smallest of three rounds' ratios is kept. A loaded runner
// slows both sides alike, when minima per size cannot: the short 1 MB parse
// dodges preemption while the 8 MB one eats the scheduler's queue, and at
// load ~200 that read 32.8×, past the bound of 24 the fastest-of-five of
// 99299c7b1 left (#4109). A parse that grows with size squared costs 8× the
// small budget where a linear one costs 1×, so the bound of 3 leaves about
// 3× on both sides.
func parseTOMLRatio(t *testing.T, small, large []string) (tomlFile, float64, time.Duration, time.Duration) {
	t.Helper()
	var f tomlFile
	var best float64
	var smallSum, largeAt time.Duration
	for rep := 0; rep < 3; rep++ {
		var sum time.Duration
		for i := 0; i < 8; i++ {
			start := time.Now()
			got, err := parseTOMLFile(small)
			if err != nil {
				t.Fatal(err)
			}
			if rep == 0 && i == 0 {
				f = got
			}
			sum += time.Since(start)
		}
		start := time.Now()
		if _, err := parseTOMLFile(large); err != nil {
			t.Fatal(err)
		}
		d := time.Since(start)
		if r := float64(d) / float64(sum); rep == 0 || r < best {
			best, smallSum, largeAt = r, sum, d
		}
	}
	return f, best, smallSum, largeAt
}

// A big config.toml is parsed in a time that grows with its size, not its
// square: Codex's own reached 1.5 MB, and each key's line was counted from
// the top of the file, a third of a second at every magpie start.
//
// The check is a ratio of parses, not a bound in seconds: a runner at load
// ~190 took the still-linear 3 MB parse past the old 1s bound and went red
// without anything quadratic (#2219). A parse that grows with size squared
// costs 8× the 1 MB budget where a linear one costs 1×, so the bound of 3
// leaves about 3× on both sides.
func TestTOMLBigFileParsesInLinearTime(t *testing.T) {
	small, large := tomlBigFile(1<<20), tomlBigFile(8<<20)
	f, ratio, smallSum, largeAt := parseTOMLRatio(t, small, large)
	if len(f.tables) < 10000 || f.root.keys[0].name != "model_provider" {
		t.Fatalf("parsed %d tables, root %+v", len(f.tables), f.root.keys)
	}
	last := f.tables[len(f.tables)-1]
	if k := last.keys[len(last.keys)-1]; k.name != "model_provider" || k.from != len(small)-2 && k.from != len(small)-1 {
		t.Errorf("last key %+v of %d lines", k, len(small))
	}
	if ratio > 3 {
		t.Errorf("an 8 MB file cost %.1f× the eight 1 MB parses (%v against %v): near the 8× of size squared, not the 1× of size", ratio, largeAt, smallSum)
	}
}
