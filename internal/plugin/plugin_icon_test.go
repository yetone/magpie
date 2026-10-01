package plugin

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Lemon on Discord: can a plugin give its provider its own icon, and say
// what its API key is asked as? The auth hook's icon, else package.json's
// magpie.icon, is passed on when it is an https URL or a data:image URI;
// an "api" method's placeholder is its key's hint, and its label the
// key's title unless that only says "API key".
func TestPluginOwnIconAndKeyHint(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/ownicon")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Provider{}
	for _, p := range ps {
		by[p.ID] = p
	}
	lemon, lime := by["lemon"], by["lime"]
	if !strings.HasPrefix(lemon.Icon, "data:image/svg+xml;base64,") {
		t.Errorf("lemon's icon = %.60q", lemon.Icon)
	}
	if lime.Icon != "https://ownicon.example/icon.png" {
		t.Errorf("lime's icon = %q, want package.json's", lime.Icon)
	}
	if len(lemon.Methods) != 2 || lemon.Methods[0] != (Method{Type: "api", Label: "Lemon API key (from lemon.example/keys)", Placeholder: "sk-lemon-…"}) || lemon.Methods[1].Placeholder != "" {
		t.Fatalf("lemon's methods = %+v", lemon.Methods)
	}
	if len(lime.Methods) != 1 || lime.Methods[0].Placeholder != "" {
		t.Fatalf("lime's methods = %+v", lime.Methods)
	}
	if got := lemon.Methods[0].KeyTitle("Lemon"); got != "Lemon API key (from lemon.example/keys)" {
		t.Errorf("lemon's key title = %q", got)
	}
	if got := lime.Methods[0].KeyTitle("Lime"); got != "Lime API key" {
		t.Errorf("lime's key title = %q", got)
	}
	if got := (Method{Type: "api"}).KeyTitle("Lime"); got != "Lime API key" {
		t.Errorf("no label's key title = %q", got)
	}
	// kept for when magpie has only just started
	for _, p := range Cached() {
		if p.ID == "lemon" && p.Icon != lemon.Icon {
			t.Errorf("cached icon = %.60q", p.Icon)
		}
	}
}
