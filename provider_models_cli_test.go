package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// magpie provider models <id> +m adds a model to those exposed and -m takes
// one out, keeping the rest; the whole-list form still replaces the list,
// and says it did and how many there were before (MOMO on Discord: 35
// exposed became 8 with nothing said).
func TestProviderModelsAddsRemovesAndSaysWhatItReplaced(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil
	if err := provider.Save(provider.Provider{ID: "og", Name: "OG", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("og")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.LivePath("og"), []byte(`{"models":[{"id":"m1"},{"id":"m2"},{"id":"m3"},{"id":"deepseek-flash"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	run := func(args ...string) (string, error) {
		return stdoutOf(t, func() error { return providerCmd(append([]string{"provider", "models", "og"}, args...)) })
	}
	picks := func() []string { return mustFind(t, "og").Models }

	out, err := run("+deepseek-flash")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"m1", "m2", "m3", "deepseek-flash"}; !slices.Equal(picks(), want) {
		t.Fatalf("+deepseek-flash left %v; want %v", picks(), want)
	}
	if !strings.Contains(out, "3 → 4") || !strings.Contains(out, "deepseek-flash") {
		t.Errorf("+ said %q; want 3 → 4 and the model added", out)
	}
	// its provider/model id is taken too, and -m takes one out
	if _, err := run("-og/m2", "+m2"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("-m2"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"m1", "m3", "deepseek-flash"}; !slices.Equal(picks(), want) {
		t.Fatalf("-m2 left %v; want %v", picks(), want)
	}
	// taking out what isn't exposed, mixing the forms, or leaving none
	// is an error that changes nothing
	for _, args := range [][]string{{"-nope"}, {"m1", "+m2"}, {"-m1", "-m3", "-deepseek-flash"}} {
		if _, err := run(args...); err == nil {
			t.Errorf("%v was taken", args)
		}
		if want := []string{"m1", "m3", "deepseek-flash"}; !slices.Equal(picks(), want) {
			t.Fatalf("%v changed the picks to %v", args, picks())
		}
	}
	// the whole list replaces, as scripts already use it, and says so
	out, err = run("m1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(picks(), []string{"m1"}) {
		t.Fatalf("replace left %v", picks())
	}
	if !strings.Contains(out, "replaced: 3 → 1") || !strings.Contains(out, "+<model>") {
		t.Errorf("replace said %q; want 3 → 1 and how to add one instead", out)
	}
	// all gives the default back
	if _, err := run("all"); err != nil || picks() != nil {
		t.Fatalf("all left %v, %v", picks(), err)
	}
}

// magpie models and magpie provider <id> name the models a provider's list
// has that it doesn't expose, and why, so a group naming one ("no member
// ready (not served)") can be put right (MOMO on Discord: 35 exposed of 37,
// opencode-go/deepseek-flash not in magpie models).
func TestModelsSayWhatIsNotExposed(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil
	if err := provider.Save(provider.Provider{ID: "og", Name: "OG", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("og")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.LivePath("og"), []byte(`{"models":[{"id":"m1"},{"id":"m2"},{"id":"deepseek-flash"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for name, f := range map[string]func() error{
		"models":   func() error { return models(nil) },
		"provider": func() error { return providerCmd([]string{"provider", "og"}) },
	} {
		out, err := stdoutOf(t, f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "deepseek-flash") || !strings.Contains(out, "not picked") || !strings.Contains(out, "magpie provider models og +<model>") {
			t.Errorf("%s said %q; want deepseek-flash named as not picked, and how to expose it", name, out)
		}
	}
	// with nothing left out, nothing is said
	if err := provider.Save(provider.Provider{ID: "og", Name: "OG", Key: "k", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if out, _ := stdoutOf(t, func() error { return models(nil) }); strings.Contains(out, "not exposed") {
		t.Errorf("with every model exposed, models said %q", out)
	}
}

// A group's member its provider lists but doesn't expose says so, and how
// to expose it, in magpie groups and magpie group show <id>.
func TestGroupMemberNotExposedSaysHow(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil
	if err := provider.Save(provider.Provider{ID: "og", Name: "OG", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("og")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.LivePath("og"), []byte(`{"models":[{"id":"m1"},{"id":"deepseek-flash"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.SaveGroup(provider.Group{ID: "flash", Name: "Flash", Members: []string{"og/deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func() error{
		"groups":      groups,
		"group flash": func() error { return groupCmd([]string{"group", "show", "flash"}) },
	} {
		out, err := stdoutOf(t, f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "not exposed · magpie provider models og +deepseek-flash") {
			t.Errorf("%s said %q; want how to expose og/deepseek-flash", name, out)
		}
	}
}
