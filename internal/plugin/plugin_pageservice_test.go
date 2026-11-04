package plugin

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// holdPort listens on a free port on 127.0.0.1 for the test, as the app's
// host holds its page service's port, and gives the port.
func holdPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// #5187 (MYCF711): magpie plugin update, run while the app runs, started
// a host whose page-chat waited on the port the app's host holds. A host
// in a magpie that serves no pages loads such a plugin at once, its
// service on a port of its own, and leaves the held port alone.
func TestHostServingNoPagesLeavesHeldPort(t *testing.T) {
	sandbox(t)
	load := pluginLoadWait
	pluginLoadWait = 10 * time.Second
	ServePages(false)
	t.Cleanup(func() { pluginLoadWait = load; ServePages(true) })
	port := holdPort(t)
	t.Setenv("PAGESVC_PORT", strconv.Itoa(port))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/pageservice")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ls, err := Plugins(ctx)
	took := time.Since(start)
	if err != nil || len(ls) != 1 || ls[0].Error != "" {
		t.Fatalf("Plugins = %+v, %v", ls, err)
	}
	// the load is a bun start and a listen; waiting on the held port, it
	// takes the whole of pluginLoadWait
	if took > 5*time.Second {
		t.Errorf("the plugins were listed after %v: the host waited on the held port", took)
	}
}

// hostOptions moves the service only in a magpie that serves no pages,
// only through the option the page names, and keeps the plugin's other
// options; one that serves pages hands the options on as they are.
func TestHostOptionsMoveOnlyTheService(t *testing.T) {
	t.Cleanup(func() { ServePages(true) })
	dir, _ := filepath.Abs("testdata/pageservice")
	opts := map[string]any{"port": float64(3477), "other": "kept"}

	ServePages(true)
	if got := hostOptions(dir, opts); got["port"] != float64(3477) || got["other"] != "kept" {
		t.Errorf("serving pages, options = %v, want them as they are", got)
	}
	ServePages(false)
	got := hostOptions(dir, opts)
	p, ok := portOf(got["port"])
	if !ok || p == 3477 || got["other"] != "kept" {
		t.Errorf("serving no pages, options = %v, want port moved and other kept", got)
	}
	if opts["port"] != float64(3477) {
		t.Errorf("the plugin's own options were changed: %v", opts)
	}
	// a plugin with no page service, and one given by a file, as they are
	fake, _ := filepath.Abs("testdata/fake/index.js")
	plain, _ := filepath.Abs("testdata/options")
	for _, target := range []string{fake, plain} {
		if got := hostOptions(target, opts); got["port"] != float64(3477) {
			t.Errorf("%s: options = %v, want them as they are", target, got)
		}
	}
}
