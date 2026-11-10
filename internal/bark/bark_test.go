package bark

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fake is a Bark server: it records each push and answers as Bark does.
type fake struct {
	mu   sync.Mutex
	got  []map[string]any
	urls []string
	code int
	say  string
	*httptest.Server
}

func newFake(t *testing.T, code int, say string) *fake {
	f := &fake{code: code, say: say}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		f.mu.Lock()
		f.got, f.urls = append(f.got, m), append(f.urls, r.Method+" "+r.URL.String())
		f.mu.Unlock()
		w.WriteHeader(f.code)
		io.WriteString(w, f.say)
	}))
	t.Cleanup(f.Close)
	old := client
	client = &http.Client{} // the test's own: never the system proxy, never the network
	t.Cleanup(func() { client = old })
	return f
}

func TestPushPostsJSONAndNeverPutsTheKeyInTheAddress(t *testing.T) {
	f := newFake(t, 200, `{"code":200,"message":"success"}`)
	if err := Push(context.Background(), Target{Server: f.URL + "/", Key: " SECRETKEY123 "}, "Title", "Body"); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.urls[0] != "POST /push" {
		t.Fatalf("requests %v", f.urls)
	}
	m := f.got[0]
	if m["device_key"] != "SECRETKEY123" || m["title"] != "Title" || m["body"] != "Body" || m["group"] != "magpie" {
		t.Fatalf("body %v", m)
	}
}

func TestPushRefusalNamesNeitherKey(t *testing.T) {
	f := newFake(t, 400, `{"code":400,"message":"failed to get device token: SECRETKEY123 not found"}`)
	err := Push(context.Background(), Target{Server: f.URL, Key: "SECRETKEY123"}, "t", "b")
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "SECRETKEY123") {
		t.Fatalf("the error says the key: %v", err)
	}
}

func TestPushNeedsAKeyAndFailedConnectionsSayNoKey(t *testing.T) {
	if err := Push(context.Background(), Target{Server: "http://127.0.0.1:1"}, "t", "b"); err == nil {
		t.Fatal("pushed with no key")
	}
	old := client
	client = &http.Client{} // port 1 on this computer: refused, nothing leaves it
	t.Cleanup(func() { client = old })
	err := Push(context.Background(), Target{Server: "http://127.0.0.1:1/SECRETKEY123", Key: "SECRETKEY123"}, "t", "b")
	if err == nil || strings.Contains(err.Error(), "SECRETKEY123") {
		t.Fatalf("err %v", err)
	}
}

func TestDefaultServer(t *testing.T) {
	if got := (Target{Key: "k"}).url(); got != "https://api.day.app/push" {
		t.Fatal(got)
	}
	if got := (Target{Server: " https://bark.example.com/ ", Key: "k"}).url(); got != "https://bark.example.com/push" {
		t.Fatal(got)
	}
}
