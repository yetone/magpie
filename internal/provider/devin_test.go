package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseDevinStatus(t *testing.T) {
	user, plan, ok := parseDevinStatus(`Logged in (via Devin).

Credentials:
  File:              /home/u/.local/share/devin/credentials.toml
  API server:        https://server.codeium.com

User:
  Name:              someuser
  Email:             someuser@example.com
  User ID:           user-123

Account:
  Tier:              Devin Pro
  Plan:              Pro
  Enterprise:        no
`)
	if !ok || user != "someuser@example.com" || plan != "Devin Pro" {
		t.Fatalf("user=%q plan=%q ok=%v", user, plan, ok)
	}
	if _, _, ok := parseDevinStatus("Not logged in."); ok {
		t.Fatal("a signed-out status parsed as signed in")
	}
	// no email field: the handle still identifies the account
	user, _, ok = parseDevinStatus("Logged in (via Devin).\n\nUser:\n  Name:  handle\n")
	if !ok || user != "handle" {
		t.Fatalf("handle fallback: %q %v", user, ok)
	}
}

func TestParseDevinModels(t *testing.T) {
	families := parseDevinModels([]byte(`{"families":[
		{"family_label":"SWE-2","family_uid":"swe-2","slug":"swe-2","aliases":["swe"],
		 "variants":[{"model_uid":"swe-2-max","label":"SWE-2 Max"},{"model_uid":"swe-2-min","label":"SWE-2 Min"}]},
		{"family_label":"Claude Opus 5.5","family_uid":"claude-opus-5-5","slug":"claude-opus-5.5","aliases":["opus"],
		 "variants":[{"model_uid":"claude-opus-5-5-high","label":"Claude Opus 5.5 High"}]}
	]}`))
	if len(families) != 2 {
		t.Fatalf("families: %v", families)
	}
	f := families[0]
	if f.UID != "swe-2" || f.Label != "SWE-2" || len(f.Models) != 2 || f.Models[0].ID != "swe-2-max" {
		t.Fatalf("family: %+v", f)
	}
	flat := devinModelsFlatten(families)
	if len(flat) != 5 || flat[0].ID != "swe-2" || flat[1].ID != "swe-2-max" || flat[4].ID != "claude-opus-5-5-high" {
		t.Fatalf("flat: %v", flat)
	}
	for _, m := range flat {
		if m.Provider != "devin" {
			t.Fatalf("provider: %v", m)
		}
	}
	if parseDevinModels([]byte("not json")) != nil || parseDevinModels([]byte(`{"families":[]}`)) != nil {
		t.Fatal("bad payloads must not parse")
	}
}

func TestDevinCredentialsPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG paths are unix")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	got := DevinCredentialsPath()
	if got != filepath.Join(home, "data", "devin", "credentials.toml") {
		t.Fatalf("path: %q", got)
	}
	t.Setenv("XDG_DATA_HOME", "")
	if got := DevinCredentialsPath(); got != filepath.Join(home, ".local", "share", "devin", "credentials.toml") {
		t.Fatalf("default path: %q", got)
	}
	_ = os.Getenv("HOME")
}
