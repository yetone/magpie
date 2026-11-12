package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func sourceFor(t *testing.T, src string) {
	t.Helper()
	before := settings.Load()
	s := before
	s.UpdateSource = src
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(before) })
}

// liveFeed makes the site answer: its feed names the release signed with
// w's key, and its file is on the site's own address, with the github.com
// address beside it. It counts what it is asked.
func liveFeed(t *testing.T, w *fallbackWorld, version string) (feed, file *atomic.Int32) {
	t.Helper()
	feed, file = new(atomic.Int32), new(atomic.Int32)
	var site *httptest.Server
	site = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/latest":
			feed.Add(1)
			h := sha256.Sum256(w.body)
			json.NewEncoder(rw).Encode(Release{Version: version, Signed: &Signed{Sums: w.sums, Sig: w.sig},
				Assets: map[string]Asset{w.asset: {URL: site.URL + "/dl/" + w.asset, Size: int64(len(w.body)), SHA256: hex.EncodeToString(h[:]),
					GitHub: fmt.Sprintf("%s/%s/releases/download/v%s/%s", w.srv.URL, releaseRepo, version, w.asset)}}})
		case "/dl/" + w.asset:
			file.Add(1)
			rw.Write(w.body)
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(site.Close)
	siteFeed = site.URL + "/api/latest"
	return
}

// GitHub only: the feed isn't asked, the release comes from GitHub and the
// file is downloaded from GitHub, though the site answers.
func TestSourceGitHubNeverAsksTheSite(t *testing.T) {
	w := newFallbackWorld(t, "0.1.4000")
	feed, file := liveFeed(t, w, "0.1.4000")
	sourceFor(t, settings.UpdateSourceGitHub)
	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if feed.Load() != 0 || w.apiHits.Load() == 0 {
		t.Fatalf("feed asked %d, GitHub API %d", feed.Load(), w.apiHits.Load())
	}
	staged, err := stageBinaryFor(context.Background(), rel.Assets[w.asset], filepath.Join(t.TempDir(), "magpie"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(staged); string(b) != string(w.body) || file.Load() != 0 || w.dlHits.Load() != 1 {
		t.Fatalf("file from the site %d, from GitHub %d", file.Load(), w.dlHits.Load())
	}
}

// GitHub only, with the file named by the site's feed (Stable reads its
// release from the site): the download still starts at github.com.
func TestSourceGitHubDownloadsAFeedFileFromGitHub(t *testing.T) {
	w := newFallbackWorld(t, "0.1.4000")
	_, file := liveFeed(t, w, "0.1.4000")
	rel, err := Latest(context.Background()) // automatic: the site's feed
	if err != nil {
		t.Fatal(err)
	}
	sourceFor(t, settings.UpdateSourceGitHub)
	if _, err := stageBinaryFor(context.Background(), rel.Assets[w.asset], filepath.Join(t.TempDir(), "magpie")); err != nil {
		t.Fatal(err)
	}
	if file.Load() != 0 || w.dlHits.Load() != 1 {
		t.Fatalf("site %d, GitHub %d", file.Load(), w.dlHits.Load())
	}
}

// Automatic (the default) and the site only both use the site while it answers.
func TestSourceSiteAndAutomaticUseTheSite(t *testing.T) {
	for _, src := range []string{"", settings.UpdateSourceSite} {
		t.Run("source "+src, func(t *testing.T) {
			w := newFallbackWorld(t, "0.1.4000")
			feed, file := liveFeed(t, w, "0.1.4000")
			sourceFor(t, src)
			rel, err := Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stageBinaryFor(context.Background(), rel.Assets[w.asset], filepath.Join(t.TempDir(), "magpie")); err != nil {
				t.Fatal(err)
			}
			if feed.Load() != 1 || file.Load() != 1 || w.apiHits.Load() != 0 || w.dlHits.Load() != 0 {
				t.Fatalf("feed %d, site file %d, GitHub API %d, GitHub file %d", feed.Load(), file.Load(), w.apiHits.Load(), w.dlHits.Load())
			}
		})
	}
}

// The site only never goes on to GitHub: not for the release when the
// feed is down, and not for the file when the site's copy fails.
func TestSourceSiteNeverFallsBackToGitHub(t *testing.T) {
	w := newFallbackWorld(t, "0.1.4000")
	sourceFor(t, settings.UpdateSourceSite)
	if _, err := Latest(context.Background()); err == nil {
		t.Fatal("the site's feed was down and a release came")
	}
	if w.apiHits.Load() != 0 {
		t.Fatal("GitHub was asked for the release")
	}
	site := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { http.Error(rw, "busy", 503) }))
	defer site.Close()
	h := sha256.Sum256(w.body)
	a := Asset{URL: site.URL + "/f", SHA256: hex.EncodeToString(h[:]), Size: int64(len(w.body)),
		GitHub: fmt.Sprintf("%s/%s/releases/download/v0.1.4000/%s", w.srv.URL, releaseRepo, w.asset)}
	if err := download(context.Background(), a, filepath.Join(t.TempDir(), "f")); err == nil {
		t.Fatal("the site's file failed and a download came")
	}
	if w.dlHits.Load() != 0 {
		t.Fatal("GitHub was asked for the file")
	}
	// automatic goes on to GitHub for it
	sourceFor(t, "")
	if err := download(context.Background(), a, filepath.Join(t.TempDir(), "f")); err != nil || w.dlHits.Load() != 1 {
		t.Fatalf("automatic: %v, GitHub file %d", err, w.dlHits.Load())
	}
}

// A release read from GitHub by choice is held to the signature as one
// read as a fallback is: every bad one is refused, with no file taken.
func TestSourceGitHubRefusesWhatIsNotSigned(t *testing.T) {
	for _, bad := range []string{"wrong key", "other sums", "no sig", "junk", "old unsigned"} {
		t.Run(bad, func(t *testing.T) {
			w := newFallbackWorld(t, "0.1.4000")
			liveFeed(t, w, "0.1.4000") // a good feed, which GitHub only doesn't use
			sourceFor(t, settings.UpdateSourceGitHub)
			switch bad {
			case "wrong key":
				_, priv, _ := ed25519.GenerateKey(nil)
				w.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("magpie release 0.1.4000\n"+w.sums)))
			case "other sums":
				h := sha256.Sum256([]byte("evil"))
				w.sums = hex.EncodeToString(h[:]) + "  " + w.asset + "\n"
			case "no sig":
				w.sig = ""
			case "junk":
				w.sig = "not base64!!"
			case "old unsigned":
				w.sig = ""
			}
			if rel, err := Latest(context.Background()); err == nil {
				t.Fatalf("accepted %+v", rel)
			}
			if w.dlHits.Load() != 0 {
				t.Fatal("a file was downloaded")
			}
		})
	}
}

// A feed of MAGPIE_UPDATE_FEED and the Stable channel are the site's
// whatever the source says; the file of a feed release that names no
// github.com address is downloaded from where it is.
func TestSourceGitHubKeepsACustomFeed(t *testing.T) {
	w := newFallbackWorld(t, "0.1.4000")
	feed, _ := liveFeed(t, w, "0.1.4000")
	t.Setenv("MAGPIE_UPDATE_FEED", siteFeed+"?mine")
	sourceFor(t, settings.UpdateSourceGitHub)
	if _, err := Latest(context.Background()); err != nil || feed.Load() != 1 || w.apiHits.Load() != 0 {
		t.Fatalf("err %v, feed %d, GitHub API %d", err, feed.Load(), w.apiHits.Load())
	}
}

func TestUpdateSourceSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := settings.Save(settings.Settings{UpdateSource: "cloudflare"}); err == nil {
		t.Fatal("an unknown source was saved")
	}
	for _, v := range []string{"", "site", "github"} {
		if err := settings.Save(settings.Settings{UpdateSource: v}); err != nil || settings.Load().UpdateSource != v {
			t.Fatalf("%q: %v", v, err)
		}
	}
	from := settings.Settings{Theme: "dark", UpdateSource: "site"}
	from.KeepOwn(settings.Settings{UpdateSource: "github"})
	if from.UpdateSource != "github" || from.Theme != "dark" {
		t.Fatalf("KeepOwn: %+v", from)
	}
}
