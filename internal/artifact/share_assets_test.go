package artifact

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// the assets the site holds for share id, as id → type
func (f *fakeSite) assetsOf(id string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for aid, x := range f.shares[id].assets {
		out[aid] = x[0]
	}
	return out
}

func (f *fakeSite) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.said)
}

// A shared copy shows the page's assets (#7649): they go with the share,
// follow an upload or a delete while it is shared, and come down with it.
func TestSharedCopyCarriesAssets(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><title>Board</title><p>one")})
	if err != nil {
		t.Fatal(err)
	}
	one, err := UploadAsset(a.ID, png, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	two, err := UploadAsset(a.ID, []byte("a,b\n1,2\n"), "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	assetSyncs.Wait()
	if _, asks := site.count(); asks != 0 {
		t.Fatalf("an unshared artifact's uploads asked the site %d times", asks)
	}
	sh, err := ShareLatest(context.Background(), a.ID)
	if err != nil || sh.Note != "" {
		t.Fatalf("shared: %+v %v", sh, err)
	}
	id, got := site.only(t)
	if held := site.assetsOf(id); len(held) != 2 || held[one.ID] != "image/png" || held[two.ID] != "text/csv" || got.assets[one.ID][1] != string(png) {
		t.Fatalf("the shared copy holds %v", held)
	}
	// uploaded while shared: it follows
	three, err := UploadAsset(a.ID, []byte("hello"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	assetSyncs.Wait()
	if held := site.assetsOf(id); len(held) != 3 || held[three.ID] != "text/plain" {
		t.Fatalf("after an upload the shared copy holds %v", held)
	}
	// deleted: it comes down there too
	if ok, err := DeleteAsset(a.ID, one.ID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	assetSyncs.Wait()
	if held := site.assetsOf(id); len(held) != 2 || held[one.ID] != "" {
		t.Fatalf("after a delete the shared copy holds %v", held)
	}
	if err := Unshare(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	if len(site.blobs) != 0 {
		t.Fatalf("taken down, the site still holds %v", site.blobs)
	}
}

// A new version's assets are on the shared copy before the version is:
// its viewers reload to a page whose every asset is there.
func TestSharedAssetsGoBeforeTheVersion(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><p>one")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ShareLatest(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	// the upload's own sync held back, as if it hadn't run yet
	queued.Store(a.ID, true)
	up, err := UploadAsset(a.ID, png, "image/png")
	queued.Delete(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishPage(Publish{ID: a.ID, Page: []byte(`<!doctype html><img src="/_blob/` + up.ID + `">`)}); err != nil {
		t.Fatal(err)
	}
	before := len(site.asked())
	if _, err := ShareLatest(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	said := site.asked()[before:]
	put, ver := slices.Index(said, "PUT blobs/"+up.ID), slices.Index(said, "PUT ")
	if put < 0 || ver < 0 || put > ver {
		t.Fatalf("asked %q", said)
	}
}

// What the shared copy can't hold (its budget, or an id another share
// holds) is left out and said; the rest go, and the version too.
func TestSharedAssetsLeftOutAreSaid(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><p>one")})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, body := range []string{"a", "b", "c"} {
		up, err := UploadAsset(a.ID, []byte(body), "text/plain")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, up.ID)
	}
	// another share holds the last one's id already
	site.mu.Lock()
	site.blobs[ids[2]] = "zzzzzzzzzzzzzzzz"
	site.mu.Unlock()
	site.maxBlobs = 1
	sh, err := ShareLatest(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sh.Note, "2 of its 3 assets aren't on the shared copy") || !strings.Contains(sh.Note, "another shared page holds") || !strings.Contains(sh.Note, "at most 1 files") {
		t.Fatalf("note %q", sh.Note)
	}
	id, got := site.only(t)
	if held := site.assetsOf(id); len(held) != 1 || held[ids[0]] == "" || got.version != 1 {
		t.Fatalf("the shared copy holds %v at version %d", held, got.version)
	}
	if s, _ := Shared(a.ID); s == nil || s.Note != "" {
		t.Fatalf("share.json kept %+v", s)
	}
}

// Taken down on usemagpie.ai, an upload's sync finds it gone: it isn't
// shared here any more, and nothing is shared again.
func TestSharedAssetsSyncFindsItTakenDown(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><p>one")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ShareLatest(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	id, _ := site.only(t)
	site.drop(id)
	if _, err := UploadAsset(a.ID, png, "image/png"); err != nil {
		t.Fatal(err)
	}
	assetSyncs.Wait()
	if s, err := Shared(a.ID); s != nil || err != nil {
		t.Fatalf("still shared: %+v %v", s, err)
	}
	if n, _ := site.count(); n != 0 {
		t.Fatal("shared again")
	}
}

// A site without the asset calls (not updated yet) answers them 404: the
// share stays, and its version goes all the same.
func TestSharedAssetsOnASiteWithoutThem(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><p>one")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UploadAsset(a.ID, png, "image/png"); err != nil {
		t.Fatal(err)
	}
	site.mu.Lock()
	site.noBlobs = true
	site.mu.Unlock()
	sh, err := ShareLatest(context.Background(), a.ID)
	if err != nil || !strings.Contains(sh.Note, "no such call") {
		t.Fatalf("shared: %+v %v", sh, err)
	}
	if _, err := PublishPage(Publish{ID: a.ID, Page: []byte("<!doctype html><p>two")}); err != nil {
		t.Fatal(err)
	}
	if _, err := UploadAsset(a.ID, png, "image/png"); err != nil {
		t.Fatal(err)
	}
	assetSyncs.Wait()
	sh, err = ShareLatest(context.Background(), a.ID)
	if err != nil || sh.Version != 2 {
		t.Fatalf("version 2: %+v %v", sh, err)
	}
	if _, got := site.only(t); got.version != 2 {
		t.Fatalf("the site shows version %d", got.version)
	}
}
