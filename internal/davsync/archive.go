package davsync

import (
	"context"
	"errors"
	"io"
	"net/http"
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
