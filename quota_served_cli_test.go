package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// magpie quota --json tells when each key last answered through the gateway
// — a process of its own, which wrote it down — and marks the latest last,
// as GET /v1/magpie/quotas does (#570).
func TestQuotaJSONLastServedAt(t *testing.T) {
	groupsHome(t)
	for _, k := range agentenv.Vars {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	// no agent's CLI nor the keychain is asked: what answers is inert
	bin := t.TempDir()
	for _, name := range []string{"security", "secret-tool", "claude", "codex", "cursor-agent", "devin", "grok", "kiro-cli"} {
		testenv.Program(t, filepath.Join(bin, name), "#!/bin/sh\nexit 1\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"balance":"7"}`) }))
	t.Cleanup(up.Close)
	for _, id := range []string{"a", "b"} {
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		p.BalanceURL, p.BalancePath = up.URL, "balance"
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
	}
	provider.ForgetBalances()
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	a, _ := provider.Find("a")
	provider.NoteServed(*a, at)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	err = quotaCmd([]string{"quota", "a", "b", "--json"})
	w.Close()
	os.Stdout = old
	out := <-done
	if err != nil {
		t.Fatal(err)
	}
	var qs []map[string]any
	if err := json.Unmarshal(out, &qs); err != nil {
		t.Fatal(err, string(out))
	}
	if len(qs) != 2 {
		t.Fatalf("%s", out)
	}
	for _, q := range qs {
		switch q["provider"] {
		case "a":
			if q["lastServedAt"] != at.UTC().Format(time.RFC3339) || q["last"] != true {
				t.Errorf("served: %v", q)
			}
		case "b":
			if _, ok := q["lastServedAt"]; ok || q["last"] != nil {
				t.Errorf("never served: %v", q)
			}
		}
	}
}
