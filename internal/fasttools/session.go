package fasttools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// snapshotCap is the most a snapshot says: it is in every turn's prompt
// from then on.
const snapshotCap = 1500

// SessionStart is the SessionStart hook: it reads the hook's input from in
// and writes what a new conversation is told of its workspace to out — its
// folder, its git state, what kind of project it is and what is at its
// top — so the first turns aren't spent finding out. Anything it can't
// read is left out; with nothing to say it writes {}, which changes
// nothing, so a hook that fails never stops a conversation.
func SessionStart(in io.Reader, out io.Writer) {
	b, _ := io.ReadAll(io.LimitReader(in, 1<<20))
	dir := workspaceOf(b)
	text := ""
	if dir != "" {
		text = Snapshot(dir)
	}
	if text == "" {
		io.WriteString(out, "{}\n")
		return
	}
	json.NewEncoder(out).Encode(map[string]any{
		"injectSteps": []any{map[string]any{"systemMessage": map[string]any{"systemMessage": text}}},
	})
}

// workspaceOf is the folder a SessionStart input names: its first
// workspace, else its cwd. The hook's own working folder is the plugin's,
// so it is never the workspace.
func workspaceOf(b []byte) string {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"workspacePaths", "workspace_paths", "workspaceRoots", "workspace_roots", "workspaces"} {
		if l, ok := m[k].([]any); ok {
			for _, v := range l {
				if d := asDir(v); d != "" {
					return d
				}
			}
		}
	}
	for _, k := range []string{"cwd", "workspacePath", "workspace_path", "workspaceRoot", "workspace_root"} {
		if d := asDir(m[k]); d != "" {
			return d
		}
	}
	return ""
}

// asDir is v as a folder that is there: a path, a file:// URI, or an
// object with one under path or uri.
func asDir(v any) string {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case map[string]any:
		for _, k := range []string{"path", "uri", "fsPath"} {
			if d := asDir(x[k]); d != "" {
				return d
			}
		}
		return ""
	default:
		return ""
	}
	s = strings.TrimPrefix(s, "file://")
	if len(s) > 2 && s[0] == '/' && s[2] == ':' {
		s = s[1:] // file:///C:/…
	}
	if !filepath.IsAbs(s) {
		return ""
	}
	if fi, err := os.Stat(s); err == nil && fi.IsDir() {
		return filepath.Clean(s)
	}
	return ""
}

// Snapshot is what a new conversation in dir is told of it.
func Snapshot(dir string) string {
	var lines []string
	lines = append(lines, "Workspace: "+dir+gitLine(dir))
	if p := projectLine(dir); p != "" {
		lines = append(lines, "Project: "+p)
	}
	if t := topLine(dir); t != "" {
		lines = append(lines, "Top level: "+t)
	}
	lines = append(lines, fmt.Sprintf("Batch independent work: batch_search for several searches, read_slices for several files, apply_edits for every edit of a change, run_checks for builds and tests side by side; pass cwd %q.", dir))
	s := strings.Join(lines, "\n")
	if len(s) > snapshotCap {
		s = s[:snapshotCap-1]
		for len(s) > 0 && !utf8Start(s[len(s)-1]) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// gitLine is dir's branch and what is changed there, briefly.
func gitLine(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain=v1", "--branch", "--untracked-files=normal")
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	rows := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	branch := ""
	if len(rows) > 0 && strings.HasPrefix(rows[0], "## ") {
		branch = strings.TrimPrefix(rows[0], "## ")
		if i := strings.Index(branch, "..."); i >= 0 {
			// main...origin/main [ahead 1]: the branch, and how far off
			rest := branch[i+3:]
			branch = branch[:i]
			if j := strings.IndexByte(rest, '['); j >= 0 {
				branch += " " + rest[j:]
			}
		}
		rows = rows[1:]
	}
	var changed []string
	for _, r := range rows {
		if strings.TrimSpace(r) != "" {
			changed = append(changed, strings.TrimSpace(r))
		}
	}
	s := " (git"
	if branch != "" {
		s += ": " + branch
	}
	if len(changed) == 0 {
		return s + ", clean)"
	}
	s += fmt.Sprintf(", %d changed: ", len(changed))
	shownN := min(len(changed), 12)
	s += strings.Join(changed[:shownN], ", ")
	if len(changed) > shownN {
		s += ", …"
	}
	return s + ")"
}

// projectLine names the kinds of project dir is, by the files that say so.
func projectLine(dir string) string {
	var kinds []string
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(b) > 1<<20 {
			return ""
		}
		return string(b)
	}
	if s := read("go.mod"); s != "" {
		k := "Go"
		for _, l := range strings.Split(s, "\n") {
			if m, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
				k += " module " + strings.TrimSpace(m)
				break
			}
		}
		kinds = append(kinds, k)
	}
	if s := read("package.json"); s != "" {
		var p struct {
			Name           string            `json:"name"`
			Scripts        map[string]string `json:"scripts"`
			PackageManager string            `json:"packageManager"`
			Workspaces     any               `json:"workspaces"`
		}
		if json.Unmarshal([]byte(s), &p) == nil {
			k := "Node"
			if p.Name != "" {
				k += " package " + p.Name
			}
			pm := ""
			switch {
			case p.PackageManager != "":
				pm, _, _ = strings.Cut(p.PackageManager, "@")
			case exists(dir, "bun.lockb") || exists(dir, "bun.lock"):
				pm = "bun"
			case exists(dir, "pnpm-lock.yaml"):
				pm = "pnpm"
			case exists(dir, "yarn.lock"):
				pm = "yarn"
			}
			if pm != "" {
				k += " (" + pm + ")"
			}
			if p.Workspaces != nil {
				k += ", workspaces"
			}
			if len(p.Scripts) > 0 {
				names := make([]string, 0, len(p.Scripts))
				for n := range p.Scripts {
					names = append(names, n)
				}
				slices.Sort(names)
				if len(names) > 12 {
					names = append(names[:12], "…")
				}
				k += ", scripts: " + strings.Join(names, " ")
			}
			kinds = append(kinds, k)
		}
	}
	for _, f := range []struct{ file, kind string }{
		{"Cargo.toml", "Rust (Cargo)"}, {"pyproject.toml", "Python (pyproject)"}, {"requirements.txt", "Python"},
		{"pom.xml", "Java (Maven)"}, {"build.gradle", "JVM (Gradle)"}, {"build.gradle.kts", "JVM (Gradle)"},
		{"Package.swift", "Swift package"}, {"Gemfile", "Ruby"}, {"composer.json", "PHP (Composer)"},
		{"mix.exs", "Elixir"}, {"deno.json", "Deno"}, {"CMakeLists.txt", "CMake"}, {"Makefile", "Makefile"},
		{"pubspec.yaml", "Dart/Flutter"}, {"build.zig", "Zig"},
	} {
		if exists(dir, f.file) && !slices.Contains(kinds, f.kind) {
			kinds = append(kinds, f.kind)
		}
	}
	for _, f := range []string{"AGENTS.md", "GEMINI.md", "CLAUDE.md", "README.md"} {
		if exists(dir, f) {
			kinds = append(kinds, "has "+f)
		}
	}
	return strings.Join(kinds, "; ")
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// topLine is what is at dir's top, folders first.
func topLine(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var dirs, files []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") || slices.Contains(skipDirs, n) {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, n+"/")
		} else {
			files = append(files, n)
		}
	}
	all := append(dirs, files...)
	if len(all) == 0 {
		return ""
	}
	if len(all) > 40 {
		return strings.Join(all[:40], " ") + fmt.Sprintf(" … (%d in all)", len(all))
	}
	return strings.Join(all, " ")
}
