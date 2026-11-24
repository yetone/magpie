package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/artifact"
)

// A page's assets (docs/subsystems/artifacts.md, "Assets"): what
// claude.use("assets") uploads, lists and deletes, under <page>/_api/,
// and what a page shows at artifact.BlobPath<asset id>, the one address
// every version of every artifact reaches an asset at.

// artifactBlobPath is where an asset is served, by its id alone.
const artifactBlobPath = artifact.BlobPath

// artifactUploadMost is how many assets a page uploads in a minute.
const artifactUploadMost = 60

var artifactUploads = newMinuteLimit(artifactUploadMost)

// artifactAssetsCall is a list or a delete, as runtime.js sends it.
type artifactAssetsCall struct {
	Token string `json:"t"`
	Op    string `json:"op"`
	Ref   string `json:"ref"`
}

// artifactAssetError maps the store's refusals onto the codes a page
// reads.
func artifactAssetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, artifact.ErrTooLarge):
		artifactAPIError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, artifact.ErrAssetType):
		artifactAPIError(w, http.StatusUnsupportedMediaType, "unsupported_type", err.Error())
	case errors.Is(err, artifact.ErrAssetRequest):
		artifactAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, artifact.ErrAssetQuota):
		artifactAPIError(w, http.StatusConflict, "quota_or_state", err.Error())
	case errors.Is(err, artifact.ErrNotFound):
		artifactAPIError(w, http.StatusConflict, "quota_or_state", "the artifact is gone")
	default:
		// the disk, which a page may try once more
		artifactAPIError(w, http.StatusServiceUnavailable, "store_unavailable", err.Error())
	}
}

func artifactAssetJSON(a artifact.Asset) map[string]any {
	return map[string]any{"id": a.ID, "url": artifact.BlobPath + a.ID, "contentType": a.ContentType, "sizeBytes": a.Size, "createdAt": a.Created.Format(time.RFC3339)}
}

// artifactUpload stores the body as one of the page's assets. The page
// sends it as text/plain, which needs no preflight from its opaque
// origin; its type and token are in the query.
func (s *Server) artifactUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a, _, ok := artifactAPIOpen(w, r, q.Get("t"), "assets")
	if !ok {
		return
	}
	t := q.Get("type")
	body, err := io.ReadAll(io.LimitReader(r.Body, artifact.MaxAsset+1))
	if err != nil {
		artifactAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(body) > artifact.MaxAsset {
		artifactAPIError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("an asset is at most %d MiB", artifact.MaxAsset>>20))
		return
	}
	if !artifactUploads.allow(a.ID, time.Now()) {
		artifactAPIError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("a page uploads at most %d assets a minute", artifactUploadMost))
		return
	}
	got, err := artifact.UploadAsset(a.ID, body, t)
	if err != nil {
		artifactAssetError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(artifactAssetJSON(*got))
}

// artifactAssets lists the page's assets or deletes one.
func (s *Server) artifactAssets(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, artifactAPIBody+1))
	var c artifactAssetsCall
	if err == nil && len(body) <= artifactAPIBody {
		err = json.Unmarshal(body, &c)
	}
	if err != nil || len(body) > artifactAPIBody {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		artifactAPIError(w, http.StatusBadRequest, "transform_error", "the call isn't JSON")
		return
	}
	a, _, ok := artifactAPIOpen(w, r, c.Token, "assets")
	if !ok {
		return
	}
	switch c.Op {
	case "list":
		list, u, err := artifact.Assets(a.ID)
		if err != nil {
			artifactAssetError(w, err)
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, x := range list {
			out = append(out, artifactAssetJSON(x))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"assets": out, "usage": u})
	case "delete":
		// an id, or the url upload and list gave
		aid := strings.TrimPrefix(c.Ref, artifact.BlobPath)
		deleted, err := artifact.DeleteAsset(a.ID, aid)
		if err != nil {
			artifactAssetError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"deleted": deleted})
	default:
		artifactAPIError(w, http.StatusBadRequest, "invalid_request", "op is list or delete")
	}
}

// artifactBlob serves an asset to a browser on this computer, as the
// type it was uploaded as. Opened as a document, it is sandboxed (an
// opaque origin, no scripts), so not even an SVG runs at the gateway's
// own origin; a PDF is left to the browser's viewer.
func (s *Server) artifactBlob(w http.ResponseWriter, r *http.Request) {
	if !local(r) || !loopbackHost(r.Host) {
		http.Error(w, "an artifact's assets open in a browser on this computer, at "+URL(), http.StatusForbidden)
		return
	}
	p, a, err := artifact.BlobFile(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", a.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", `"`+a.ID+`"`)
	if a.ContentType != "application/pdf" {
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; font-src 'self'; media-src 'self'")
	}
	http.ServeContent(w, r, "", a.Created, f)
}
