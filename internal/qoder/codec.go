package qoder

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

import "strings"

// The body codec: the worker base64-encodes the plaintext with a shuffled
// alphabet, then swaps the first and last thirds of the result. Ported
// verbatim from CLIProxyAPI, whose alphabet and grouping were dumped from a
// live Qoder client and verified end-to-end against captured traffic.
//
// Alphabet (64 chars, dumped from live Qoder.exe memory):
//
//	_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!
//
// Grouping is standard base64: four chars = 24 bits = three plaintext bytes.
// '!' is the 64th char and encodes the 6-bit value 63; '$' is the pad, which
// holds a slot but carries no data.

const (
	// BodyAlphabet is the custom base64 alphabet.
	BodyAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

var bodyAlphabetIndex = func() [256]int8 {
	var m [256]int8
	for i := range m {
		m[i] = -1
	}
	for i, c := range BodyAlphabet {
		m[c] = int8(i)
	}
	return m
}()

// groupBytes decodes one 4-char group to its N bytes, N = floor(6*k/8) where
// k is the number of non-pad chars. '$' is skipped; '!' maps to 63.
func groupBytes(grp string) []byte {
	var vals []int
	for _, c := range grp {
		if c == '$' {
			continue
		}
		vals = append(vals, int(bodyAlphabetIndex[c]))
	}
	nv := len(vals)
	if nv == 0 {
		return nil
	}
	x := 0
	for _, v := range vals {
		x = (x << 6) | v
	}
	nb := (6 * nv) / 8
	out := make([]byte, nb)
	for k := 0; k < nb; k++ {
		sh := 6*nv - 8*(k+1)
		out[k] = byte((x >> sh) & 255)
	}
	return out
}

// segmentEncode encodes bytes through the alphabet as one continuous base64
// bitstream (MSB first): each byte adds 8 bits, every 6 emitted as a char; a
// final partial group is zero-padded into one char; the char count is padded
// out to a multiple of 4 with '$'. The 6-bit value 63 is emitted as '!'.
func segmentEncode(data []byte) string {
	var sb strings.Builder
	acc, nb := 0, 0
	emit := func(v int) { sb.WriteByte(BodyAlphabet[v]) }
	for _, b := range data {
		acc = (acc << 8) | int(b)
		nb += 8
		for nb >= 6 {
			nb -= 6
			emit((acc >> nb) & 0x3F)
		}
	}
	if nb > 0 {
		emit((acc << (6 - nb)) & 0x3F)
	}
	for sb.Len()%4 != 0 {
		sb.WriteByte('$')
	}
	return sb.String()
}

// BodyEncode encodes one plaintext into custom base64 (no third swap).
func BodyEncode(data []byte) string { return segmentEncode(data) }

// BodyDecode decodes custom base64 (no third swap).
func BodyDecode(s string) []byte {
	var out []byte
	for gi := 0; gi+4 <= len(s); gi += 4 {
		if b := groupBytes(s[gi : gi+4]); b != nil {
			out = append(out, b...)
		}
	}
	return out
}

// swapBodyOuterThirds exchanges the first and last thirds of an encoded
// string; the middle third stays put.
func swapBodyOuterThirds(encoded string) string {
	third := len(encoded) / 3
	if third == 0 {
		return encoded
	}
	return encoded[len(encoded)-third:] + encoded[third:len(encoded)-third] + encoded[:third]
}

// EncodeRequestBody is the wire body for an agent_chat_generation request:
// custom base64, then the outer-third swap.
func EncodeRequestBody(plaintext []byte) string {
	return swapBodyOuterThirds(BodyEncode(plaintext))
}

// DecodeRequestBody reverses the swap, then decodes.
func DecodeRequestBody(wire string) []byte {
	return BodyDecode(swapBodyOuterThirds(wire))
}
