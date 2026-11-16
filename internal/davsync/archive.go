package davsync

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Bucket is the S3 sync's bucket for files other than the backup — the
// gateway's request archive — each at <prefix>/magpie/<name>, beside the
// backup, written as it is: no passphrase seals them, so they can be read
// in the bucket too.
type Bucket struct{ s *s3 }

// ErrNoObject is a name with nothing at it.
var ErrNoObject = errors.New("not in the bucket")

// S3Bucket is the bucket sync keeps the backup in; false when sync is off
// or to a WebDAV folder.
func S3Bucket() (*Bucket, bool) {
	c, ok := Load()
	if !ok || !c.S3() {
		return nil, false
	}
	s, err := newS3(c)
	if err != nil {
		return nil, false
	}
	return &Bucket{s}, true
}

// at is the client for name rather than the backup.
func (b *Bucket) at(name string) *s3 {
	s := *b.s
	s.key = strings.TrimSuffix(s.key, file) + strings.TrimLeft(name, "/")
	return &s
}

// Where names the bucket, its prefix and its server, as messages do.
func (b *Bucket) Where() string { return b.at("").where() }

// Put writes data at name, over what is there.
func (b *Bucket) Put(ctx context.Context, name string, data []byte) error {
	s := b.at(name)
	res, err := s.send(ctx, http.MethodPut, data, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return nil
	}
	return s.explain("write", readError(res))
}

// Get reads what is at name, its first 16 MiB; ErrNoObject when there is
// nothing.
func (b *Bucket) Get(ctx context.Context, name string) ([]byte, error) {
	body, _, err := b.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, 16<<20))
}

// Open is what is at name as it is read, however large, and its size (-1
// when the server doesn't say); ErrNoObject when there is nothing. The
// caller closes it.
func (b *Bucket) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	s := b.at(name)
	res, err := s.send(ctx, http.MethodGet, nil, nil)
	if err != nil {
		return nil, 0, err
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		e := readError(res)
		if e.Status == http.StatusNotFound && e.Code != "NoSuchBucket" {
			return nil, 0, ErrNoObject
		}
		return nil, 0, s.explain("read", e)
	}
	return res.Body, res.ContentLength, nil
}

// Each calls f with every object under prefix, a name as Put takes it
// ("archive/"), its name as Put takes it and its size, a page of a
// thousand at a time.
func (b *Bucket) Each(ctx context.Context, prefix string, f func(name string, size int64)) error {
	s := b.at(prefix)
	base := strings.TrimSuffix(b.s.key, file)
	token := ""
	seen := map[string]bool{}
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {s.key}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := s.bucketURL()
		u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
		res, err := s.sendTo(ctx, http.MethodGet, u, nil, nil)
		if err != nil {
			return err
		}
		if res.StatusCode != http.StatusOK {
			e := readError(res)
			res.Body.Close()
			return s.explain("list", e)
		}
		var l struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&l)
		res.Body.Close()
		if err != nil {
			return err
		}
		for _, c := range l.Contents {
			if name, ok := strings.CutPrefix(c.Key, base); ok && strings.HasPrefix(c.Key, s.key) {
				f(name, c.Size)
			}
		}
		if !l.Truncated {
			return nil
		}
		if l.Next == "" || seen[l.Next] {
			return fmt.Errorf("listing %s: incomplete listing (missing or repeated continuation token)", s.where())
		}
		seen[l.Next] = true
		token = l.Next
	}
}

// Delete removes what is at name; nothing there is no error.
func (b *Bucket) Delete(ctx context.Context, name string) error {
	s := b.at(name)
	res, err := s.send(ctx, http.MethodDelete, nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 || res.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return nil
	}
	return s.explain("remove", readError(res))
}
