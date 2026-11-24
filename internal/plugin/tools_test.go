package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// A plugin's tool hook (testdata/tools): the host lists its tools, with
// zod arguments read as JSON Schema and a JSON Schema kept as it is, and
// leaves out a name an MCP client can't take and a tool with no execute.
// A tool runs with its arguments parsed as zod would (a default filled
// in, a wrong one refused), its answer as text, its error said, and one
// that runs past its time is ended. Which plugins give tools is noted for
// the Plugin tools plugin, and counted only while they are on.
func TestPluginTools(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/tools")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	pts, err := Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].Spec != abs {
		t.Fatalf("tools = %+v", pts)
	}
	by := map[string]Tool{}
	var names []string
	for _, x := range pts[0].Tools {
		by[x.Name] = x
		names = append(names, x.Name)
	}
	if strings.Join(names, " ") != "shout add sleep fail" {
		t.Fatalf("tool names = %v", names)
	}
	if by["shout"].Description != "Says the text back in capitals." {
		t.Errorf("shout's description = %q", by["shout"].Description)
	}
	var shout map[string]any
	json.Unmarshal(by["shout"].Schema, &shout)
	want := `{"properties":{"style":{"enum":["plain","bang"],"type":"string"},"text":{"description":"What to say","type":"string"},"times":{"default":1,"description":"How many times","type":"number"}},"required":["text"],"type":"object"}`
	if got := jsonOf(shout); got != want {
		t.Errorf("shout's schema\n got %s\nwant %s", got, want)
	}
	if got := string(by["add"].Schema); !strings.Contains(got, `"required":["xs"]`) || !strings.Contains(got, `"items":{"type":"number"}`) {
		t.Errorf("add's JSON Schema not kept: %s", got)
	}

	run := func(name, args string) (string, error) {
		return RunTool(ctx, abs, name, json.RawMessage(args), "claude")
	}
	if got, err := run("shout", `{"text":"hi","style":"bang"}`); err != nil || got != "HI! (for claude)" {
		t.Errorf("shout = %q, %v", got, err)
	}
	if got, err := run("shout", `{"text":"hi","times":2}`); err != nil || got != "HI HI (for claude)" {
		t.Errorf("shout twice = %q, %v", got, err)
	}
	if _, err := run("shout", `{"times":2}`); err == nil || !strings.Contains(err.Error(), "text: expected string") {
		t.Errorf("shout with no text: %v", err)
	}
	if got, err := run("add", `{"xs":[1,2,3.5]}`); err != nil || got != `{"sum":6.5}` {
		t.Errorf("add = %q, %v", got, err)
	}
	if _, err := run("fail", `{}`); err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Errorf("fail: %v", err)
	}
	if _, err := run("noexec", `{}`); err == nil {
		t.Error("a tool with no execute ran")
	}
	old := ToolTimeout
	ToolTimeout = 300 * time.Millisecond
	t0 := time.Now()
	_, err = run("sleep", `{"ms":30000}`)
	ToolTimeout = old
	// the host answers at 300ms; Go's own backstop (ToolTimeout+5s) only
	// at 5.3s, so 4s leaves room on both sides and tells the two apart
	if err == nil || !strings.Contains(err.Error(), "sleep took longer than 300ms") || time.Since(t0) > 4*time.Second {
		t.Errorf("a tool past its time: %v after %s", err, time.Since(t0))
	}
	if got, err := run("shout", `{"text":"still"}`); err != nil || !strings.HasPrefix(got, "STILL") {
		t.Errorf("the host after a tool ran out of time: %q, %v", got, err)
	}

	// noted, and counted while on
	b, err := os.ReadFile(toolsPath())
	if err != nil || !strings.Contains(string(b), `"shout"`) {
		t.Fatalf("noted: %s, %v", b, err)
	}
	if n, err := ToolsOn(); n != 1 || err != nil {
		t.Errorf("ToolsOn = %d, %v", n, err)
	}
	if err := SetOff(abs, true); err != nil {
		t.Fatal(err)
	}
	if n, err := ToolsOn(); n != 0 || err != nil {
		t.Errorf("ToolsOn with the plugin off = %d, %v", n, err)
	}
	if got := ToolNames()[abs]; strings.Join(got, " ") != "add fail shout sleep" {
		t.Errorf("ToolNames = %v", got)
	}
	// a note that can't be read is not known
	if err := os.WriteFile(toolsPath(), []byte("{half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ToolsOn(); err == nil {
		t.Error("a corrupt note counted as no tools")
	}
}

// The library's sync after a host noted its tools writes agents' settings
// in HOME: Settle waits for it, so a test's folder isn't written in while
// it goes (#7611, "directory not empty" at TempDir's cleanup).
func TestSettleWaitsForToolsNoted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(dir, "synced")
	was := ToolsNoted
	t.Cleanup(func() { ToolsNoted = was })
	ToolsNoted = func() {
		time.Sleep(500 * time.Millisecond)
		os.WriteFile(late, []byte("x"), 0o644)
	}
	noteTools([]Loaded{{Spec: "/p/tools", Tools: []Tool{{Name: "shout"}}}})
	if _, err := os.Stat(toolsPath()); err != nil {
		t.Fatalf("the tools weren't noted, so ToolsNoted never ran: %v", err)
	}
	Settle()
	if _, err := os.Stat(late); err != nil {
		t.Fatalf("Settle returned before the sync ToolsNoted started was done: %v", err)
	}
}
