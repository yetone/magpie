package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A magpie using this one's Library reads it every minute (frankittee
// on Discord, library.keepBorrowed): it is tagged, and the tag it has
// read gets a 304 with no body until the Library changes.
func TestSharedLibraryTagged(t *testing.T) {
	fresh(t)
	lib := `{"texts":{"default":"one"}}`
	was := SharedLibrary
	SharedLibrary = func() ([]byte, error) { return []byte(lib), nil }
	t.Cleanup(func() { SharedLibrary = was })
	h := New().Handler()
	get := func(tag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/magpie/library", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Host = "127.0.0.1:3425"
		if tag != "" {
			r.Header.Set("If-None-Match", tag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("")
	tag := w.Header().Get("ETag")
	if w.Code != 200 || w.Body.String() != lib || tag == "" {
		t.Fatalf("%d %q, ETag %q", w.Code, w.Body.String(), tag)
	}
	for _, sent := range []string{tag, "W/" + tag, `"other", ` + tag} {
		if w := get(sent); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Fatalf("If-None-Match %s: %d %q", sent, w.Code, w.Body.String())
		}
	}
	if w := get(`"other"`); w.Code != 200 || w.Body.String() != lib {
		t.Fatalf("another tag: %d", w.Code)
	}
	lib = `{"texts":{"default":"two"}}`
	if w := get(tag); w.Code != 200 || w.Body.String() != lib || w.Header().Get("ETag") == tag {
		t.Fatalf("changed: %d %q, ETag %q", w.Code, w.Body.String(), w.Header().Get("ETag"))
	}
}
