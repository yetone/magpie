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
// of the wait, and as this gateway stops, 304 says it is the same. A list
// is looked at again on each write magpie makes and on each change
// another process makes to its files (filememo.Written), not polled.
// A client asking without the header is answered as before, no ETag.

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
	end := time.NewTimer(listWaitOf(r))
	defer end.Stop()
	tag := ""
	for {
		// before the look, so a write made during it isn't missed
		written := filememo.Written()
		a := s.lookAtList(r)
		tag = listTag(a.status, a.body.Bytes())
		if a.status != http.StatusOK || tag != since {
			for k, v := range a.header {
				w.Header()[k] = v
			}
			if a.status == http.StatusOK {
				w.Header().Set("ETag", tag)
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
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotModified)
		return
	}
}
