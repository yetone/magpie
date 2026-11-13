package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A plugin subscription had no "Import accounts from a file…" (marchen_cc
// on X: WorkBuddy's): a plugin that declares auth.import is listed as
// importable with its words, and each account its import gives is kept as
// a sign-in is, one already signed in updated rather than added twice.
func TestPluginImportFiles(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, name := range []string{"importer", "apiprompts"} {
		abs, _ := filepath.Abs("testdata/" + name)
		if _, err := Add(ctx, abs); err != nil {
			t.Fatal(err)
		}
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var imp, other *Provider
	for i := range ps {
		switch ps[i].ID {
		case "importer":
			imp = &ps[i]
		case "apiprompts":
			other = &ps[i]
		}
	}
	if imp == nil || other == nil {
		t.Fatalf("providers = %+v", ps)
	}
	if !imp.Importable || imp.ImportFrom != "Choose the Importer app's accounts file" || imp.ImportFroms["zh"] != "选择 Importer 的账号文件" {
		t.Errorf("importer = importable %v, from %q, froms %v", imp.Importable, imp.ImportFrom, imp.ImportFroms)
	}
	if other.Importable {
		t.Error("a plugin with no auth.import is listed as importable")
	}

	got, err := ImportFiles(ctx, "importer", []string{"ann:r-ann\nbad:x", "bob:r-bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Status != "added" || got[0].User != "ann" || got[1].Status != "failed" || got[1].Error != "Importer refused it" || got[2].Status != "added" || got[2].User != "bob" {
		t.Fatalf("first import = %+v", got)
	}
	if got[0].Provider != "importer" || got[0].Account == "" || got[0].Account == got[2].Account {
		t.Errorf("saved as %+v", got)
	}
	again, err := ImportFiles(ctx, "importer", []string{"ann:r-ann2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Status != "updated" || again[0].Account != got[0].Account {
		t.Errorf("ann imported again = %+v, want updated at %s", again, got[0].Account)
	}
	b, err := os.ReadFile(AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "r-ann2") || strings.Contains(string(b), `"r-ann"`) || !strings.Contains(string(b), "r-bob") {
		t.Errorf("auth.json = %s", b)
	}
	if _, err := ImportFiles(ctx, "apiprompts", []string{"x"}); err == nil {
		t.Error("a plugin with no auth.import took a file")
	}
}
