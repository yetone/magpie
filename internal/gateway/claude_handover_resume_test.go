//go:build darwin || linux

package gateway

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The successor starts serving before the predecessor finishes draining.
// A turn received then must wait for the saved session, not replay history.
func TestClaudeHandoverWaitsForSavedSession(t *testing.T) {
	h := newResumeHarness(t)
	h.ask("rules A", "a1")
	old := h.s
	before := h.runs()
	dir, err := os.MkdirTemp("/tmp", "mready-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "cb")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	asked := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_magpie/claude-ready" {
			http.NotFound(w, r)
			return
		}
		close(asked)
		select {
		case <-release:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	var once sync.Once
	unhold := func() { once.Do(func() { close(release) }) }
	defer unhold()
	ForwardCallbacks(sock)
	defer ForwardCallbacks("")
	h.s = New()
	t.Cleanup(h.s.subscription.abortAll)
	done := make(chan struct{})
	go func() { defer close(done); h.ask("rules A", "a1", "a2") }()
	select {
	case <-asked:
		old.subscription.abortAll()
		unhold()
	case <-done:
		t.Fatal("the successor answered before the predecessor saved its session")
	case <-time.After(10 * time.Second):
		t.Fatal("the successor neither waited for the saved session nor answered")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the successor did not resume after the save")
	}
	resumed := h.runStartedSince(before)
	if !strings.Contains(resumed, "--resume ") || !strings.Contains(resumed, "\ntold ") || strings.Contains(resumed[strings.Index(resumed, "\ntold "):], "a1") {
		t.Fatalf("the successor replayed the conversation instead of resuming: %s", resumed)
	}
}

func TestClaudeHandoverReadiness(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mready-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "cb")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := New()
	go s.ServeCallbacks(ln)
	ForwardCallbacks(sock)
	defer ForwardCallbacks("")
	// A cancelled wait must not mark the predecessor ready.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := awaitPredecessorSessions(ctx); err == nil {
		t.Fatal("unfinished predecessor was ready")
	}
	close(s.sessionsSaved)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := awaitPredecessorSessions(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeHandoverReadinessCompatibility(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "mready-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			sock := filepath.Join(dir, "cb")
			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })}
			go srv.Serve(ln)
			defer srv.Close()
			ForwardCallbacks(sock)
			defer ForwardCallbacks("")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = awaitPredecessorSessions(ctx)
			if (err == nil) != (status == http.StatusNotFound) {
				t.Fatalf("status %d: %v", status, err)
			}
		})
	}
}

// A predecessor still draining other providers' long replies holds a Claude
// turn predecessorWait at most, and none after it: the turn goes on (a
// replay) instead of waiting out a 15-minute drain past its client's
// timeout, or being refused.
func TestClaudeHandoverWaitIsBounded(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mready-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "cb")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := New() // its sessions never saved: still draining
	go s.ServeCallbacks(ln)
	ForwardCallbacks(sock)
	defer ForwardCallbacks("")
	old := predecessorWait
	predecessorWait = 50 * time.Millisecond
	defer func() { predecessorWait = old }()
	if err := awaitPredecessorSessions(context.Background()); err != errPredecessorSlow {
		t.Fatalf("a predecessor past predecessorWait: %v, want errPredecessorSlow", err)
	}
	predecessorWait = time.Hour
	began := time.Now()
	if err := awaitPredecessorSessions(context.Background()); err != nil || time.Since(began) > 5*time.Second {
		t.Fatalf("the next turn waited again (%v, %s)", err, time.Since(began))
	}
}
