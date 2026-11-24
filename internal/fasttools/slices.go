package fasttools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

const (
	maxFiles = 20
	// a file of at most this many lines comes whole when no range is asked
	wholeLines = 250
	// the most outline lines given for one file
	outlineMax = 120
)

type sliceReq struct {
	Path    string  `json:"path"`
	Ranges  [][]int `json:"ranges"`
	Outline bool    `json:"outline"`
}

func readSlices(raw json.RawMessage) (string, error) {
	var a struct {
		Cwd   string     `json:"cwd"`
		Files []sliceReq `json:"files"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	if len(a.Files) == 0 {
		return "", fmt.Errorf("no files")
	}
	if len(a.Files) > maxFiles {
		return "", fmt.Errorf("%d files: at most %d in one call", len(a.Files), maxFiles)
	}
	dir, err := root(a.Cwd)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(a.Files))
	for _, f := range a.Files {
		parts = append(parts, slice(dir, f))
	}
	share := outCap / len(parts)
	for i, s := range parts {
		if len(s) > share && share > 300 {
			parts[i] = capTo(s, share)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func slice(dir string, f sliceReq) string {
	p := at(dir, f.Path)
	name := shown(dir, p)
	b, err := os.ReadFile(p)
	if err != nil {
		return "## " + name + "\nerror: " + err.Error()
	}
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return fmt.Sprintf("## %s\na binary file, %d bytes", name, len(b))
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	var out strings.Builder
	fmt.Fprintf(&out, "## %s (%d lines)\n", name, len(lines))
	ranges := f.Ranges
	outline := f.Outline
	if len(ranges) == 0 {
		if len(lines) <= wholeLines {
			ranges = [][]int{{1, len(lines)}}
		} else {
			outline = true
		}
	}
	if outline {
		o := Outline(p, lines)
		if len(o) == 0 {
			if len(f.Ranges) == 0 {
				// nothing to outline: the start of it, then
				ranges = [][]int{{1, min(len(lines), 120)}}
			}
		} else {
			out.WriteString("outline:\n")
			for i, l := range o {
				if i == outlineMax {
					fmt.Fprintf(&out, "… %d more\n", len(o)-i)
					break
				}
				out.WriteString(l)
				out.WriteByte('\n')
			}
		}
	}
	for _, r := range ranges {
		if len(r) != 2 {
			fmt.Fprintf(&out, "a range is [start, end], not %v\n", r)
			continue
		}
		from, to := max(r[0], 1), min(r[1], len(lines))
		if r[1] <= 0 {
			to = len(lines)
		}
		if from > to {
			fmt.Fprintf(&out, "lines %d-%d: the file has %d\n", r[0], r[1], len(lines))
			continue
		}
		if len(ranges) > 1 || from != 1 || to != len(lines) {
			fmt.Fprintf(&out, "lines %d-%d:\n", from, to)
		}
		for i := from; i <= to; i++ {
			fmt.Fprintf(&out, "%d\t%s\n", i, strings.TrimRight(lines[i-1], "\r"))
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// outliners find a file's declarations by its extension: a line that
// matches is one, given with its number. They are lines, not a parse: a
// declaration over several lines shows its first.
// They are compiled the first time one is used: magpie's every launch
// runs this package's init.
var outliners = map[string]*lazyre.RE{}

func init() {
	add := func(re string, exts ...string) {
		r := lazyre.New(re)
		for _, e := range exts {
			outliners[e] = r
		}
	}
	add(`^(func|type|var|const) |^\)$|^(var|const|type) \($`, ".go")
	add(`^\s*(export\s+)?(default\s+)?(async\s+)?(function\*?|class|interface|type|enum|namespace|abstract\s+class)\s|^\s*(export\s+)?(const|let|var)\s+\w+\s*=\s*(async\s*)?(\(|function|\w+\s*=>)|^\s*(public|private|protected|static|async|get|set|readonly|\s)*\s*[A-Za-z_$][\w$]*\s*\([^)]*\)\s*(:\s*[^{]+)?\{\s*$|^export\s+(default|\{)`, ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte")
	add(`^\s*(async\s+)?def\s|^\s*class\s|^[A-Z_][A-Z0-9_]*\s*=`, ".py", ".pyi")
	add(`^\s*(pub(\([^)]*\))?\s+)?(async\s+)?(unsafe\s+)?(fn|struct|enum|trait|impl|mod|type|const|static|macro_rules!)\b`, ".rs")
	add(`^\s*(public|private|protected|internal|static|final|abstract|sealed|open|override|data|suspend|inline|\s)*\s*(class|interface|enum|record|object|fun|struct|protocol|extension|func|init|deinit|var|let)\s+\w|^\s*(public|private|protected|internal|static|final|abstract|override|async|virtual|\s)+[\w<>\[\],.? ]+\s+\w+\s*\([^;]*$`, ".java", ".kt", ".kts", ".cs", ".swift", ".scala")
	add(`^\s*(class|struct|enum|namespace|union|typedef|template)\b|^[A-Za-z_][\w:<>,\s*&]*\s+\**[A-Za-z_][\w:~]*\s*\([^;]*$|^#define\s`, ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".m", ".mm")
	add(`^\s*(def|class|module)\s`, ".rb")
	add(`^\s*((public|private|protected|static|abstract|final)\s+)*(function|class|interface|trait|enum)\s`, ".php")
	add(`^\s*(local\s+)?function\s|^\s*[\w.]+\s*=\s*function\b`, ".lua")
	add(`^\s*(function\s+\w+|\w+\s*\(\)\s*\{)`, ".sh", ".bash", ".zsh")
	add(`^#{1,6}\s`, ".md", ".mdx", ".markdown")
	add(`^\s*\[[^\]]+\]\s*$`, ".toml", ".ini", ".cfg")
	add(`^[A-Za-z_][\w-]*:`, ".yaml", ".yml")
	add(`^\s*(@media|@layer|@keyframes|@supports)|^[^\s{][^{]*\{\s*$`, ".css", ".scss", ".less")
	add(`^\s*(CREATE|ALTER)\s+(TABLE|VIEW|INDEX|FUNCTION|PROCEDURE|TRIGGER)\b`, ".sql")
	add(`^\s*(func|class|enum|extends|signal|const)\s`, ".gd")
	add(`^\s*(defmodule|def|defp|defmacro)\s`, ".ex", ".exs")
	add(`^\s*(fn|pub fn|const|pub const|struct|enum|test)\s`, ".zig")
}

// Outline is the declarations of the file at path, one line each with its
// line number; nil for a kind of file with no outliner.
func Outline(path string, lines []string) []string {
	re := outliners[strings.ToLower(filepath.Ext(path))]
	if re == nil {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	var out []string
	inBlock := false // a Go var ( … ) or const ( … ) block
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if ext == ".go" {
			switch {
			case l == ")":
				inBlock = false
				continue
			case l == "var (" || l == "const (" || l == "type (":
				inBlock = true
			case inBlock && strings.HasPrefix(l, "\t") && !strings.HasPrefix(l, "\t\t") && !strings.HasPrefix(l, "\t//") && strings.TrimSpace(l) != "":
				out = append(out, fmt.Sprintf("%d: %s", i+1, cutOutline(l)))
				continue
			}
		}
		if !re.MatchString(l) {
			continue
		}
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") {
			continue
		}
		if isJS(ext) && jsKeyword.MatchString(t) {
			continue
		}
		out = append(out, fmt.Sprintf("%d: %s", i+1, cutOutline(l)))
	}
	return out
}

// jsKeyword: `if (…) {` looks like a method to the JS outliner
var jsKeyword = lazyre.New(`^(if|for|while|switch|catch|with|return|else|do|try|function\s*\()\b`)

func isJS(ext string) bool {
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte":
		return true
	}
	return false
}

func cutOutline(l string) string {
	l = strings.TrimRight(l, " \t{")
	l = strings.ReplaceAll(l, "\t", "  ")
	if len(l) > 160 {
		cut := 160
		for cut > 0 && !utf8Start(l[cut]) {
			cut--
		}
		l = l[:cut] + "…"
	}
	return l
}
