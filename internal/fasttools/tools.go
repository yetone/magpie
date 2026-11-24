package fasttools

// ServerName is the MCP server's name in the plugin; agy names its tools
// mcp_<plugin>_<server>_<tool>.
const ServerName = "fast"

// instructions go to the model with the server (initialize). They say what
// the tools are for, and nothing of who wrote them or of the agent's own
// tools' limits: the text reaches the model's vendor.
const instructions = "Batch work into few calls. batch_search runs up to 20 searches (text, regex, file globs, read-only git) in one call. read_slices gives outlines and line ranges of several files at once. apply_edits makes every edit of a change across files in one call, checks them all before writing any, and can run a verify command after. run_checks runs independent build, test and lint commands side by side. Pass cwd, the workspace's absolute path, on every call."

// toolNames are the tools, in the order they're listed.
var toolNames = []string{"batch_search", "read_slices", "apply_edits", "run_checks"}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

var cwdProp = str("The workspace's absolute path; relative paths are read from it. Defaults to the server's working folder.")

func toolList() []any {
	return []any{
		map[string]any{
			"name":        "batch_search",
			"description": "Run several independent searches in one call: text or regex over file contents, globs over file names, and read-only git commands. Use it instead of one search per turn; put every search you need next into one call.",
			"inputSchema": obj(map[string]any{
				"cwd": cwdProp,
				"queries": map[string]any{
					"type": "array", "maxItems": maxQueries,
					"description": "Up to 20 searches, run side by side.",
					"items": obj(map[string]any{
						"kind":       map[string]any{"type": "string", "enum": []string{"text", "regex", "glob", "git"}, "description": "text: a literal string in contents; regex: an RE2 regular expression in contents; glob: file names (** for any folders); git: a read-only git command's arguments (status, diff, log, show, blame, ls-files, grep, branch, rev-parse)."},
						"pattern":    str("The text, regex, glob, or git arguments."),
						"path":       str("Only under this folder or file (relative to cwd)."),
						"glob":       str("Only files whose path matches this glob, e.g. **/*.go."),
						"ignoreCase": flag("Match case-insensitively (text and regex)."),
						"context":    num("Lines of context around each match (0 to 5)."),
						"max":        num("Most matches to give (default 40, at most 200)."),
					}, "kind", "pattern"),
				},
			}, "queries"),
		},
		map[string]any{
			"name":        "read_slices",
			"description": "Read several files at once: an outline of each one's declarations (functions, types, classes, headings) with line numbers, and the line ranges you ask for. A file asked for with no ranges comes whole when short, as an outline when long. Use it instead of reading files one by one.",
			"inputSchema": obj(map[string]any{
				"cwd": cwdProp,
				"files": map[string]any{
					"type": "array", "maxItems": maxFiles,
					"items": obj(map[string]any{
						"path":    str("The file (relative to cwd)."),
						"ranges":  map[string]any{"type": "array", "description": "Line ranges to read, each [start, end], 1-based and inclusive.", "items": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "minItems": 2, "maxItems": 2}},
						"outline": flag("Give the outline (also when ranges are asked for)."),
					}, "path"),
				},
			}, "files"),
		},
		map[string]any{
			"name":        "apply_edits",
			"description": "Make every edit of a change in one call, across one or more files. Each edit replaces old (which must appear exactly once, unless replaceAll) with new, or writes a whole file from content. Every edit is checked first: when any can't be made, none is written (unless bestEffort), and the reason says where old nearly matched. verify runs a command after writing, such as a build or a test, and gives its result.",
			"inputSchema": obj(map[string]any{
				"cwd": cwdProp,
				"edits": map[string]any{
					"type": "array", "maxItems": maxEdits,
					"description": "Applied in order; several edits of one file see the earlier ones' result.",
					"items": obj(map[string]any{
						"path":       str("The file (relative to cwd)."),
						"old":        str("The exact text to replace, with enough lines around it to be unique."),
						"new":        str("What it becomes."),
						"replaceAll": flag("Replace every occurrence of old."),
						"content":    str("Write the whole file with this text instead (creates it, and its folders)."),
					}, "path"),
				},
				"bestEffort":    flag("Write the edits that can be made even when others can't."),
				"dryRun":        flag("Check the edits and say what they'd change, writing nothing."),
				"verify":        str("A shell command run in cwd after writing, e.g. go build ./... or npm test."),
				"verifyTimeout": num("Seconds verify may take (default 120, at most 900)."),
			}, "edits"),
		},
		map[string]any{
			"name":        "run_checks",
			"description": "Run independent commands side by side, such as a build, tests and a linter, and give each one's result: one line when it passes, the end of its output when it fails. Use it instead of running them one per turn.",
			"inputSchema": obj(map[string]any{
				"cwd": cwdProp,
				"commands": map[string]any{
					"type": "array", "maxItems": maxChecks,
					"items": obj(map[string]any{
						"cmd":     str("The shell command."),
						"name":    str("A short name for it in the result."),
						"timeout": num("Seconds it may take (default 300, at most 1800)."),
					}, "cmd"),
				},
			}, "commands"),
		},
	}
}
