package testenv

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// FakeVendor serves h as the vendor of the fake plugin
// (internal/plugin/testdata/fake), at $FAKE_BASE, for as long as the test
// runs. Only this test's requests reach h: the fake plugin's fetch sends
// $FAKE_RUN, a value made for this test, as X-Fake-Run, and anything
// without it is answered 421 and logged, with what it was and where it
// came from, never counted as the test's.
//
// The kernel hands a port a test freed to the next listener that asks,
// in this process or another shard's, and a request still on its way to
// the old one lands here (#3357): TestClinePluginTickSendsNoProviderOptions's
// vendor (then named TestClinePluginPinsDeepSeekAndGLM) got a seventh
// request while magpie had sent six, and
// TestPluginToldWhereTheAgentWorks's a second (#3347). A test that holds
// its plugin to exactly one request per ask keeps that guarantee for its
// own requests, and a stranger's is no longer one of them.
func FakeVendor(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	run := hex.EncodeToString(b[:])
	t.Setenv("FAKE_RUN", run)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Fake-Run"); got != run {
			n, _ := io.Copy(io.Discard, r.Body)
			t.Logf("the vendor got a request that isn't this test's (X-Fake-Run %q): %s %s from %s, User-Agent %q, Authorization %q, %d bytes; answered 421", got, r.Method, r.RequestURI, r.RemoteAddr, r.UserAgent(), r.Header.Get("Authorization"), n)
			w.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		r.Header.Del("X-Fake-Run")
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FAKE_BASE", srv.URL+"/v1")
	return srv
}
