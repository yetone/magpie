package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fallbackWorld is a fake GitHub with a signed release and a feed host
// that refuses every connection, the way a company allow list that lets
// only github.com through does (ssharpcc on X).
type fallbackWorld struct {
	srv       *httptest.Server
	body      []byte
	asset     string
	limited   bool // the API answers 403, as when anonymous asks run out
	sums, sig string
	feedHits  atomic.Int32
	apiHits   atomic.Int32
	dlHits    atomic.Int32
}

func newFallbackWorld(t *testing.T, version string) *fallbackWorld {
	t.Helper()
	w := &fallbackWorld{body: []byte("the new magpie"), asset: BinaryAsset()}
	h := sha256.Sum256(w.body)
	w.sums = hex.EncodeToString(h[:]) + "  " + w.asset + "\n" + strings.Repeat("ab", 32) + "  other-file\n"
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := signingKey
	signingKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { signingKey = old })
	w.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("magpie release "+version+"\n"+w.sums)))

	tag := "v" + version
	dl := "/" + releaseRepo + "/releases/download/" + tag + "/"
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + releaseRepo + "/releases/latest":
			w.apiHits.Add(1)
			if w.limited {
				http.Error(rw, "rate limit exceeded", 403)
				return
			}
			fmt.Fprintf(rw, `{"tag_name":%q,"body":"notes here","html_url":"https://github.com/%s/releases/tag/%s","assets":[{"name":%q,"size":%d,"url":%q}]}`,
				tag, releaseRepo, tag, w.asset, len(w.body), w.srv.URL+"/repos/"+releaseRepo+"/releases/assets/1")
		case "/" + releaseRepo + "/releases/latest":
			http.Redirect(rw, r, "/"+releaseRepo+"/releases/tag/"+tag, http.StatusFound)
		case dl + "SHA256SUMS":
			rw.Write([]byte(w.sums))
		case dl + "SHA256SUMS.sig":
			rw.Write([]byte(w.sig + "\n"))
		case dl + w.asset:
			w.dlHits.Add(1)
			rw.Write(w.body)
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(w.srv.Close)

	oldWeb, oldAPI, oldFeed := githubWeb, githubAPI, siteFeed
	githubWeb, githubAPI = w.srv.URL, w.srv.URL
	// the site's feed: a port nobody listens on
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	siteFeed = "http://" + l.Addr().String() + "/api/latest"
	l.Close()
	t.Cleanup(func() { githubWeb, githubAPI, siteFeed = oldWeb, oldAPI, oldFeed })
	t.Setenv("MAGPIE_UPDATE_FEED", "")
	stableFor(t, "")
	quickRetries(t)
	return w
}

// The feed host refuses connections: the release is read from GitHub,
// checked against the embedded key, and its file downloaded from GitHub
// and installed by hash.
func TestUpdateWorksFromGitHubWhenTheFeedIsUnreachable(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(fmt.Sprintf("api limited %v", limited), func(t *testing.T) {
			w := newFallbackWorld(t, "0.1.4000")
			w.limited = limited
			rel, err := Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if rel.Version != "0.1.4000" || rel.Signed == nil {
				t.Fatalf("release %+v", rel)
			}
			if !limited && rel.Notes != "notes here" {
				t.Fatalf("notes %q", rel.Notes)
			}
			if _, ok := rel.Assets["other-file"]; !ok {
				t.Fatal("the signed list's files are all offered")
			}
			exe := filepath.Join(t.TempDir(), "magpie")
			staged, err := stageBinaryFor(context.Background(), rel.Assets[w.asset], exe)
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(staged); string(b) != string(w.body) {
				t.Fatalf("staged %q", b)
			}
			if w.dlHits.Load() != 1 {
				t.Fatalf("file asked %d times", w.dlHits.Load())
			}
		})
	}
}

// A release GitHub gives with a signature that isn't the key's is refused,
// on the API path and on the redirect path alike.
func TestGitHubFallbackRefusesABadSignature(t *testing.T) {
	for _, limited := range []bool{false, true} {
		for _, bad := range []string{"wrong key", "other sums", "no sig", "junk"} {
			t.Run(fmt.Sprintf("limited %v %s", limited, bad), func(t *testing.T) {
				w := newFallbackWorld(t, "0.1.4000")
				w.limited = limited
				switch bad {
				case "wrong key":
					_, priv, _ := ed25519.GenerateKey(nil)
					w.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("magpie release 0.1.4000\n"+w.sums)))
				case "other sums": // a swapped file with a hash that matches it
					h := sha256.Sum256([]byte("evil"))
					w.sums = hex.EncodeToString(h[:]) + "  " + w.asset + "\n"
				case "no sig":
					w.sig = ""
				case "junk":
					w.sig = "not base64!!"
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
}

// A release signed for another version can't be passed off as the tag's.
func TestGitHubFallbackBindsTheVersion(t *testing.T) {
	w := newFallbackWorld(t, "0.1.4000")
	pub, priv, _ := ed25519.GenerateKey(nil)
	signingKey = base64.StdEncoding.EncodeToString(pub)
	w.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("magpie release 0.1.3999\n"+w.sums)))
	if _, err := Latest(context.Background()); err == nil {
		t.Fatal("accepted a signature for another version")
	}
}

// The feed's old unsigned allowance doesn't carry over: a release GitHub
// gives with no signature is refused even for an old version.
func TestGitHubFallbackNeedsASignatureForOldVersionsToo(t *testing.T) {
	w := newFallbackWorld(t, "0.1.100")
	w.sig = ""
	if _, err := Latest(context.Background()); err == nil {
		t.Fatal("accepted an unsigned release from GitHub")
	}
}

// A feed that answers is used and GitHub isn't asked; a custom feed
// (MAGPIE_UPDATE_FEED) never falls back.
func TestGitHubIsOnlyAFallback(t *testing.T) {
	w := newFallbackWorld(t, "0.1.4000")
	t.Setenv("MAGPIE_UPDATE_FEED", siteFeed+"?mine")
	if _, err := Latest(context.Background()); err == nil {
		t.Fatal("a custom feed fell back")
	}
	if w.apiHits.Load() != 0 {
		t.Fatal("GitHub was asked")
	}
}

// On Stable the site's choice can't be made by GitHub: no fallback.
func TestGitHubFallbackSkipsStable(t *testing.T) {
	w := newFallbackWorld(t, "0.1.4000")
	stableFor(t, "stable")
	if _, err := Latest(context.Background()); err == nil {
		t.Fatal("Stable fell back to the newest release")
	}
	if w.apiHits.Load() != 0 {
		t.Fatal("GitHub was asked")
	}
}

// A feed that names its files on the site gets their github.com address
// too, so a network without the site downloads them from GitHub.
func TestFeedFilesGetTheirGitHubAddress(t *testing.T) {
	rel := &Release{Version: "0.1.4000", Assets: map[string]Asset{
		"a": {URL: Site + "/dl/v0.1.4000/a"},
		"b": {URL: "https://github.com/x/y/releases/download/v1/b"},
		"c": {URL: Site + "/dl/v0.1.4000/c", GitHub: "https://github.com/keep"},
	}}
	rel.githubAddresses()
	if got := rel.Assets["a"].GitHub; got != githubWeb+"/"+releaseRepo+"/releases/download/v0.1.4000/a" {
		t.Fatalf("a: %q", got)
	}
	if rel.Assets["b"].GitHub != "" || rel.Assets["c"].GitHub != "https://github.com/keep" {
		t.Fatalf("b, c: %+v %+v", rel.Assets["b"], rel.Assets["c"])
	}
}
