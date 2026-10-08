package testenv

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocal(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:80": true, "[::1]:443": true, "localhost:3425": true, "LOCALHOST.:1": true,
		"magpie.localhost:80": true, "0.0.0.0:80": true, "[::]:80": true, "127.8.0.1:9": true,
		"models.dev:443": false, "api.kimi.com:443": false, "10.0.0.132:22": false,
		"192.168.1.2:80": false, "localhost.example.com:80": false, "decide.test:443": false,
	} {
		if got := local(addr); got != want {
			t.Errorf("local(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Offline lets a loopback server answer through http.DefaultClient and
// refuses a vendor's host without dialing it.
func TestOffline(t *testing.T) {
	tr := http.DefaultTransport.(*http.Transport)
	was := tr.DialContext
	t.Cleanup(func() { tr.DialContext = was })
	Offline()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("here")) }))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback refused: %v", err)
	}
	resp.Body.Close()
	clone := tr.Clone() // a transport made from it after the call
	for _, c := range []*http.Client{http.DefaultClient, {Transport: clone}} {
		_, err := c.Get("https://models.dev/api.json")
		if err == nil || !strings.Contains(err.Error(), "tests stay off the network") {
			t.Errorf("models.dev: %v", err)
		}
	}
}
