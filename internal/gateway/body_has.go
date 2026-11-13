package gateway

import (
	"bytes"
	"strings"
	"unsafe"
)

// bodyHas is bytes.Contains(b, sep) for the request path's many "is this
// key in the body at all" checks. Most of their needles begin with a
// quote, the commonest byte in JSON: bytes.Index stops at every quote,
// soon gives up on that, and compares at every byte of the body, ~45
// times a request. A 2 MB body spent ~9 ms a request there, a third of
// the gateway's CPU (BenchmarkLargeRequest). bodyHas looks for the
// needle's rarest byte instead, which the CPU finds many bytes at a
// time, and compares the needle only where that byte is.
func bodyHas(b []byte, sep string) bool { return bodyIndex(b, sep) >= 0 }

// bodyIndex is bytes.Index(b, sep), found as bodyHas finds it.
func bodyIndex(b []byte, sep string) int {
	if len(sep) < 2 || len(b) < 1024 {
		return indexStr(b, sep)
	}
	k := rarestByte(sep)
	c := sep[k]
	misses := 0
	for i := k; i < len(b); {
		j := bytes.IndexByte(b[i:], c)
		if j < 0 {
			return -1
		}
		at := i + j - k
		if at+len(sep) > len(b) {
			return -1
		}
		if string(b[at:at+len(sep)]) == sep {
			return at
		}
		i += j + 1
		// a needle of only common bytes stops too often: what is left
		// goes to bytes.Index, which is no slower than before
		if misses++; misses > 64 && misses*64 > i {
			if n := indexStr(b[at+1:], sep); n >= 0 {
				return at + 1 + n
			}
			return -1
		}
	}
	return -1
}

// indexStr is strings.Index over b, which a []byte(sep) of a long
// needle would have copied to the heap on each call.
func indexStr(b []byte, sep string) int {
	return strings.Index(unsafe.String(unsafe.SliceData(b), len(b)), sep)
}

// commonBytes are the bytes JSON, English and code use most, commonest
// first; a byte not here is rarer than all of them.
const commonBytes = " \"etaoinsrhldcu:,mpf{}gy_wb.\nv/k-x0123456789TISACE\\"

// byteRank is a byte's place in commonBytes counted from the end: higher
// is commoner, 0 is rare.
var byteRank = func() (r [256]uint8) {
	for i := range len(commonBytes) {
		r[commonBytes[i]] = uint8(len(commonBytes) - i)
	}
	return r
}()

// rarestByte is the index of sep's rarest byte, the last of equals.
func rarestByte(sep string) int {
	k := 0
	for i := 1; i < len(sep); i++ {
		if byteRank[sep[i]] <= byteRank[sep[k]] {
			k = i
		}
	}
	return k
}

// bodyHasBytes is bodyHas for a needle kept as bytes.
func bodyHasBytes(b, sep []byte) bool { return bodyIndex(b, bytesStr(sep)) >= 0 }

// bodyIndexBytes is bodyIndex for a needle kept as bytes.
func bodyIndexBytes(b, sep []byte) int { return bodyIndex(b, bytesStr(sep)) }

// bytesStr reads b as a string without copying it; b must not change
// while the string is in use.
func bytesStr(b []byte) string { return unsafe.String(unsafe.SliceData(b), len(b)) }
