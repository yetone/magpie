package gateway

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestBodyIndexIsIndex holds bodyIndex to bytes.Index: needles at the
// start, the end, across where the search gives up on rare bytes, absent,
// and made only of common bytes.
func TestBodyIndexIsIndex(t *testing.T) {
	body := largeConversation(provider.Responses, "fake/m1", 20_000)
	needles := []string{`"developer"`, `"compaction`, "image/hei", `image\/hei`, `"display"`,
		`"output_config"`, passthroughKey, `"type"`, `" "`, "  ", `e"`, "eXB", `"input"`,
		string(body[:40]), string(body[len(body)-40:]), string(body[len(body)/2 : len(body)/2+9]), "zzzzQ"}
	for _, n := range needles {
		if got, want := bodyIndex(body, n), bytes.Index(body, []byte(n)); got != want {
			t.Errorf("bodyIndex(%q) = %v, want %v", n, got, want)
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	alpha := []byte(` "eta:,{}vXQ\`)
	for range 3000 {
		b := make([]byte, 1024+r.IntN(4096))
		for i := range b {
			b[i] = alpha[r.IntN(len(alpha))]
		}
		var n string
		if r.IntN(2) == 0 {
			at := r.IntN(len(b) - 8)
			n = string(b[at : at+2+r.IntN(6)])
		} else {
			nb := make([]byte, 2+r.IntN(5))
			for i := range nb {
				nb[i] = alpha[r.IntN(len(alpha))]
			}
			n = string(nb)
		}
		if got, want := bodyIndex(b, n), bytes.Index(b, []byte(n)); got != want {
			t.Fatalf("bodyIndex(%q) = %v, want %v in %q", n, got, want, b)
		}
	}
	if !bodyHas([]byte(strings.Repeat("a", 2000)+`"display"`), `"display"`) {
		t.Error("missed a needle at the very end")
	}
}

func BenchmarkBodyHas(b *testing.B) {
	body := largeConversation(provider.Responses, "fake/m1", 300_000)
	needles := []string{`"developer"`, `"compaction`, "image/hei", `"display"`, `"output_config"`, passthroughKey, `"text"`}
	for _, f := range []struct {
		name string
		has  func([]byte, string) bool
	}{{"contains", func(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }}, {"bodyHas", bodyHas}} {
		b.Run(f.name, func(b *testing.B) {
			b.SetBytes(int64(len(body) * len(needles)))
			for range b.N {
				for _, n := range needles {
					f.has(body, n)
				}
			}
		})
	}
}

// oddBase64Walk is mayHoldOddBase64 as it walked every byte, the answer
// its look back from each quote must give.
func oddBase64Walk(body []byte) bool {
	const least = 64
	start, odd := -1, false
	for i, c := range body {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/', c == '=':
			if start < 0 {
				start, odd = i, false
			}
		case c == '-' || c == '_':
			if start < 0 {
				start = i
			}
			odd = true
		default:
			if start >= 0 && c == '"' && i-start >= least && (odd || (i-start)%4 != 0) {
				return true
			}
			start = -1
		}
	}
	return false
}

func TestOddBase64LooksAsItWalked(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	alpha := []byte(`AZaz09+/=-_" :`)
	yes := 0
	for range 20000 {
		b := make([]byte, r.IntN(400))
		// long runs of one alphabet, so a run reaches 64
		for i := 0; i < len(b); {
			n := 1 + r.IntN(90)
			pick := alpha[:10+r.IntN(5)]
			for j := 0; j < n && i < len(b); j++ {
				b[i] = pick[r.IntN(len(pick))]
				i++
			}
			if i < len(b) {
				b[i] = alpha[10+r.IntN(4)]
				i++
			}
		}
		want := oddBase64Walk(b)
		if want {
			yes++
		}
		if got := mayHoldOddBase64(b); got != want {
			t.Fatalf("mayHoldOddBase64(%q) = %v, want %v", b, got, want)
		}
	}
	if yes < 100 {
		t.Fatalf("only %d of the bodies held odd base64: the test reaches too little", yes)
	}
	for _, p := range []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic} {
		body := largeConversation(p, "fake/m1", 20_000)
		if got, want := mayHoldOddBase64(body), oddBase64Walk(body); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}
