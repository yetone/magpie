package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/usage"
)

// Magpie Artifacts are an MCP server, served as the sessions' is
// (serveMCP) to the agents on this computer alone and given as `magpie
// mcp artifacts`, with which an agent publishes a page for the user to
// look at or use (a report, a chart, a small app) and updates, reads,
// lists, opens and deletes it. The pages are internal/artifact's, kept on
// this computer with every version, and served at ArtifactPagePath<id>/,
// whichever account or agent made them: an artifact made under one
// Claude account still opens after magpie switches to another.
//
// A page is someone else's code, run in the user's browser at the
// gateway's address, so it is served in a sandbox of its own
// (artifactPageCSP): an opaque origin, which reads no cookie, storage or
// reply of the gateway's, and which corsGuard turns away on every other
// route, as it does any Origin: null without a key. Its own files are the
// one thing it reads (artifactRead).

const (
	// ArtifactsMCPPath is where the artifacts' MCP server is served.
	ArtifactsMCPPath = "/mcp/magpie/artifacts"
	// ArtifactPagePath is where an artifact's page is: <id>/, or
	// <id>@<version>/ for one version, and its files beside it.
	ArtifactPagePath = "/a/"

	artifactsServerName = "magpie-artifacts"
	artifactPublishTool = "publish_artifact"
	artifactReadTool    = "read_artifact"
	artifactListTool    = "list_artifacts"
	artifactDeleteTool  = "delete_artifact"
	artifactOpenTool    = "open_artifact"
	artifactDataTool    = "artifact_data"

	// artifactsMCPBody is the most a message may be: a version's 64 MiB in
	// base64, and the JSON around it
	artifactsMCPBody = 96 << 20
)

// artifactPageCSP is what an artifact's page and files are served under:
// a sandbox without allow-same-origin, so the page's origin is opaque.
// It runs its scripts, forms, dialogs, popups (links in a new tab
// included, out of the sandbox) and downloads.
const artifactPageCSP = "sandbox allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads"

// ArtifactURL is the address of artifact id's page on this computer;
// version 0 is the newest.
func ArtifactURL(id string, version int) string {
	ref := id
	if version > 0 {
		ref += "@" + strconv.Itoa(version)
	}
	return URL() + ArtifactPagePath + ref + "/"
}

// openArtifactURL opens an address in the user's browser; tests stand in.
var openArtifactURL = proc.OpenBrowser

var artifactRefRe = lazyre.New(`(?:^|/a/)([a-z2-7]{26})(?:@(\d+))?(?:/|$)`)

// ArtifactRef reads an artifact named by its id, id@version or address.
func ArtifactRef(s string) (id string, version int, ok bool) {
	s = strings.TrimSpace(s)
	m := artifactRefRe.FindStringSubmatch(s)
	if m == nil {
		return "", 0, false
	}
	if m[2] != "" {
		n, err := strconv.Atoi(m[2])
		if err != nil || n < 1 {
			return "", 0, false
		}
		version = n
	}
	return m[1], version, true
}

func (s *Server) artifactsMCP(w http.ResponseWriter, r *http.Request) {
	// it reads and writes files on this computer for the agent: only an
	// agent on this computer calls it, never a page
	if !local(r) || fromBrowser(r) {
		http.Error(w, "magpie's artifacts MCP server answers the agents on this computer alone", http.StatusForbidden)
		return
	}
	serveMCP(w, r, "magpie's artifacts", artifactsServerName, artifactsMCPBody, s.artifactsMCPCall)
}

func artifactsMCPTools() []any {
	ref := map[string]any{"type": "string", "description": "The artifact: its id or its address, as a publish or list gave it"}
	return []any{
		map[string]any{
			"name":  artifactPublishTool,
			"title": "Publish artifact",
			"description": "Publishes an HTML page as an artifact: a private page on this computer, kept with every version, that the user opens in their browser. " +
				"Use it whenever a page would be clearer than text, or when the user would use the page rather than only read it: a report, a chart or dashboard, a diagram, an explainer, a prototype, a small app or game. " +
				"Write the page to a file and pass file_path (or pass the page as html). The page is self-contained HTML; a <title> of two to four words names it. " +
				"file_path and files' paths are read on the computer magpie runs on: from another computer or container, pass the page as html and its files as text or base64. " +
				"Supporting files (scripts, styles, data, images, more pages) go in files, by the relative path the page uses, each from a local file. " +
				"To change an artifact, pass its id: a new version is made at the same address; files left out are kept and null removes one. " +
				"Pass the version you last read or published to refuse a publish over a newer one. Give the user the address from the answer. " +
				"Only the user shares an artifact, from magpie; once they have, its shared link shows each version published after.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path":   map[string]any{"type": "string", "description": "The page: a .html file on the computer magpie runs on, absolute or relative to the folder you work in"},
					"html":        map[string]any{"type": "string", "description": "The page itself, in place of file_path"},
					"title":       map[string]any{"type": "string", "description": "Its name, when the page has no <title>"},
					"description": map[string]any{"type": "string", "description": "One sentence on what it is"},
					"id":          map[string]any{"type": "string", "description": "An artifact to update (its id or address); left out, a new one is made"},
					"files": map[string]any{
						"type":        "object",
						"description": "Files beside the page, by the relative path the page references (e.g. \"app.js\", \"img/logo.png\"). Each is a local file's path, {\"text\": \"…\"} or {\"base64\": \"…\"} for content given here, or null to remove the file from an update.",
						"additionalProperties": map[string]any{"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{"type": "object", "properties": map[string]any{"from": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}, "base64": map[string]any{"type": "string"}}},
							map[string]any{"type": "null"},
						}},
					},
					"capabilities": map[string]any{
						"type": "object",
						"description": "What the page may do beyond static HTML, as Claude's artifacts declare it: {\"db\": {}, \"user\": {}}. " +
							"db keeps data outside the page, which the page reads and writes through `await claude.use(\"db\")` (doc, collection, get, set, update, delete, where, orderBy, limit, onSnapshot) and you read and seed with " + artifactDataTool + "; " +
							"{\"db\": {\"rules\": [{\"path\": …, \"read\": …, \"write\": …}]}} sets who reads and writes where. user is who views it, through `await claude.use(\"user\")`. " +
							"artifact lets the page save itself: `await (await claude.use(\"artifact\")).publish(html)` publishes the whole page, state embedded, as the newest version, and every open view reloads to it; publish({path: content}) saves files and keeps the page running. " +
							"assets keeps files people add (images, video, PDF, fonts, CSV and text, 20 MiB each): `(await claude.use(\"assets\")).upload(blob)` gives {id, url}; keep the id in db and show it at \"/_blob/\" + id. " +
							"Any other capability resolves null on the page here, so a page lights it up only when it is there. Left out, an update keeps the version before's; {} clears them.",
					},
					"label":   map[string]any{"type": "string", "description": "A short name for this version, at most 60 characters"},
					"version": map[string]any{"type": "integer", "minimum": 1, "description": "The version this publish was built on; a newer one makes it fail instead of being written over"},
				},
			},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
			// declared to the model at once, as a built-in tool is, so an ask
			// for a page reaches it without naming it (hubAlwaysLoaded)
			"_meta": hubAlwaysLoadMeta(),
		},
		map[string]any{
			"name":        artifactReadTool,
			"title":       "Read artifact",
			"description": "Reads an artifact: what it is, its versions and files, and its page as published (or one of its files, with path). Read one before changing it, and build the new version on what comes back.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      ref,
					"path":    map[string]any{"type": "string", "description": "One of its files instead of the page"},
					"version": map[string]any{"type": "integer", "minimum": 1, "description": "An earlier version; left out, the newest"},
				},
				"required": []string{"id"},
			},
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		},
		map[string]any{
			"name":        artifactListTool,
			"title":       "List artifacts",
			"description": "Lists the artifacts on this computer, the most recently updated first, with their titles, addresses and versions. Use it to find an artifact to update or open again.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Only those whose title or description has these words"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 25},
				},
			},
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		},
		map[string]any{
			"name":        artifactOpenTool,
			"title":       "Open artifact",
			"description": "Opens an artifact in the user's browser, without changing it. Use it when the user asks to see one; a publish needs no open unless they ask.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": ref, "version": map[string]any{"type": "integer", "minimum": 1}},
				"required":   []string{"id"},
			},
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		},
		map[string]any{
			"name":  artifactDataTool,
			"title": "Artifact data",
			"description": "Reads and writes the data of an artifact whose page declares the db capability: the documents its page reads with claude.use(\"db\"). " +
				"Paths alternate collection and document: \"tasks\" is a collection, \"tasks/t1\" a document in it; a document's body is a JSON object. " +
				"Seed a new page's data here after publishing it, never hard-coded in the page. Each write shows at once on every open view of the page. " +
				"action: get (the document at path), list (the collections, or the documents of the collection at path), query (the collection at path with where, order_by, desc and limit), " +
				"set (write the whole document), update (merge data into it; nested objects merge, arrays are replaced), str_replace (replace old_str, once, in the document's JSON as get shows it), " +
				"delete (the document at path), batch (ops, together or not at all).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":       ref,
					"action":   map[string]any{"type": "string", "enum": []string{"get", "list", "query", "set", "update", "str_replace", "delete", "batch"}},
					"path":     map[string]any{"type": "string", "description": "A document's path (an even number of segments) or a collection's (odd)"},
					"data":     map[string]any{"type": "object", "description": "The document's body, for set and update"},
					"where":    map[string]any{"type": "array", "description": "For query: at most 10 filters, each {field, op, value}; op is one of == != < <= > >= in not-in array-contains", "items": map[string]any{"type": "object", "properties": map[string]any{"field": map[string]any{"type": "string"}, "op": map[string]any{"type": "string"}, "value": map[string]any{}}}},
					"order_by": map[string]any{"type": "string", "description": "For query: the field to sort by; left out, by id"},
					"desc":     map[string]any{"type": "boolean"},
					"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
					"old_str":  map[string]any{"type": "string", "description": "For str_replace: text that is in the document's JSON once"},
					"new_str":  map[string]any{"type": "string", "description": "For str_replace: what takes its place"},
					"ops":      map[string]any{"type": "array", "description": "For batch: at most 500 writes, each {op: set|update|delete, path, data}", "items": map[string]any{"type": "object", "properties": map[string]any{"op": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}, "data": map[string]any{"type": "object"}}}},
				},
				"required": []string{"id", "action"},
			},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false},
		},
		map[string]any{
			"name":        artifactDeleteTool,
			"title":       "Delete artifact",
			"description": "Deletes an artifact and every version of it, for good: its address stops working. Only when the user asks for that artifact to be deleted.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": ref},
				"required":   []string{"id"},
			},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": false},
		},
	}
}

// artifactSource is a supporting file as a publish names it: a local
// file's path, inline text or base64, or null to remove it.
type artifactSource struct {
	from, text, b64 string
	inline, remove  bool
}

func (a *artifactSource) UnmarshalJSON(b []byte) error {
	switch {
	case string(b) == "null":
		a.remove = true
		return nil
	case len(b) > 0 && b[0] == '"':
		return json.Unmarshal(b, &a.from)
	}
	var o struct {
		From   *string `json:"from"`
		Text   *string `json:"text"`
		Base64 *string `json:"base64"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return errors.New("a file is a path, {\"text\": …}, {\"base64\": …} or null")
	}
	switch {
	case o.Text != nil:
		a.text, a.inline = *o.Text, true
	case o.Base64 != nil:
		a.b64, a.inline = *o.Base64, true
	case o.From != nil:
		a.from = *o.From
	default:
		return errors.New("a file is a path, {\"text\": …}, {\"base64\": …} or null")
	}
	return nil
}

type artifactArgs struct {
	FilePath    string                     `json:"file_path"`
	HTML        *string                    `json:"html"`
	Title       string                     `json:"title"`
	Description string                     `json:"description"`
	ID          string                     `json:"id"`
	URL         string                     `json:"url"`
	Files       map[string]*artifactSource `json:"files"`
	Label       string                     `json:"label"`
	Version     flexCount                  `json:"version"`
	Path        string                     `json:"path"`
	Query       string                     `json:"query"`
	Limit       flexCount                  `json:"limit"`
	// capabilities, for a publish; as given, so ParseCapabilities tells
	// left out from {}
	Capabilities json.RawMessage `json:"capabilities"`
	// artifact_data's
	Action  string            `json:"action"`
	Data    json.RawMessage   `json:"data"`
	Where   []artifact.Filter `json:"where"`
	OrderBy string            `json:"order_by"`
	Desc    bool              `json:"desc"`
	OldStr  *string           `json:"old_str"`
	NewStr  *string           `json:"new_str"`
	Ops     []artifact.Op     `json:"ops"`
}

// artifactCaller is who publishes, and the folder its relative paths are
// in ("" when not known). base is the address an agent on another
// computer reaches magpie at, as the hub passed it on (HubBaseMeta), ""
// for one on this computer.
type artifactCaller struct {
	r     *http.Request
	meta  json.RawMessage
	agent string
	base  string
}

func newArtifactCaller(r *http.Request, rawMeta json.RawMessage) *artifactCaller {
	c := &artifactCaller{r: r, meta: rawMeta}
	var extra struct {
		Client string `json:"magpie/client"`
		Base   string `json:"magpie/base"`
	}
	json.Unmarshal(rawMeta, &extra)
	c.base = artifactBase(extra.Base)
	for _, name := range []string{extra.Client, r.Header.Get(MemoryClientHeader)} {
		if c.agent == "" && strings.TrimSpace(name) != "" {
			c.agent = memoryAgent(usage.AgentOf(name))
		}
	}
	if c.agent == "" {
		c.agent = memoryAgent(callerOf(r).agent)
	}
	if c.agent == "" {
		c.agent = memoryClients.of(r.Header.Get("Mcp-Session-Id"))
	}
	return c
}

// artifactBase is s when it is an http(s) address with a host and
// nothing after it, without its trailing slash; else "".
func artifactBase(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(u.Host, " \"\\<>") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// away: the caller is an agent on another computer (or in another
// container), whose files magpie doesn't see.
func (c *artifactCaller) away() bool { return c.base != "" }

// link is the address of artifact id's page (version 0: the newest) for
// the caller: at the address it reaches magpie at when it is away, else
// this computer's (ArtifactURL).
func (c *artifactCaller) link(id string, version int) string {
	if c == nil || c.base == "" {
		return ArtifactURL(id, version)
	}
	return c.base + strings.TrimPrefix(ArtifactURL(id, version), URL())
}

// artifactFarFile is why an agent on another computer can't give the
// page as file_path: its files are on its own computer, and magpie reads
// only those of the one it runs on.
func artifactFarFile(p string) error {
	return fmt.Errorf("%s isn't on the computer magpie runs on: you are on another computer or in another container, and magpie reads only its own files. "+
		"Pass the page itself as html, and each file beside it in files as {\"text\": …} or {\"base64\": …}; "+
		"or put the file in a folder mounted into both, and give the path magpie sees it at", p)
}

// dir is the folder the caller works in: the hub's roots, the folder
// `magpie mcp artifacts` runs in, else the agent's roots.
func (c *artifactCaller) dir() string {
	var meta hubCallMeta
	var extra struct {
		Roots *[]string `json:"magpie/roots"`
	}
	if len(c.meta) > 0 {
		json.Unmarshal(c.meta, &meta)
		json.Unmarshal(c.meta, &extra)
	}
	var dirs []string
	if extra.Roots != nil {
		dirs = rootDirs(*extra.Roots)
	} else if d := strings.TrimSpace(c.r.Header.Get(MemoryCwdHeader)); d != "" {
		dirs = []string{d}
	} else {
		dirs = rootDirs(hubRoots(c.r, meta))
	}
	if len(dirs) == 0 {
		return ""
	}
	return dirs[0]
}

// readLocal reads the file p names, relative to the caller's folder.
func (c *artifactCaller) readLocal(p string, most int64) ([]byte, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") || p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	if strings.HasPrefix(p, "file://") {
		if ds := rootDirs([]string{p}); len(ds) == 1 {
			p = ds[0]
		}
	}
	if !filepath.IsAbs(p) {
		if c.away() {
			// relative to the agent's own folder, which isn't here
			return nil, artifactFarFile(p)
		}
		d := c.dir()
		if d == "" {
			return nil, fmt.Errorf("%s: give its absolute path; the folder you work in isn't known here", p)
		}
		p = filepath.Join(d, p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		if c.away() && errors.Is(err, fs.ErrNotExist) {
			return nil, artifactFarFile(p)
		}
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a folder, not a file", p)
	}
	if fi.Size() > most {
		return nil, fmt.Errorf("%s is %d bytes; the most is %d", p, fi.Size(), most)
	}
	return os.ReadFile(p)
}

func (s *Server) artifactsMCPCall(r *http.Request, m mcpMessage) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		rememberMemoryClient(r, m)
		return map[string]any{
			"protocolVersion": mcpVersion(m),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": artifactsServerName, "title": "Magpie Artifacts", "version": Version},
			"instructions": "Artifacts are private pages on this computer, kept with every version, that the user opens in their browser. " +
				"When a page would be clearer than text, or the user would use the page rather than only read it (a report, a chart, a dashboard, a diagram, an explainer, a prototype, a small app), write it as HTML and publish it with " + artifactPublishTool + ", then give the user its address. " +
				"To change one, read it first with " + artifactReadTool + " and publish with its id. Delete one only when the user asks. " +
				"Files are read on the computer magpie runs on: an agent on another computer or in another container passes the page as html. " +
				"A page that keeps data (a poll, a tracker, a board, a sign-up sheet) declares capabilities {\"db\": {}, \"user\": {}} and reads it with `await claude.use(\"db\")`, as Claude's artifacts do; seed and read its data with " + artifactDataTool + ". " +
				"A page that is its own record (a checklist kept in the page) declares {\"artifact\": {}} and publishes itself with `(await claude.use(\"artifact\")).publish(html)`.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": artifactsMCPTools()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		var a artifactArgs
		if len(p.Arguments) > 0 {
			if err := json.Unmarshal(p.Arguments, &a); err != nil {
				return toolResult("the arguments don't read: "+err.Error(), true), nil
			}
		}
		if a.ID == "" {
			a.ID = a.URL
		}
		c := newArtifactCaller(r, p.Meta)
		var said string
		var err error
		switch p.Name {
		case artifactPublishTool:
			said, err = artifactPublish(c, a)
		case artifactReadTool:
			said, err = artifactRead(c, a)
		case artifactListTool:
			said, err = artifactList(c, a)
		case artifactOpenTool:
			said, err = artifactOpen(c, a)
		case artifactDeleteTool:
			said, err = artifactDelete(c, a)
		case artifactDataTool:
			said, err = artifactData(a)
		default:
			return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name + ": this server has " +
				strings.Join([]string{artifactPublishTool, artifactReadTool, artifactListTool, artifactOpenTool, artifactDataTool, artifactDeleteTool}, ", ")}
		}
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(said, false), nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + m.Method}
}

// artifactNamed is the artifact the arguments name, and the version.
func artifactNamed(a artifactArgs) (*artifact.Artifact, int, error) {
	if strings.TrimSpace(a.ID) == "" {
		return nil, 0, errors.New("name the artifact: its id or address")
	}
	id, n, ok := ArtifactRef(a.ID)
	if !ok {
		return nil, 0, fmt.Errorf("%q isn't an artifact's id or address", a.ID)
	}
	if a.Version > 0 {
		n = int(a.Version)
	}
	art, err := artifact.Get(id)
	if errors.Is(err, artifact.ErrNotFound) {
		return nil, 0, fmt.Errorf("there is no artifact %s on this computer (%s lists them)", id, artifactListTool)
	}
	if err != nil {
		return nil, 0, err
	}
	if n > 0 && art.Version(n) == nil {
		return nil, 0, fmt.Errorf("artifact %s has versions 1 to %d, not %d", id, art.Latest().N, n)
	}
	return art, n, nil
}

func artifactPublish(c *artifactCaller, a artifactArgs) (string, error) {
	p := artifact.Publish{Title: a.Title, Description: a.Description, Label: a.Label, Agent: c.agent, Base: int(a.Version)}
	caps, err := artifact.ParseCapabilities(a.Capabilities)
	if err != nil {
		return "", err
	}
	p.Capabilities = caps
	if strings.TrimSpace(a.ID) != "" {
		id, _, ok := ArtifactRef(a.ID)
		if !ok {
			return "", fmt.Errorf("%q isn't an artifact's id or address", a.ID)
		}
		p.ID = id
	}
	switch {
	case a.HTML != nil && a.FilePath != "":
		return "", errors.New("give the page as file_path or as html, not both")
	case a.HTML != nil:
		p.Page = []byte(*a.HTML)
	case a.FilePath != "":
		b, err := c.readLocal(a.FilePath, artifact.MaxPage)
		if err != nil {
			return "", err
		}
		p.Page = b
	case p.ID == "":
		return "", errors.New("give the page: file_path (a local .html file) or html")
	}
	if len(a.Files) > 0 {
		p.Files = map[string][]byte{}
	}
	for name, src := range a.Files {
		switch {
		case src == nil || src.remove:
			p.Files[name] = nil
		case src.inline && src.b64 != "":
			b, err := base64.StdEncoding.DecodeString(src.b64)
			if err != nil {
				return "", fmt.Errorf("%s: the base64 doesn't read: %v", name, err)
			}
			p.Files[name] = b
		case src.inline:
			p.Files[name] = []byte(src.text)
		default:
			b, err := c.readLocal(src.from, artifact.MaxFile)
			if err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			p.Files[name] = b
		}
		if p.Files[name] == nil && !(src == nil || src.remove) {
			p.Files[name] = []byte{}
		}
	}
	art, err := artifact.PublishPage(p)
	if errors.Is(err, artifact.ErrNotFound) {
		return "", fmt.Errorf("there is no artifact %s on this computer (%s lists them); leave id out to make a new one", p.ID, artifactListTool)
	}
	if errors.Is(err, artifact.ErrConflict) {
		return "", fmt.Errorf("%v: read it again with %s, make your change on what comes back, and publish with that version", err, artifactReadTool)
	}
	if err != nil {
		return "", err
	}
	v := art.Latest()
	var b strings.Builder
	if v.N == 1 {
		fmt.Fprintf(&b, "Published %q: %s\n", art.Title, c.link(art.ID, 0))
	} else {
		fmt.Fprintf(&b, "Published version %d of %q: %s\n", v.N, art.Title, c.link(art.ID, 0))
	}
	fmt.Fprintf(&b, "id: %s, version %d", art.ID, v.N)
	if len(v.Files) > 0 {
		fmt.Fprintf(&b, ", with %s", strings.Join(v.Files, ", "))
	}
	b.WriteString(".\n")
	if l := capsLine(v); l != "" {
		b.WriteString(l + "\n")
		if v.Capabilities.Has("db") {
			if docs, _, _ := artifact.Stats(art.ID); docs == 0 {
				fmt.Fprintf(&b, "Its data is empty: seed what the page needs with %s.\n", artifactDataTool)
			}
		}
	}
	sh, err := artifact.Shared(art.ID)
	switch {
	case err != nil:
		fmt.Fprintf(&b, "Whether it is shared doesn't read: %v. Give the user the address.", err)
	case sh == nil && c.away():
		b.WriteString("Give the user the address: a browser that reaches magpie there opens it, without a key.")
		if c.base != publicURL() {
			b.WriteString(" It is the address you called magpie at; if the user's browser reaches magpie at another, magpie's MAGPIE_PUBLIC_URL set to it gives links there.")
		}
	case sh == nil:
		b.WriteString("It is private to this computer: give the user the address.")
	default:
		// the user shared it: its link shows each new version
		ctx, cancel := context.WithTimeout(c.r.Context(), 3*time.Minute)
		sh2, err := artifact.ShareLatest(ctx, art.ID)
		cancel()
		if err != nil {
			fmt.Fprintf(&b, "The user shared it at %s, which still shows version %d: %v. Give the user the address, and say so.", sh.URL, sh.Version, err)
		} else {
			fmt.Fprintf(&b, "The user shared it: anyone with its link, %s, sees this version now. Give the user the address.", sh2.URL)
		}
	}
	return b.String(), nil
}

// sharedLine says where the artifact is shared, or "" when it isn't.
func sharedLine(id string) string {
	sh, err := artifact.Shared(id)
	if err != nil || sh == nil {
		return ""
	}
	return fmt.Sprintf("shared at %s (version %d)", sh.URL, sh.Version)
}

// artifactTextMost is the most of a file read_artifact gives as text.
const artifactTextMost = 1 << 20

func artifactRead(c *artifactCaller, a artifactArgs) (string, error) {
	art, n, err := artifactNamed(a)
	if err != nil {
		return "", err
	}
	v := art.Version(n)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\naddress: %s\nid: %s, version %d of %d", art.Title, c.link(art.ID, 0), art.ID, v.N, art.Latest().N)
	if art.Description != "" {
		fmt.Fprintf(&b, "\ndescription: %s", art.Description)
	}
	if l := sharedLine(art.ID); l != "" {
		fmt.Fprintf(&b, "\n%s", l)
	}
	if len(v.Files) > 0 {
		fmt.Fprintf(&b, "\nfiles: %s", strings.Join(v.Files, ", "))
	}
	if l := capsLine(v); l != "" {
		fmt.Fprintf(&b, "\n%s", l)
		if v.Capabilities.Has("db") {
			if docs, size, err := artifact.Stats(art.ID); err == nil {
				fmt.Fprintf(&b, "\ndata: %d documents, %d bytes (%s reads them)", docs, size, artifactDataTool)
			}
		}
		if list, u, err := artifact.Assets(art.ID); err == nil && len(list) > 0 {
			fmt.Fprintf(&b, "\nassets: %d, %d bytes, served at %s<id>", u.Files, u.Bytes, artifactBlobPath)
		}
	}
	where, _, err := artifact.File(art.ID, v.N, a.Path)
	if err != nil {
		return "", fmt.Errorf("%v (its files: %s)", err, strings.Join(append([]string{artifact.Page}, v.Files...), ", "))
	}
	name := a.Path
	if name == "" {
		name = artifact.Page
	}
	fi, err := os.Stat(where)
	if err != nil {
		return "", err
	}
	if fi.Size() > artifactTextMost && name != artifact.Page {
		fmt.Fprintf(&b, "\n\n%s is %d bytes, at %s", name, fi.Size(), where)
		return b.String(), nil
	}
	body, err := os.ReadFile(where)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(body) {
		fmt.Fprintf(&b, "\n\n%s is %d bytes of binary data, at %s", name, len(body), where)
		return b.String(), nil
	}
	fmt.Fprintf(&b, "\n\n%s:\n%s", name, body)
	return b.String(), nil
}

func artifactList(c *artifactCaller, a artifactArgs) (string, error) {
	all, err := artifact.List()
	if len(all) == 0 && err != nil {
		return "", err
	}
	limit := int(a.Limit)
	if limit <= 0 {
		limit = 25
	}
	words := strings.Fields(strings.ToLower(a.Query))
	var b strings.Builder
	shown := 0
	for _, art := range all {
		hay := strings.ToLower(art.Title + " " + art.Description)
		match := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if shown == limit {
			fmt.Fprintf(&b, "… and more: ask for a higher limit\n")
			break
		}
		shown++
		v := art.Latest()
		fmt.Fprintf(&b, "- %s — %s (id %s, version %d, updated %s)", art.Title, c.link(art.ID, 0), art.ID, v.N, art.Updated.Local().Format(time.DateTime))
		if art.Description != "" {
			fmt.Fprintf(&b, ": %s", art.Description)
		}
		if l := sharedLine(art.ID); l != "" {
			fmt.Fprintf(&b, "; %s", l)
		}
		b.WriteString("\n")
	}
	if shown == 0 {
		if len(words) > 0 {
			return "No artifact's title or description has those words.", nil
		}
		return "There are no artifacts on this computer yet.", nil
	}
	if err != nil {
		fmt.Fprintf(&b, "Some couldn't be read: %v\n", err)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func artifactOpen(c *artifactCaller, a artifactArgs) (string, error) {
	art, n, err := artifactNamed(a)
	if err != nil {
		return "", err
	}
	if c.away() {
		// a browser would open on the computer magpie runs on, not the user's
		return "", fmt.Errorf("magpie runs on another computer than yours, so it opens no browser for you: give the user the address, %s", c.link(art.ID, n))
	}
	u := ArtifactURL(art.ID, n)
	if err := openArtifactURL(u); err != nil {
		return "", fmt.Errorf("the browser didn't open: %v; the address is %s", err, u)
	}
	return fmt.Sprintf("Opened %q in the browser: %s", art.Title, u), nil
}

func artifactDelete(c *artifactCaller, a artifactArgs) (string, error) {
	art, _, err := artifactNamed(a)
	if err != nil {
		return "", err
	}
	sh, _ := artifact.Shared(art.ID)
	if err := artifact.Delete(art.ID); err != nil {
		return "", err
	}
	said := fmt.Sprintf("Deleted %q and its %d versions; %s no longer opens", art.Title, art.Latest().N, c.link(art.ID, 0))
	if sh != nil {
		said += ", and neither does its shared link, " + sh.URL
	}
	return said + ".", nil
}

// artifactPageRead: r reads an artifact's page, file or asset, which the
// page itself asks for with Origin: null (a module script, a fetch of its
// data), and corsGuard lets it.
func artifactPageRead(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && (strings.HasPrefix(r.URL.Path, ArtifactPagePath) || strings.HasPrefix(r.URL.Path, artifact.BlobPath))
}

// artifactPageAsked: r, from another computer, reads an artifact's page
// or one of its files, by the address an agent there was given (#7890).
// lanGuard lets it in without a key while magpie is on the network
// (remoteKeyed): the page's id, 128 random bits, is what opens it, as a
// shared copy's link on usemagpie.ai is. Its store (artifactAPIDir) and
// assets aren't reached this way.
func artifactPageAsked(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead || r.URL.RawPath != "" || len(callerKeys(r)) > 0 {
		return false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, ArtifactPagePath)
	return ok && rest != "" && !strings.Contains(rest, "/"+artifactAPIDir)
}

// artifactPage serves an artifact's page and files: at
// ArtifactPagePath<id>/ the newest version, at <id>@<n>/ version n, each
// file at its path after that. The browser on this computer runs the
// page with its runtime and store; one on another computer, while magpie
// is on the network, reads it as a shared copy's is read, each
// capability null and no token in it.
func (s *Server) artifactPage(w http.ResponseWriter, r *http.Request) {
	reader := !local(r)
	// from this computer, at its own name only: a page rebound to
	// 127.0.0.1 by its DNS (another Host) reads nothing here
	if reader && !remoteKeyed() || !reader && !loopbackHost(r.Host) {
		http.Error(w, "an artifact opens in a browser on this computer, at "+URL(), http.StatusForbidden)
		return
	}
	ref := r.PathValue("ref")
	id, n, ok := ArtifactRef(ref)
	if !ok || strings.Contains(ref, "/") {
		http.NotFound(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, ArtifactPagePath+ref+"/") {
		// the page's relative links are to its own folder
		http.Redirect(w, r, ArtifactPagePath+ref+"/", http.StatusFound)
		return
	}
	name := r.PathValue("path")
	where, art, err := artifact.File(id, n, name)
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			http.Error(w, "no such artifact, version or file here", http.StatusNotFound)
			return
		}
		http.Error(w, "the artifact can't be read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := os.ReadFile(where)
	if err != nil {
		http.Error(w, "the artifact can't be read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	page := name == "" || name == artifact.Page
	ct := artifactType(name)
	if page {
		ct = "text/html; charset=utf-8"
		body = artifact.Served(body)
	}
	h := w.Header()
	if strings.HasPrefix(ct, "text/html") {
		// the page's runtime, and the token to its store, go into what a
		// browser navigates to alone (artifactRuntimeFor)
		h.Add("Vary", "Sec-Fetch-Mode")
		if reader {
			body = artifact.WithRuntime(body, artifact.SharedRuntime())
		} else if artifactRuntimeFor(r) {
			rt, err := artifact.PageRuntime(art, n, ArtifactPagePath+ref+"/"+strings.TrimSuffix(artifactAPIDir, "/"))
			if err != nil {
				http.Error(w, "the artifact's runtime can't be made: "+err.Error(), http.StatusInternalServerError)
				return
			}
			rt.Home = ArtifactPagePath + id + "/"
			body = artifact.WithRuntime(body, rt)
		}
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", artifactPageCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// the page's own module scripts and fetches come from its opaque
	// origin (Origin: null)
	h.Set("Access-Control-Allow-Origin", "*")
	if n > 0 {
		// a version never changes
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", strconv.Quote(art.ID+"-"+strconv.Itoa(art.Latest().N)))
	}
	http.ServeContent(w, r, "", art.Latest().At, bytes.NewReader(body))
}

// artifactType is the Content-Type a file is served with, by its name.
func artifactType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".js", ".mjs", ".cjs":
		return "text/javascript; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".md", ".txt", ".csv", ".tsv", "":
		if ext == ".csv" {
			return "text/csv; charset=utf-8"
		}
		return "text/plain; charset=utf-8"
	case ".wasm":
		return "application/wasm"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
