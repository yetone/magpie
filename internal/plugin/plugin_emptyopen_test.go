package plugin

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// unbrokefix on 𝕏 (Windows 11, magpie 0.1.4732): Explorer opened
// %USERPROFILE%\.config\magpie\plugin-work about 10s after every start of
// the plugin host, and again each minute after it was closed. A page
// plugin ran `magpie-bun -e <scan>` with no folder of its own (so in
// plugin-work), the scan called every installed plugin's factory, and one
// of them ran `cmd /d /s /c start "" ""`: start with no target opens the
// folder it runs in. The host and every Bun script started from it refuse
// a command that opens an empty target, on each route a program can be
// started by, and still open a real one.
func TestEmptyOpenIsRefusedOnEveryRoute(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/emptyopen/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	a, err := Authorize(ctx, "emptyopen", 0, nil, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(u.Query().Get("out")), &out); err != nil {
		t.Fatalf("%v: %s", err, u.Query().Get("out"))
	}
	var loaded bool
	_ = json.Unmarshal(out["loaded"], &loaded)
	if !loaded {
		t.Fatal("opens.js isn't loaded in the host")
	}
	var ran []string
	_ = json.Unmarshal(out["ran"], &ran)
	for _, route := range []string{"Bun.spawn", "Bun.spawn({cmd})", "Bun.spawnSync", "spawn", "spawnSync", "execFile", "execFileSync", "exec", "execSync"} {
		var got struct{ Refused, RealRefused int }
		if err := json.Unmarshal(out[route], &got); err != nil {
			t.Errorf("%s: no result: %v", route, err)
			continue
		}
		if got.Refused != 1 {
			t.Errorf("%s: an empty open was refused %d times, want once", route, got.Refused)
		}
		if got.RealRefused != 0 {
			t.Errorf("%s: a real target was refused", route)
		}
		tag := ""
		for _, r := range route {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
				tag += string(r)
			} else {
				tag += "_"
			}
		}
		if !slices.Contains(ran, tag) {
			t.Errorf("%s: a real target didn't open (ran %v)", route, ran)
		}
	}
	var child int
	_ = json.Unmarshal(out["child"], &child)
	if child != 1 {
		t.Errorf("a Bun script the plugin started refused %d empty opens, want 1 (-1: opens.js not preloaded there)", child)
	}
	if !slices.Contains(ran, "child") {
		t.Errorf("a Bun script the plugin started didn't open its real target (ran %v)", ran)
	}
	var wrong [][]string
	_ = json.Unmarshal(out["wrongShapes"], &wrong)
	for _, w := range wrong {
		t.Errorf("emptyOpen judged %q wrong", w)
	}
}
