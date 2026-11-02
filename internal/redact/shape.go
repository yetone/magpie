package redact

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// A key can go to the vendor as a made-up key of its own form instead of
// a placeholder (Options.Shape): sk_w3axCByTQgK1Px… as sk_hYQWRBFmdHNI…,
// with the same prefix, length and separators and the same kind of
// character in each place. An agent's or a tool's check of a key's form
// passes, and the model has no placeholder to remark on (Charles Cao on X).
// It is made from the placeholder key as a placeholder is, so one key goes
// as the same made-up one every turn and a vendor's cache still matches,
// and it comes back as it was wherever the vendor writes it.
//
// Only a secret's kind, of minShape characters or more with minRandom of
// them made up (not its prefix or separators), and only letters, digits,
// '-', '_' and '.': a made-up key like that is in no other text by chance,
// and it needs no escaping in JSON. Anything else goes as a placeholder
// still. 16 left a short key such as sk_abcdefg a placeholder, which read
// as the setting not working.

const (
	minShape  = 8
	minRandom = 6
)

var (
	// shapes are the made-up keys in values, with their kinds; guarded by
	// mu, as values is
	shapes = map[string]string{}
)

// keyPrefixes name a key's kind across more than one part, and are kept as
// they are; keyHead keeps one part of a few letters.
var keyPrefixes = []string{"sk-ant-api03-", "sk-ant-oat01-", "sk-ant-admin01-", "sk-proj-", "sk-svcacct-", "sk-or-v1-", "github_pat_"}

// keyHead is the start of v that names its kind and stays as it is: one of
// keyPrefixes, or up to four letters and the '-' or '_' after them (sk-,
// ghp_, xoxb-), or AWS's AKIA and ASIA. A head without its separator could
// be the key's own random letters, which stay with the user.
func keyHead(v string) string {
	for _, p := range keyPrefixes {
		if strings.HasPrefix(v, p) {
			return p
		}
	}
	if strings.HasPrefix(v, "AKIA") || strings.HasPrefix(v, "ASIA") {
		return v[:4]
	}
	for i := 0; i < len(v) && i <= 4; i++ {
		c := v[i]
		if (c == '-' || c == '_') && i > 0 {
			return v[:i+1]
		}
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			break
		}
	}
	return ""
}

// namedValue is a name before = or : and the value after it.
var namedValue = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*[ \t]*[=:][ \t]*()`)

// splitName parts a value found with its name, as 密钥
// OPENAI_API_KEY=sk_… is, into the name, which stays as written, and the
// key after it, which alone is made up and put back.
func splitName(v string) (name, key string) {
	if m := namedValue.FindStringSubmatchIndex(v); m != nil {
		return v[:m[2]], v[m[2]:]
	}
	return "", v
}

func shapeByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// shapeOf is the made-up key v of kind goes as, if it may go as one.
func shapeOf(kind, v string) (string, bool) {
	if _, personal := kindCats[kind]; personal || kind == "TERM" || len(v) < minShape {
		return "", false
	}
	for i := 0; i < len(v); i++ {
		if !shapeByte(v[i]) {
			return "", false
		}
	}
	head := keyHead(v)
	rest := v[len(head):]
	// a hex key's characters are any hex digit, of the case it has
	hexLower, hexUpper := true, true
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c >= '0' && c <= '9', c == '-' || c == '_' || c == '.':
		case c >= 'a' && c <= 'f':
			hexUpper = false
		case c >= 'A' && c <= 'F':
			hexLower = false
		default:
			hexLower, hexUpper = false, false
		}
	}
	h := hmac.New(sha256.New, key())
	h.Write([]byte("shape\x00" + kind + "\x00" + v))
	seed := h.Sum(nil)
	var stream []byte
	n := 0
	next := func(k int) byte {
		if len(stream) == 0 {
			h := hmac.New(sha256.New, seed)
			var c [8]byte
			binary.BigEndian.PutUint64(c[:], uint64(n))
			n++
			h.Write(c[:])
			stream = h.Sum(nil)
		}
		b := stream[0]
		stream = stream[1:]
		return b % byte(k)
	}
	const (
		upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		lower = "abcdefghijklmnopqrstuvwxyz"
		digit = "0123456789"
	)
	out, made := []byte(head), 0
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		set := ""
		switch {
		case c == '-' || c == '_' || c == '.':
		case hexLower:
			set = digit + "abcdef"
		case hexUpper:
			set = digit + "ABCDEF"
		case c >= 'A' && c <= 'Z':
			set = upper
		case c >= 'a' && c <= 'z':
			set = lower
		default:
			set = digit
		}
		if set == "" {
			out = append(out, c)
			continue
		}
		made++
		out = append(out, set[next(len(set))])
	}
	if made < minRandom {
		return "", false
	}
	f := string(out)
	return f, f != v
}

// shaped is put for Options.Shape: v as a made-up key of its form where it
// may go as one, else as its placeholder.
func shaped(kind, v string) string {
	name, k := splitName(v)
	f, ok := shapeOf(kind, k)
	if !ok {
		return placeholder(kind, v)
	}
	mu.Lock()
	have, taken := values[f]
	if !taken {
		makeRoom()
		values[f] = k
		kinds[kind] = true
		shapes[f] = kind
		addKnown(f, k)
	}
	touch(f)
	mu.Unlock()
	if taken && have != k {
		// another value's made-up key, which nothing ever is by chance
		return placeholder(kind, v)
	}
	return name + f
}

// restoreShapes puts back each made-up key in s, counting it in put
// where put isn't nil; called with mu held for reading. A made-up key
// needs no escaping, nor does the value it stands for. An address's
// stand-in is put back only where it is a whole name: public-ip-kvxqmrtz
// is no part of public-ip-kvxqmrtzab or my-public-ip-kvxqmrtz.
//
// A user name's and an address's stand-ins are put back in any case
// (foldStandIn). A model takes a made-up login name for the user's own
// name and writes it as one: "Hi Qmzvkrta," or "作者：Qmzvkrta", which
// came back to the user as letters that are no one's (yapechan on
// Discord: a name was put back as garbage, with no braces). A host's name
// is the same in any case too. Eight made-up letters are in no other text
// by chance in any case, as a placeholder's 8 of its hash are not
// (canonical). A key's made-up form is put back only as written: a key's
// case is part of it.
func restoreShapes(s string, put map[string]int) string {
	lower, stale := "", true // s in small letters, made when first needed
	for f, kind := range shapes {
		if !foldStandIn(kind) {
			if n := strings.Count(s, f); n > 0 {
				s = strings.ReplaceAll(s, f, values[f])
				stale = true
				if put != nil {
					put[f] += n
				}
			}
			continue
		}
		if stale {
			lower, stale = lowerASCII(s), false
		}
		if !strings.Contains(lower, f) {
			continue
		}
		whole := wholeAddr
		if kind != "IP" {
			whole = nil
		}
		if out := replaceStandIn(s, lower, f, values[f], put, whole); out != s {
			s, stale = out, true
		}
	}
	return s
}

// foldStandIn says a made-up key of kind is a stand-in of lowercase
// letters (standInOf, ipStandIns), found in any case.
func foldStandIn(kind string) bool { return kind == "USERNAME" || kind == "IP" }

// wholeAddr says s[a:z], an address's stand-in, is a whole name: not
// where a letter, a digit, '-' or '_' is against it, or a dot before it
// or before a letter or digit after it, which make it a piece of another
// name (public-ip-kvxqmrtz.lan); a port, a path or a full stop may
// follow it.
func wholeAddr(s string, a, z int) bool {
	return !(a > 0 && (nameByte(s[a-1]) || s[a-1] == '.') || z < len(s) && nameByte(s[z]) ||
		z+1 < len(s) && s[z] == '.' && nameByte(s[z+1]))
}

// replaceStandIn is s with each stand-in f in it, in any case (found in
// lower, lowerASCII(s)), put back as v where whole says it may be
// (anywhere with whole nil), counted in put where put isn't nil.
func replaceStandIn(s, lower, f, v string, put map[string]int, whole func(s string, a, z int) bool) string {
	var b strings.Builder
	last := 0
	for i := 0; ; {
		j := strings.Index(lower[i:], f)
		if j < 0 {
			break
		}
		a, z := i+j, i+j+len(f)
		i = a + 1
		if whole != nil && !whole(s, a, z) {
			continue
		}
		b.WriteString(s[last:a])
		b.WriteString(v)
		last, i = z, z
		if put != nil {
			put[f]++
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// nameByte says c may be part of a host's name beside a stand-in: a
// letter, a digit, '-' or '_'.
func nameByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_'
}

// shapeTail is how much of the end of s may be the start of a made-up
// key, which the next piece of a stream goes on with: the longest end of s
// that one of them starts with, after the last whole one in s. A key that
// ends as one starts (…sqZs, sk_…) has come whole, and is put back whole.
func shapeTail(s string) int {
	mu.RLock()
	defer mu.RUnlock()
	lower := lowerASCII(s)
	whole := 0
	for f, kind := range shapes {
		i := strings.LastIndex(s, f)
		if foldStandIn(kind) {
			i = strings.LastIndex(lower, f)
		}
		if i >= 0 {
			whole = max(whole, i+len(f))
		}
	}
	best := 0
	for f, kind := range shapes {
		for k := min(len(f)-1, len(s)-whole); k > best; k-- {
			if strings.HasSuffix(s, f[:k]) || foldStandIn(kind) && strings.HasSuffix(lower, f[:k]) {
				best = k
				break
			}
		}
	}
	return best
}

// hasShapes says some made-up key is in b. With none made, as while
// Options.Shape is off, a whole reply isn't copied to find out.
func hasShapes(b []byte) bool {
	mu.RLock()
	defer mu.RUnlock()
	if len(shapes) == 0 {
		return false
	}
	s := string(b)
	lower := ""
	for f, kind := range shapes {
		if strings.Contains(s, f) {
			return true
		}
		if foldStandIn(kind) {
			if lower == "" {
				lower = lowerASCII(s)
			}
			if strings.Contains(lower, f) {
				return true
			}
		}
	}
	return false
}

// outsideShapes is found without what lies in a made-up key already in s,
// as a placeholder's inside is left alone: a key that came back unrestored
// is not masked again as a key of its own.
func outsideShapes(s string, found []span) []span {
	mu.RLock()
	defer mu.RUnlock()
	lower := lowerASCII(s)
	for f, kind := range shapes {
		in := s
		if foldStandIn(kind) {
			in = lower
		}
		for i := 0; ; {
			a := strings.Index(in[i:], f)
			if a < 0 {
				break
			}
			a += i
			z := a + len(f)
			found = slices.DeleteFunc(found, func(x span) bool { return x.start < z && x.end > a })
			i = z
		}
	}
	return found
}

// A user name goes to the vendor as a made-up name, not a placeholder:
// C:\Users\eerttreeeert as C:\Users\qmzvkrta, never C:\Users\{{USERNAME_…}}.
// A path with braces in it reads as a template's or a temporary folder's,
// and an agent on mimo-v2.6-flash took the user's home for a temporary
// folder and set out to delete it (eerttreeeert on X, 10-28). A made-up
// name keeps the path a home folder's, which is what it is, and still
// tells the vendor nothing of the user. It is made from the name as a
// made-up key is (shapeOf), so it is the same every turn and a vendor's
// cache still matches, and it is put back wherever the vendor writes it
// (restoreShapes), in a tool call's path too. Only USERNAME's: what the
// user chose to mask (their words) goes as they chose.

// standInLen is a made-up name's length: eight lowercase letters, which
// read as a login name and are in no other text by chance.
const standInLen = 8

// standInOf is the made-up name the user name v goes as.
func standInOf(v string) string {
	h := hmac.New(sha256.New, key())
	h.Write([]byte("user\x00" + v))
	sum := h.Sum(nil)
	out := make([]byte, standInLen)
	for i := range out {
		out[i] = 'a' + sum[i]%26
	}
	return string(out)
}

// A public IP address goes to the vendor as a host's name that says what
// the address is, public-ip- and eight lowercase letters
// (public-ip-kvxqmrtz), not {{IP_…}} and not another address. Told "my
// server is {{IP_…}}, ssh in", models asked the user for the real address
// or searched ~/.ssh and the environment for it (0x0_air on X, 10-28).
// Told an address from the documentation ranges (192.0.2.0/24 and the
// like), they ran ssh, but every model knows those ranges: asked why ssh
// to it timed out, deepseek-v4-pro, kimi-k3, gpt-6-luna and
// claude-sonnet-5-5 told the user they had an example address and not
// their server's, and a 192.0.2. address read to the user as one on their
// own network (akic404 on Discord, 10-31: 公网 IP 被脱敏成内网 IP). Told
// public-ip-kvxqmrtz, the same five models ran ssh root@public-ip-kvxqmrtz
// at once, which comes back as the real address in the tool call, and
// took it for the public address it is when asked what was wrong. A name
// with no dot in it is no one's on the internet, so a stand-in the vendor
// tries itself reaches nobody. The same address gets the same stand-in
// every turn while magpie runs, and two addresses never share one; it is
// put back only as a whole name (wholeAddr).

// ipStandInHead starts every address's stand-in, and ipStandInLen
// letters follow it.
const (
	ipStandInHead = "public-ip-"
	ipStandInLen  = 8
)

// ipStandIns are the stand-ins the address v may go as, in the order
// they are tried: n of them, the i-th made by at(i), with nothing locked,
// so it may be called with mu held. False where v is no address.
func ipStandIns(v string) (at func(i int) string, n int, ok bool) {
	a, err := netip.ParseAddr(v)
	if err != nil {
		return nil, 0, false
	}
	k := key()
	return func(i int) string {
		h := hmac.New(sha256.New, k)
		h.Write([]byte("ip\x00" + a.Unmap().String()))
		var c [8]byte
		binary.BigEndian.PutUint64(c[:], uint64(i))
		h.Write(c[:])
		sum := h.Sum(nil)
		out := []byte(ipStandInHead)
		for j := 0; j < ipStandInLen; j++ {
			out = append(out, 'a'+sum[j]%26)
		}
		return string(out)
	}, 8, true
}

// ipStandIn is the stand-in the address v goes as, kept to be put back as
// a made-up key is: the one it went as before, or the first that is no
// other value's. False where v is no address or every one is taken.
func ipStandIn(v string) (string, bool) {
	at, n, ok := ipStandIns(v)
	if !ok {
		return "", false
	}
	mu.Lock()
	defer mu.Unlock()
	free := ""
	for i := 0; i < n; i++ {
		f := at(i)
		have, taken := values[f]
		if taken && have == v {
			touch(f)
			return f, true
		}
		if !taken && free == "" {
			free = f
		}
	}
	if free == "" {
		return "", false
	}
	makeRoom()
	values[free] = v
	kinds["IP"] = true
	shapes[free] = "IP"
	addKnown(free, v)
	touch(free)
	return free, true
}

// standIn is put with a user name sent as its made-up name and an IP
// address as a public-ip- name, each kept to be put back as a made-up
// key is; put's own where that name is another value's already.
func standIn(put func(kind, v string) string) func(kind, v string) string {
	return func(kind, v string) string {
		if kind == "IP" {
			if f, ok := ipStandIn(v); ok {
				return f
			}
			return put(kind, v)
		}
		if kind != "USERNAME" {
			return put(kind, v)
		}
		f := standInOf(v)
		if f == v {
			return put(kind, v)
		}
		mu.Lock()
		have, taken := values[f]
		if !taken {
			makeRoom()
			values[f] = v
			kinds[kind] = true
			shapes[f] = kind
			addKnown(f, v)
		}
		touch(f)
		mu.Unlock()
		if taken && have != v {
			return put(kind, v)
		}
		return f
	}
}
