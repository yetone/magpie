package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Clef asked through magpie's System One API gets the images the request
// embeds, as Workers AI takes them (ARNO on Discord: clef模型是支持图像输入的):
// a request of a few MiB isn't refused, and the images reach the Clef's
// own path as they were sent.
func TestSystemOneClefImages(t *testing.T) {
	setHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var path string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path = r.URL.Path
		json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":{"model":"clef","answers":{"cat":{"type":"noul","noul":0.97}}},"success":true,"errors":[],"messages":[]}`)
	}))
	defer srv.Close()
	p, err := provider.FromPreset("cloudflare-jev")
	if err != nil {
		t.Fatal(err)
	}
	p.Key, p.Decide = "k", srv.URL+"/client/v4/accounts/a/ai/run"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	img := "data:image/png;base64," + strings.Repeat("iVBORw0K", 3<<20/8) // 3 MiB
	body, _ := json.Marshal(map[string]any{
		"model":     p.ID + "/@cf/cloudflare/clef",
		"state":     "the picture",
		"images":    []string{img},
		"questions": map[string]any{"cat": map[string]any{"type": "noul", "instructions": "Is there a cat?"}},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer magpie")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"noul":0.97`) {
		t.Fatalf("%d %.300s", rec.Code, rec.Body)
	}
	if path != "/client/v4/accounts/a/ai/run/@cf/cloudflare/clef" || got["model"] != "clef" {
		t.Fatalf("asked %s model %v", path, got["model"])
	}
	if ims, _ := got["images"].([]any); len(ims) != 1 || ims[0] != img {
		t.Fatalf("the Clef got images %d", len(ims))
	}
}
