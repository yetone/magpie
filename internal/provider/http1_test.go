package provider

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

// kepner_53726: a provider the user added on a company gateway whose
// HTTP/2 connections broke mid-reply is spoken HTTP/1.1 once asked
// (HTTP1), through every request Via makes: its models list lists the
// protocol it was asked over. A preset's provider is refused it, and one
// set by hand is spoken HTTP/2 as before.
func TestHTTP1OwnProviderOnly(t *testing.T) {
	azureHome(t)
	dialsOut(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"`+r.Proto+`"}]}`)
	}))
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ctx := context.Background()
	proto := func(p Provider) string {
		t.Helper()
		ms, err := p.List(ctx)
		if err != nil || len(ms) != 1 {
			t.Fatalf("%s: %v %v", p.ID, ms, err)
		}
		return ms[0].ID
	}

	if err := Save(Provider{ID: "gw", Name: "Gateway", Anthropic: srv.URL + "/v1", InsecureTLS: true, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("gw")
	if got := proto(*p); got != "HTTP/2.0" {
		t.Fatalf("not asked, spoken %s", got)
	}
	p.HTTP1 = true
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("gw")
	if !p.HTTP1 {
		t.Fatal("HTTP1 not kept")
	}
	if got := proto(*p); got != "HTTP/1.1" {
		t.Fatalf("asked HTTP/1.1, spoken %s", got)
	}

	if err := Save(Provider{ID: "ollama", Name: "Ollama", Preset: "ollama", Chat: srv.URL + "/v1", HTTP1: true}); err == nil {
		t.Fatal("a preset's provider saved with HTTP1")
	}
	hand := Provider{ID: "codex", Chat: srv.URL + "/v1", HTTP1: true}
	if hand.TakesInsecureTLS() {
		t.Fatal("a subscription's id takes HTTP1")
	}
}
