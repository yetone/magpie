package davsync

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"strings"
	"sync"
)

// digestTransport answers a WebDAV server's Digest challenge (RFC 7616: MD5
// and SHA-256, qop=auth). The callers sign every request with Basic, as they
// always did; when the server answers 401 with a Digest challenge, the
// request is sent again with a Digest header made from the same user name
// and password, and the challenge is kept, so the requests after it carry
// their Digest header at once and Basic is not sent to that server again.
// A server that asks for Basic is not touched.
type digestTransport struct {
	next http.RoundTripper

	mu   sync.Mutex
	last *digestChallenge // the challenge in force, nil until a server sends one
	nc   uint32
}

type digestChallenge struct {
	realm, nonce, opaque, algorithm string
	qopAuth                         bool
	newHash                         func() hash.Hash
	session                         bool
}

func (t *digestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	user, pass, ok := req.BasicAuth()
	if !ok {
		return t.next.RoundTrip(req)
	}
	t.mu.Lock()
	ch := t.last
	t.mu.Unlock()
	first := req
	if ch != nil {
		first = signedDigest(t, req, ch, user, pass)
	}
	res, err := t.next.RoundTrip(first)
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		return res, err
	}
	fresh := parseDigest(res.Header.Values("WWW-Authenticate"))
	if fresh == nil {
		return res, nil
	}
	// the body can only be sent again when it can be made again
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return res, nil
	}
	res.Body.Close()
	t.mu.Lock()
	t.last, t.nc = fresh, 0
	t.mu.Unlock()
	return t.next.RoundTrip(signedDigest(t, req, fresh, user, pass))
}

// signedDigest is a copy of req with its Authorization made for ch.
func signedDigest(t *digestTransport, req *http.Request, ch *digestChallenge, user, pass string) *http.Request {
	t.mu.Lock()
	t.nc++
	nc := t.nc
	t.mu.Unlock()
	out := req.Clone(req.Context())
	if req.GetBody != nil {
		if b, err := req.GetBody(); err == nil {
			out.Body = b
		}
	}
	out.Header.Set("Authorization", ch.authorize(req.Method, req.URL.RequestURI(), user, pass, nc))
	return out
}

func (c *digestChallenge) authorize(method, uri, user, pass string, nc uint32) string {
	h := func(s string) string {
		x := c.newHash()
		x.Write([]byte(s))
		return hex.EncodeToString(x.Sum(nil))
	}
	cnonceRaw := make([]byte, 8)
	rand.Read(cnonceRaw)
	cnonce := hex.EncodeToString(cnonceRaw)
	ha1 := h(user + ":" + c.realm + ":" + pass)
	if c.session {
		ha1 = h(ha1 + ":" + c.nonce + ":" + cnonce)
	}
	ha2 := h(method + ":" + uri)
	ncs := fmt.Sprintf("%08x", nc)
	var resp string
	if c.qopAuth {
		resp = h(ha1 + ":" + c.nonce + ":" + ncs + ":" + cnonce + ":auth:" + ha2)
	} else {
		resp = h(ha1 + ":" + c.nonce + ":" + ha2)
	}
	q := func(s string) string {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	parts := []string{
		"username=" + q(user), "realm=" + q(c.realm), "nonce=" + q(c.nonce),
		"uri=" + q(uri), "algorithm=" + c.algorithm, "response=" + q(resp),
	}
	if c.qopAuth {
		parts = append(parts, "qop=auth", "nc="+ncs, "cnonce="+q(cnonce))
	}
	if c.opaque != "" {
		parts = append(parts, "opaque="+q(c.opaque))
	}
	return "Digest " + strings.Join(parts, ", ")
}

// parseDigest picks the challenge to answer from a response's
// WWW-Authenticate headers: SHA-256 over MD5 when the server offers both.
// nil when there is no Digest challenge magpie can answer.
func parseDigest(values []string) *digestChallenge {
	var best *digestChallenge
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < 7 || !strings.EqualFold(v[:7], "digest ") {
			continue
		}
		p := authParams(v[7:])
		c := &digestChallenge{realm: p["realm"], nonce: p["nonce"], opaque: p["opaque"]}
		if c.nonce == "" {
			continue
		}
		alg := strings.ToUpper(p["algorithm"])
		if alg == "" {
			alg = "MD5"
		}
		if strings.HasSuffix(alg, "-SESS") {
			c.session = true
		}
		switch strings.TrimSuffix(alg, "-SESS") {
		case "MD5":
			c.newHash = md5.New
		case "SHA-256":
			c.newHash = sha256.New
		default:
			continue
		}
		c.algorithm = alg
		for _, q := range strings.Split(p["qop"], ",") {
			if strings.TrimSpace(q) == "auth" {
				c.qopAuth = true
			}
		}
		if p["qop"] != "" && !c.qopAuth {
			continue // only auth-int is offered: not answered
		}
		if best == nil || (strings.HasPrefix(alg, "SHA-256") && !strings.HasPrefix(best.algorithm, "SHA-256")) {
			best = c
		}
	}
	return best
}

// authParams reads key=value pairs, a value a token or a quoted string.
func authParams(s string) map[string]string {
	out := map[string]string{}
	for {
		s = strings.TrimLeft(s, ", \t")
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return out
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = strings.TrimLeft(s[eq+1:], " \t")
		var val string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			val = b.String()
			if i < len(s) {
				i++
			}
			s = s[i:]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			val = strings.TrimSpace(s[:end])
			s = s[end:]
		}
		out[key] = val
	}
}
