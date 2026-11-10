package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func modelPriceAsk(t *testing.T, mux *http.ServeMux, method, target, body string) (int, modelPriceJSON) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, target, strings.NewReader(body)))
	var out modelPriceJSON
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, out
}

// The Usage page's Set price… (soilzhu80 on X) prices a model no provider
// lists: it is kept as the model's price from any provider (*/model), the
// key `magpie model price '*/model'` writes and a session's model is
// counted at, and a null price takes it away again.
func TestModelPriceFromAnyProvider(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	modelPriceRoutes(mux)

	code, got := modelPriceAsk(t, mux, "GET", "/api/model-price?model=Agent-Only-9", "")
	if code != 200 || got.Price != nil || got.Model != "Agent-Only-9" {
		t.Fatalf("before: %d %+v", code, got)
	}
	code, got = modelPriceAsk(t, mux, "POST", "/api/model-price", `{"model":"Agent-Only-9","price":{"input":1.5,"output":6,"cache_read":0.15,"cache_write":0}}`)
	if code != 200 || got.Price == nil || got.Price.Input != 1.5 || got.Price.Output != 6 || got.Price.CacheRead != 0.15 {
		t.Fatalf("set: %d %+v", code, got)
	}
	m, ok := settings.Load().ModelPrices["*/agent-only-9"]
	if !ok || m.Input == nil || *m.Input != 1.5 {
		t.Fatalf("not kept under */agent-only-9: %v", settings.Load().ModelPrices)
	}
	if _, got = modelPriceAsk(t, mux, "GET", "/api/model-price?model=agent-only-9", ""); got.Price == nil || got.Price.Output != 6 {
		t.Fatalf("read back: %+v", got)
	}
	// a price no vendor could charge, and no one model, are refused
	for _, body := range []string{`{"model":"agent-only-9","price":{"input":-1,"output":6}}`, `{"model":"*","price":{"input":1,"output":6}}`, `{"model":"*/x","price":{"input":1,"output":6}}`, `{"model":" ","price":null}`} {
		if code, _ := modelPriceAsk(t, mux, "POST", "/api/model-price", body); code != 400 {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}
	if m := settings.Load().ModelPrices["*/agent-only-9"]; m.Input == nil || *m.Input != 1.5 {
		t.Fatalf("a refused price changed the kept one: %+v", m)
	}
	code, got = modelPriceAsk(t, mux, "POST", "/api/model-price", `{"model":"Agent-Only-9","price":null}`)
	if code != 200 || got.Price != nil {
		t.Fatalf("take away: %d %+v", code, got)
	}
	if _, ok := settings.Load().ModelPrices["*/agent-only-9"]; ok {
		t.Fatal("still kept after a null price")
	}
}
