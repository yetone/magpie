package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// A host that is busy or whose gateway timed out (GitHub's asset host
// answered 504 Gateway Timeout for 0.1.3316) is asked again, as a
// connection that broke is, and the download goes through once it
// answers.
func TestDownloadRetriesABusyHost(t *testing.T) {
	quickRetries(t)
	body := []byte(strings.Repeat("magpie", 4096))
	sum := sha256.Sum256(body)
	for _, code := range []int{http.StatusGatewayTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusTooManyRequests} {
		var tries atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tries.Add(1) <= 2 {
				w.WriteHeader(code)
				return
			}
			w.Write(body)
		}))
		path := filepath.Join(t.TempDir(), "magpie.zip")
		err := download(context.Background(), Asset{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, path)
		srv.Close()
		if err != nil {
			t.Fatalf("%d: %v", code, err)
		}
		if n := tries.Load(); n != 3 {
			t.Fatalf("%d: tries = %d, want 3", code, n)
		}
		if b, _ := os.ReadFile(path); string(b) != string(body) {
			t.Fatalf("%d: file has %d bytes", code, len(b))
		}
	}
}

// A host busy on every try fails as the network's, naming the file and
// the status; a file the host hasn't got (404) is asked for once.
func TestDownloadBusyEveryTryAndMissingOnce(t *testing.T) {
	quickRetries(t)
	for _, c := range []struct {
		code    int
		tries   int32
		network bool
	}{{http.StatusGatewayTimeout, int32(len(retryWaits) + 1), true}, {http.StatusNotFound, 1, false}} {
		var tries atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tries.Add(1)
			w.WriteHeader(c.code)
		}))
		path := filepath.Join(t.TempDir(), "magpie-darwin-arm64.zip")
		err := download(context.Background(), Asset{URL: srv.URL, SHA256: strings.Repeat("0", 64)}, path)
		srv.Close()
		if err == nil {
			t.Fatalf("%d: no error", c.code)
		}
		if n := tries.Load(); n != c.tries {
			t.Fatalf("%d: tries = %d, want %d", c.code, n, c.tries)
		}
		if Network(err) != c.network {
			t.Fatalf("%d: Network = %v, want %v (%v)", c.code, Network(err), c.network, err)
		}
		if msg := err.Error(); !strings.Contains(msg, "magpie-darwin-arm64.zip") || !strings.Contains(msg, http.StatusText(c.code)) {
			t.Fatalf("%d: error %q names neither the file nor the status", c.code, msg)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%d: a file is left at %s", c.code, path)
		}
	}
}
