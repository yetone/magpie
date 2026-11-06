package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
)

// biubiu8802 on X: a proxy that cuts every HTTP/2 stream part way, while
// it carries HTTP/1.1 to the same host whole. Each try of the update broke
// off the same way ("unexpected EOF"), as each went over HTTP/2 again; the
// tries after the first go over HTTP/1.1, and on from where it broke off.
func TestDownloadBrokenOverHTTP2TriesHTTP1(t *testing.T) {
	quickRetries(t)
	body := make([]byte, 512<<10)
	for i := range body {
		body[i] = byte(i * 13)
	}
	sum := sha256.Sum256(body)
	var mu sync.Mutex
	var asked []string // "proto range" of each request
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Proto+" "+r.Header.Get("Range"))
		mu.Unlock()
		from := 0
		if rg := r.Header.Get("Range"); rg != "" {
			from, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		if r.ProtoMajor == 2 {
			// the proxy cuts the stream part way: a reset
			// (INTERNAL_ERROR) after the first 100KB
			w.Write(body[from:min(from+100<<10, len(body))])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		w.Write(body[from:])
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	old := client
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: pool}}
	client = &http.Client{Transport: netproxy.Dispatch(tr)}
	t.Cleanup(func() { client = old; tr.CloseIdleConnections() })

	path := filepath.Join(t.TempDir(), "magpie.new")
	if err := download(context.Background(), Asset{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, path); err != nil {
		t.Fatalf("download: %v (asked %q)", err, asked)
	}
	want := []string{"HTTP/2.0 ", "HTTP/1.1 bytes=102400-"}
	if fmt.Sprint(asked) != fmt.Sprint(want) {
		t.Fatalf("asked %q, want %q", asked, want)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, body) {
		t.Fatalf("file has %d bytes, not the release's", len(b))
	}
}
