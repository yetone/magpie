package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// The Claude editor's screens are the gateway's runs list asked with
// ?screen=1 (#8075), as the gateway sent them; a gateway that answers
// anything else is an error the editor shows, never an empty list that
// says nothing runs.
func TestClaudeScreensAskTheGateway(t *testing.T) {
	asked := make(chan string, 4)
	var refuse atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.URL.Path + "?" + r.URL.RawQuery
		if refuse.Load() {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(gateway.ClaudeRuns{Object: "claude-runs", Runs: []gateway.ClaudeRun{{PID: 7, Model: "claude-opus-5-5", Screen: []string{"╭──╮", "│ > hi │"}}}})
	}))
	t.Cleanup(srv.Close)
	was := claudeScreensFrom
	claudeScreensFrom = func() string { return srv.URL }
	t.Cleanup(func() { claudeScreensFrom = was })

	runs, err := claudeScreens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := <-asked; got != gateway.ClaudeRunsPath+"?screen=1" {
		t.Fatalf("asked %s", got)
	}
	if len(runs.Runs) != 1 || runs.Runs[0].PID != 7 || !slices.Equal(runs.Runs[0].Screen, []string{"╭──╮", "│ > hi │"}) {
		t.Fatalf("runs %+v", runs)
	}
	refuse.Store(true)
	if _, err := claudeScreens(context.Background()); err == nil {
		t.Fatal("a refusal read as no runs")
	}
}
