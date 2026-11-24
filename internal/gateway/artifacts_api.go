package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/artifact"
)

// The page runtime's API (docs/subsystems/artifacts.md, "Page runtime"):
// what an artifact's page calls through window.claude.use, under its own
// folder, <page>/_api/. The page's origin is opaque, so its calls come
// with Origin: null; corsGuard lets the store's POST through
// (artifactAPICall), and each call carries the page's token, which only
// the page magpie served at a navigation holds (artifactRuntimeFor): not
// another artifact's page, nor one fetching this page's HTML.

const (
	artifactAPIDir = "_api/"
	// artifactAPIBody is the most a store call may be: a 256 KiB document
	// and the JSON around it
	artifactAPIBody = 1 << 20
	// how often the events stream looks for another process's writes, and
	// says it is there
	artifactPollEvery = 2 * time.Second
	artifactBeat      = 25 * time.Second
)

// artifactAPICall: r is a page's call into its store, which corsGuard lets
// through from Origin: null, as artifactPageRead lets its reads.
func artifactAPICall(r *http.Request) bool {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, ArtifactPagePath) {
		return false
	}
	ref, rest, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, ArtifactPagePath), "/")
	return ok && ref != "" && rest == artifactAPIDir+"db"
}

// artifactRuntimeFor: the page runtime, with the token, goes into a page
// the browser navigates to (a tab, a frame), never into one a script
// fetches: another page on this computer reads the HTML of any artifact
// it fetches (its reply is Access-Control-Allow-Origin: *), and would
// read the token in it.
func artifactRuntimeFor(r *http.Request) bool {
	mode := r.Header.Get("Sec-Fetch-Mode")
	return mode == "" || mode == "navigate"
}

// artifactAPI answers what a page asks, or says why not.
func artifactAPIError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// artifactAPIOpen is the page's artifact and version, when r may reach
// its store: from this computer, at its own name, with the page's token,
// from a version that declares db.
func artifactAPIOpen(w http.ResponseWriter, r *http.Request, token string) (string, int, bool) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if !local(r) || !loopbackHost(r.Host) {
		artifactAPIError(w, http.StatusForbidden, "revoked", "an artifact's data is reached from a browser on this computer, at "+URL())
		return "", 0, false
	}
	id, n, ok := ArtifactRef(r.PathValue("ref"))
	if !ok || strings.Contains(r.PathValue("ref"), "/") {
		artifactAPIError(w, http.StatusNotFound, "not_granted", "no such artifact here")
		return "", 0, false
	}
	if !artifact.PageTokenOK(id, token) {
		artifactAPIError(w, http.StatusForbidden, "not_granted", "this isn't the artifact's own page")
		return "", 0, false
	}
	a, err := artifact.Get(id)
	if err != nil {
		artifactAPIError(w, http.StatusNotFound, "revoked", "the artifact is gone")
		return "", 0, false
	}
	v := a.Version(n)
	if v == nil {
		artifactAPIError(w, http.StatusNotFound, "revoked", "no such version")
		return "", 0, false
	}
	if !v.Capabilities.Has("db") {
		artifactAPIError(w, http.StatusForbidden, "not_granted", "this version of the page doesn't declare db")
		return "", 0, false
	}
	return id, n, true
}

// artifactCall is one store call, as runtime.js sends it.
type artifactCall struct {
	Token      string            `json:"t"`
	Op         string            `json:"op"`
	Path       string            `json:"path"`
	Data       map[string]any    `json:"data"`
	Collection string            `json:"collection"`
	Where      []artifact.Filter `json:"where"`
	OrderBy    string            `json:"orderBy"`
	Desc       bool              `json:"desc"`
	Limit      int               `json:"limit"`
	Holder     string            `json:"holder"`
	TTLMs      int64             `json:"ttlMs"`
}

func (s *Server) artifactDB(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, artifactAPIBody+1))
	if err != nil {
		artifactAPIError(w, http.StatusBadRequest, "unavailable", err.Error())
		return
	}
	var c artifactCall
	if len(body) > artifactAPIBody {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		artifactAPIError(w, http.StatusRequestEntityTooLarge, "invalid_argument", "a call is at most 1 MiB")
		return
	}
	if err := json.Unmarshal(body, &c); err != nil {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		artifactAPIError(w, http.StatusBadRequest, "transform_error", "the call isn't JSON: "+err.Error())
		return
	}
	id, n, ok := artifactAPIOpen(w, r, c.Token)
	if !ok {
		return
	}
	who, err := artifact.LocalViewer()
	if err != nil {
		artifactAPIError(w, http.StatusInternalServerError, "unavailable", err.Error())
		return
	}
	db, err := artifact.OpenDB(id, n, who)
	if err != nil {
		artifactAPIError(w, http.StatusNotFound, "revoked", err.Error())
		return
	}
	var out any = map[string]any{}
	switch c.Op {
	case "get":
		var d *artifact.Doc
		if d, err = db.Get(c.Path); err == nil {
			out = map[string]any{"doc": d}
		}
	case "query":
		var docs []*artifact.Doc
		if docs, err = db.Run(artifact.Query{Collection: c.Collection, Where: c.Where, OrderBy: c.OrderBy, Desc: c.Desc, Limit: c.Limit}); err == nil {
			out = map[string]any{"docs": docs}
		}
	case "set":
		err = db.Set(c.Path, c.Data)
	case "update":
		err = db.Update(c.Path, c.Data)
	case "delete":
		err = db.Delete(c.Path)
	case "acquire":
		var l *artifact.Lease
		if l, err = db.Acquire(c.Path, c.Holder, time.Duration(c.TTLMs)*time.Millisecond, c.Data); err == nil {
			out = map[string]any{"lease": l}
		}
	default:
		err = &artifact.DBError{Code: "capability_removed", Message: fmt.Sprintf("%q isn't a store call", c.Op)}
	}
	if err != nil {
		var de *artifact.DBError
		if errors.As(err, &de) {
			artifactAPIError(w, http.StatusBadRequest, de.Code, de.Message)
			return
		}
		if errors.Is(err, artifact.ErrNotFound) {
			artifactAPIError(w, http.StatusNotFound, "revoked", "the artifact is gone")
			return
		}
		artifactAPIError(w, http.StatusInternalServerError, "unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// artifactEvents streams the paths written in the page's store, for its
// listeners to read again: this process's writes as they are made,
// another's (the CLI's, another magpie's) within artifactPollEvery. It
// ends when the page goes, the artifact does, or the gateway stops
// serving (streamsEnding).
func (s *Server) artifactEvents(w http.ResponseWriter, r *http.Request) {
	id, _, ok := artifactAPIOpen(w, r, r.URL.Query().Get("t"))
	if !ok {
		return
	}
	changes, stop := artifact.Watch(id)
	defer stop()
	rc := http.NewResponseController(w)
	defer rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(line string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(w, line); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("retry: 2000\n: magpie\n\n") {
		return
	}
	poll := time.NewTicker(artifactPollEvery)
	defer poll.Stop()
	beat := time.NewTicker(artifactBeat)
	defer beat.Stop()
	seen := artifact.DataVersion(id)
	for {
		var line string
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsEnding():
			return
		case c := <-changes:
			b, _ := json.Marshal(map[string]any{"paths": c.Paths})
			if c.Paths == nil {
				b = []byte(`{}`)
			}
			line = "data: " + string(b) + "\n\n"
		case <-poll.C:
			if _, err := artifact.Get(id); errors.Is(err, artifact.ErrNotFound) {
				return
			}
			// PRAGMA data_version moves for another connection's writes:
			// another process's, as this one's have been told already
			if v := artifact.DataVersion(id); v != seen {
				seen = v
				line = "data: {}\n\n"
			}
		case <-beat.C:
			line = ": beat\n\n"
		}
		if line != "" && !send(line) {
			return
		}
	}
}
