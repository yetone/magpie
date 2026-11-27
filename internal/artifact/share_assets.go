package artifact

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// A shared copy's assets (docs/subsystems/artifacts.md, "Sharing"): a
// page shows an asset at BlobPath<asset id>, which names no version and
// no share, so usemagpie.ai keeps the artifact's assets beside the
// share and serves each by its id alone (site/shares.js, blobAPI). They
// go up before a new version does, so the version reaching viewers
// finds every asset it points at, and an asset uploaded or deleted while
// shared follows in the background.

// sharedBlob is one asset as usemagpie.ai lists a share's.
type sharedBlob struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Bytes int64  `json:"bytes"`
}

// syncAssets makes the shared copy's assets the artifact's: those it
// lacks go up, those the artifact deleted come down. One its budget has
// no room for, or another share holds, is left out and named in the
// error; the rest go all the same. Called with shareMu held.
func syncAssets(ctx context.Context, site, id string, s *Share) error {
	list, err := readAssets(id)
	if err != nil {
		return err
	}
	var got struct {
		Blobs []sharedBlob `json:"blobs"`
	}
	if err := gone(shareAsk(ctx, http.MethodGet, site+"/api/shares/"+s.ID+"/blobs", s, nil, "", &got)); err != nil {
		return err
	}
	there := map[string]bool{}
	for _, b := range got.Blobs {
		there[b.ID] = true
	}
	here := map[string]bool{}
	for _, a := range list {
		here[a.ID] = true
	}
	// down first: what the artifact deleted makes room
	for _, b := range got.Blobs {
		if here[b.ID] || !ValidAssetID(b.ID) {
			continue
		}
		if err := gone(shareAsk(ctx, http.MethodDelete, site+"/api/shares/"+s.ID+"/blobs/"+b.ID, s, nil, "", nil)); err != nil {
			return err
		}
	}
	var left []string
	for _, a := range list {
		if there[a.ID] {
			continue
		}
		body, err := os.ReadFile(assetPath(id, a.ID))
		if errors.Is(err, os.ErrNotExist) {
			// deleted since the index was read
			continue
		}
		if err != nil {
			return err
		}
		status, err := shareAsk(ctx, http.MethodPut, site+"/api/shares/"+s.ID+"/blobs/"+a.ID, s, body, a.ContentType, nil)
		switch {
		case err == nil:
		case status == http.StatusNotFound:
			return gone(status, err)
		case status == http.StatusRequestEntityTooLarge || status == http.StatusConflict || status == http.StatusUnsupportedMediaType:
			left = append(left, a.ID+" ("+strings.TrimPrefix(err.Error(), "usemagpie.ai: ")+")")
		default:
			return err
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("%d of its %d assets aren't on the shared copy, which shows them missing: %s", len(left), len(list), strings.Join(left, "; "))
	}
	return nil
}

// gone is shareAsk's error, errShareNotFound for a share taken down: a
// 404 that says so. A site without these calls (one not updated yet)
// answers a 404 of its own, which takes nothing down here.
func gone(status int, err error) error {
	if status == http.StatusNotFound && err != nil && strings.Contains(err.Error(), "no such shared page") {
		return errShareNotFound
	}
	return err
}

// assetSyncs counts the background syncs running; queued holds the
// artifacts one is queued for, so a burst of uploads syncs once or twice,
// not once an upload.
var (
	assetSyncs sync.WaitGroup
	queued     sync.Map
)

// assetsChanged brings a shared copy's assets up to the artifact's, in
// the background, after one was uploaded or deleted. One not shared, or
// with sharing off, has nothing to do.
func assetsChanged(id string) {
	if s, _ := Shared(id); s == nil || ShareSite() == "" {
		return
	}
	if _, ok := queued.LoadOrStore(id, true); ok {
		return
	}
	assetSyncs.Add(1)
	go func() {
		defer assetSyncs.Done()
		shareMu.Lock()
		defer shareMu.Unlock()
		// a change from here on queues another
		queued.Delete(id)
		s, err := Shared(id)
		site := ShareSite()
		if err != nil || s == nil || site == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := syncAssets(ctx, site, id, s); errors.Is(err, errShareNotFound) {
			sharedGone(id, err)
		} else if err != nil {
			log.Printf("artifact %s: its shared copy's assets: %v", id, err)
		}
	}()
}
