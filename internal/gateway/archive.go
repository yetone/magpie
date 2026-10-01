package gateway

// The request archive: with it on (settings' RequestArchive), each call the
// gateway serves is kept — the headers and bodies both ways, every secret
// taken out first — in the user's own S3 bucket, the one sync keeps its
// backup in, at <prefix>/magpie/archive/<date>/<id>.json: the call's date,
// in UTC, and an id of its own, sent back on the response as
// X-Magpie-Archive-Id. The call, and its rows in the usage log, carry
// "<date>/<id>", for the Gateway and Usage pages to read it back by.
//
// The bodies are kept whole, not as Recent calls has them (the first 256
// KB), up to archiveLimit each (#447): the response is written to a
// temporary file as it goes to the agent, and the request to one once it
// is answered, so a call waiting for its upload holds no body in memory.
//
// The upload goes after the request, one at a time and never holding one
// up: a queue of a few calls, and a call that finds it full is dropped,
// with a line in the log, as is one the bucket refuses.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// Putter is a bucket the archive is written to.
type Putter interface {
	Put(ctx context.Context, name string, data []byte) error
}

// ArchiveBucket is the bucket the archive goes to, and false when there is
// none (sync is off, or to a WebDAV folder): set by main to the S3 sync's.
var ArchiveBucket func() (Putter, bool)

// ArchiveHeader carries a call's archive id on its response.
const ArchiveHeader = "X-Magpie-Archive-Id"

// archiveQueue is how many calls wait for their upload at most: their
// bodies wait in files, so only their headers are held, however fast calls
// come.
const archiveQueue = 16

// archiveLimit is how much of each body the archive keeps, in bytes:
// settings' RequestArchiveMaxMB, 32 MiB unless set, at most a GiB. A body
// is read whole into memory once, for its secrets to be taken out, while
// its call is uploaded, one call at a time.
func archiveLimit() int64 {
	mb := settings.Load().RequestArchiveMaxMB
	if mb <= 0 {
		mb = 32
	}
	return int64(min(mb, 1024)) << 20
}

// wire is what an archived call keeps besides what Call has: the request's
// method, path, headers and whole body, the response's, and where it goes.
type wire struct {
	name         string // <date>/<id>
	method, path string
	query        string // its secrets taken out on the way (a key= Gemini's way)
	req          http.Header
	body         []byte // the request's, until it is written to reqFull
	reqFull      *spool
	res          *captureResponseWriter
	to           Putter
}

// archiving is r's wire when the archive is on and has a bucket, nil
// otherwise: nothing is kept, and nothing sent. body is the request's, as
// Recent calls has it before the first 256 KB is cut from it.
func archiving(r *http.Request, res *captureResponseWriter, start time.Time, body []byte) *wire {
	if ArchiveBucket == nil || !settings.Load().RequestArchive {
		return nil
	}
	to, ok := ArchiveBucket()
	if !ok || to == nil {
		return nil
	}
	b := make([]byte, 8)
	rand.Read(b)
	id := start.UTC().Format("150405") + "-" + hex.EncodeToString(b)
	res.Header().Set(ArchiveHeader, id)
	res.full = &spool{limit: archiveLimit()}
	return &wire{name: start.UTC().Format("2006-01-02") + "/" + id, method: r.Method, path: r.URL.Path,
		query: r.URL.RawQuery, req: r.Header.Clone(), body: body, res: res, to: to}
}

// archiveName is where the archive keeps c, "<date>/<id>", or "" when it
// isn't kept: what its usage rows carry.
func (c *Call) archiveName() string {
	if c.wire != nil {
		return c.wire.name
	}
	return c.Archive
}

// discardArchive drops what a call's archive kept that never went to the
// queue — a request that ended without being recorded.
func discardArchive(res *captureResponseWriter) {
	if res.full != nil {
		res.full.discard()
	}
}

// ArchiveName is where the archive keeps the call named "<date>/<id>",
// under the bucket's magpie folder; false for a name not of that shape,
// which is never one magpie made.
func ArchiveName(date, id string) (string, bool) {
	if _, err := time.Parse("2006-01-02", date); err != nil || len(id) == 0 || len(id) > 64 {
		return "", false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c == '-') {
			return "", false
		}
	}
	return "archive/" + date + "/" + id + ".json", true
}

// Archived is a call as the archive keeps it.
type Archived struct {
	ID       string      `json:"id"`   // <date>/<id>
	Call     Call        `json:"call"` // as Recent calls has it, its bodies below
	Request  ArchivePart `json:"request"`
	Response ArchivePart `json:"response"`
}

// ArchivePart is one way of a call: the request, or the response.
type ArchivePart struct {
	Method  string              `json:"method,omitempty"`
	Path    string              `json:"path,omitempty"`
	Status  int                 `json:"status,omitempty"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
	// Size is the whole body's, in bytes, as the gateway had it, before
	// its secrets were taken out; none in an archive from before #447
	Size int64 `json:"size,omitempty"`
	// Truncated: only the start of the body is kept — its first
	// RequestArchiveMaxMB, or, in an archive from before #447 (no Size),
	// its first 256 KB
	Truncated bool `json:"truncated,omitempty"`
}

type archiveJob struct {
	c        Call
	w        *wire
	resHead  http.Header
	req, res *spool
}

var (
	archiveOnce sync.Once
	archiveJobs chan archiveJob
	// archivePending counts the uploads queued and under way, for a test
	// to wait for
	archivePending sync.WaitGroup
	archiveMu      sync.Mutex
	archiveErr     string // the last upload's failure, "" once one succeeds
	archiveErrAt   time.Time
)

// archive queues c's upload; c.wire is set.
func archive(c Call) {
	w := c.wire
	archiveOnce.Do(func() {
		archiveJobs = make(chan archiveJob, archiveQueue)
		go func() {
			for j := range archiveJobs {
				upload(j)
				archivePending.Done()
			}
		}()
	})
	// the response's file is the job's now, and the request's body goes
	// to one, so nothing whole is held while the call waits
	req := &spool{limit: archiveLimit()}
	req.add(w.body)
	w.body = nil
	res := w.res.full
	w.res.full = nil
	if res == nil {
		res = &spool{}
	}
	j := archiveJob{c: c, w: w, resHead: w.res.Header().Clone(), req: req, res: res}
	archivePending.Add(1)
	select {
	case archiveJobs <- j:
	default:
		archivePending.Done()
		req.discard()
		res.discard()
		log.Printf("request archive: %s dropped: %d uploads already waiting", w.name, archiveQueue)
	}
}

func upload(j archiveJob) {
	defer j.req.discard()
	defer j.res.discard()
	c := j.c
	c.wire = nil
	c.RequestBody, c.ResponseBody = "", ""
	reqBody, err1 := j.req.read()
	resBody, err2 := j.res.read()
	if err := errors.Join(err1, err2); err != nil {
		archiveFailed(j.w.name, err)
		return
	}
	c.Error, c.Fallback = redact.Scrub(c.Error), redact.Scrub(c.Fallback)
	path := j.w.path
	if j.w.query != "" {
		path += "?" + scrubQuery(j.w.query)
	}
	a := Archived{ID: j.w.name, Call: c,
		Request: ArchivePart{Method: j.w.method, Path: path, Headers: scrubHeaders(j.w.req),
			Body: string(redact.ScrubJSON(reqBody)), Size: j.req.size, Truncated: j.req.cut()},
		Response: ArchivePart{Status: c.Status, Headers: scrubHeaders(j.resHead),
			Body: string(redact.ScrubJSON(resBody)), Size: j.res.size, Truncated: j.res.cut()},
	}
	// as it reads in the bucket: a query's & and a body's <tags> as they are
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(a)
	if err == nil {
		// a minute, and a second more for each 256 KB a slow link takes
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute+time.Duration(buf.Len()>>18)*time.Second)
		err = j.w.to.Put(ctx, "archive/"+j.w.name+".json", buf.Bytes())
		cancel()
	}
	if err != nil {
		archiveFailed(j.w.name, err)
		return
	}
	archiveMu.Lock()
	archiveErr = ""
	archiveMu.Unlock()
}

func archiveFailed(name string, err error) {
	archiveMu.Lock()
	defer archiveMu.Unlock()
	archiveErr, archiveErrAt = err.Error(), time.Now()
	log.Printf("request archive: %s not uploaded: %v", name, err)
}

// ArchiveError is the last upload's failure, and when; "" once one after
// it went.
func ArchiveError() (string, time.Time) {
	archiveMu.Lock()
	defer archiveMu.Unlock()
	return archiveErr, archiveErrAt
}

func scrubHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, vs := range h {
		s := make([]string, len(vs))
		for i, v := range vs {
			s[i] = redact.ScrubHeader(k, v)
		}
		out[k] = s
	}
	return out
}

// scrubQuery is a query string with the values of secret-named fields
// (key=, access_token=) and any secret in the others taken out.
func scrubQuery(q string) string {
	vs, err := url.ParseQuery(q)
	if err != nil {
		return redact.Scrub(q)
	}
	for k, v := range vs {
		for i := range v {
			if redact.SecretName(k) {
				v[i] = redact.Scrubbed
			} else {
				v[i] = redact.Scrub(v[i])
			}
		}
	}
	return vs.Encode()
}
