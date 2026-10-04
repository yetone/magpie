package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func updateHome(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

// A release's file comes through a GitHub download mirror only when one is
// given (magpie update --mirror, or Settings' UpdateMirror): its prefix
// before the github.com URL. The feed, with the file's SHA-256, is never
// asked through it, and a file the mirror changed is refused (akic404 on
// Discord: install and update without a proxy).
func TestUpdateThroughAMirror(t *testing.T) {
	updateHome(t)
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	asset := Asset{URL: "https://github.com/yetone/magpie-releases/releases/download/v9.9.9/magpie-cli-linux-amd64", SHA256: hex.EncodeToString(sum[:])}

	var mu sync.Mutex
	var seen []string
	tamper := false
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		bad := tamper
		mu.Unlock()
		if bad {
			w.Write([]byte("not magpie"))
			return
		}
		w.Write(body)
	}))
	defer mirror.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Release{Version: "9.9.9", Assets: map[string]Asset{BinaryAsset(): asset}})
	}))
	defer feed.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", feed.URL+"/api/latest")

	// none unless given: no mirror is magpie's own
	if m := Mirror(context.Background()); m != "" {
		t.Fatalf("a mirror by default: %q", m)
	}
	ctx := WithMirror(context.Background(), mirror.URL)
	rel, err := Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "magpie.new")
	if err := download(ctx, rel.Assets[BinaryAsset()], path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(body) {
		t.Fatalf("downloaded %q", b)
	}
	mu.Lock()
	if want := []string{"/" + asset.URL}; !slices.Equal(seen, want) {
		t.Fatalf("the mirror was asked %v, want %v (the feed never)", seen, want)
	}
	seen, tamper = nil, true
	mu.Unlock()

	// what the mirror changed doesn't match the feed's checksum
	os.Remove(path)
	err = download(ctx, asset, path)
	if err == nil || !strings.Contains(err.Error(), "does not match its checksum") || !strings.Contains(err.Error(), "mirror") {
		t.Fatalf("a tampered file: %v", err)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Fatal("the tampered file was kept")
	}

	// Settings' mirror is the app's too; off turns it off for one run
	mu.Lock()
	tamper, seen = false, nil
	mu.Unlock()
	if err := settings.Save(settings.Settings{UpdateMirror: mirror.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	if m := Mirror(context.Background()); m != mirror.URL+"/" {
		t.Fatalf("Settings' mirror: %q", m)
	}
	if err := download(context.Background(), asset, path); err != nil {
		t.Fatal(err)
	}
	if m := Mirror(WithMirror(context.Background(), "off")); m != "" {
		t.Fatalf("off: %q", m)
	}
	// a file not on GitHub goes as it is
	if got := mirrored(mirror.URL, feed.URL+"/x"); got != feed.URL+"/x" {
		t.Fatalf("a non-GitHub URL mirrored: %q", got)
	}
	if err := settings.Save(settings.Settings{UpdateMirror: "ftp://nope"}); err == nil {
		t.Fatal("an ftp mirror was saved")
	}
}

// magpie update --proxy takes the feed and the download through the proxy
// given, over Settings' and the environment's.
func TestUpdateThroughAProxyGiven(t *testing.T) {
	updateHome(t)
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var mu sync.Mutex
	var seen []string
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		if r.URL.Path == "/asset" {
			w.Write(body)
			return
		}
		json.NewEncoder(w).Encode(Release{Version: "9.9.9", Assets: map[string]Asset{
			BinaryAsset(): {URL: "http://releases.magpie.invalid/asset", SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer px.Close()
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_UPDATE_FEED", "http://feed.magpie.invalid/api/latest")
	ctx := WithProxy(context.Background(), px.URL)
	rel, err := Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := download(ctx, rel.Assets[BinaryAsset()], filepath.Join(t.TempDir(), "magpie.new")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"http://feed.magpie.invalid/api/latest", "http://releases.magpie.invalid/asset"}; !slices.Equal(seen, want) {
		t.Fatalf("through the proxy: %v, want %v", seen, want)
	}
	for _, bad := range []string{"ftp://127.0.0.1:21", "socks4://127.0.0.1:1080"} {
		if CheckProxy(bad) == nil {
			t.Errorf("%s taken as a proxy", bad)
		}
	}
	for _, good := range []string{"127.0.0.1:7890", "http://127.0.0.1:7890", "socks5://127.0.0.1:1080", "socks5h://u:p@127.0.0.1:1080", "direct"} {
		if err := CheckProxy(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
}
