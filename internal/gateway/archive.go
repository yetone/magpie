package gateway

// The request archive: with it on (settings' RequestArchive), each call the
// gateway serves is kept — the headers and bodies both ways, the bodies as
// Recent calls has them (the first 256 KB of each), every secret taken out
// first — in the user's own S3 bucket, the one sync keeps its backup in, at
// <prefix>/magpie/archive/<date>/<id>.json: the call's date, in UTC, and an
// id of its own, sent back on the response as X-Magpie-Archive-Id. The call
// carries "<date>/<id>", for the Gateway page to read it back by.
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

// archiveQueue is how many calls wait for their upload at most: with each
// body 256 KB at most, a few MB held, however fast calls come.
const archiveQueue = 16

// wire is what an archived call keeps besides what Call has: the request's
// method, path and headers, the response's, and where it goes.
type wire struct {
	name         string // <date>/<id>
	method, path string
	query        string // its secrets taken out on the way (a key= Gemini's way)
	req          http.Header
	res          *captureResponseWriter
	to           Putter
}

// archiving is r's wire when the archive is on and has a bucket, nil
// otherwise: nothing is kept, and nothing sent.
func archiving(r *http.Request, res *captureResponseWriter, start time.Time) *wire {
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
	return &wire{name: start.UTC().Format("2006-01-02") + "/" + id, method: r.Method, path: r.URL.Path,
		query: r.URL.RawQuery, req: r.Header.Clone(), res: res, to: to}
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
	Method    string              `json:"method,omitempty"`
	Path      string              `json:"path,omitempty"`
	Status    int                 `json:"status,omitempty"`
	Headers   map[string][]string `json:"headers"`
	Body      string              `json:"body"`
	Truncated bool                `json:"truncated,omitempty"` // only the first 256 KB is kept
}

type archiveJob struct {
	c       Call
	w       *wire
	resHead http.Header
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
	j := archiveJob{c: c, w: w, resHead: w.res.Header().Clone()}
	archivePending.Add(1)
	select {
	case archiveJobs <- j:
	default:
		archivePending.Done()
		log.Printf("request archive: %s dropped: %d uploads already waiting", w.name, archiveQueue)
	}
}

func upload(j archiveJob) {
	c := j.c
	c.wire = nil
	reqBody, resBody := c.RequestBody, c.ResponseBody
	c.RequestBody, c.ResponseBody = "", ""
	c.Error, c.Fallback = redact.Scrub(c.Error), redact.Scrub(c.Fallback)
	path := j.w.path
	if j.w.query != "" {
		path += "?" + scrubQuery(j.w.query)
	}
	a := Archived{ID: j.w.name, Call: c,
		Request: ArchivePart{Method: j.w.method, Path: path, Headers: scrubHeaders(j.w.req),
			Body: string(redact.ScrubJSON([]byte(reqBody))), Truncated: c.RequestTruncated},
		Response: ArchivePart{Status: c.Status, Headers: scrubHeaders(j.resHead),
			Body: string(redact.ScrubJSON([]byte(resBody))), Truncated: c.ResponseTruncated},
	}
	// as it reads in the bucket: a query's & and a body's <tags> as they are
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(a)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err = j.w.to.Put(ctx, "archive/"+j.w.name+".json", buf.Bytes())
		cancel()
	}
	archiveMu.Lock()
	defer archiveMu.Unlock()
	if err != nil {
		archiveErr, archiveErrAt = err.Error(), time.Now()
		log.Printf("request archive: %s not uploaded: %v", j.w.name, err)
		return
	}
	archiveErr = ""
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
