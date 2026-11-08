package gui

// The editor's Fetch the model list (axelburks on Discord): unticked, it is
// saved as listOff and kept by a save that doesn't name it; the card says
// so, and serves the ids added alone, not the list fetched before.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestProviderSaveListOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	card := func() (off, takes bool, models []string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
		var got struct {
			Providers []struct {
				ID           string `json:"id"`
				ListOff      bool   `json:"listOff"`
				TakesListOff bool   `json:"takesListOff"`
				Models       []struct {
					ID string `json:"id"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		for _, p := range got.Providers {
			if p.ID == "ark" {
				for _, m := range p.Models {
					models = append(models, m.ID)
				}
				return p.ListOff, p.TakesListOff, models
			}
		}
		t.Fatal("ark not listed")
		return
	}

	const chat = "http://127.0.0.1:1/api/v3"
	post(`{"id":"ark","name":"Ark","key":"ark-k","chat":"` + chat + `","new":true}`)
	if err := catalog.SaveLive("ark", chat, []catalog.Model{{ID: "doubao-seed-2-0"}, {ID: "glm-5"}}); err != nil {
		t.Fatal(err)
	}
	if off, takes, ms := card(); off || !takes || !slices.Contains(ms, "doubao-seed-2-0") {
		t.Fatalf("before: off %v takes %v models %v", off, takes, ms)
	}
	post(`{"id":"ark","from":"ark","name":"Ark","key":"","chat":"` + chat + `","models":["ep-20261108-abcde"],"listOff":true,"headers":{}}`)
	if off, _, ms := card(); !off || !slices.Equal(ms, []string{"ep-20261108-abcde"}) {
		t.Fatalf("off: %v models %v", off, ms)
	}
	// a save that doesn't name it (another editor's) keeps it
	post(`{"id":"ark","from":"ark","name":"Ark","key":"","chat":"` + chat + `","models":["ep-20261108-abcde"],"headers":{}}`)
	if off, _, _ := card(); !off {
		t.Fatal("a save that didn't name it turned the list back on")
	}
	post(`{"id":"ark","from":"ark","name":"Ark","key":"","chat":"` + chat + `","models":["ep-20261108-abcde"],"listOff":false,"headers":{}}`)
	if off, _, ms := card(); off || !slices.Contains(ms, "doubao-seed-2-0") {
		t.Fatalf("on again: off %v models %v", off, ms)
	}
}
