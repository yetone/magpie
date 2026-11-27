package gui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/filememo"
)

// A GET or HEAD writes nothing, so it drops nothing held (provider.Changed
// moves filememo.Gen): the page's files and icons too, not only /api's
// reads. A request that may write drops it, wherever it goes.
func TestHeldDropsOnlyForWrites(t *testing.T) {
	h := held(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, c := range []struct {
		method, path string
		drops        bool
	}{
		{http.MethodGet, "/", false},
		{http.MethodGet, "/app.js", false},
		{http.MethodGet, "/logos/openai.svg", false},
		{http.MethodHead, "/i18n.js", false},
		{http.MethodGet, "/api/state", false},
		{http.MethodGet, "/_magpie/plugin/x/index.html", false},
		{http.MethodPost, "/api/provider/save", true},
		{http.MethodPost, "/_magpie/plugin/x/do", true},
		{http.MethodDelete, "/api/provider", true},
		{http.MethodPost, "/api/page-away", false},
	} {
		before := filememo.Gen()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, nil))
		if dropped := filememo.Gen() != before; dropped != c.drops {
			t.Errorf("%s %s: dropped what was held %v, want %v", c.method, c.path, dropped, c.drops)
		}
	}
}
