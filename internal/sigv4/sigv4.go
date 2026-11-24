// Package sigv4 signs a request with AWS Signature Version 4: by hand, a
// few dozen lines, rather than an SDK, for S3-compatible sync (davsync)
// and Amazon Bedrock's access keys (provider).
package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Signer is a credential and where it is used.
type Signer struct {
	ID, Secret string
	// Token is a temporary credential's session token, sent (and signed)
	// in X-Amz-Security-Token; "" for a long-term access key
	Token           string
	Region, Service string
	// Few signs host and the x-amz-* headers alone, for a request whose
	// other headers may still change after it is signed (the gateway's
	// anthropic-beta); every header is signed otherwise, as S3 wants
	Few bool
	// Twice escapes the path once more for its canonical form, as every
	// service but S3 checks it (SigV4's "each path segment URI-encoded
	// twice"); an S3 request's path is used as it is sent
	Twice bool
}

// Sign sets req's X-Amz-Date, X-Amz-Security-Token when there is a token,
// and Authorization, over the headers req has at this point (Few: host and
// x-amz-* alone) and its host. payload is the body's SHA-256, in hex
// (Hash); an S3 server wants it in X-Amz-Content-Sha256 too, which the
// caller sets.
func (s Signer) Sign(req *http.Request, payload string, t time.Time) {
	stamp := t.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	req.Header.Set("X-Amz-Date", stamp)
	if s.Token != "" {
		req.Header.Set("X-Amz-Security-Token", s.Token)
	}

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host}
	for k, vs := range req.Header {
		k = strings.ToLower(k)
		if k == "authorization" || s.Few && !strings.HasPrefix(k, "x-amz-") {
			continue
		}
		vals := make([]string, len(vs))
		for i, v := range vs {
			vals[i] = strings.Join(strings.Fields(v), " ")
		}
		headers[k] = strings.Join(vals, ",")
	}
	names := slices.Sorted(maps.Keys(headers))
	var canon strings.Builder
	for _, k := range names {
		canon.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")

	uri := req.URL.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	if s.Twice {
		uri = Escape(uri, true)
	}
	request := strings.Join([]string{req.Method, uri, canonicalQuery(req.URL), canon.String(), signed, payload}, "\n")
	scope := day + "/" + s.Region + "/" + s.Service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + Hash([]byte(request))

	key := mac([]byte("AWS4"+s.Secret), day)
	key = mac(key, s.Region)
	key = mac(key, s.Service)
	key = mac(key, "aws4_request")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.ID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+hex.EncodeToString(mac(key, toSign)))
}

// Hash is b's SHA-256, in hex: a payload's hash for Sign.
func Hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// canonicalQuery is the query, each name and value escaped as AWS does,
// sorted: ?lifecycle is "lifecycle=".
func canonicalQuery(u *url.URL) string {
	q := u.Query()
	var out []string
	for k, vs := range q {
		for _, v := range vs {
			out = append(out, Escape(k, false)+"="+Escape(v, false))
		}
	}
	slices.Sort(out)
	return strings.Join(out, "&")
}

// Escape is URI encoding as SigV4 has it: all but A–Z a–z 0–9 - _ . ~
// as %XX, upper case, and / kept in a path.
func Escape(s string, path bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~', path && c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

func mac(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}
