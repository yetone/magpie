package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// magpie healthcheck, a container's HEALTHCHECK, passes only while a
// magpie gateway answers at MAGPIE_ADDR.
func TestHealthcheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	magpie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"magpie","version":"dev"}`))
	}))
	defer magpie.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(magpie.URL, "http://"))
	if err := run([]string{"healthcheck"}); err != nil {
		t.Fatal("magpie answering:", err)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"nginx"}`))
	}))
	defer other.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(other.URL, "http://"))
	if err := run([]string{"healthcheck"}); err == nil {
		t.Error("something else answering passed")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("MAGPIE_ADDR", addr)
	if err := run([]string{"healthcheck"}); err == nil {
		t.Error("nothing listening passed")
	}
}

// The Docker image sets HOME and the XDG folders inside its volume, which
// a volume an older image made doesn't have yet: magpie makes them.
func TestMakeDirs(t *testing.T) {
	vol := t.TempDir()
	for k, d := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state"} {
		t.Setenv(k, filepath.Join(vol, d))
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:1")
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"home", "config", "cache", "data", "state"} {
		if fi, err := os.Stat(filepath.Join(vol, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s not made: %v", d, err)
		}
	}
}
