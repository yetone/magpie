package gui

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// A custom provider on OpenAI's Responses API, saved by the editor with
// "Searches the web by itself" ticked and its models typed in because it
// has no model list, is offered in Settings › Models › Web search's searcher
// picker with those models (axelburks on Discord): it was named among the
// relays to pick there, with nothing to pick.
func TestSearcherPickerOffersResponsesRelay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.NotFoundHandler()) // no /models
	t.Cleanup(srv.Close)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(
		`{"new":true,"name":"Relay","responses":"`+srv.URL+`/v1","baseAPI":"responses","key":"k","searches":true,"icon":"generic","models":["gpt-5.5"]}`)))
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	var s settingsJSON
	searchState(&s)
	if len(s.SearchChoices) != 1 || s.SearchChoices[0].Name != "Relay" {
		t.Fatalf("choices = %+v", s.SearchChoices)
	}
	c := s.SearchChoices[0]
	if len(c.Models) != 1 || c.Models[0].ID != c.ID+"/gpt-5.5" || c.Small != "gpt-5.5" {
		t.Errorf("choice = %+v", c)
	}
	if len(s.SearchRelays) != 1 || len(s.SearchLeftOut) != 0 {
		t.Errorf("relays %v, left out %v", s.SearchRelays, s.SearchLeftOut)
	}
}

// Settings saves the searchers after the one named (searcherThen) when
// each can search, keeps them through a save that doesn't send them (the
// page's other rows), and clears them on an empty list.
func TestSearcherThenFromSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	for _, name := range []string{"One", "Two"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(
			`{"new":true,"name":"`+name+`","responses":"`+srv.URL+`/v1","baseAPI":"responses","key":"k-`+name+`","searches":true,"icon":"generic","models":["gpt-5.5"]}`)))
		if w.Code != 200 {
			t.Fatalf("save %s: %d %s", name, w.Code, w.Body)
		}
	}
	var st settingsJSON
	searchState(&st)
	if len(st.SearchChoices) != 2 {
		t.Fatalf("choices = %+v", st.SearchChoices)
	}
	one, two := st.SearchChoices[0].Models[0].ID, st.SearchChoices[1].Models[0].ID
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(w, httptest.NewRequest("POST", "/api/settings", strings.NewReader(body)))
		return w
	}
	if w := post(`{"searcher":"` + one + `","searcherThen":["nowhere/m1"]}`); w.Code == 200 || !strings.Contains(w.Body.String(), "can't search the web") {
		t.Errorf("one that can't search: %d %s", w.Code, w.Body)
	}
	if got := settings.Load().SearcherThen; len(got) != 0 {
		t.Errorf("saved after a refusal: %v", got)
	}
	if w := post(`{"searcher":"` + one + `","searcherThen":["` + two + `"]}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if got := settings.Load().SearcherThen; !slices.Equal(got, []string{two}) {
		t.Errorf("saved %v", got)
	}
	if w := post(`{"searcher":"` + one + `","theme":"dark"}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if got := settings.Load().SearcherThen; !slices.Equal(got, []string{two}) {
		t.Errorf("after a save without it: %v", got)
	}
	if w := post(`{"searcher":"` + one + `","searcherThen":[]}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if got := settings.Load().SearcherThen; len(got) != 0 {
		t.Errorf("after clearing: %v", got)
	}
}
