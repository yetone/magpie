package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

// A key's model may be a pattern ("re:<regular expression>"), refused when
// it isn't one rather than kept to allow every model, and a key keeps the
// names it gives models (tsingchao on X).
func TestCallerKeyPatternsAndNames(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	request := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w
	}
	var s struct{ Keys []access.Key }
	if w := request("/api/caller-keys/add-key", `{"name":"Phone"}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := s.Keys[0].ID
	if w := request("/api/caller-keys/models-key", `{"key":"`+id+`","models":["re:^gpt-","openai/o3"]}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil || !slices.Equal(s.Keys[0].Models, []string{"re:^gpt-", "openai/o3"}) {
		t.Fatal("pattern", w.Code, w.Body)
	}
	if w := request("/api/caller-keys/models-key", `{"key":"`+id+`","models":["re:("]}`); w.Code != 400 || !strings.Contains(w.Body.String(), "regular expression") {
		t.Fatal("kept a bad pattern", w.Code, w.Body)
	}
	if w := request("/api/caller-keys/aliases-key", `{"key":"`+id+`","aliases":{"fast":"openai/gpt-5"}}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil || s.Keys[0].Aliases["fast"] != "openai/gpt-5" {
		t.Fatal("alias", w.Code, w.Body)
	}
	// a model the key may not use can't be named
	if w := request("/api/caller-keys/aliases-key", `{"key":"`+id+`","aliases":{"x":"anthropic/claude"}}`); w.Code != 400 {
		t.Fatal("named a model the key may not use", w.Code, w.Body)
	}
	var none struct{ Keys []access.Key }
	if w := request("/api/caller-keys/aliases-key", `{"key":"`+id+`","aliases":{}}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &none) != nil || len(none.Keys[0].Aliases) != 0 {
		t.Fatal("names not taken off", w.Code, w.Body)
	}
}
