package davsync

// Shared usage (#542): with Config.Usage on, each computer puts its calls on
// the server beside the backup, at magpie/usage/<id>-<day>.magpie-usage, one
// file a day, sealed with the passphrase, and brings the other computers'
// down for the Usage page. A computer writes and deletes its own files
// alone, so none is ever written over by another; nothing of them is in the
// backup, nor in what is hashed to tell whether it changed. A magpie
// before them never looks in the folder.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/usage"
)

// usageFolder is the folder the days go in, in magpie's.
const (
	usageFolder = "usage"
	usageExt    = ".magpie-usage"
)

// usageEvery is how often a sync shares usage; a sync asked for (SyncNow)
// shares it whatever the time.
var usageEvery = 15 * time.Minute

// usageState is what was shared and brought in, kept with the sync's state.
type usageState struct {
	At time.Time `json:"at,omitzero"`
	// Sent are this computer's days put on the server, each hashed as sent
	Sent map[string]string `json:"sent,omitempty"`
	// Got are the other computers' files brought in, each at the version listed
	Got map[string]string `json:"got,omitempty"`
	// Whole is the window of days having been put once; after it, only
	// yesterday and today are made again
	Whole bool   `json:"whole,omitempty"`
	Error string `json:"error,omitempty"`
}

// files are a remote's usage files: listed with a version each, read,
// written and deleted by name.
type files interface {
	list(ctx context.Context) (map[string]string, error)
	read(ctx context.Context, name string) ([]byte, error)
	write(ctx context.Context, name string, data []byte) error
	remove(ctx context.Context, name string) error
}

// shareUsage puts this computer's days on the server and brings the others'
// in. Its failure is kept apart from the sync's: the setup synced all the same.
func shareUsage(ctx context.Context, c Config, st *state, force bool) {
	if !c.Usage { // turned off: the others' usage is shown no more
		st.Usage = nil
		usage.DropAllShared()
		return
	}
	u := st.Usage
	if u == nil {
		u = &usageState{}
		st.Usage = u
	}
	if !force && time.Since(u.At) < usageEvery {
		return
	}
	r, err := newRemote(c)
	if err != nil {
		return
	}
	fs, ok := r.(files)
	if !ok {
		return
	}
	u.Error = ""
	if err := shareWith(ctx, c, u, fs); err != nil {
		u.Error = err.Error()
		return
	}
	u.At = time.Now()
}

func shareWith(ctx context.Context, c Config, u *usageState, fs files) error {
	id, name := usage.Computer()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	first := today.AddDate(0, 0, 1-usage.SharedDays)
	from := first.Format(time.DateOnly)
	since := first
	if u.Whole {
		since = today.AddDate(0, 0, -1)
	}
	if u.Sent == nil {
		u.Sent = map[string]string{}
	}
	if u.Got == nil {
		u.Got = map[string]string{}
	}
	for day, calls := range usage.OwnDays(since) {
		if day < from {
			continue
		}
		plain, err := json.Marshal(usage.SharedDay{Computer: id, Name: name, Day: day, Calls: calls})
		if err != nil {
			return err
		}
		h := sum(plain)
		if u.Sent[day] == h {
			continue
		}
		var z bytes.Buffer
		w := gzip.NewWriter(&z)
		w.Write(plain)
		w.Close()
		sealed, err := backup.SealData(z.Bytes(), c.Passphrase)
		if err != nil {
			return err
		}
		if err := fs.write(ctx, id+"-"+day+usageExt, sealed); err != nil {
			return err
		}
		u.Sent[day] = h
	}
	u.Whole = true

	listed, err := fs.list(ctx)
	if err != nil {
		return err
	}
	there := map[string]bool{}
	for f, ver := range listed {
		who, day, ok := usageName(f)
		if !ok {
			continue
		}
		if who == id {
			if day < from { // the window's end: its owner takes it away
				fs.remove(ctx, f)
			}
			continue
		}
		if day < from {
			continue
		}
		there[who+"/"+day] = true
		if ver != "" && u.Got[f] == ver && kept(who, day) {
			continue
		}
		d, err := readDay(ctx, fs, f, c.Passphrase)
		if err != nil {
			return err
		}
		if d.Computer != who || d.Day != day { // named for another: left alone
			continue
		}
		if err := usage.KeepShared(d); err != nil {
			return err
		}
		u.Got[f] = ver
	}
	for day := range u.Sent {
		if day < from {
			delete(u.Sent, day)
		}
	}
	// what the server no longer has goes here too
	for _, k := range usage.SharedKept() {
		if !there[k] {
			who, day, _ := strings.Cut(k, "/")
			usage.DropShared(who, day)
		}
	}
	for f := range u.Got {
		if who, day, ok := usageName(f); !ok || !there[who+"/"+day] {
			delete(u.Got, f)
		}
	}
	return nil
}

func kept(who, day string) bool {
	for _, k := range usage.SharedKept() {
		if k == who+"/"+day {
			return true
		}
	}
	return false
}

// usageName is a usage file's computer and day.
func usageName(f string) (who, day string, ok bool) {
	base, ok := strings.CutSuffix(f, usageExt)
	if !ok || len(base) < 12 {
		return "", "", false
	}
	who, day = base[:len(base)-11], base[len(base)-10:]
	if base[len(base)-11] != '-' || !usage.ValidComputer(who) {
		return "", "", false
	}
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return "", "", false
	}
	return who, day, true
}

func readDay(ctx context.Context, fs files, f, pass string) (usage.SharedDay, error) {
	var d usage.SharedDay
	sealed, err := fs.read(ctx, f)
	if err != nil {
		return d, err
	}
	z, err := backup.OpenData(sealed, pass)
	if errors.Is(err, backup.ErrPassphrase) {
		return d, fmt.Errorf("the passphrase doesn't open %s: another computer shares its usage sealed with another one", f)
	}
	if err != nil {
		return d, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(z))
	if err != nil {
		return d, err
	}
	plain, err := io.ReadAll(io.LimitReader(zr, 256<<20))
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(plain, &d)
}

// WebDAV

func (d *dav) usageURL(name string) string { return d.url(folder, usageFolder, name) }

func (d *dav) list(ctx context.Context) (map[string]string, error) {
	res, err := d.do(ctx, "PROPFIND", d.usageURL(""), nil, map[string]string{"Depth": "1"})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict || res.StatusCode == http.StatusGone {
		return map[string]string{}, nil
	}
	if res.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("listing %s/%s on the WebDAV server: HTTP %d", folder, usageFolder, res.StatusCode)
	}
	var ms struct {
		Responses []struct {
			Href      string    `xml:"href"`
			ETag      string    `xml:"propstat>prop>getetag"`
			Modified  string    `xml:"propstat>prop>getlastmodified"`
			Length    string    `xml:"propstat>prop>getcontentlength"`
			Collected *struct{} `xml:"propstat>prop>resourcetype>collection"`
		} `xml:"response"`
	}
	if err := xml.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("listing %s/%s on the WebDAV server: %w", folder, usageFolder, err)
	}
	out := map[string]string{}
	for _, r := range ms.Responses {
		h := r.Href
		if u, err := url.Parse(h); err == nil {
			h = u.Path
		}
		name := pathpkg.Base(strings.TrimRight(h, "/"))
		if r.Collected != nil || !strings.HasSuffix(name, usageExt) {
			continue
		}
		v := r.ETag
		if v == "" { // a server without ETags: its time and size tell
			v = r.Modified + "/" + r.Length
		}
		out[name] = v
	}
	return out, nil
}

func (d *dav) read(ctx context.Context, name string) ([]byte, error) {
	res, err := d.do(ctx, http.MethodGet, d.usageURL(name), nil, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reading %s from the WebDAV server: HTTP %d", name, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<20))
}

func (d *dav) write(ctx context.Context, name string, data []byte) error {
	for try := 0; ; try++ {
		res, err := d.send(ctx, http.MethodPut, d.usageURL(name), data, map[string]string{"Content-Type": "application/octet-stream"})
		if err != nil {
			return err
		}
		res.Body.Close()
		switch {
		case res.StatusCode >= 200 && res.StatusCode < 300:
			return nil
		case try == 0 && (res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict || res.StatusCode == http.StatusForbidden):
			// the folder isn't there yet: magpie's, then usage in it
			if err := d.mkcol(ctx); err != nil {
				return err
			}
			mk, err := d.do(ctx, "MKCOL", d.url(folder, usageFolder)+"/", nil, nil)
			if err != nil {
				return err
			}
			mk.Body.Close()
			continue
		}
		return fmt.Errorf("writing %s to the WebDAV server: HTTP %d", name, res.StatusCode)
	}
}

func (d *dav) remove(ctx context.Context, name string) error {
	res, err := d.do(ctx, http.MethodDelete, d.usageURL(name), nil, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

// S3

func (s *s3) usageKey(name string) string {
	return strings.TrimSuffix(s.key, file) + usageFolder + "/" + name
}

func (s *s3) list(ctx context.Context) (map[string]string, error) {
	prefix := s.usageKey("")
	out := map[string]string{}
	token := ""
	for range 100 {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := s.bucketURL()
		u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
		res, err := s.sendTo(ctx, http.MethodGet, u, nil, nil)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != http.StatusOK {
			e := readError(res)
			res.Body.Close()
			return nil, s.explain("read", e)
		}
		var l struct {
			Contents []struct {
				Key  string `xml:"Key"`
				ETag string `xml:"ETag"`
			} `xml:"Contents"`
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&l)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, c := range l.Contents {
			if name, ok := strings.CutPrefix(c.Key, prefix); ok && !strings.Contains(name, "/") {
				out[name] = c.ETag
			}
		}
		if !l.Truncated || l.Next == "" {
			break
		}
		token = l.Next
	}
	return out, nil
}

func (s *s3) read(ctx context.Context, name string) ([]byte, error) {
	res, err := s.sendTo(ctx, http.MethodGet, s.urlOf(s.usageKey(name)), nil, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, s.explain("read", readError(res))
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<20))
}

func (s *s3) write(ctx context.Context, name string, data []byte) error {
	res, err := s.sendTo(ctx, http.MethodPut, s.urlOf(s.usageKey(name)), data, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	return s.explain("write", readError(res))
}

func (s *s3) remove(ctx context.Context, name string) error {
	res, err := s.sendTo(ctx, http.MethodDelete, s.urlOf(s.usageKey(name)), nil, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}
