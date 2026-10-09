package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A package that is only a command — magpie-x-search 1.2.0's own
// package.json, an MCP stdio server with a bin and no main, exports or
// index file (#1327) — isn't added as a plugin: Add says what it is and
// where it goes instead. One on the list already (added before) says so
// when the host loads it, without a stack.
func TestCommandOnlyPackageIsNoPlugin(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/notplugin/magpie-x-search")
	_, err := Add(ctx, abs)
	if err == nil || !strings.Contains(err.Error(), "magpie-x-search isn't an OpenCode or magpie plugin") || !strings.Contains(err.Error(), "Library → MCP servers") {
		t.Fatalf("Add = %v", err)
	}
	if l := Load().Plugins; len(l) != 0 {
		t.Fatalf("listed: %+v", l)
	}

	// added by an older magpie: the host says the same, plainly
	if err := save(List{Plugins: []Entry{{Spec: abs}}}); err != nil {
		t.Fatal(err)
	}
	Restart()
	ps, err := Plugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || !strings.Contains(ps[0].Error, "magpie-x-search isn't an OpenCode or magpie plugin") || !strings.Contains(ps[0].Error, "MCP servers") || strings.Contains(ps[0].Error, "    at ") {
		t.Fatalf("loaded: %+v", ps)
	}
}

// What a package that names no file still loads stays a plugin: an index
// file, main or exports; so do pi's packages and middleware, and a file.
func TestNotPluginKeepsPlugins(t *testing.T) {
	sandbox(t)
	pkg := func(json string, files ...string) string {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, "package.json"), []byte(json), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(d, f), []byte("export default {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	for name, d := range map[string]string{
		"index.mjs":  pkg(`{"name":"a","bin":{"a":"cli.js"}}`, "index.mjs"),
		"main":       pkg(`{"name":"b","main":"dist/x.js"}`),
		"exports":    pkg(`{"name":"c","exports":{"./server":"./s.js"}}`),
		"middleware": pkg(`{"name":"d","magpie":{"middleware":"d.middleware.js"}}`),
		"pi":         pkg(`{"name":"e","pi":{"extensions":["./x.ts"]},"peerDependencies":{"@earendil-works/pi-coding-agent":"*"}}`),
	} {
		if err := notPlugin(d); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if abs, _ := filepath.Abs("testdata/fake/index.js"); notPlugin(abs) != nil {
		t.Error("a file")
	}
}
