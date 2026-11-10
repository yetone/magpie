package provider

import (
	"cmp"
	"container/list"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
)

// OpenCodeVersion is the oldest OpenCode release magpie says it is to
// OpenCode's gateway: 2.2.5, npm's latest @opencode/cli on 2026-11-01.
// Since 2026-10-26 Zen's free tier answers 429 FreeUsageLimitError ("Free
// models work in OpenCode") to OpenCode 1.19.0's User-Agent whatever the
// request says (#3491). A fixed version goes stale as OpenCode updates
// itself, so the one sent is what OpenCode's own updater says is latest
// (openCodeLatest), never older than this.
const OpenCodeVersion = "2.2.5"

// OpenCodeUserAgent is the User-Agent OpenCode sends on every protocol,
// opencode/${channel}/${version}/${name} (packages/core/src/app.ts), as
// @opencode/cli 2.2.5 sent it to Zen on 2026-11-01:
// opencode/latest/2.2.5/cli.
func OpenCodeUserAgent() string {
	return "opencode/latest/" + openCodeLatest() + "/cli"
}

// openCodeVersionURL is OpenCode's updater, which answers
// {"version":"x.y.z"} for its npm channel.
var openCodeVersionURL = openCodeUpdater

const openCodeUpdater = "https://opencode.ai/update/api/latest/cli/npm"

// openCodeVersionEvery is how often the updater is asked again.
const openCodeVersionEvery = 6 * time.Hour

var openCodeVersion struct {
	sync.Mutex
	v      string    // the latest read, "" before one is
	at     time.Time // when it was last asked
	asking bool
}

// openCodeLatest is the OpenCode version to send: the updater's latest
// as last read, or OpenCodeVersion. The updater is asked in the
// background at most every openCodeVersionEvery, never from a test
// binary unless a test points openCodeVersionURL at a fake.
func openCodeLatest() string {
	openCodeVersion.Lock()
	defer openCodeVersion.Unlock()
	v := cmp.Or(openCodeVersion.v, OpenCodeVersion)
	u := openCodeVersionURL
	if openCodeVersion.asking || time.Since(openCodeVersion.at) < openCodeVersionEvery || testing.Testing() && u == openCodeUpdater {
		return v
	}
	openCodeVersion.asking = true
	go func() {
		found := openCodeAskVersion(u, v)
		openCodeVersion.Lock()
		if found != "" {
			openCodeVersion.v = found
		}
		openCodeVersion.at, openCodeVersion.asking = time.Now(), false
		openCodeVersion.Unlock()
	}()
	return v
}

var openCodeVersionRe = lazyre.New(`^\d{1,4}\.\d{1,4}\.\d{1,6}$`)

// openCodeAskVersion is the latest version u says, or "" when it can't be
// read, isn't a release version or is older than OpenCodeVersion (a stale
// mirror). Asked as the zen-free plugin asks it: 3s, no redirect.
func openCodeAskVersion(u, current string) string {
	c := &http.Client{
		Timeout:       3 * time.Second,
		Transport:     http.DefaultClient.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirected") },
	}
	res, err := c.Get(u + "?current=" + url.QueryEscape(current))
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	var got struct {
		Version any `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&got) != nil {
		return ""
	}
	v, _ := got.Version.(string)
	if !openCodeVersionRe.MatchString(v) || !openCodeAtLeast(v, OpenCodeVersion) {
		return ""
	}
	return v
}

// openCodeAtLeast says whether version a is b or later.
func openCodeAtLeast(a, b string) bool {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		m, _ := strconv.Atoi(x[i])
		n, _ := strconv.Atoi(y[i])
		if m != n {
			return m > n
		}
	}
	return true
}

// OpenCodeAnonymousKey is the key OpenCode asks Zen with when no one is
// signed in to it, which Zen's free models answer.
const OpenCodeAnonymousKey = "public"

// OpenCodeClient makes a request to OpenCode's gateway (Zen or Go) carry
// what OpenCode 2.2.5 itself sends there, as captured on 2026-11-01
// (testdata/opencode-2.2.5-chat.json): its User-Agent (OpenCodeUserAgent),
// trace headers b3 and traceparent (one trace a session, a new span each
// request), x-opencode-client cli, the project dir is in
// (OpenCodeProject), and the session in x-opencode-session,
// x-opencode-session-id, x-session-affinity and x-session-id, with Accept
// */*. 1.x's x-opencode-request and its project "global" (which 2.x sends
// only from a git repository with no remote and no commit) don't go: Zen's
// free tier answers 429 FreeUsageLimitError to 1.x's shape (#3491). Zen
// reads the session id as OpenCode lays one out (openCodeSession); session
// names the conversation, whose turns keep one session id, which Zen routes
// and caches by. dir is the folder the agent works in, the home folder
// when "". The order 2.2.5 sends them in isn't kept: Go's HTTP/1 writes
// headers sorted and HTTP/2 in no fixed order.
func OpenCodeClient(h http.Header, session, dir string) {
	id := openCodeSession(session)
	b3, traceparent := openCodeTrace(id)
	h.Set("User-Agent", OpenCodeUserAgent())
	h.Set("b3", b3)
	h.Set("traceparent", traceparent)
	h.Set("x-opencode-client", "cli")
	h.Set("x-opencode-project", OpenCodeProject(dir))
	for _, k := range []string{"x-opencode-session", "x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		h.Set(k, id)
	}
	h.Del("x-opencode-request")
	h.Set("Accept", "*/*")
}

// OpenCodeURL says whether u is at OpenCode's gateway (Zen, Go or the
// inference API): opencode.ai or a name under it, whatever port the URL
// gives. What goes there is judged by the URL it goes to, not by the
// provider's first base (IsOpenCode), so a custom provider whose URL
// names a port, or one with only some of its endpoints there, is asked
// as OpenCode asks too.
func OpenCodeURL(u string) bool {
	pu, err := url.Parse(strings.TrimSpace(u))
	return err == nil && openCodeHost(pu.Hostname())
}

// openCodeHost says whether h (no port) is one of OpenCode's names.
func openCodeHost(h string) bool {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// OpenCodeSession names a session on a request to OpenCode's gateway that
// names none yet: since September 2026 it answers every request without
// x-opencode-session 400 "Request is missing x-opencode-session and
// cannot be routed efficiently" (luci on Discord). A call that is no
// conversation's turn — a model list, a System One decision, a key check —
// gets a session of its own, as OpenCode gives its own stateless calls
// one (packages/core/src/generate.ts: "Gateways require session
// attribution even for a stateless call"). A turn's is named before this,
// by OpenCodeClient, and stays; so does one the user set on the provider.
// Sign calls it, so every signed request has it.
func OpenCodeSession(h http.Header, u string) {
	if !OpenCodeURL(u) || h.Get("x-opencode-session") != "" {
		return
	}
	id := openCodeSession("")
	h.Set("x-opencode-session", id)
	h.Set("x-opencode-session-id", id)
}

// OpenCodeFree says whether model is one of OpenCode Zen's free ones on p,
// which Zen serves only to what looks like OpenCode: besides the headers
// OpenCodeClient sets, a streamed request offering OpenCode's core tools
// (OpenCodeTools, OpenCodeToolSchemas) with OpenCode's environment block
// in its system prompt (OpenCodeEnvironment), at the paths OpenCode 2
// asks (OpenCodeFreeURL), as checked against Zen in October 2026. The
// gateway asks them so whoever the client is (gateway/zenfree.go), and the
// Test button's probe so (zenFreeProbe). A free model is one whose id ends
// in -free, or one models.dev's opencode prices at nothing, as big-pickle,
// whose id doesn't say so (361 on Discord: it answered 403 "OpenCode's
// free tier can only be used from within OpenCode"); openCodeFreeIDs are
// such ones known without the catalog.
func (p Provider) OpenCodeFree(model string) bool {
	if !p.IsOpenCode() {
		return false
	}
	if strings.HasSuffix(model, "-free") || openCodeFreeIDs[model] {
		return true
	}
	pr, ok := catalog.PriceOf("opencode", model)
	return ok && pr.Input == 0 && pr.Output == 0
}

// openCodeFreeIDs are Zen's free models whose ids don't end in -free.
var openCodeFreeIDs = map[string]bool{"big-pickle": true, "grok-code": true}

// OpenCodeTools are OpenCode 2's core tools, which Zen's free tier wants
// offered, by name: shell, not 1.x's bash (what 2.2.5 sent on 2026-11-01).
var OpenCodeTools = []string{"edit", "glob", "grep", "read", "shell"}

// OpenCodeToolSchemas are the parameters OpenCode 2.2.5 gives its core
// tools, as it sent them to Zen (testdata/opencode-2.2.5-chat.json). Zen's
// free tier answers 403 FreeTierError to one offered with no parameters.
var OpenCodeToolSchemas = map[string]string{
	"edit":  `{"type":"object","properties":{"path":{"type":"string","description":"File to edit"},"oldString":{"type":"string","description":"Exact text to find and replace"},"newString":{"type":"string","description":"Text to replace oldString with (must differ from oldString)"},"replaceAll":{"type":"boolean","description":"Whether to replace every occurrence of oldString. When false, oldString must match exactly once. Defaults to false."}},"required":["path","oldString","newString"],"additionalProperties":false}`,
	"glob":  `{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern to match files against"},"path":{"type":"string","description":"Directory to search. Defaults to the current working directory."},"hidden":{"type":"boolean","description":"Include hidden files and directories (default: false)."},"limit":{"type":"integer","exclusiveMinimum":0,"description":"Maximum number of matching files to return (default: 100)"}},"required":["pattern"],"additionalProperties":false}`,
	"grep":  `{"type":"object","properties":{"pattern":{"type":"string","minLength":1,"description":"Regular expression or literal text to match in file contents."},"path":{"type":"string","description":"File or directory to search. Defaults to the current working directory."},"include":{"type":"string","description":"Glob pattern to filter files (for example, \"*.js\" or \"*.{ts,tsx}\")"},"literal":{"type":"boolean","description":"Treat ` + "`pattern`" + ` as exact text instead of a regular expression (default: false)."},"caseSensitive":{"type":"boolean","description":"Use case-sensitive matching (default: true)."},"limit":{"type":"integer","exclusiveMinimum":0,"description":"Maximum number of matching lines to return (default: 100)"}},"required":["pattern"],"additionalProperties":false}`,
	"read":  `{"type":"object","properties":{"path":{"type":"string","description":"File or directory to read"},"offset":{"type":"integer","minimum":0,"description":"The line or directory entry to start reading from (1-based)"},"limit":{"type":"integer","minimum":0,"description":"The maximum number of lines or directory entries to read (defaults to and capped at 2000)"}},"required":["path"],"additionalProperties":false}`,
	"shell": `{"type":"object","properties":{"command":{"type":"string","description":"Shell command string to execute"},"workdir":{"type":"string","description":"Working directory to execute the command in. Defaults to the current working directory. When possible, avoid changing directories in the command and set the working directory here instead."},"timeout":{"type":"integer","minimum":0,"description":"Timeout in milliseconds. Set to 0 to disable the timeout. Defaults to 120000 for foreground commands. Background commands have no timeout by default."},"background":{"type":"boolean","description":"Run the command in the background and return immediately (useful for dev servers and long-running builds). You do not need to use '&' at the end of the command when using this parameter. You will be notified when it completes. DO NOT poll for completion."}},"required":["command"],"additionalProperties":false}`,
}

// OpenCodeFreeURL is where a request on proto to Zen's free tier goes,
// given the URL it would go to at Zen (https://opencode.ai/zen/v1/…, or
// under /zen for Messages): where OpenCode 2 sends it, the base_url
// models.dev's v2 catalog (models.opencode.ai/experimental/v2.0/api.json,
// which 2.2.5 reads) gives every opencode model on 2026-11-01:
// /inference/openai/v1 for Chat and Responses, /inference/anthropic/v1 for
// Messages. 1.x's /inference/v1, which no OpenCode 2 request goes to, is
// moved there too. Gemini, on which Zen gives nothing free, goes under
// /inference as before. Any other URL, Go's (/zen/go) among them, is u.
func OpenCodeFreeURL(u string, proto Protocol) string {
	pu, err := url.Parse(strings.TrimSpace(u))
	if err != nil || pu.Scheme == "" || !openCodeHost(pu.Hostname()) {
		return u
	}
	rest, ok := strings.CutPrefix(pu.Path, "/zen")
	if !ok {
		rest, ok = strings.CutPrefix(pu.Path, "/inference")
	}
	under := func(dir string) bool { return rest == dir || strings.HasPrefix(rest, dir+"/") }
	if !ok || rest != "" && rest[0] != '/' || under("/go") || under("/openai") || under("/anthropic") || under("/google") || under("/systemone") {
		return u
	}
	switch proto {
	case Chat, Responses:
		pu.Path = "/inference/openai" + rest
	case Anthropic:
		pu.Path = "/inference/anthropic" + rest
	default:
		pu.Path = "/inference" + rest
	}
	return pu.String()
}

// OpenCodeStub describes a tool offered only for Zen's free tier to see,
// which the client doesn't have.
const OpenCodeStub = "Not available in this session. Never call this tool."

// zenFreeProbe is the Test button's smallest request, body, asked as Zen's
// free tier wants it: streamed, offering OpenCode's core tools, which say
// they aren't to be called (strict false on Chat, as OpenCode's are), with
// OpenCode's environment block for the home folder.
func zenFreeProbe(proto Protocol, body string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return body
	}
	var tools []any
	for _, n := range OpenCodeTools {
		var schema map[string]any
		_ = json.Unmarshal([]byte(OpenCodeToolSchemas[n]), &schema)
		switch proto {
		case Chat:
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": n, "description": OpenCodeStub, "parameters": schema, "strict": false}})
		case Responses:
			tools = append(tools, map[string]any{"type": "function", "name": n, "description": OpenCodeStub, "parameters": schema})
		default:
			tools = append(tools, map[string]any{"name": n, "description": OpenCodeStub, "input_schema": schema})
		}
	}
	m["stream"], m["tools"] = true, tools
	env := OpenCodeEnvironment("")
	switch proto {
	case Chat:
		msgs, _ := m["messages"].([]any)
		m["messages"] = append([]any{map[string]any{"role": "system", "content": env}}, msgs...)
		m["stream_options"] = map[string]any{"include_usage": true}
	case Responses:
		m["instructions"] = env
	default:
		m["system"] = env
	}
	b, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return string(b)
}

// openCodeHasEnvironment matches OpenCode's environment block: <env>, a
// Working directory: line and </env>.
var openCodeHasEnvironment = lazyre.New(`<env>[\s\S]*Working directory:[\s\S]*</env>`)

// OpenCodeHasEnvironment says whether a system prompt already carries
// OpenCode's environment block, as OpenCode's own does.
func OpenCodeHasEnvironment(system string) bool {
	return openCodeHasEnvironment.MatchString(system)
}

// OpenCodeEnvironment is OpenCode 2.2.5's environment block, word for word
// (packages/core/src/instructions/builtins.ts, as 2.2.5 sent it to Zen on
// 2026-11-01), said for dir, the home folder when "". Zen's free tier
// serves a request as OpenCode's own only when a system message carries
// one; without it a request gets a small allowance a day, then 429
// FreeUsageLimitError ("Free models work in OpenCode"), and fixed text in
// it that only magpie would send (a directory "unknown") is a fingerprint.
func OpenCodeEnvironment(dir string) string {
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	local := openCodeLocalDir(dir)
	root, _, isRepo := openCodeRepository(local)
	workspace := dir
	if isRepo && root != local {
		workspace = root
	}
	git := "no"
	if isRepo {
		git = "yes"
	}
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	tmp := os.TempDir()
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	return strings.Join([]string{
		"Here is some useful information about the environment you are running in:",
		"<env>",
		"  Working directory: " + dir,
		"  Workspace root folder: " + workspace,
		"  Is directory a git repo: " + git,
		"  Platform: " + platform,
		"  Prefer " + filepath.Join(tmp, "opencode") + " over generic system temporary directories such as /tmp; it is pre-created and approved for external access.",
		"</env>",
	}, "\n")
}

// OpenCodeProject is OpenCode's project id for dir (packages/core/src/
// project.ts), the home folder when "": in a git repository with a remote,
// sha1 of git-remote:host/path; anywhere else sha1 of directory: and the
// repository's root, or dir. A repository with no remote, which OpenCode
// names by its root commit, is named by its folder, as the zen-free plugin
// names it: the same shape, and no git to run.
func OpenCodeProject(dir string) string {
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	local := openCodeLocalDir(dir)
	root, remote, isRepo := openCodeRepository(local)
	var key string
	switch {
	case remote != "":
		key = "git-remote:" + remote
	case isRepo:
		key = "directory:" + root
	default:
		key = "directory:" + local
	}
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])
}

// openCodeLocalDir is dir with ~ for the home folder and its links
// resolved, as far as they resolve.
func openCodeLocalDir(dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = home + dir[1:]
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

var (
	openCodeRemoteRe = lazyre.New(`(?m)^\s*\[remote\s+"([^"]+)"\]([^\[]*)`)
	openCodeURLRe    = lazyre.New(`(?m)^\s*url\s*=\s*(.+)$`)
	openCodeGitdirRe = lazyre.New(`(?m)^gitdir:\s*(.+)$`)
	openCodeSchemeRe = lazyre.New(`(?i)^[a-z][a-z0-9+.-]*://`)
	openCodeSCPRe    = lazyre.New(`^([^@/:]+@)?([^/:]+):(.+)$`)
	openCodeDotGitRe = lazyre.New(`\.git/?$`)
)

// openCodeRepository is the git repository dir is in, as OpenCode finds
// it: the nearest .git up from dir (a worktree's file naming its gitdir
// and that one's commondir), its root, and the remote OpenCode names it by
// (origin, else the first one listed) as host/path, "" when none.
func openCodeRepository(dir string) (root, remote string, ok bool) {
	for d := dir; ; {
		dotGit := filepath.Join(d, ".git")
		if fi, err := os.Stat(dotGit); err == nil {
			common := dotGit
			if !fi.IsDir() {
				b, err := os.ReadFile(dotGit)
				if err != nil {
					return d, "", true
				}
				m := openCodeGitdirRe.FindStringSubmatch(string(b))
				if m == nil || strings.TrimSpace(m[1]) == "" {
					return "", "", false
				}
				own := strings.TrimSpace(m[1])
				if !filepath.IsAbs(own) {
					own = filepath.Join(d, own)
				}
				common = own
				if c, err := os.ReadFile(filepath.Join(own, "commondir")); err == nil {
					shared := strings.TrimSpace(string(c))
					if !filepath.IsAbs(shared) {
						shared = filepath.Join(own, shared)
					}
					common = shared
				}
			}
			config, err := os.ReadFile(filepath.Join(common, "config"))
			if err != nil {
				return d, "", true
			}
			var first, origin string
			for i, m := range openCodeRemoteRe.FindAllStringSubmatch(string(config), -1) {
				var u string
				if um := openCodeURLRe.FindStringSubmatch(m[2]); um != nil {
					u = um[1]
				}
				if i == 0 {
					first = u
				}
				if m[1] == "origin" {
					origin = u
					break
				}
			}
			u := first
			if origin != "" || openCodeRemoteRe.MatchString(string(config)) && strings.Contains(string(config), `"origin"`) {
				u = origin
			}
			return d, openCodeRemotePath(u), true
		}
		up := filepath.Dir(d)
		if up == d {
			return "", "", false
		}
		d = up
	}
}

// openCodeRemotePath is a remote's URL as OpenCode names a project by it:
// host/path, the host lowercased, without .git; "" for a file: URL or one
// it can't read.
func openCodeRemotePath(value string) string {
	in := strings.TrimSpace(value)
	if in == "" {
		return ""
	}
	parts := func(host, name string) string {
		p := strings.TrimLeft(name, "/")
		p = openCodeDotGitRe.ReplaceAllString(p, "")
		p = strings.TrimRight(p, "/")
		if host == "" || p == "" {
			return ""
		}
		return strings.ToLower(host) + "/" + p
	}
	if openCodeSchemeRe.MatchString(in) {
		u, err := url.Parse(in)
		if err != nil || strings.EqualFold(u.Scheme, "file") {
			return ""
		}
		return parts(u.Hostname(), u.EscapedPath())
	}
	if m := openCodeSCPRe.FindStringSubmatch(in); m != nil {
		return parts(m[2], m[3])
	}
	return ""
}

// openCodeTraces is the trace each session's requests are in, the last
// openCodeKeep sessions'.
var openCodeTraces = struct {
	sync.Mutex
	of    map[string]*list.Element // session → its element in order
	order *list.List               // of openCodeKept (seed: session, id: trace)
}{of: map[string]*list.Element{}, order: list.New()}

// openCodeTrace is the b3 and traceparent headers OpenCode's tracer sends
// with a request in session: one trace a session, a new span each request.
func openCodeTrace(session string) (b3, traceparent string) {
	hexOf := func(n int) string {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			copy(b, randomUUID())
		}
		return hex.EncodeToString(b)
	}
	trace := hexOf(16)
	openCodeTraces.Lock()
	if e, ok := openCodeTraces.of[session]; ok {
		openCodeTraces.order.MoveToBack(e)
		trace = e.Value.(openCodeKept).id
	} else {
		if openCodeTraces.order.Len() >= openCodeKeep {
			oldest := openCodeTraces.order.Front()
			openCodeTraces.order.Remove(oldest)
			delete(openCodeTraces.of, oldest.Value.(openCodeKept).seed)
		}
		openCodeTraces.of[session] = openCodeTraces.order.PushBack(openCodeKept{session, trace})
	}
	openCodeTraces.Unlock()
	span := hexOf(8)
	return trace + "-" + span + "-1-" + hexOf(8), "00-" + trace + "-" + span + "-01"
}

var openCodeIDRe = lazyre.New(`^[a-z]{3}_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// openCodeIDs is the clock and counter ids are made with, and the session
// id made for each conversation.
var openCodeIDs = struct {
	sync.Mutex
	last, counter int64
	sessions      map[string]*list.Element // seed → its element in order
	order         *list.List               // of openCodeKept, the least recently used first
}{sessions: map[string]*list.Element{}, order: list.New()}

type openCodeKept struct{ seed, id string }

// openCodeKeep is how many conversations' session ids are kept.
const openCodeKeep = 1000

// openCodeNewID is an id as OpenCode makes one (packages/schema/src/
// identifier.ts): prefix_, 12 hex digits of the time in milliseconds times
// 0x1000 plus a count of the ids made in that millisecond (bit-inverted
// for a session, so the newest sorts first), then 14 random base62 ones.
func openCodeNewID(prefix string, descending bool, now time.Time) string {
	ms := now.UnixMilli()
	openCodeIDs.Lock()
	if ms != openCodeIDs.last {
		openCodeIDs.last, openCodeIDs.counter = ms, 0
	}
	openCodeIDs.counter++
	current := uint64(ms)*0x1000 + uint64(openCodeIDs.counter)
	openCodeIDs.Unlock()
	if descending {
		current = ^current
	}
	var b [14]byte
	if _, err := rand.Read(b[:]); err != nil {
		copy(b[:], randomUUID())
	}
	id := []byte(fmt.Sprintf("%s_%012x", prefix, current&0xffffffffffff))
	for _, c := range b {
		id = append(id, base62[int(c)%len(base62)])
	}
	return string(id)
}

// openCodeRealSession says whether id is a session id OpenCode could have
// made: of its shape, with a small count in its low 12 bits (inverted).
// Zen's free tier answers 403 FreeTierError to one that isn't, a hash cut
// to the same shape among them (c9cu on Discord).
func openCodeRealSession(id string) bool {
	if !strings.HasPrefix(id, "ses_") || !openCodeIDRe.MatchString(id) {
		return false
	}
	low, err := strconv.ParseUint(id[13:16], 16, 16)
	return err == nil && 0xfff-low < 16
}

// openCodeSession is the session id sent for the conversation seed names:
// seed itself when it is one OpenCode could have made, else one made the
// first time seed is seen and kept for its next turns (the last
// openCodeKeep conversations' are kept), or a new one when there is no
// seed.
func openCodeSession(seed string) string {
	if openCodeRealSession(seed) {
		return seed
	}
	if seed == "" {
		return openCodeNewID("ses", true, time.Now())
	}
	id := openCodeNewID("ses", true, time.Now()) // made first: it takes the lock
	openCodeIDs.Lock()
	defer openCodeIDs.Unlock()
	if e, ok := openCodeIDs.sessions[seed]; ok {
		openCodeIDs.order.MoveToBack(e)
		return e.Value.(openCodeKept).id
	}
	if openCodeIDs.order.Len() >= openCodeKeep {
		oldest := openCodeIDs.order.Front()
		openCodeIDs.order.Remove(oldest)
		delete(openCodeIDs.sessions, oldest.Value.(openCodeKept).seed)
	}
	openCodeIDs.sessions[seed] = openCodeIDs.order.PushBack(openCodeKept{seed, id})
	return id
}
