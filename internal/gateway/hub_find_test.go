package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// realHubTools are tools as real MCP servers describe them: GitHub's
// (github-mcp-server), the reference filesystem server's, Linear's, and
// magpie-image's and magpie-browser's own.
func realHubTools() []hubTool {
	var ts []HubTool
	add := func(server, tool, desc string) {
		d, _ := json.Marshal(desc)
		n, _ := json.Marshal(tool)
		ts = append(ts, HubTool{Server: server, Tool: tool, Spec: map[string]json.RawMessage{
			"name": n, "description": d, "inputSchema": json.RawMessage(`{"type":"object","properties":{}}`),
		}})
	}
	add("github", "create_issue", "Create a new issue in a GitHub repository.")
	add("github", "list_issues", "List issues in a GitHub repository. For pagination, use the 'endCursor' from the previous response's 'pageInfo' in the 'after' parameter.")
	add("github", "search_code", "Fast and precise code search across ALL GitHub repositories using GitHub's native search engine. Best for finding exact symbols, functions, classes, or specific code patterns.")
	add("github", "create_pull_request", "Create a new pull request in a GitHub repository.")
	add("github", "get_file_contents", "Get the contents of a file or directory from a GitHub repository")
	add("filesystem", "read_file", "Read the complete contents of a file from the file system. Handles various text encodings and provides detailed error messages if the file cannot be read. Use this tool when you need to examine the contents of a single file. Only works within allowed directories.")
	add("filesystem", "write_file", "Create a new file or completely overwrite an existing file with new content. Use with caution as it will overwrite existing files without warning. Handles text content with proper encoding. Only works within allowed directories.")
	add("filesystem", "list_directory", "Get a detailed listing of all files and directories in a specified path. Results clearly distinguish between files and directories with [FILE] and [DIR] prefixes. This tool is essential for understanding directory structure and finding specific files within a directory. Only works within allowed directories.")
	add("filesystem", "search_files", "Recursively search for files and directories matching a pattern. Searches through all subdirectories from the starting path. The search is case-insensitive and matches partial names. Returns full paths to all matching items. Great for finding files when you don't know their exact location. Only searches within allowed directories.")
	add("linear", "save_issue", "Create or update a Linear issue. If `id` is provided, updates the existing issue; otherwise creates a new one. When creating, `team` is required, and `title` is required unless `template` is set.")
	add("linear", "list_issues", "List issues in the user's Linear workspace, including active Triage Intelligence suggestions for issues in triage. For issues assigned to me, use \"me\" as the assignee.")
	add("linear", "search_documentation", "Search Linear's product documentation for feature and usage guidance. Returns up to five results with titles, URLs, and content snippets per page.")
	add("magpie-image", "generate_image", "Generate an image from a text prompt, or edit/combine images given as reference_images, with the image model set in Magpie. The image is saved as a file in the project (generated-images/ unless path says where) and its path is returned.")
	add("magpie-browser", "browser", "Act in the user's own browser. Actions, with the arguments each takes:\ntabs: list them. tab_new [url, background]. navigate [url]. back. forward. reload.\nsnapshot [interactive, ref, maxChars]: the page as an accessibility tree, a ref on each element you can act on.\nclick [ref]. type [ref, text]. screenshot [ref, fullPage, png]. text [maxChars]: the visible text.")
	return hubNamed(ts)
}

func findText(t *testing.T, ts []hubTool, args string) string {
	t.Helper()
	b, _ := json.Marshal(hubFind(ts, json.RawMessage(args), ""))
	var r struct {
		Content []struct{ Text string }
		IsError bool
	}
	if err := json.Unmarshal(b, &r); err != nil || len(r.Content) == 0 {
		t.Fatalf("%v: %s", err, b)
	}
	return r.Content[0].Text
}

// find_tools gives a tool only when it does what the query asks, and when
// none does it says no tool was found, naming the servers, rather than
// handing over the tools whose names or descriptions share a word or two
// with it. Words of what it is for but in other words ("draw an image
// of a cat") find nothing, and the answer names each server's tools, as
// what there is, so the model can choose generate_image itself
// (sahuaja3669 on X, 11-18: a router mustn't fill its answer with
// a tool of a near name that does something else).
func TestHubFindNothingClose(t *testing.T) {
	ts := realHubTools()
	for _, q := range []string{"send an email", "search the web", "picture of a cat", "book a flight to Tokyo", "draw an image of a cat", "translate this text to French", "it"} {
		if ms := hubMatches(ts, q); len(ms) > 0 {
			t.Errorf("%q matched %v", q, hubToolNames(ms))
		}
		text := findText(t, ts, `{"query":`+jsonString(q)+`}`)
		if !strings.HasPrefix(text, "No tool was found for") || strings.Contains(text, "inputSchema") || strings.Contains(text, "__") {
			t.Errorf("%q: %s", q, text)
		}
		for _, s := range []string{"github (5 tools: create_issue, list_issues,", "filesystem (4 tools:", "linear (3 tools:", "magpie-image (1 tool: generate_image)", "magpie-browser (1 tool: browser)"} {
			if !strings.Contains(text, s) {
				t.Errorf("%q: no %q in %s", q, s, text)
			}
		}
	}
	for q, want := range map[string]string{
		"generate an image":          "magpie-image__generate_image",
		"open a page in the browser": "magpie-browser__browser",
		"read a file":                "filesystem__read_file",
		"create a GitHub issue":      "github__create_issue",
		"open a pull request":        "github__create_pull_request",
		"list my Linear issues":      "linear__list_issues",
		"search code":                "github__search_code",
		"browser":                    "magpie-browser__browser",
		"write_file":                 "filesystem__write_file",
		"列出":                         "",
	} {
		ms := hubMatches(ts, q)
		if want == "" {
			if len(ms) > 0 {
				t.Errorf("%q matched %v", q, hubToolNames(ms))
			}
			continue
		}
		if len(ms) == 0 || ms[0].name != want {
			t.Errorf("%q matched %v, want %s first", q, hubToolNames(ms), want)
		}
	}
	// a match comes with its parameters, and only matches do
	text := findText(t, ts, `{"query":"read a file"}`)
	if !strings.Contains(text, `"filesystem__read_file"`) || strings.Contains(text, "magpie-browser") || strings.Contains(text, "linear__") {
		t.Fatalf("read a file: %s", text)
	}
}

func hubToolNames(ts []hubTool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.name
	}
	return out
}
