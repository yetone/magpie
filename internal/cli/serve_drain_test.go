package cli

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

func TestDrainGrace(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	for v, want := range map[string]time.Duration{
		"":      defaultDrain,
		"30":    30 * time.Second,
		"90s":   90 * time.Second,
		"2m":    2 * time.Minute,
		"0":     0,
		"soon":  defaultDrain,
		"-5":    defaultDrain,
		" 45 ":  45 * time.Second,
		"1.5s":  1500 * time.Millisecond,
		"-1m":   defaultDrain,
		"10000": 10000 * time.Second,
	} {
		t.Setenv("MAGPIE_DRAIN", v)
		if got := drainGrace(); got != want {
			t.Errorf("MAGPIE_DRAIN=%q: %s, want %s", v, got, want)
		}
	}
	// outside Kubernetes the default ends inside docker stop's 10s
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	for _, v := range []string{"", "soon"} {
		t.Setenv("MAGPIE_DRAIN", v)
		if got := drainGrace(); got != plainDrain || got >= 10*time.Second {
			t.Errorf("MAGPIE_DRAIN=%q outside Kubernetes: %s, want %s", v, got, plainDrain)
		}
	}
	t.Setenv("MAGPIE_DRAIN", "30")
	if got := drainGrace(); got != 30*time.Second {
		t.Errorf("MAGPIE_DRAIN=30 outside Kubernetes: %s", got)
	}
}

// Only a SIGTERM drains; in a Kubernetes pod the gateway goes on
// accepting for kubeLinger first, out of the grace.
func TestDrainOnSignal(t *testing.T) {
	var slept time.Duration
	old := lingerSleep
	lingerSleep = func(d time.Duration) { slept += d }
	t.Cleanup(func() { lingerSleep = old })
	t.Setenv("MAGPIE_DRAIN", "")

	drainOnSignal(nil, syscall.SIGTERM) // magpie web beside another magpie's gateway

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGHUP} {
		g := gateway.New()
		drainOnSignal(g, sig)
		if g.Draining() {
			t.Errorf("%v drained the gateway", sig)
		}
	}
	g := gateway.New()
	drainOnSignal(g, syscall.SIGTERM)
	if !g.Draining() || slept != 0 {
		t.Fatalf("SIGTERM outside Kubernetes: draining %v, lingered %s", g.Draining(), slept)
	}
	if g.Grace() != plainDrain {
		t.Errorf("SIGTERM outside Kubernetes: grace %s, want %s", g.Grace(), plainDrain)
	}

	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	g = gateway.New()
	drainOnSignal(g, syscall.SIGTERM)
	if !g.Draining() || slept != kubeLinger {
		t.Fatalf("SIGTERM in Kubernetes: draining %v, lingered %s, want %s", g.Draining(), slept, kubeLinger)
	}
	if g.Grace() != defaultDrain-kubeLinger {
		t.Errorf("SIGTERM in Kubernetes: grace %s, want %s", g.Grace(), defaultDrain-kubeLinger)
	}

	t.Setenv("MAGPIE_DRAIN", "0")
	g = gateway.New()
	drainOnSignal(g, syscall.SIGTERM)
	if g.Draining() {
		t.Fatal("MAGPIE_DRAIN=0 drained")
	}
}
