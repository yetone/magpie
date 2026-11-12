package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

// Another magpie that has this one as its provider (remote-magpie) waits
// here for this magpie's model list to change, and fetches it again when
// it has (KaCli on Discord: a route or group changed on the server reached
// the computer using it only on its next 10-minute fetch, and its agents'
// configs after that). It is the list's own route, GET /v1/models, so
// every guard a list asked for passes (the key, the origin, the network
// share, a key's models) holds for the wait as well, with
// provider.ListWaitHeader saying how many seconds to wait at most and
// If-None-Match the list it has (the ETag of its last answer). The list
// is answered, with its ETag, once it is other than that one; at the end
// of the wait, and as this gateway stops, 304 says it is the same. The tag
// is compared weakly (tagNamed), for a proxy such as Cloudflare's that
// compresses the list turns its ETag weak (W/"…") and sends it back so:
// compared exactly, each wait was answered at once and asked again, once a
// second (y0ngb1n on X). A list
// is looked at again on each write magpie makes and on each change
// another process makes to its files (filememo.Written), not polled.
// A client asking without the header is answered the list with the same
// ETag, and 304 with no body when its If-None-Match names it already
// (listModelsTagged): a script or a magpie polling the list pays a few
// bytes while it is unchanged (clyzhicore on Discord). A wait answer says
// in provider.ListWaitHeader how long it held, which a plain answer
// doesn't: a proxy that drops the header would turn the wait into a plain
// list, and its 304 alone can't tell the two apart.

// listWaitMost is the longest a wait for the list is held: under the
// minute a proxy in front of a gateway (nginx's proxy_read_timeout) lets
// an answer take.
const listWaitMost = 50 * time.Second

// listSettle is how long a write is given for the ones that come with it
// (a group saved, then the catalog touched) before the list is looked at.
var listSettle = 200 * time.Millisecond

// waitsForList reports whether r is another magpie waiting for the list to
// change.
func waitsForList(r *http.Request) bool {
	if r.Method != http.MethodGet || r.Header.Get(provider.ListWaitHeader) == "" {
		return false
	}
	return r.URL.Path == "/v1/models" || r.URL.Path == "/models"
}

// listWaitOf is how long r asks to wait, at most listWaitMost.
func listWaitOf(r *http.Request) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(r.Header.Get(provider.ListWaitHeader)))
	if err != nil || n <= 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, listWaitMost)
}

// listTag is a list answer's ETag: of the bytes answered, so it moves
// exactly when what another magpie would fetch does.
func listTag(status int, body []byte) string {
	h := sha256.New()
	h.Write([]byte(strconv.Itoa(status)))
	h.Write(body)
	return `"ml-` + hex.EncodeToString(h.Sum(nil)[:12]) + `"`
}

// listAnswer is the list as r would be answered it now.
type listAnswer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (a *listAnswer) Header() http.Header { return a.header }
func (a *listAnswer) WriteHeader(code int) {
	if a.status == 0 {
		a.status = code
	}
}
func (a *listAnswer) Write(b []byte) (int, error) {
	a.WriteHeader(http.StatusOK)
	return a.body.Write(b)
}

// lookAtList answers r's list into a listAnswer, holding the catalog and
// files it reads for the one look, as holdCatalog does for a request.
func (s *Server) lookAtList(r *http.Request) *listAnswer {
	a := &listAnswer{header: http.Header{}}
	release, held := provider.Hold(), filememo.Hold()
	defer release()
	defer held()
	s.listModels(a, r)
	if a.status == 0 {
		a.status = http.StatusOK
	}
	return a
}

// modelsOnceChanged answers r's list once it is other than the one r's
// If-None-Match names, or 304 at the end of the wait.
func (s *Server) modelsOnceChanged(w http.ResponseWriter, r *http.Request) {
	since := strings.TrimSpace(r.Header.Get("If-None-Match"))
	wait := listWaitOf(r)
	held := strconv.Itoa(int(wait / time.Second))
	end := time.NewTimer(wait)
	defer end.Stop()
	tag := ""
	for {
		// before the look, so a write made during it isn't missed
		written := filememo.Written()
		a := s.lookAtList(r)
		tag = listTag(a.status, a.body.Bytes())
		if a.status != http.StatusOK || !tagNamed(since, tag) {
			for k, v := range a.header {
				w.Header()[k] = v
			}
			if a.status == http.StatusOK {
				w.Header().Set("ETag", tag)
				w.Header().Set(provider.ListWaitHeader, held)
			}
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(a.status)
			w.Write(a.body.Bytes())
			return
		}
		// another process's writes are seen at once while filememo.Watch
		// runs, and every few seconds where it can't
		others := time.NewTimer(filememo.OthersEvery())
		select {
		case <-r.Context().Done():
			others.Stop()
			return
		case <-s.streamsEnding():
		case <-end.C:
		case <-written:
			others.Stop()
			settle := time.NewTimer(listSettle)
			select {
			case <-r.Context().Done():
				settle.Stop()
				return
			case <-settle.C:
			}
			continue
		case <-others.C:
			continue
		}
		others.Stop()
		w.Header().Set("ETag", tag)
		w.Header().Set(provider.ListWaitHeader, held)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotModified)
		return
	}
}

// listModelsTagged answers r's list with its ETag, the one a wait for the
// list answers (listTag), and 304 with no body when r's If-None-Match
// names it already. The answer depends on who asks (a key's models, an
// agent's picks), so a cache must ask again each time (private,
// no-cache), and the tag is of the bytes this caller would be answered.
func (s *Server) listModelsTagged(w http.ResponseWriter, r *http.Request) {
	a := &listAnswer{header: w.Header()}
	s.listModels(a, r)
	if a.status == 0 {
		a.status = http.StatusOK
	}
	if a.status != http.StatusOK {
		w.WriteHeader(a.status)
		w.Write(a.body.Bytes())
		return
	}
	tag := listTag(a.status, a.body.Bytes())
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if tagNamed(r.Header.Get("If-None-Match"), tag) {
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(a.body.Bytes())
}

// tagNamed reports whether an If-None-Match value names tag: one of its
// comma-separated tags, weak or strong (RFC 9110's weak comparison), or *.
func tagNamed(ifNoneMatch, tag string) bool {
	want := strings.TrimPrefix(tag, "W/")
	for t := range strings.SplitSeq(ifNoneMatch, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || t != "" && strings.TrimPrefix(t, "W/") == want {
			return true
		}
	}
	return false
}
