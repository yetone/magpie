package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/artifact"
)

// The artifact capability (claude.ai's `artifact`, once `self`): a page
// publishes a new version of itself, its whole page or the files that
// changed, and reads the titles of other artifacts it links to
// (docs/subsystems/artifacts.md, "Page runtime").

const (
	// artifactPublishBody is the most a publish may be: a whole version,
	// in base64, and the JSON around it
	artifactPublishBody = artifact.MaxVersion*4/3 + 1<<20
	// a page publishes at most artifactPublishMost times a minute
	artifactPublishMost = 30
	// metadata names at most this many artifacts a call
	artifactMetaMost = 200
)

// artifactPublishCall is a page's publish, as runtime.js sends it: html,
// or files by path, each null (delete), {"delete": true, "ifMatch": sha},
// or {"text" | "base64", "contentType", "ifMatch"}. Base is the version
// the view is.
type artifactPublishCall struct {
	Token string                     `json:"t"`
	Base  int                        `json:"base"`
	HTML  *string                    `json:"html"`
	Files map[string]json.RawMessage `json:"files"`
}

type artifactFileCall struct {
	Delete      bool    `json:"delete"`
	Text        *string `json:"text"`
	Base64      *string `json:"base64"`
	ContentType string  `json:"contentType"`
}

// minuteLimit spends at most most calls a minute, for each key.
type minuteLimit struct {
	sync.Mutex
	most int
	at   map[string][]time.Time
}

func newMinuteLimit(most int) *minuteLimit {
	return &minuteLimit{most: most, at: map[string][]time.Time{}}
}

// allow spends one of key's calls this minute, or says there is none
// left.
func (l *minuteLimit) allow(key string, now time.Time) bool {
	l.Lock()
	defer l.Unlock()
	keep := l.at[key][:0]
	for _, t := range l.at[key] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.most {
		l.at[key] = keep
		return false
	}
	l.at[key] = append(keep, now)
	return true
}

var artifactPublishes = newMinuteLimit(artifactPublishMost)

// artifactPublishError refuses a publish, with what the page reads besides
// its code: the live version on a conflict, the files that no longer held.
func artifactPublishError(w http.ResponseWriter, status int, code, msg string, more map[string]any) {
	e := map[string]any{"code": code, "message": msg}
	for k, v := range more {
		e[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": e})
}

// artifactPublish saves what the page sends as the artifact's newest
// version, as the user on this computer, and tells every open view
// (artifactEvents), which reloads to it.
func (s *Server) artifactPublish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	body, err := io.ReadAll(io.LimitReader(r.Body, artifactPublishBody+1))
	if err != nil {
		artifactAPIError(w, http.StatusBadRequest, "upstream_error", err.Error())
		return
	}
	if len(body) > artifactPublishBody {
		artifactAPIError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("a version is at most %d MiB", artifact.MaxVersion>>20))
		return
	}
	var c artifactPublishCall
	if err := json.Unmarshal(body, &c); err != nil {
		artifactAPIError(w, http.StatusBadRequest, "transform_error", "the call isn't JSON: "+err.Error())
		return
	}
	a, _, ok := artifactAPIOpen(w, r, c.Token, "artifact", "self")
	if !ok {
		return
	}
	latest := a.Latest()
	if pub, declared := latest.Capabilities.PagePublishes(); !declared {
		artifactAPIError(w, http.StatusForbidden, "not_declared", "the artifact's newest version doesn't declare artifact")
		return
	} else if !pub {
		artifactAPIError(w, http.StatusForbidden, "not_granted", "the artifact declares artifact with publish: false")
		return
	}
	p := artifact.Publish{ID: a.ID, Agent: artifact.PageAgent, Base: c.Base}
	switch {
	case (c.HTML == nil) == (c.Files == nil):
		artifactAPIError(w, http.StatusBadRequest, "invalid_content", "publish takes the page's html or its files")
		return
	case c.HTML != nil:
		if !artifact.Whole([]byte(*c.HTML)) {
			artifactAPIError(w, http.StatusBadRequest, "invalid_content", "the page must be a whole document, <!doctype html> first")
			return
		}
		p.Page = []byte(*c.HTML)
	default:
		guarded, err := artifactFiles(&p, c.Files)
		if err != nil {
			artifactAPIError(w, http.StatusBadRequest, "invalid_content", err.Error())
			return
		}
		if guarded == len(c.Files) && guarded > 0 {
			// every file is held to what the view read: another writer's
			// save to other files may come between
			p.Base = 0
		}
	}
	if p.Base <= 0 && p.IfMatch == nil {
		p.Base = latest.N
	}
	if !artifactPublishes.allow(a.ID, time.Now()) {
		artifactAPIError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("a page publishes at most %d times a minute: save several changes at once", artifactPublishMost))
		return
	}
	every := c.Files != nil && len(p.IfMatch) == len(c.Files) && len(c.Files) > 0
	art, err := artifact.PublishPage(p)
	if err != nil {
		var fc *artifact.FileConflict
		var fl *artifact.FileLimit
		switch {
		case errors.As(err, &fc):
			more := map[string]any{"live": strconv.Itoa(fc.Newest), "paths": fc.Paths}
			if every {
				more["changed"] = artifactChanged(a.ID, c.Base, fc.Newest)
			}
			artifactPublishError(w, http.StatusConflict, "conflict", err.Error(), more)
		case errors.Is(err, artifact.ErrConflict):
			now := latest.N
			if b, err := artifact.Get(a.ID); err == nil {
				now = b.Latest().N
			}
			artifactPublishError(w, http.StatusConflict, "conflict", err.Error(), map[string]any{"live": strconv.Itoa(now)})
		case errors.As(err, &fl):
			artifactPublishError(w, http.StatusBadRequest, "invalid_content", err.Error(), map[string]any{"limit": map[string]any{"kind": "files", "max": fl.Max}})
		case errors.Is(err, artifact.ErrTooLarge):
			artifactAPIError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
		case errors.Is(err, artifact.ErrInvalid):
			artifactAPIError(w, http.StatusBadRequest, "invalid_content", err.Error())
		case errors.Is(err, artifact.ErrNotFound):
			artifactAPIError(w, http.StatusNotFound, "revoked", "the artifact is gone")
		default:
			artifactAPIError(w, http.StatusInternalServerError, "upstream_error", err.Error())
		}
		return
	}
	v := art.Latest()
	out := map[string]any{"version": strconv.Itoa(v.N)}
	if p.IfMatch != nil {
		shas := map[string]string{}
		for name, b := range p.Files {
			if b == nil {
				continue
			}
			if sum, there, err := artifact.FileSHA(art.ID, v.N, name); err == nil && there {
				shas[name] = sum
			}
		}
		if p.Page != nil {
			if sum, there, err := artifact.FileSHA(art.ID, v.N, artifact.Page); err == nil && there {
				shas[artifact.Page] = sum
			}
		}
		out["shas"] = shas
	}
	if every {
		out["changed"] = artifactChanged(art.ID, c.Base, v.N-1)
	}
	// the user shared it: its link shows each new version
	if sh, err := artifact.Shared(art.ID); err == nil && sh != nil {
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if _, err := artifact.ShareLatest(ctx, id); err != nil {
				log.Printf("artifact %s: its shared link still shows version %d: %v", id, sh.Version, err)
			}
		}(art.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

// artifactFiles puts a files publish into p: each file's bytes, nil for
// one deleted, and IfMatch for each that names it. It says how many did.
func artifactFiles(p *artifact.Publish, files map[string]json.RawMessage) (int, error) {
	if len(files) == 0 {
		return 0, errors.New("name the files that changed")
	}
	p.Files = map[string][]byte{}
	guarded := 0
	for name, raw := range files {
		var held map[string]json.RawMessage
		var f artifactFileCall
		raw = bytes.TrimSpace(raw)
		gone := bytes.Equal(raw, []byte("null"))
		if !gone {
			if err := json.Unmarshal(raw, &held); err != nil {
				return 0, fmt.Errorf("%s: %v", name, err)
			}
			if err := json.Unmarshal(raw, &f); err != nil {
				return 0, fmt.Errorf("%s: %v", name, err)
			}
			gone = f.Delete
		}
		key := name
		if name == artifact.Page {
			if gone {
				return 0, fmt.Errorf("%s is the page: it can't be deleted", artifact.Page)
			}
		} else {
			c, err := artifact.CleanPath(name)
			if err != nil {
				return 0, err
			}
			key = c
		}
		if m, ok := held["ifMatch"]; ok {
			var want *string
			if err := json.Unmarshal(m, &want); err != nil {
				return 0, fmt.Errorf("%s: ifMatch is a sha256 or null", name)
			}
			if p.IfMatch == nil {
				p.IfMatch = map[string]*string{}
			}
			p.IfMatch[key] = want
			guarded++
		}
		var b []byte
		switch {
		case gone:
		case f.Text != nil && f.Base64 != nil:
			return 0, fmt.Errorf("%s: its content is text or bytes, not both", name)
		case f.Text != nil:
			if f.ContentType != "" && !artifactTextType(f.ContentType) {
				return 0, fmt.Errorf("%s: text goes as a text type, not %s; send bytes as a Blob", name, f.ContentType)
			}
			b = []byte(*f.Text)
		case f.Base64 != nil:
			d, err := base64.StdEncoding.DecodeString(*f.Base64)
			if err != nil {
				return 0, fmt.Errorf("%s: its bytes don't read: %v", name, err)
			}
			b = d
		default:
			return 0, fmt.Errorf("%s: give its content, or null to delete it", name)
		}
		if f.ContentType != "" {
			if t, params, err := mime.ParseMediaType(f.ContentType); err != nil || len(params) > 0 || t != strings.ToLower(f.ContentType) {
				return 0, fmt.Errorf("%s: %q isn't a bare media type", name, f.ContentType)
			}
		}
		if !gone && b == nil {
			b = []byte{}
		}
		if key == artifact.Page {
			if !artifact.Whole(b) {
				return 0, fmt.Errorf("%s must be a whole document, <!doctype html> first", artifact.Page)
			}
			p.Page = b
			continue
		}
		p.Files[key] = b
	}
	return guarded, nil
}

func artifactTextType(t string) bool {
	t, _, _ = strings.Cut(strings.ToLower(t), ";")
	return strings.HasPrefix(t, "text/") || slices.Contains([]string{"application/json", "application/javascript", "application/xml", "image/svg+xml", "application/manifest+json"}, t)
}

// artifactChanged is the files that differ between versions from and to
// of id, each with its sha256 in to (nil: gone); nil when it can't tell.
func artifactChanged(id string, from, to int) []map[string]any {
	if from <= 0 || to <= 0 {
		return nil
	}
	out := []map[string]any{}
	if from == to {
		return out
	}
	a, err := artifact.Get(id)
	if err != nil || a.Version(from) == nil || a.Version(to) == nil {
		return nil
	}
	names := append([]string{artifact.Page}, a.Version(from).Files...)
	names = append(names, a.Version(to).Files...)
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		was, wasThere, err1 := artifact.FileSHA(id, from, name)
		now, nowThere, err2 := artifact.FileSHA(id, to, name)
		if err1 != nil || err2 != nil {
			return nil
		}
		if wasThere == nowThere && was == now {
			continue
		}
		e := map[string]any{"path": name, "sha256": nil}
		if nowThere {
			e["sha256"] = now
		}
		out = append(out, e)
	}
	return out
}

// artifactMeta is the titles of the artifacts a page names, by id or
// link, for the link chips it draws: one entry per name, as given, and an
// empty one for what names none here.
func (s *Server) artifactMeta(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	body, err := io.ReadAll(io.LimitReader(r.Body, artifactAPIBody+1))
	if err != nil || len(body) > artifactAPIBody {
		artifactAPIError(w, http.StatusBadRequest, "invalid_argument", "a call is at most 1 MiB")
		return
	}
	var c struct {
		Token string   `json:"t"`
		Refs  []string `json:"refs"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		artifactAPIError(w, http.StatusBadRequest, "transform_error", "the call isn't JSON: "+err.Error())
		return
	}
	if _, _, ok := artifactAPIOpen(w, r, c.Token, "artifact", "self"); !ok {
		return
	}
	out := map[string]any{}
	ids := map[string]bool{}
	for _, ref := range c.Refs {
		e := map[string]any{"id": "", "title": "", "type": nil}
		out[ref] = e
		id := artifactLinked(ref)
		if id == "" {
			continue
		}
		if !ids[id] && len(ids) >= artifactMetaMost {
			continue
		}
		ids[id] = true
		if a, err := artifact.Get(id); err == nil {
			e["id"], e["title"] = a.ID, a.Title
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": out})
}

// artifactLinked is the artifact id a page names: the id itself, or a
// link whose path is an artifact's (/a/<id>/, or claude.ai's
// /artifact/<id>); only the path is read, never the host.
func artifactLinked(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		rest := ref[strings.Index(ref, "//")+2:]
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			return ""
		}
		ref, _, _ = strings.Cut(rest[i:], "?")
		ref, _, _ = strings.Cut(ref, "#")
		if p, ok := strings.CutPrefix(ref, "/artifact/"); ok {
			ref = "/a/" + p[strings.LastIndexByte(p, '-')+1:]
		}
		if !strings.HasPrefix(ref, ArtifactPagePath) {
			return ""
		}
	}
	id, _, ok := ArtifactRef(ref)
	if !ok {
		return ""
	}
	return id
}
