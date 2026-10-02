package davsync

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// s3 keeps the backup in an S3-compatible bucket — AWS S3, Cloudflare R2,
// Backblaze B2, MinIO, Garage, a NAS — at <prefix>/magpie/magpie.magpie-backup,
// as the WebDAV folder does: the object read with its ETag, written back
// with If-Match, the first one with If-None-Match: *. The address is
// s3://bucket/prefix; User is the access key ID and Password its secret.
type s3 struct {
	endpoint    *url.URL // scheme and host, and a path for a server under one
	bucket, key string
	pathStyle   bool
	sig         signer
	client      *http.Client
	now         func() time.Time
}

// defaultRegion is the region for an endpoint given none: R2 signs with
// "auto", everything else as AWS's first.
func defaultRegion(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && strings.HasSuffix(strings.ToLower(u.Hostname()), ".r2.cloudflarestorage.com") {
		return "auto"
	}
	return "us-east-1"
}

// s3Endpoint is the endpoint as typed, with https:// when it has no
// scheme; none is AWS's own for the region.
func s3Endpoint(endpoint, region string) string {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if e == "" {
		return "https://s3." + region + ".amazonaws.com"
	}
	if !strings.Contains(e, "://") {
		e = "https://" + e
	}
	return e
}

func newS3(c Config) (*s3, error) {
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || !strings.EqualFold(u.Scheme, "s3") || u.Host == "" || strings.ContainsAny(u.Host, " :@") {
		return nil, fmt.Errorf("%q is not an S3 address (s3://bucket or s3://bucket/prefix)", c.URL)
	}
	region := strings.TrimSpace(c.Region)
	if region == "" {
		region = defaultRegion(s3Endpoint(c.Endpoint, "us-east-1"))
	}
	e, err := url.Parse(s3Endpoint(c.Endpoint, region))
	if err != nil || (e.Scheme != "https" && e.Scheme != "http") || e.Host == "" || e.RawQuery != "" {
		return nil, fmt.Errorf("%q is not an S3 endpoint (https://…)", c.Endpoint)
	}
	e.Path = strings.TrimRight(e.Path, "/")
	key := folder + "/" + file
	if p := strings.Trim(u.Path, "/"); p != "" {
		key = p + "/" + key
	}
	s := &s3{endpoint: e, bucket: u.Host, key: key, pathStyle: c.PathStyle,
		sig: signer{id: strings.TrimSpace(c.User), secret: c.Password, region: region, service: "s3"}, client: http.DefaultClient, now: time.Now}
	// a bucket can't go before an address or localhost, nor one with a dot
	// in it before a certificate for *.host: the bucket goes in the path
	host := e.Hostname()
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || e.Scheme == "https" && strings.Contains(s.bucket, ".") {
		s.pathStyle = true
	}
	return s, nil
}

// where names the object in messages: bucket/key at the endpoint's host.
func (s *s3) where() string { return s.bucket + "/" + s.key + " at " + s.endpoint.Host }

func (s *s3) objectURL() *url.URL { return s.urlOf(s.key) }

// urlOf is the object key's address; bucketURL the bucket's, for a listing.
func (s *s3) urlOf(key string) *url.URL {
	u := *s.endpoint
	p := u.Path + "/" + key
	if s.pathStyle {
		p = u.Path + "/" + s.bucket + "/" + key
	} else {
		u.Host = s.bucket + "." + u.Host
	}
	u.Path, u.RawPath = p, awsEscape(p, true)
	return &u
}

func (s *s3) bucketURL() *url.URL {
	u := *s.endpoint
	p := u.Path + "/"
	if s.pathStyle {
		p = u.Path + "/" + s.bucket
	} else {
		u.Host = s.bucket + "." + u.Host
	}
	u.Path, u.RawPath = p, awsEscape(p, true)
	return &u
}

func (s *s3) send(ctx context.Context, method string, body []byte, h map[string]string) (*http.Response, error) {
	return s.sendTo(ctx, method, s.objectURL(), body, h)
}

func (s *s3) sendTo(ctx context.Context, method string, u *url.URL, body []byte, h map[string]string) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	payload := sum(body)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	s.sig.sign(req, payload, s.now())
	return s.client.Do(req)
}

// s3Error is what an S3 server says went wrong: its XML body's code and
// message, and the region it names for a bucket that is elsewhere.
type s3Error struct {
	Status  int           `xml:"-"`
	Code    string        `xml:"Code"`
	Message string        `xml:"Message"`
	Region  string        `xml:"Region"`
	After   time.Duration `xml:"-"` // Retry-After's wait, when it said one
}

func readError(res *http.Response) s3Error {
	var e s3Error
	xml.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&e)
	e.Status = res.StatusCode
	e.After = retryAfter(res.Header.Get("Retry-After"))
	if e.Region == "" {
		e.Region = res.Header.Get("X-Amz-Bucket-Region")
	}
	return e
}

func (e s3Error) String() string {
	s := fmt.Sprintf("HTTP %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	return s
}

// explain says what an answer to reading or writing ("read", "write")
// meant.
func (s *s3) explain(op string, e s3Error) error {
	switch {
	// 503 SlowDown: AWS's, and others', for too many requests
	case e.Status == http.StatusTooManyRequests || e.Status == http.StatusServiceUnavailable || e.Code == "SlowDown":
		return &rateLimited{kind: "S3", status: e.Status, after: e.After}
	case e.Code == "InvalidAccessKeyId" || e.Code == "SignatureDoesNotMatch" || e.Status == http.StatusUnauthorized:
		return fmt.Errorf("the S3 server refused the access key ID or secret (%s)", e)
	case e.Code == "RequestTimeTooSkewed":
		return fmt.Errorf("the S3 server says this computer's clock is off (%s): set it right and sync again", e)
	case e.Region != "" && e.Region != s.sig.region &&
		(e.Status == http.StatusMovedPermanently || e.Code == "AuthorizationHeaderMalformed" || e.Code == "PermanentRedirect" || e.Code == "IllegalLocationConstraintException"):
		return fmt.Errorf("the bucket %s is in the region %s, not %s (%s): set the region to %s", s.bucket, e.Region, s.sig.region, e, e.Region)
	case e.Status == http.StatusMovedPermanently || e.Code == "PermanentRedirect":
		return fmt.Errorf("the S3 server says the bucket %s is at another endpoint (%s): check the endpoint and the region", s.bucket, e)
	case e.Code == "NoSuchBucket":
		return fmt.Errorf("there is no bucket %s at %s (%s): make it there first", s.bucket, s.endpoint.Host, e)
	case e.Status == http.StatusForbidden && op == "read":
		// AWS answers 403, not 404, for an object not there when the key
		// may not list the bucket: before the first sync, it isn't there
		return fmt.Errorf("the S3 server doesn't let this access key read %s (%s): it needs to read and write there — on AWS, list the bucket too (s3:ListBucket), or a file not there yet reads as forbidden", s.where(), e)
	case e.Status == http.StatusForbidden:
		return fmt.Errorf("the S3 server doesn't let this access key write %s (%s): it needs to read and write there", s.where(), e)
	}
	verb := map[string]string{"read": "reading", "write": "writing"}[op]
	if e.Message != "" {
		return fmt.Errorf("%s %s on the S3 server: %s: %s", verb, s.where(), e, e.Message)
	}
	return fmt.Errorf("%s %s on the S3 server: %s", verb, s.where(), e)
}

// get reads the backup; nil data and no error when there is none yet, and
// errNotModified when it is still have: the If-None-Match is signed with
// the rest.
func (s *s3) get(ctx context.Context, have version) (data []byte, v version, err error) {
	cond := have.conditions()
	res, err := s.send(ctx, http.MethodGet, nil, cond)
	if err != nil {
		return nil, version{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified && cond != nil {
		return nil, have, errNotModified
	}
	if res.StatusCode != http.StatusOK {
		e := readError(res)
		if e.Status == http.StatusNotFound && e.Code != "NoSuchBucket" {
			return nil, version{}, nil
		}
		return nil, version{}, s.explain("read", e)
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, version{}, err
	}
	return data, versionOf(res.Header), nil
}

// unconditional are the servers, by endpoint and bucket, found to refuse a
// conditional PUT: each write there looks at the ETag first instead.
var unconditional sync.Map

// put writes the backup over the version read (etag) — or, with none, only
// where there is none yet — and errChanged when another computer wrote in
// between.
func (s *s3) put(ctx context.Context, data []byte, etag string) (version, error) {
	where := s.endpoint.String() + " " + s.bucket
	if _, no := unconditional.Load(where); !no {
		h := map[string]string{"Content-Type": "application/octet-stream"}
		if etag != "" {
			h["If-Match"] = etag
		} else {
			h["If-None-Match"] = "*"
		}
		res, err := s.send(ctx, http.MethodPut, data, h)
		if err != nil {
			return version{}, err
		}
		defer res.Body.Close()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return version{ETag: res.Header.Get("ETag")}, nil
		}
		e := readError(res)
		switch {
		// 409 ConditionalRequestConflict: AWS's for two conditional writes at once
		case e.Status == http.StatusPreconditionFailed || e.Code == "ConditionalRequestConflict":
			return version{}, errChanged
		case !noConditions(e):
			return version{}, s.explain("write", e)
		}
		unconditional.Store(where, true)
	}
	// A server without conditional writes (an older MinIO, Ceph or Garage,
	// many a NAS) answers them 501 NotImplemented, or 400 naming the
	// header. There the ETag is looked at with a HEAD just before an
	// unconditional PUT: another computer's write in the moment between
	// the two would be lost, where a conditional PUT would refuse it, but
	// one at any other time since this computer read is still caught.
	now, there, err := s.head(ctx)
	if err != nil {
		return version{}, err
	}
	if there != (etag != "") || there && bareETag(now) != bareETag(etag) {
		return version{}, errChanged
	}
	res, err := s.send(ctx, http.MethodPut, data, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return version{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return version{ETag: res.Header.Get("ETag")}, nil
	}
	return version{}, s.explain("write", readError(res))
}

// noConditions is an answer to a conditional PUT that says the server
// doesn't do them, not that the condition failed.
func noConditions(e s3Error) bool {
	if e.Status == http.StatusNotImplemented || e.Code == "NotImplemented" {
		return true
	}
	m := strings.ToLower(e.Message)
	return e.Status == http.StatusBadRequest && (strings.Contains(m, "if-match") || strings.Contains(m, "if-none-match") || strings.Contains(m, "conditional"))
}

// head is the object's ETag now, and whether it is there at all.
func (s *s3) head(ctx context.Context) (etag string, there bool, err error) {
	res, err := s.send(ctx, http.MethodHead, nil, nil)
	if err != nil {
		return "", false, err
	}
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusOK:
		return res.Header.Get("ETag"), true, nil
	case res.StatusCode == http.StatusNotFound:
		return "", false, nil
	}
	// a HEAD has no body to say why: the status alone
	return "", false, s.explain("read", s3Error{Status: res.StatusCode, Region: res.Header.Get("X-Amz-Bucket-Region")})
}

func bareETag(e string) string {
	return strings.Trim(strings.TrimPrefix(strings.TrimSpace(e), "W/"), `"`)
}
