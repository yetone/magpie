package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Gateway page's key picker offers the models a routing group listing
// alone keeps out of the lists, after their provider's other models: a key
// not given the group is listed them (the gateway's keyListed), and with
// them out of the picker a provider's section offered only the models no
// such group named (luckymanjc on Discord: Codex offered GPT-6-Luna
// alone).
func TestCallerKeyModelsOfferGroupKept(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"o1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "alone", Name: "Alone", Members: []string{"relay/m1", "relay/m3"}, Unlists: true}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/caller-keys/models", nil))
	var l struct{ Models []keyModelJSON }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal(w.Code, w.Body)
	}
	var ids []string
	for _, m := range l.Models {
		if !m.Group {
			ids = append(ids, m.ID)
		}
	}
	at := func(id string) int { return slices.Index(ids, id) }
	if at("relay/m1") < 0 || at("relay/m3") < 0 || at("relay/m2") < 0 || at("other/o1") < 0 {
		t.Fatalf("picker offers %v, want every model of relay and other", ids)
	}
	if max(at("relay/m1"), at("relay/m2"), at("relay/m3")) > at("other/o1") && at("other/o1") > min(at("relay/m1"), at("relay/m2"), at("relay/m3")) {
		t.Fatalf("relay's models are not together: %v", ids)
	}
}
