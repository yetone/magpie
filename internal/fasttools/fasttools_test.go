package fasttools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// tree writes files (name → text) under a new folder.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, s := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func args(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustCall(t *testing.T, name string, v any) string {
	t.Helper()
	out, err := call(name, args(t, v))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func git(t *testing.T, dir string, a ...string) {
	t.Helper()
	cmd := proc.CommandContext(context.Background(), "git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, a...)...)
	cmd.Env = append(gitEnv(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", a, err, out)
	}
}

// One batch_search answers every query in it, each under its own heading:
// text with context, regex without case, a glob over paths and one over
// names, and files in node_modules aren't searched.
func TestBatchSearch(t *testing.T) {
	dir := tree(t, map[string]string{
		"a/one.go":               "package a\n\nfunc Alpha() {}\n\n// beta lives here\nfunc Beta() {}\n",
		"b/two.ts":               "export function gamma() {}\nconst BETA = 1\n",
		"docs/README.md":         "# beta\n",
		"node_modules/x/beta.js": "beta beta beta\n",
		"bin.dat":                "beta\x00\x01",
		"c/crlf.go":              "package c\r\nfunc Bc() {}\r\n",
	})
	out := mustCall(t, "batch_search", map[string]any{"cwd": dir, "queries": []map[string]any{
		{"kind": "text", "pattern": "beta lives", "context": 1},
		{"kind": "regex", "pattern": `^func B\w+\(\) \{\}$`, "ignoreCase": true},
		{"kind": "glob", "pattern": "**/*.ts"},
		{"kind": "glob", "pattern": "*.md"},
		{"kind": "text", "pattern": "beta", "ignoreCase": true, "glob": "*.ts"},
	}})
	for _, want := range []string{
		"## 1. text beta lives", "a/one.go:5: // beta lives here", "  a/one.go:4- ", "  a/one.go:6- func Beta() {}",
		"## 2. regex", "a/one.go:6: func Beta() {}", "c/crlf.go:2: func Bc() {}",
		"## 3. glob **/*.ts", "b/two.ts",
		"## 4. glob *.md", "docs/README.md",
		"## 5. text beta (*.ts)", "b/two.ts:2: const BETA = 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "node_modules") || strings.Contains(out, "bin.dat") {
		t.Errorf("searched node_modules or a binary:\n%s", out)
	}
	if strings.Contains(out, "a/one.go:3") {
		t.Errorf("the regex matched Alpha:\n%s", out)
	}
}

// git queries run read-only commands only: one that writes, runs a program
// or writes a file through a flag is turned away before git runs.
func TestBatchSearchGitReadOnly(t *testing.T) {
	dir := tree(t, map[string]string{"f.txt": "one\n"})
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "f.txt")
	git(t, dir, "commit", "-qm", "first")
	out := mustCall(t, "batch_search", map[string]any{"cwd": dir, "queries": []map[string]any{
		{"kind": "git", "pattern": "log --oneline"},
		{"kind": "git", "pattern": "push origin main"},
		{"kind": "git", "pattern": "branch -D main"},
		{"kind": "git", "pattern": "diff --output=" + filepath.Join(dir, "x")},
		{"kind": "git", "pattern": "log; touch y"},
		{"kind": "git", "pattern": "-c core.pager=touch log"},
	}})
	if !strings.Contains(out, "first") {
		t.Errorf("log didn't run:\n%s", out)
	}
	parts := strings.Split(out, "## ")[2:]
	for i, p := range parts {
		if strings.Count(p, "\n") > 3 || !strings.Contains(strings.ToLower(p), "error") {
			t.Errorf("query %d wasn't turned away:\n%s", i+2, p)
		}
	}
	for _, f := range []string{"x", "y"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s was written", f)
		}
	}
	git(t, dir, "rev-parse", "--verify", "main") // the branch is still there
}

// A short file comes whole, a long one as its outline, and ranges as asked,
// numbered.
func TestReadSlices(t *testing.T) {
	var long strings.Builder
	long.WriteString("package p\n\nimport \"fmt\"\n\nconst (\n\tA = 1\n\tB = 2\n)\n\n")
	for i := range 300 {
		long.WriteString("// filler\n")
		if i == 150 {
			long.WriteString("func Middle(x int) error {\n\treturn fmt.Errorf(\"x\")\n}\n")
		}
	}
	long.WriteString("type T struct{}\n")
	dir := tree(t, map[string]string{"short.txt": "a\nb\nc\n", "long.go": long.String()})
	out := mustCall(t, "read_slices", map[string]any{"cwd": dir, "files": []map[string]any{
		{"path": "short.txt"},
		{"path": "long.go"},
		{"path": "long.go", "ranges": [][]int{{6, 7}}},
		{"path": "gone.txt"},
	}})
	for _, want := range []string{
		"## short.txt (3 lines)\n1\ta\n2\tb\n3\tc",
		"outline:", "6:   A = 1", "7:   B = 2", ": func Middle(x int) error", ": type T struct",
		"lines 6-7:\n6\t\tA = 1\n7\t\tB = 2",
		"## gone.txt\nerror:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "filler") && !strings.Contains(out, "lines ") {
		t.Errorf("the long file came whole:\n%s", out)
	}
}

// The JS outliner lists declarations, not an if or a for that looks like a
// method.
func TestOutlineJS(t *testing.T) {
	lines := strings.Split("export function a() {\n  if (x) {\n  }\n  for (const y of z) {\n  }\n}\nclass B {\n  run(x) {\n  }\n}\nconst c = (x) => x\n", "\n")
	got := strings.Join(Outline("f.ts", lines), "\n")
	for _, want := range []string{"1: export function a()", "7: class B", "8:   run(x)", "11: const c = (x) => x"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "if (x)") || strings.Contains(got, "for (") {
		t.Errorf("a keyword in the outline:\n%s", got)
	}
}

// apply_edits writes nothing when any edit can't be made, and says why, with
// where the text nearly is.
func TestApplyEditsAllOrNothing(t *testing.T) {
	dir := tree(t, map[string]string{"a.go": "package a\n\nfunc A() {\n\treturn\n}\n", "b.go": "package b\nvar x = 1\nvar x = 1\n"})
	out := mustCall(t, "apply_edits", map[string]any{"cwd": dir, "edits": []map[string]any{
		{"path": "a.go", "old": "func A()", "new": "func AA()"},
		{"path": "a.go", "old": "    return\n}", "new": "x"},
		{"path": "b.go", "old": "var x = 1", "new": "var y = 2"},
		{"path": "c.go", "old": "x", "new": "y"},
	}})
	for _, want := range []string{"nothing written: 3 of 4 edits can't be made", "at line 4 with other spacing", "2 times, at lines 2, 3", "c.go: edit 4: the file isn't there"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if s := read(t, filepath.Join(dir, "a.go")); strings.Contains(s, "AA") {
		t.Errorf("a.go was written: %q", s)
	}
}

// Every edit is made: several in one file, a new file, a CRLF file kept
// CRLF, replaceAll, and a link's file written with the link left one.
func TestApplyEdits(t *testing.T) {
	dir := tree(t, map[string]string{"a.go": "one\ntwo\nthree\n", "w.txt": "x\r\ny\r\n", "r.txt": "k k k\n", "real/t.txt": "old\n"})
	if err := os.Symlink(filepath.Join(dir, "real", "t.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	os.Chmod(filepath.Join(dir, "a.go"), 0o755)
	out := mustCall(t, "apply_edits", map[string]any{"cwd": dir, "edits": []map[string]any{
		{"path": "a.go", "old": "one\n", "new": "uno\n"},
		{"path": "a.go", "old": "three", "new": "tres\nquattro"},
		{"path": "new/n.txt", "content": "fresh\n"},
		{"path": "w.txt", "old": "x\ny", "new": "x\nz"},
		{"path": "r.txt", "old": "k", "new": "q", "replaceAll": true},
		{"path": "link.txt", "old": "old", "new": "new"},
	}})
	checks := map[string]string{"a.go": "uno\ntwo\ntres\nquattro\n", "new/n.txt": "fresh\n", "w.txt": "x\r\nz\r\n", "r.txt": "q q q\n", "real/t.txt": "new\n"}
	for f, want := range checks {
		if got := read(t, filepath.Join(dir, filepath.FromSlash(f))); got != want {
			t.Errorf("%s = %q, want %q\n%s", f, got, want, out)
		}
	}
	if fi, err := os.Lstat(filepath.Join(dir, "link.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link.txt isn't a link any more: %v %v", fi, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dir, "a.go")); fi.Mode().Perm() != 0o755 {
			t.Errorf("a.go's mode: %v", fi.Mode())
		}
	}
	if !strings.Contains(out, "new/n.txt: created (1 line written)") || !strings.Contains(out, "a.go: edited (line 1; line 3 +1 line)") {
		t.Errorf("result:\n%s", out)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("a temp file left: %s", e.Name())
		}
	}
}

// A dry run writes nothing; verify runs after a write and says how it went.
func TestApplyEditsDryRunVerify(t *testing.T) {
	dir := tree(t, map[string]string{"a.txt": "x\n"})
	out := mustCall(t, "apply_edits", map[string]any{"cwd": dir, "dryRun": true, "verify": "exit 1", "edits": []map[string]any{{"path": "a.txt", "old": "x", "new": "y"}}})
	if read(t, filepath.Join(dir, "a.txt")) != "x\n" || !strings.Contains(out, "dry run") || strings.Contains(out, "verify") {
		t.Fatalf("dry run:\n%s", out)
	}
	out = mustCall(t, "apply_edits", map[string]any{"cwd": dir, "verify": "echo broken-build; exit 3", "edits": []map[string]any{{"path": "a.txt", "old": "x", "new": "y"}}})
	if read(t, filepath.Join(dir, "a.txt")) != "y\n" || !strings.Contains(out, "FAIL verify: exit 3") || !strings.Contains(out, "broken-build") {
		t.Fatalf("verify:\n%s", out)
	}
}

// run_checks runs its commands side by side; a failure shows the end of its
// output, a pass shows none.
func TestRunChecks(t *testing.T) {
	dir := t.TempDir()
	began := time.Now()
	out := mustCall(t, "run_checks", map[string]any{"cwd": dir, "commands": []any{
		map[string]any{"cmd": "echo quiet-pass", "name": "pass"},
		map[string]any{"cmd": "echo loud-failure; exit 2", "name": "fail"},
		"sleep 2",
		"sleep 2",
	}})
	took := time.Since(began)
	for _, want := range []string{"3 of 4 passed", "ok   pass", "FAIL fail: exit 2", "  loud-failure"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "quiet-pass") {
		t.Errorf("a pass showed its output:\n%s", out)
	}
	// two 2s sleeps one after the other take 4s; side by side, 2
	if took > 3500*time.Millisecond {
		t.Errorf("the checks ran one after another: %v", took)
	}
}

// A check past its timeout is ended with everything it started, not only
// its shell: a child it left running would go on after the call.
func TestRunChecksTimeoutEndsTheGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("proc ends the tree on Windows")
	}
	dir := t.TempDir()
	out := mustCall(t, "run_checks", map[string]any{"cwd": dir, "commands": []map[string]any{{"cmd": "sleep 60 & echo $! > child.pid; wait", "timeout": 1}}})
	if !strings.Contains(out, "timed out") {
		t.Fatalf("no timeout:\n%s", out)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(read(t, filepath.Join(dir, "child.pid"))))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := os.FindProcess(pid)
	t.Cleanup(func() { p.Kill() })
	// the child is ended with its group; it is gone once the system has
	// reaped it
	deadline := time.Now().Add(10 * time.Second)
	for p.Signal(syscall.Signal(0)) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the check's child %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A failure's tail is its last lines, colours and rewritten progress taken
// out.
func TestTailOf(t *testing.T) {
	var b strings.Builder
	for i := range 100 {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%3))
		b.WriteString("\n")
	}
	b.WriteString("\x1b[31mred error\x1b[0m\n10%\r50%\r100%\n")
	got := tailOf(b.String())
	if !strings.HasPrefix(got, "… 62 lines before\n") || !strings.Contains(got, "red error\n100%") || strings.Contains(got, "\x1b") || strings.Contains(got, "50%") {
		t.Fatalf("tail:\n%q", got)
	}
}

// The SessionStart hook names the workspace agy says, never its own folder
// (the plugin's), and says nothing at all when it can't tell.
func TestSessionStart(t *testing.T) {
	dir := tree(t, map[string]string{"go.mod": "module example.com/w\n\ngo 1.26\n", "package.json": `{"name":"w","scripts":{"test":"x","build":"y"}}`, "pnpm-lock.yaml": "", "AGENTS.md": "", "cmd/main.go": ""})
	git(t, dir, "init", "-q", "-b", "trunk")
	hook := func(in string) map[string]any {
		t.Helper()
		var out bytes.Buffer
		SessionStart(strings.NewReader(in), &out)
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatalf("%v: %q", err, out.String())
		}
		return m
	}
	text := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		var v struct {
			InjectSteps []struct {
				SystemMessage struct {
					SystemMessage string `json:"systemMessage"`
				} `json:"systemMessage"`
			} `json:"injectSteps"`
		}
		json.Unmarshal(b, &v)
		if len(v.InjectSteps) != 1 {
			return ""
		}
		return v.InjectSteps[0].SystemMessage.SystemMessage
	}
	uri := "file://" + filepath.ToSlash(dir)
	if runtime.GOOS == "windows" {
		uri = "file:///" + filepath.ToSlash(dir)
	}
	for _, in := range []string{
		string(args(t, map[string]any{"workspacePaths": []string{"/not/there", dir}, "conversationId": "c"})),
		string(args(t, map[string]any{"cwd": dir})),
		string(args(t, map[string]any{"workspacePaths": []any{map[string]any{"uri": uri}}})),
	} {
		s := text(hook(in))
		for _, want := range []string{"Workspace: " + dir + " (git: ", "trunk", "Go module example.com/w", "Node package w (pnpm), scripts: build test", "has AGENTS.md", "Top level: cmd/ AGENTS.md", "batch_search"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: no %q in\n%s", in, want, s)
			}
		}
	}
	// the hook runs in the plugin's folder: that is never the workspace
	t.Chdir(dir)
	for _, in := range []string{"", "not json", `{"workspacePaths":["relative"]}`, `{"cwd":""}`} {
		if m := hook(in); len(m) != 0 {
			t.Errorf("%q: %v", in, m)
		}
	}
}

// A snapshot stays under its cap however much is at the top.
func TestSnapshotCap(t *testing.T) {
	files := map[string]string{}
	for i := range 60 {
		files[strings.Repeat("名", 30)+string(rune('a'+i%26))+strings.Repeat("x", i)] = ""
	}
	s := Snapshot(tree(t, files))
	if len(s) > snapshotCap+len("…") || !strings.HasSuffix(s, "…") && !strings.Contains(s, "in all") {
		t.Fatalf("%d bytes:\n%s", len(s), s)
	}
	if !json.Valid(args(t, s)) || !strings.Contains(s, "Workspace: ") {
		t.Fatalf("%q", s)
	}
}

// The MCP server answers initialize, lists its tools and calls them; a
// notification gets no answer, an unknown method an error.
func TestServe(t *testing.T) {
	dir := tree(t, map[string]string{"x.txt": "needle\n"})
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		string(args(t, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "batch_search", "arguments": map[string]any{"cwd": dir, "queries": []map[string]any{{"kind": "text", "pattern": "needle"}}}}})),
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	sc := bufio.NewScanner(&out)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(m["id"])
		got[string(b)] = m
	}
	if len(got) != 5 {
		t.Fatalf("%d answers:\n%s", len(got), out.String())
	}
	if r := got["1"]["result"].(map[string]any); r["serverInfo"].(map[string]any)["name"] != ServerName || r["instructions"] == "" {
		t.Errorf("initialize: %v", r)
	}
	var names []string
	for _, tl := range got["2"]["result"].(map[string]any)["tools"].([]any) {
		m := tl.(map[string]any)
		if m["inputSchema"] == nil || m["description"] == "" {
			t.Errorf("tool %v has no schema or description", m["name"])
		}
		names = append(names, m["name"].(string))
	}
	if strings.Join(names, " ") != strings.Join(toolNames, " ") {
		t.Errorf("tools %v, want %v", names, toolNames)
	}
	res3 := got["3"]["result"].(map[string]any)
	if res3["isError"] == true || !strings.Contains(res3["content"].([]any)[0].(map[string]any)["text"].(string), "x.txt:1: needle") {
		t.Errorf("tools/call: %v", res3)
	}
	if got["4"]["result"].(map[string]any)["isError"] != true {
		t.Errorf("an unknown tool: %v", got["4"])
	}
	if got["5"]["error"] == nil {
		t.Errorf("an unknown method: %v", got["5"])
	}
}

// What a tool gives back is cut to what agy shows inline, saying so.
func TestCapText(t *testing.T) {
	s := strings.Repeat("一行\n", 5000)
	got := capText(s)
	if len(got) > outCap || !strings.Contains(got, "more bytes left out") || !strings.HasPrefix(got, "一行\n") {
		t.Fatalf("%d bytes: …%q", len(got), got[max(0, len(got)-200):])
	}
}

// In the plugin's folder, where agy may start its server, a call with no
// cwd is turned away rather than run there.
func TestRootNotThePluginFolder(t *testing.T) {
	d := filepath.Join(t.TempDir(), "plugins", PluginName)
	os.MkdirAll(d, 0o755)
	t.Chdir(d)
	for _, cwd := range []string{"", "sub"} {
		if _, err := root(cwd); err == nil || !strings.Contains(err.Error(), "give cwd") {
			t.Errorf("%q: %v", cwd, err)
		}
	}
	if _, err := call("read_slices", args(t, map[string]any{"files": []map[string]any{{"path": "plugin.json"}}})); err == nil {
		t.Error("read the plugin's folder")
	}
}

// The plugin is written, found, kept and taken out; one of the same name
// that magpie didn't write is never touched.
func TestPlugin(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "app", "magpie")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	if On(home) {
		t.Fatal("on before written")
	}
	if err := Keep(home, exe); err != nil || On(home) {
		t.Fatalf("Keep turned it on: %v", err)
	}
	if err := Write(home, exe); err != nil {
		t.Fatal(err)
	}
	if !On(home) {
		t.Fatal("not on after Write")
	}
	var mcp struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Tools   map[string]struct {
				Eager bool `json:"eager"`
			} `json:"tools"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(Dir(home), "mcp_config.json"))), &mcp); err != nil {
		t.Fatal(err)
	}
	s := mcp.MCPServers[ServerName]
	if s.Command != exe || strings.Join(s.Args, " ") != "fast-tools mcp" || len(s.Tools) != len(toolNames) || !s.Tools["batch_search"].Eager {
		t.Fatalf("mcp_config.json: %+v", mcp)
	}
	if h := read(t, filepath.Join(Dir(home), "hooks.json")); !strings.Contains(h, `"SessionStart"`) || !strings.Contains(h, "fast-tools session-start") {
		t.Fatalf("hooks.json: %s", h)
	}
	if r := read(t, filepath.Join(Dir(home), "rules", "AGENTS.md")); !strings.Contains(r, "batch_search") {
		t.Fatalf("rules: %s", r)
	}

	// another magpie that is there is left; a gone one is replaced
	other := filepath.Join(home, "other", "magpie")
	os.MkdirAll(filepath.Dir(other), 0o755)
	os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755)
	starts := func() string {
		var c struct {
			MCPServers map[string]struct {
				Command string `json:"command"`
			} `json:"mcpServers"`
		}
		json.Unmarshal([]byte(read(t, filepath.Join(Dir(home), "mcp_config.json"))), &c)
		return c.MCPServers[ServerName].Command
	}
	if err := Keep(home, other); err != nil || starts() != exe {
		t.Fatalf("Keep took it from a magpie still there: %v %s", err, starts())
	}
	os.Remove(exe)
	if err := Keep(home, other); err != nil || starts() != other {
		t.Fatalf("Keep left it starting a gone magpie: %v %s", err, starts())
	}
	// one an older magpie wrote is written again
	mk := filepath.Join(Dir(home), markFile)
	os.WriteFile(mk, []byte(`{"fastTools":0}`), 0o644)
	if On(home) {
		t.Fatal("a mark of 0 counted as on")
	}
	os.WriteFile(mk, []byte(`{"fastTools":-1}`), 0o644)
	if On(home) {
		t.Fatal("a negative mark counted as on")
	}
	os.WriteFile(mk, []byte(`{"fastTools":`+fmt.Sprint(rulesVersion)+`}`), 0o644)
	os.WriteFile(filepath.Join(Dir(home), "rules", "AGENTS.md"), []byte("old rules"), 0o644)
	if err := Keep(home, other); err != nil || read(t, filepath.Join(Dir(home), "rules", "AGENTS.md")) != "old rules" {
		t.Fatalf("Keep wrote a current plugin again: %v", err)
	}
	// a newer magpie, with newer rules, finds this one's
	rulesVersion++
	defer func() { rulesVersion-- }()
	if err := Keep(home, other); err != nil || read(t, filepath.Join(Dir(home), "rules", "AGENTS.md")) != rules || written(home) != rulesVersion {
		t.Fatalf("Keep left an older plugin: %v", err)
	}
	// a plugin.json of only the fields Antigravity knows; the mark is beside it
	var pj map[string]any
	json.Unmarshal([]byte(read(t, filepath.Join(Dir(home), "plugin.json"))), &pj)
	for k := range pj {
		if !slices.Contains([]string{"name", "displayName", "version", "description", "logo"}, k) {
			t.Errorf("plugin.json has %q", k)
		}
	}
	if _, err := os.Stat(filepath.Join(Dir(home), pj["logo"].(string))); err != nil {
		t.Errorf("no logo: %v", err)
	}
	// a folder left half-written, the mark and no plugin.json: not on, and
	// written over or taken out
	os.Remove(filepath.Join(Dir(home), "plugin.json"))
	if On(home) {
		t.Fatal("on with no plugin.json")
	}
	if err := Write(home, other); err != nil || !On(home) {
		t.Fatalf("Write over a half-written one: %v", err)
	}
	if err := Remove(home); err != nil || On(home) {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(Dir(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder is still there: %v", err)
	}
	if err := Remove(home); err != nil {
		t.Fatalf("Remove with nothing there: %v", err)
	}

	// the user's own plugin of that name
	os.MkdirAll(Dir(home), 0o755)
	pjPath := filepath.Join(Dir(home), "plugin.json")
	own := `{"name":"fast-tools","version":"3.0.0"}`
	os.WriteFile(pjPath, []byte(own), 0o644)
	if err := Write(home, exe); !errors.Is(err, ErrNotOurs) {
		t.Errorf("Write over theirs: %v", err)
	}
	if err := Remove(home); !errors.Is(err, ErrNotOurs) {
		t.Errorf("Remove theirs: %v", err)
	}
	if err := Keep(home, other); err != nil {
		t.Errorf("Keep theirs: %v", err)
	}
	if read(t, pjPath) != own || On(home) {
		t.Errorf("theirs was touched: %s", read(t, pjPath))
	}
	if _, err := os.Stat(filepath.Join(Dir(home), "mcp_config.json")); err == nil {
		t.Error("mcp_config.json written into theirs")
	}
	// a folder of the user's own files, with no plugin.json yet
	os.Remove(pjPath)
	os.WriteFile(filepath.Join(Dir(home), "notes.md"), []byte("mine"), 0o644)
	if err := Remove(home); !errors.Is(err, ErrNotOurs) {
		t.Errorf("Remove a folder of theirs: %v", err)
	}
}

// The hook's command line is quoted for the shell agy runs it in.
func TestHookCommand(t *testing.T) {
	for _, c := range []struct{ exe, goos, want string }{
		{"/usr/local/bin/magpie", "darwin", "/usr/local/bin/magpie fast-tools session-start"},
		{"/Applications/magpie.app/Contents/MacOS/magpie", "darwin", "/Applications/magpie.app/Contents/MacOS/magpie fast-tools session-start"},
		{"/Users/a b/magpie", "linux", "'/Users/a b/magpie' fast-tools session-start"},
		{"/home/o'neil/magpie", "linux", `'/home/o'\''neil/magpie' fast-tools session-start`},
		{`C:\Program Files\magpie\magpie.exe`, "windows", `"C:\Program Files\magpie\magpie.exe" fast-tools session-start`},
	} {
		if got := hookCommandOn(c.exe, c.goos); got != c.want {
			t.Errorf("%s on %s: %s, want %s", c.exe, c.goos, got, c.want)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	// sh runs the quoted line as written
	dir := filepath.Join(t.TempDir(), "o'ne il")
	os.MkdirAll(dir, 0o755)
	exe := filepath.Join(dir, "fake")
	os.WriteFile(exe, []byte("#!/bin/sh\necho \"ran $*\"\n"), 0o755)
	out, err := proc.CommandContext(context.Background(), "/bin/sh", "-c", hookCommand(exe)).Output()
	if err != nil || strings.TrimSpace(string(out)) != "ran fast-tools session-start" {
		t.Fatalf("%q: %v", out, err)
	}
}

// `magpie fast-tools` with no or an unknown subcommand says how it is used.
func TestRunUsage(t *testing.T) {
	for _, a := range [][]string{nil, {"what"}} {
		if err := Run(a); err == nil || !strings.Contains(err.Error(), "usage: magpie fast-tools") {
			t.Errorf("%v: %v", a, err)
		}
	}
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := Run([]string{"--help"})
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	if err != nil || !strings.Contains(string(b), "session-start") {
		t.Errorf("--help: %v %q", err, b)
	}
}
