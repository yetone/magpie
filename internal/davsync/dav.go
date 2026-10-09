package davsync

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/backup"
)

// folder and file are where the backup lives under the address given.
const (
	folder = "magpie"
	file   = "magpie.magpie-backup"
)

// errChanged is a PUT the server refused because the file changed since it
// was read: another computer synced in between.
var errChanged = errors.New("the file on the server changed meanwhile")

// errNotModified is a read sent with the version last seen that the server
// answered 304: the file is the one read then, and wasn't sent again.
var errNotModified = errors.New("the file on the server is unchanged")

// version is how the server tells one version of the file from another:
// its ETag, or, from a server without them, its Last-Modified — kept only
// when it was a second or more before the server's clock at the read, so
// that a write later in that same second can't go unseen.
type version struct {
	ETag     string `json:"etag,omitempty"`
	Modified string `json:"modified,omitempty"`
}

// conditions are the headers that ask for the file only when it isn't v.
func (v version) conditions() map[string]string {
	switch {
	case v.ETag != "":
		return map[string]string{"If-None-Match": v.ETag}
	case v.Modified != "":
		return map[string]string{"If-Modified-Since": v.Modified}
	}
	return nil
}

// versionOf is the version an answer's headers tell; none when they tell
// nothing that can be asked about again.
func versionOf(h http.Header) version {
	if e := h.Get("ETag"); e != "" {
		return version{ETag: e}
	}
	lm := h.Get("Last-Modified")
	t, err := http.ParseTime(lm)
	if err != nil {
		return version{}
	}
	if d, err := http.ParseTime(h.Get("Date")); err != nil || d.Sub(t) < time.Second {
		return version{}
	}
	return version{Modified: lm}
}

// rateLimited is a server that answered 429 Too Many Requests or 503: it
// is limiting how often it is reached — 坚果云 (Nutstore) counts requests
// and downloads per account — or is busy. Run waits longer before trying
// again: after, when the server said how long.
type rateLimited struct {
	kind   string // "WebDAV" or "S3"
	status int
	after  time.Duration
}

func (e *rateLimited) Error() string {
	if e.status == http.StatusTooManyRequests {
		return fmt.Sprintf("the %s server is limiting how often magpie may reach it (HTTP 429 Too Many Requests): sync waits longer before trying again", e.kind)
	}
	return fmt.Sprintf("the %s server is busy, or limiting how often magpie may reach it (HTTP %d): sync waits longer before trying again", e.kind, e.status)
}

// limited is the error for an answer that says the server is limiting
// requests; nil for any other.
func limited(kind string, status int, h http.Header) error {
	if status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable {
		return nil
	}
	return &rateLimited{kind: kind, status: status, after: retryAfter(h.Get("Retry-After"))}
}

// retryAfter is a Retry-After header's wait, in seconds or until a time;
// 0 when it says none.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// remote is where the backup is kept: a WebDAV folder, or an S3 bucket.
type remote interface {
	// get reads the backup and its version; nil data and no error when
	// there is none yet. With have, the version last read, it is asked
	// for only if it changed since: errNotModified when it didn't.
	get(ctx context.Context, have version) (data []byte, v version, err error)
	// put writes it over the version read (etag) — with none, only where
	// there is none yet — and errChanged when another computer wrote in
	// between. The version written is the one the server says, if it does.
	put(ctx context.Context, data []byte, etag string) (version, error)
}

// newRemote is c's: an S3 bucket for an s3:// address, WebDAV for the rest.
func newRemote(c Config) (remote, error) {
	if c.S3() {
		return newS3(c)
	}
	return newDAV(c)
}

type dav struct {
	base       *url.URL
	user, pass string
	client     *http.Client
}

func newDAV(c Config) (*dav, error) {
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%q is not a WebDAV address (https://…)", c.URL)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return &dav{base: u, user: c.User, pass: c.Password, client: syncClient}, nil
}

func (d *dav) url(parts ...string) string {
	u := *d.base
	u.Path += "/" + strings.Join(parts, "/")
	return u.String()
}

func (d *dav) do(ctx context.Context, method, u string, body []byte, h map[string]string) (*http.Response, error) {
	res, err := d.send(ctx, method, u, body, h)
	if err == nil && res.StatusCode == http.StatusForbidden {
		res.Body.Close()
		return nil, d.forbidden(ctx)
	}
	return res, err
}

// send is do with a 403 handed back as it is, for the calls that can tell
// what it meant.
func (d *dav) send(ctx context.Context, method, u string, body []byte, h map[string]string) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if d.user != "" || d.pass != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	res, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		return nil, errLogin
	}
	if err := limited("WebDAV", res.StatusCode, res.Header); err != nil {
		res.Body.Close()
		return nil, err
	}
	return res, nil
}

var errLogin = errors.New("the WebDAV server refused the user name or password (HTTP 401)")

// forbidden says why a 403 was: servers answer it for a folder that isn't
// there as much as for one the account may not use — a Synology does for a
// shared folder not made — so the folder in the address is looked at first.
func (d *dav) forbidden(ctx context.Context) error {
	dir := d.base.Path
	if dir == "" {
		dir = "/"
	}
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", d.url(), nil)
	if err == nil {
		req.Header.Set("Depth", "0")
		if d.user != "" || d.pass != "" {
			req.SetBasicAuth(d.user, d.pass)
		}
		if res, err := d.client.Do(req); err == nil {
			res.Body.Close()
			switch {
			case res.StatusCode == http.StatusUnauthorized:
				return errLogin
			case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict || res.StatusCode == http.StatusGone:
				return fmt.Errorf("there is no folder %s on the WebDAV server (HTTP 403): make it there first, or leave it out of the address", dir)
			case res.StatusCode >= 200 && res.StatusCode < 300:
				return fmt.Errorf("the WebDAV server doesn't let this account write in %s (HTTP 403): check the account may change files in that folder", dir)
			}
		}
	}
	return fmt.Errorf("the WebDAV server doesn't let this account use %s (HTTP 403): the folder may not be there, or the account may not have access to it", dir)
}

// there is whether PROPFIND finds u: a server that answers 403 for what
// isn't there may do so for PROPFIND too.
func (d *dav) there(ctx context.Context, u string) bool {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", u, nil)
	if err != nil {
		return true
	}
	req.Header.Set("Depth", "0")
	if d.user != "" || d.pass != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	res, err := d.client.Do(req)
	if err != nil {
		return true
	}
	res.Body.Close()
	return res.StatusCode >= 200 && res.StatusCode < 300
}

// get reads the backup; nil data and no error when there is none yet, and
// errNotModified when it is still have. A server that doesn't do
// conditional reads sends the file, as it always did.
//
// The read asks for the file as it is, not compressed: the ETag it gives
// back is what the write after it matches with If-Match, and a server that
// compresses changes that ETag for the compressed copy. Apache's
// mod_deflate — InfiniCLOUD's (TeraCloud's) WebDAV is Apache — adds
// "-gzip" inside the quotes, and nginx and other proxies weaken it to
// W/"…"; either never matches the file in an If-Match, so every write after
// such a read was refused as changed meanwhile (#1362).
func (d *dav) get(ctx context.Context, have version) (data []byte, v version, err error) {
	cond := have.conditions()
	h := map[string]string{"Accept-Encoding": "identity"}
	for k, v := range cond {
		h[k] = v
	}
	res, err := d.send(ctx, http.MethodGet, d.url(folder, file), nil, h)
	if err != nil {
		return nil, version{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified && cond != nil {
		return nil, have, errNotModified
	}
	// a Synology answers 403, not 404, for a file in a folder that isn't
	// there: before the first sync, magpie's folder isn't
	if res.StatusCode == http.StatusForbidden {
		if !d.there(ctx, d.url(folder)+"/") {
			return nil, version{}, nil
		}
		return nil, version{}, d.forbidden(ctx)
	}
	switch {
	// 409: the folder isn't there yet — how 坚果云 (Nutstore) answers a
	// read in it, where others say 404; put makes it
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone || res.StatusCode == http.StatusConflict:
		return nil, version{}, nil
	case res.StatusCode != http.StatusOK:
		return nil, version{}, fmt.Errorf("reading %s from the WebDAV server: HTTP %d", file, res.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, version{}, err
	}
	// asked for as it is, the file still came compressed: Go unpacks it
	// only when it asked for gzip itself
	if data, err = unpacked("WebDAV", data, res.Header.Get("Content-Encoding")); err != nil {
		return nil, version{}, fmt.Errorf("reading %s from the WebDAV server: %w", file, err)
	}
	// a redirect that ended at something other than the file — a web
	// app's sign-in or files page — is said with where it went: the
	// address is the thing to fix
	if res.Request != nil && res.Request.Response != nil && !backupHead(bytes.TrimSpace(data)) {
		if f := whatIs(data); !f.Replace {
			to := *res.Request.URL
			to.RawQuery, to.Fragment, to.User = "", "", nil
			f.Moved = to.Host + to.Path
			return nil, version{}, &notBackup{f: f, where: "the file " + folder + "/" + file + " on the WebDAV server", cmd: "webdav", err: backup.ErrNotBackup}
		}
	}
	return data, versionOf(res.Header), nil
}

// put writes the backup, only over the version read (etag) when there was
// one, making the folder if the server has none. Relays and tunnels cut
// long writes short, and the half that lands reads back as "not a magpie
// backup" on every sync after, so what a write kept is read back and
// compared; a short write is tried again and told as itself in the end.
func (d *dav) put(ctx context.Context, data []byte, etag string) (version, error) {
	h := map[string]string{"Content-Type": "application/octet-stream"}
	if etag != "" {
		h["If-Match"] = etag
	}
	for try, writes := 0, 0; ; try++ {
		res, err := d.send(ctx, http.MethodPut, d.url(folder, file), data, h)
		if err != nil {
			return version{}, err
		}
		res.Body.Close()
		switch {
		case res.StatusCode >= 200 && res.StatusCode < 300:
			writes++
			he, cerr := d.check(ctx, d.url(folder, file), file, len(data))
			if cerr != nil {
				if writes < 3 {
					// the half file is a new version: the retry matches
					// it — the ETag the PUT answered, else the one the
					// check read; with neither, the old one goes, for it
					// can't hold any more
					if e := res.Header.Get("ETag"); e != "" {
						h["If-Match"] = e
					} else if he != "" {
						h["If-Match"] = he
					} else {
						delete(h, "If-Match")
					}
					continue
				}
				return version{}, cerr
			}
			// an ETag said here is the version written; with none, the
			// next sync reads the file in full, as before
			return version{ETag: res.Header.Get("ETag")}, nil
		case res.StatusCode == http.StatusPreconditionFailed:
			return version{}, errChanged
		case res.StatusCode == http.StatusForbidden && (try > 0 || d.there(ctx, d.url(folder)+"/")):
			return version{}, d.forbidden(ctx)
		// a 403 for a folder not there (a Synology) is made as a 404 is
		case (res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict || res.StatusCode == http.StatusForbidden) && try == 0:
			if err := d.mkcol(ctx); err != nil {
				return version{}, err
			}
			continue
		case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict:
			return version{}, d.nowhere(ctx, res.StatusCode)
		}
		return version{}, fmt.Errorf("writing %s to the WebDAV server: HTTP %d", file, res.StatusCode)
	}
}

// check says so when the server kept less than what was sent: relays and
// tunnels cut long writes short, and the half that lands reads back as
// "not a magpie backup" on every sync after. The size is enough — a short
// file is the whole failure — so no download rides on a write. It gives
// back the ETag it saw, for a retry to match the file as the short write
// left it. A HEAD that can't be done — an error, or not a 200 — leaves the
// write unchecked: it landed either way. A HEAD that says short is asked
// again of PROPFIND, which costs a request only then.
func (d *dav) check(ctx context.Context, u, name string, want int) (etag string, err error) {
	res, err := d.send(ctx, http.MethodHead, u, nil, nil)
	if err != nil {
		return "", nil
	}
	res.Body.Close()
	// a server without a say on HEAD can't be checked; the read after it
	// tells
	if res.StatusCode != http.StatusOK {
		return "", nil
	}
	etag = res.Header.Get("ETag")
	n := res.ContentLength
	if n < 0 || int(n) >= want {
		return etag, nil
	}
	// a HEAD's Content-Length isn't always the file's: 坚果云 (Nutstore)
	// answered a 6869-byte usage file's with 0 on every try (#1259). The
	// length WebDAV keeps for a file is PROPFIND's getcontentlength, which
	// every client of it reads, so a short HEAD is asked of that before it
	// is believed; with no length there, the HEAD's stands
	if m, ok := d.length(ctx, u); ok {
		if m >= int64(want) {
			return etag, nil
		}
		n = m
	}
	return etag, fmt.Errorf("the WebDAV server kept %d of %d bytes of %s: the write was cut short — sync again, and if it keeps happening the network to the server is dropping long uploads", n, want, name)
}

// length is the file at u's size as PROPFIND (Depth 0) says it; false when
// it says none.
func (d *dav) length(ctx context.Context, u string) (int64, bool) {
	res, err := d.send(ctx, "PROPFIND", u, nil, map[string]string{"Depth": "0"})
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMultiStatus {
		return 0, false
	}
	var ms struct {
		Responses []struct {
			Length string `xml:"propstat>prop>getcontentlength"`
		} `xml:"response"`
	}
	if xml.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&ms) != nil {
		return 0, false
	}
	for _, r := range ms.Responses {
		if n, err := strconv.ParseInt(strings.TrimSpace(r.Length), 10, 64); err == nil && n >= 0 {
			return n, true
		}
	}
	return 0, false
}

func (d *dav) mkcol(ctx context.Context) error {
	res, err := d.do(ctx, "MKCOL", d.url(folder)+"/", nil, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	// 405: it is there already; 409: the folder above it isn't
	if res.StatusCode == http.StatusConflict {
		return fmt.Errorf("there is no folder %s on the WebDAV server (HTTP 409): make it there first, or leave it out of the address", d.base.Path)
	}
	if res.StatusCode >= 300 && res.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("making the folder %s on the WebDAV server: HTTP %d", folder, res.StatusCode)
	}
	return nil
}

// nowhere is a folder made, or said to be there, that a file still can't be
// put in: OpenList's and Alist's /dav/ is a list of their storages, where
// MKCOL answers 405 and PUT 404 — the address has to go into one of them.
func (d *dav) nowhere(ctx context.Context, code int) error {
	dir := d.base.Path
	if dir == "" {
		dir = "/"
	}
	subs, status := d.folders(ctx)
	if status == http.StatusNotFound || status == http.StatusConflict || status == http.StatusGone {
		return fmt.Errorf("there is no folder %s on the WebDAV server (HTTP %d): make it there first, or leave it out of the address", dir, code)
	}
	msg := fmt.Sprintf("the WebDAV server has nowhere to put the folder %s in %s (HTTP %d): the address has to be a folder files can be written in", folder, dir, code)
	if len(subs) > 0 {
		u := *d.base
		u.Path = strings.TrimRight(u.Path, "/") + "/" + subs[0]
		msg += fmt.Sprintf(" — on OpenList or Alist, one of its storages (%s), like %s", strings.Join(subs, ", "), u.String())
	}
	return errors.New(msg)
}

// folders are the folders in the address's folder, as PROPFIND lists them,
// and what it answered.
func (d *dav) folders(ctx context.Context) ([]string, int) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", d.url(), nil)
	if err != nil {
		return nil, 0
	}
	req.Header.Set("Depth", "1")
	if d.user != "" || d.pass != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	res, err := d.client.Do(req)
	if err != nil {
		return nil, 0
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMultiStatus {
		return nil, res.StatusCode
	}
	var ms struct {
		Responses []struct {
			Href       string    `xml:"href"`
			Collection *struct{} `xml:"propstat>prop>resourcetype>collection"`
		} `xml:"response"`
	}
	if xml.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&ms) != nil {
		return nil, res.StatusCode
	}
	self := strings.TrimRight(d.base.Path, "/")
	var out []string
	for _, r := range ms.Responses {
		h := r.Href
		if u, err := url.Parse(h); err == nil {
			h = u.Path
		}
		h = strings.TrimRight(h, "/")
		if r.Collection == nil || h == self || pathpkg.Dir(h) != self && !(self == "" && pathpkg.Dir(h) == "/") {
			continue
		}
		if len(out) < 5 {
			out = append(out, pathpkg.Base(h))
		}
	}
	return out, res.StatusCode
}
