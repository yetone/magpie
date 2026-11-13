// Package imagemcp is `magpie mcp image`: a stdio MCP server an agent is
// given, when it is picked for it in the Library, to make images with the
// model the Settings' Image generation names, and videos with the first
// video model the gateway has (gateway.AutoVideomaker), or with the model
// a call names, among those the gateway lists for it. It asks the
// gateway, which knows the providers, and saves what comes back in the
// project.
package imagemcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/settings"
)

// Folder is where images go in the project when no path is given, and
// VideoFolder where videos do.
const (
	Folder      = "generated-images"
	VideoFolder = "generated-videos"
)

// The tools and their arguments are the gateway's image MCP server's too
// (gateway.ImageMCPPath), but where to save, which only this server can.
const (
	tool       = gateway.GenerateImageTool
	videoTool  = gateway.GenerateVideoTool
	resultTool = gateway.GenerationResultTool
)

// withPath is props with path, where to save, said as what.
func withPath(props map[string]any, what string) map[string]any {
	props["path"] = map[string]any{"type": "string", "description": what}
	return props
}

// answerWithin is how long a tool call waits for its image or video before
// it answers that it is still being made, with the job's id to ask after
// it by. Agents give a tool call as little as 30 s (#575: ZCode's "Tool
// execution timed out after 30000ms", the MCP SDK's 60 s "Request timed
// out") and then call it again, though an image model often takes a
// minute or more: so no call of this server outlasts the shortest of them,
// and a job goes on after its call has answered, saving what it makes.
// progressEvery is how often a call that was given a progressToken is
// told the job is still going, for clients that wait longer on progress.
var (
	answerWithin  = 20 * time.Second
	progressEvery = 5 * time.Second
)

// How often a video is asked after, and how long it is waited for.
var (
	videoPollEvery = 3 * time.Second
	videoWait      = 10 * time.Minute
)

var videoSchema = map[string]any{
	"name": videoTool,
	"description": "Generate a short video from a text prompt, optionally starting from an image to animate, with a video model set up in Magpie " +
		"(a Grok subscription's Grok Imagine, Volcengine Ark Agent Plan's Seedance, a MiniMax API key's MiniMax H3, another Magpie's, or a provider whose model list marks video models). It takes from ten seconds to a few minutes, then the video is saved as an mp4 file in the project (" + VideoFolder +
		"/ unless path says where) and its path is returned; when it is not ready within about 20 seconds, the answer is an id to wait on with " + resultTool + " instead. Describe the subject, the motion and the camera: what moves, how, and what the shot is.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": withPath(gateway.VideoToolProps("a file path (relative to the project or absolute) or an http(s) URL", "file paths or http(s) URLs"),
			"Where to save it: a file (e.g. assets/intro.mp4) or a folder, relative to the project or absolute. Never written over."),
		"required": []string{"prompt"},
	},
}

var resultSchema = map[string]any{
	"name": resultTool,
	"description": "Wait for an image or video that " + tool + " or " + videoTool + " said is still being made, by the id it gave, and get where it was saved. " +
		"It answers within about " + strconv.Itoa(int(answerWithin/time.Second)) + " seconds: if it is still not ready, call this again with the same id (don't generate it again).",
	"inputSchema": map[string]any{
		"type":       "object",
		"properties": gateway.ResultToolProps(),
		"required":   []string{"id"},
	},
}

var schema = map[string]any{
	"name": tool,
	"description": "Generate an image from a text prompt, or edit/combine images given as reference_images, with the image model set in Magpie. " +
		"The image is saved as a file in the project (" + Folder + "/ unless path says where) and its path is returned (when it is not ready within about 20 seconds, the answer is an id to wait on with " + resultTool + " instead); " +
		"reference it from code or docs by that path. Write a detailed prompt: subject, style, composition, colours, any text to render exactly.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": withPath(gateway.ImageToolProps("file paths or http(s) URLs"),
			"Where to save it: a file (e.g. assets/hero.png) or a folder, relative to the project or absolute. With n > 1 a number is added to the name."),
		"required": []string{"prompt"},
	},
}

type rpc struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type server struct {
	gateway string
	client  *http.Client

	outMu sync.Mutex
	enc   *json.Encoder

	mu       sync.Mutex
	agent    string // the User-Agent, from the client's name
	roots    bool   // the client can say its roots
	pending  map[string]chan rpc
	nextCall int
	jobs     map[string]*job
	nextJob  int
	running  sync.WaitGroup
}

// caller is who a tool call is made for: the agent that started this
// server, or, behind magpie's MCP hub (one server for every agent), the
// agent the hub's call carries in its _meta (gateway.HubRootsMeta).
type caller struct {
	hub   bool     // the call came through the hub
	roots []string // the hub's caller's roots, file URIs
	agent string   // the User-Agent the call's requests go under
}

// job is one image or video being made: it runs to its end whether or not
// the call that started it is still waiting.
type job struct {
	id, tool string
	started  time.Time
	done     chan struct{}
	text     string
	err      error
	// model is the one the gateway named, provider/model: set by run
	model string
}

// start runs run as a job of tool.
func (s *server) start(tool string, run func(*job) (string, error)) *job {
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs = map[string]*job{}
	}
	s.nextJob++
	j := &job{id: fmt.Sprintf("%s-%d-%d", strings.TrimPrefix(tool, "generate_"), os.Getpid(), s.nextJob), tool: tool, started: time.Now(), done: make(chan struct{})}
	s.jobs[j.id] = j
	s.mu.Unlock()
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		j.text, j.err = run(j)
		close(j.done)
	}()
	return j
}

// await waits up to answerWithin for j, telling the client it is still
// going every progressEvery when it gave a progressToken; false when j is
// still running.
func (s *server) await(j *job, progressToken json.RawMessage) bool {
	limit := time.NewTimer(answerWithin)
	defer limit.Stop()
	tick := time.NewTicker(progressEvery)
	defer tick.Stop()
	for {
		select {
		case <-j.done:
			return true
		case <-limit.C:
			return false
		case <-tick.C:
			if len(progressToken) > 0 && string(progressToken) != "null" {
				s.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{
					"progressToken": progressToken, "progress": int(time.Since(j.started) / time.Second),
					"message": "still being made (" + j.id + ")"}})
			}
		}
	}
}

// stillGoing is the answer of a call whose job outlasted it.
func stillGoing(j *job) string {
	what, where := "image", Folder
	if j.tool == videoTool {
		what, where = "video", VideoFolder
	}
	return fmt.Sprintf("The %s is still being made (%d s so far; it can take a few minutes). It goes on without this call and will be saved in the project (%s/ unless path said where). "+
		"Don't ask for it again: call %s with id %q to wait for it and get its path.\n", what, int(time.Since(j.started)/time.Second), where, resultTool, j.id)
}

// ToolTimeout is how long a job of this server may run: generate_video
// waits up to videoWait for its video, and a minute more. A call answers
// within answerWithin all the same; the agents whose config takes it
// (Codex, Goose) are still given it, as a call's ceiling.
func ToolTimeout() time.Duration { return videoWait + time.Minute }

// callLongest is the most a call of this server to the gateway may take,
// and how long a job left running is waited for once the agent has gone.
// The gateway gives a vendor up to Settings' Image generation timeout to
// draw (settings.ImageTimeoutMost minutes at the most), and may then fetch
// what it drew for as long again (gateway.FetchImage): a call is cut only
// past both. The gateway ends it at the user's own timeout; this server
// may run where the settings aren't (an agent in WSL), so it never cuts a
// drawing the setting allows (neighbads on X).
func callLongest() time.Duration {
	return max(ToolTimeout(), 2*settings.ImageTimeoutMost*time.Minute+time.Minute)
}

// Run serves MCP on stdin/stdout until stdin closes.
func Run(args []string) error {
	if len(args) > 0 && args[0] != "image" {
		return fmt.Errorf("unknown MCP server %q (there are: image, browser, search, vision)", args[0])
	}
	if len(args) > 1 {
		w, err := options(args[1:])
		if err != nil {
			return err
		}
		wsl = w
	}
	base := strings.TrimRight(os.Getenv("MAGPIE_GATEWAY"), "/")
	if base == "" {
		base = gateway.URL()
	}
	s := &server{gateway: base, client: &http.Client{Timeout: callLongest()}, enc: json.NewEncoder(os.Stdout), agent: gateway.DrawAgent, pending: map[string]chan rpc{}}
	err := s.serve(os.Stdin)
	// what is still being made is saved, though the agent has gone
	done := make(chan struct{})
	go func() { s.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(callLongest()):
	}
	return err
}

func (s *server) send(v any) {
	s.outMu.Lock()
	_ = s.enc.Encode(v)
	s.outMu.Unlock()
}

func (s *server) serve(r io.Reader) error {
	in := bufio.NewScanner(r)
	in.Buffer(make([]byte, 64<<10), 16<<20)
	for in.Scan() {
		var m rpc
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "" {
			// the answer to a request of ours (roots/list)
			s.mu.Lock()
			ch := s.pending[string(m.ID)]
			delete(s.pending, string(m.ID))
			s.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if len(m.ID) == 0 {
			continue // a notification
		}
		if m.Method == "initialize" {
			// before anything after it: the agent's name goes on its calls
			s.handle(m)
			continue
		}
		go s.handle(m)
	}
	return in.Err()
}

func (s *server) handle(req rpc) {
	var result any
	var rpcErr any
	began := time.Now()
	var called *gateway.MCPCalled // a tool call, for the gateway's request log
	var viaHub bool               // the hub's own row of the call is its request log's
	var j *job
	switch req.Method {
	case "initialize":
		var p struct {
			Capabilities struct {
				Roots json.RawMessage `json:"roots"`
			} `json:"capabilities"`
			ClientInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		s.roots = len(p.Capabilities.Roots) > 0 && string(p.Capabilities.Roots) != "null"
		if name := strings.ReplaceAll(strings.TrimSpace(p.ClientInfo.Name), " ", "-"); name != "" {
			// the call is the agent's: usage names it, not magpie
			if p.ClientInfo.Version != "" {
				name += "/" + p.ClientInfo.Version
			}
			s.agent = name + " " + gateway.DrawAgent
		}
		s.mu.Unlock()
		result = map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "magpie-image", "version": "1"},
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		props := s.modelProps()
		result = map[string]any{"tools": []any{withModel(schema, props[tool]), withModel(videoSchema, props[videoTool]), resultSchema}}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      struct {
				ProgressToken json.RawMessage `json:"progressToken"`
				Roots         *[]string       `json:"magpie/roots"`
				Client        string          `json:"magpie/client"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || (p.Name != tool && p.Name != videoTool && p.Name != resultTool) {
			rpcErr = map[string]any{"code": -32602, "message": "unknown tool " + p.Name}
			break
		}
		called = &gateway.MCPCalled{Tool: p.Name}
		s.mu.Lock()
		c := caller{agent: s.agent}
		s.mu.Unlock()
		if p.Meta.Roots != nil {
			c.hub, c.roots = true, *p.Meta.Roots
			if name := strings.TrimSpace(p.Meta.Client); name != "" {
				c.agent = name + " " + gateway.DrawAgent
			}
		}
		viaHub = c.hub
		if p.Name == resultTool {
			var a struct {
				ID string `json:"id"`
			}
			json.Unmarshal(p.Arguments, &a)
			s.mu.Lock()
			j = s.jobs[strings.TrimSpace(a.ID)]
			s.mu.Unlock()
			if j == nil {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("no image or video is being made as %q (magpie-image may have been restarted): look in the project's %s/ or %s/, or generate it again", a.ID, Folder, VideoFolder)}}, "isError": true}
				break
			}
		} else {
			generate := s.generate
			if p.Name == videoTool {
				generate = s.film
			}
			j = s.start(p.Name, func(j *job) (string, error) { return generate(p.Arguments, &j.model, c) })
		}
		if !s.await(j, p.Meta.ProgressToken) {
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": stillGoing(j)}}}
			break
		}
		res := map[string]any{"content": []any{map[string]any{"type": "text", "text": j.text}}}
		if j.err != nil {
			res = map[string]any{"content": []any{map[string]any{"type": "text", "text": j.err.Error()}}, "isError": true}
		}
		if viaHub && j.model != "" {
			// the hub's row of the call names the model, as report does
			res["_meta"] = map[string]any{gateway.HubModelMeta: j.model}
		}
		result = res
	default:
		rpcErr = map[string]any{"code": -32601, "message": "method not found"}
	}
	out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		out["error"] = rpcErr
	} else {
		out["result"] = result
	}
	s.send(out)
	if called != nil && !viaHub {
		called.Millis = time.Since(began).Milliseconds()
		if j != nil {
			select {
			case <-j.done:
				called.Model = j.model
			default: // still being made: the model isn't known yet
			}
		}
		if res, _ := result.(map[string]any); res["isError"] == true {
			called.Error = "failed"
			if c, _ := res["content"].([]any); len(c) > 0 {
				if t, _ := c[0].(map[string]any)["text"].(string); t != "" {
					called.Error = t
				}
			}
		}
		s.report(*called)
	}
}

// report tells the gateway of a tool call this server answered, for its
// request log (gateway.MCPCalledMethod): in the background, after the
// answer, and a gateway that can't take it (not running, an older magpie)
// is let be. The call's arguments are never sent.
func (s *server) report(c gateway.MCPCalled) {
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		s.ask(gateway.MCPCalledMethod, c)
	}()
}

type args struct {
	Prompt     string   `json:"prompt"`
	Model      string   `json:"model"`
	Path       string   `json:"path"`
	Size       string   `json:"size"`
	N          int      `json:"n"`
	References []string `json:"reference_images"`
	Quality    string   `json:"quality"`
	Complexity string   `json:"complexity"`
	Background string   `json:"background"`
}

// generate asks the gateway for the images and saves them; what it says is
// where they are. used, when not nil, is set to the model the gateway
// named, provider/model.
func (s *server) generate(raw json.RawMessage, used *string, c caller) (string, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	a.N = max(1, min(a.N, 4))
	project := s.project(c)
	// the images themselves, not URLs to fetch: the gateway fetches one a
	// vendor keeps through the vendor's own proxy, the way it just reached
	// it (#1576)
	req := map[string]any{"prompt": a.Prompt, "n": a.N, "response_format": "b64_json"}
	if strings.TrimSpace(a.Model) != "" {
		m, err := s.mediaModel(tool, a.Model)
		if err != nil {
			return "", err
		}
		req["model"] = m
	}
	for k, v := range map[string]string{"size": a.Size, "quality": a.Quality, "complexity": a.Complexity, "background": a.Background} {
		if v != "" {
			req[k] = v
		}
	}
	endpoint := "/v1/images/generations"
	if len(a.References) > 0 {
		endpoint = "/v1/images/edits"
		var images []map[string]string
		for _, ref := range a.References {
			src, err := reference(project, ref)
			if err != nil {
				return "", err
			}
			images = append(images, map[string]string{"image_url": src})
		}
		req["images"] = images
	}
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, s.gateway+endpoint, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+gateway.Token)
	r.Header.Set("User-Agent", c.agent)
	res, err := s.client.Do(r)
	if err != nil {
		return "", fmt.Errorf("magpie isn't answering at %s (is it running?): %w", s.gateway, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	var out struct {
		Model string `json:"model"`
		Data  []struct {
			B64     string `json:"b64_json"`
			URL     string `json:"url"`
			Mime    string `json:"mime_type"`
			Revised string `json:"revised_prompt"`
		} `json:"data"`
		Text  string `json:"text"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(data, &out)
	if used != nil {
		*used = out.Model
	}
	if res.StatusCode != 200 {
		msg := strings.TrimSpace(string(data))
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return "", fmt.Errorf("no image: %s", msg)
	}
	if len(out.Data) == 0 {
		return "", errors.New("no image came back")
	}
	// the reply's bytes are the images' base64 a second time: what is read
	// from here is the strings parsed out of them, each dropped once decoded
	defer debug.FreeOSMemory()
	// an image that was drawn, and paid for, is never dropped unsaid: one
	// that can't be had is answered by where the vendor keeps it, beside
	// those saved (#1576)
	var saved, revised, lost []string
	for i, d := range out.Data {
		img, mt := []byte(nil), d.Mime
		switch {
		case d.B64 != "":
			if img, err = base64.StdEncoding.DecodeString(d.B64); err != nil {
				lost = append(lost, fmt.Sprintf("image %d came back broken: %v", i+1, err))
				continue
			}
			out.Data[i].B64 = ""
		case d.URL != "":
			if img, mt, err = gateway.FetchImage(context.Background(), gateway.PublicClient(), d.URL); err != nil {
				lost = append(lost, fmt.Sprintf("image %d was drawn but couldn't be downloaded (%v); the vendor keeps it at %s for a while: download it from there", i+1, err, d.URL))
				continue
			}
		default:
			continue
		}
		// what the bytes are, over what the vendor says: a model asked for
		// a PNG may well send a JPEG
		if sniffed := http.DetectContentType(img); strings.HasPrefix(sniffed, "image/") {
			mt = sniffed
		}
		path, err := target(project, a.Path, a.Prompt, extOf(mt), i, len(out.Data))
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, img, 0o644); err != nil {
			return "", err
		}
		saved = append(saved, path)
		if d.Revised != "" {
			revised = append(revised, d.Revised)
		}
	}
	if len(saved) == 0 && len(lost) > 0 {
		return "", errors.New(strings.Join(lost, "\n"))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Generated with %s, saved to:\n", out.Model)
	for _, p := range saved {
		fmt.Fprintf(&b, "- %s\n", shownPath(p))
	}
	for _, l := range lost {
		fmt.Fprintf(&b, "Not saved: %s\n", l)
	}
	if len(revised) > 0 {
		fmt.Fprintf(&b, "The model drew from this prompt: %s\n", revised[0])
	}
	if t := strings.TrimSpace(out.Text); t != "" {
		fmt.Fprintf(&b, "The model said: %s\n", t)
	}
	return b.String(), nil
}

// project is the folder images are saved in: the client's first root when
// it can say, else the folder the agent started magpie in; behind the hub,
// the first of the roots the call carries. The home folder or / isn't a
// project: ~/Pictures/Magpie is used then (the distro's, for an agent in
// WSL), as it is when the hub's caller has no roots, since this server
// was started in the home folder for every agent.
func (s *server) project(c caller) string {
	if c.hub {
		if dir := rootOf(c.roots); dir != "" {
			return dir
		}
	} else if dir := s.root(); dir != "" {
		return dir
	}
	var dir string
	if !c.hub {
		dir, _ = os.Getwd()
	}
	if wsl != nil && wsl.home != "" {
		if d := wsl.shown(dir); d == "" || d == "/" || strings.TrimSuffix(d, "/") == strings.TrimSuffix(wsl.home, "/") {
			return wsl.local(strings.TrimSuffix(wsl.home, "/") + "/Pictures/Magpie")
		}
	}
	home, _ := os.UserHomeDir()
	if dir == "" || dir == "/" || dir == home || filepath.Dir(dir) == dir {
		return filepath.Join(home, "Pictures", "Magpie")
	}
	return dir
}

// root asks the client for its roots, the first of which on disk is the
// project; "" when it has none or doesn't answer.
func (s *server) root() string {
	s.mu.Lock()
	if !s.roots {
		s.mu.Unlock()
		return ""
	}
	s.nextCall++
	id := fmt.Sprintf(`"magpie-roots-%d"`, s.nextCall)
	ch := make(chan rpc, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	s.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": "roots/list"})
	select {
	case m := <-ch:
		var r struct {
			Roots []struct {
				URI string `json:"uri"`
			} `json:"roots"`
		}
		json.Unmarshal(m.Result, &r)
		uris := make([]string, len(r.Roots))
		for i, root := range r.Roots {
			uris[i] = root.URI
		}
		return rootOf(uris)
	case <-time.After(3 * time.Second):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}
	return ""
}

// rootOf is the folder the first file:// URI of uris names, "" when none does.
func rootOf(uris []string) string {
	for _, uri := range uris {
		if u, err := url.Parse(uri); err == nil && u.Scheme == "file" && u.Path != "" {
			if wsl != nil {
				return wsl.local(u.Path)
			}
			return filePath(u)
		}
	}
	return ""
}

// filePath is the file a file:// URI names: on Windows file:///C:/p is C:\p
// (not \C:\p, a path nowhere) and file://server/share/p is \\server\share\p.
func filePath(u *url.URL) string {
	p := u.Path
	if runtime.GOOS == "windows" {
		if u.Host != "" && u.Host != "localhost" {
			return `\\` + u.Host + filepath.FromSlash(p)
		}
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
	}
	return filepath.FromSlash(p)
}

// reference is ref as the gateway takes it: a URL as it is, a file as a
// data URL.
func reference(project, ref string) (string, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
		return ref, nil
	}
	path := strings.TrimPrefix(ref, "file://")
	if u, err := url.Parse(ref); err == nil && u.Scheme == "file" {
		path = filePath(u)
		if wsl != nil {
			path = u.Path
		}
	}
	if wsl != nil {
		path = wsl.local(path)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(project, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reference image: %w", err)
	}
	mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if !strings.HasPrefix(mt, "image/") {
		mt = http.DetectContentType(b)
	}
	if !strings.HasPrefix(mt, "image/") {
		return "", fmt.Errorf("reference image %s isn't an image (%s)", ref, mt)
	}
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

// Target is where the i-th of n files made from prompt, of extension ext,
// goes under dir: path as a file (numbered when n > 1) or a folder, else a
// name from the prompt in dir's generated-images/ (generated-videos/);
// never a file that is there. `magpie tool` saves what it is given by it.
func Target(dir, path, prompt, ext string, i, n int) (string, error) {
	return target(dir, path, prompt, ext, i, n)
}

var unslug = lazyre.New(`[^a-z0-9]+`)

// target is where the i-th of n images goes: path as a file (numbered when
// n > 1) or a folder, else generated-images/<words of the prompt>-<time>.
// A name that is taken is given a number, never written over.
func target(project, path, prompt, ext string, i, n int) (string, error) {
	dir, name := filepath.Join(project, folderFor(ext)), ""
	if path != "" {
		p := path
		if wsl != nil {
			p = wsl.local(p)
		}
		if strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, p[2:])
		} else if !filepath.IsAbs(p) {
			p = filepath.Join(project, p)
		}
		if st, err := os.Stat(p); (err == nil && st.IsDir()) || strings.HasSuffix(path, "/") || filepath.Ext(p) == "" {
			dir = p
		} else {
			dir, name = filepath.Dir(p), filepath.Base(p)
		}
	}
	if name == "" {
		slug := strings.Trim(unslug.ReplaceAllString(strings.ToLower(prompt), "-"), "-")
		if len(slug) > 40 {
			slug = strings.TrimRight(slug[:40], "-")
		}
		if slug == "" {
			slug = "image"
		}
		name = slug + "-" + time.Now().Format("20060102-150405") + ext
	}
	base, e := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
	// the image is saved as what it is: hero.png that came back a JPEG is
	// hero.jpg, and the answer says so; a name with no image extension,
	// hero.v2, is given one
	switch l := strings.ToLower(e); {
	case l == ".jpeg" && ext == ".jpg", l == ext:
	case imageExt[l], videoExt[l]:
		e = ext
	default:
		base, e = name, ext
	}
	if n > 1 {
		base = fmt.Sprintf("%s-%d", base, i+1)
	}
	out := filepath.Join(dir, base+e)
	for k := 2; ; k++ {
		if _, err := os.Stat(out); errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		out = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, k, e))
	}
}

// imageExt are the extensions of the formats an image comes back in, videoExt
// those a video may be given.
var (
	imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true}
	videoExt = map[string]bool{".mp4": true, ".mov": true, ".webm": true, ".m4v": true}
)

// folderFor is where a file of ext goes when no path says.
func folderFor(ext string) string {
	if videoExt[ext] {
		return VideoFolder
	}
	return Folder
}

func extOf(mt string) string {
	switch mt {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/svg+xml":
		// Recraft's vector models at OpenRouter
		return ".svg"
	}
	return ".png"
}

type filmArgs struct {
	Prompt     string   `json:"prompt"`
	Model      string   `json:"model"`
	Path       string   `json:"path"`
	Seconds    any      `json:"seconds"`
	Size       string   `json:"size"`
	Image      string   `json:"image"`
	References []string `json:"reference_images"`
}

// wholeSeconds is a clip length as an agent may give it: 6, 6.0 or "6".
func wholeSeconds(v any) (int, error) {
	switch n := v.(type) {
	case nil:
		return 0, nil
	case float64:
		if n != float64(int(n)) || n < 1 {
			return 0, fmt.Errorf("seconds is a whole number from 1 to 15, not %v", n)
		}
		return int(n), nil
	case string:
		if strings.TrimSpace(n) == "" {
			return 0, nil
		}
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil || i < 1 {
			return 0, fmt.Errorf("seconds is a whole number from 1 to 15, not %q", n)
		}
		return i, nil
	}
	return 0, fmt.Errorf("seconds is a whole number from 1 to 15, not %v", v)
}

// call asks the gateway, as the agent c is of.
func (s *server) call(c caller, method, path string, body []byte) ([]byte, int, error) {
	r, _ := http.NewRequest(method, s.gateway+path, bytes.NewReader(body))
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+gateway.Token)
	r.Header.Set("User-Agent", c.agent)
	res, err := s.client.Do(r)
	if err != nil {
		return nil, 0, fmt.Errorf("magpie isn't answering at %s (is it running?): %w", s.gateway, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 512<<20))
	return data, res.StatusCode, nil
}

// gatewaySaid is what the gateway's error answer says, or its body.
func gatewaySaid(data []byte) string {
	var e struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(data))
}

// film asks the gateway to make a video, waits for it and saves it; what
// it says is where it is. used, when not nil, is set to the model the
// gateway named, provider/model.
func (s *server) film(raw json.RawMessage, used *string, c caller) (string, error) {
	var a filmArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	secs, err := wholeSeconds(a.Seconds)
	if err != nil {
		return "", err
	}
	project := s.project(c)
	req := map[string]any{"prompt": a.Prompt}
	if strings.TrimSpace(a.Model) != "" {
		m, err := s.mediaModel(videoTool, a.Model)
		if err != nil {
			return "", err
		}
		req["model"] = m
	}
	if secs > 0 {
		req["seconds"] = strconv.Itoa(secs)
	}
	if a.Size != "" {
		req["size"] = a.Size
	}
	if a.Image != "" {
		src, err := reference(project, a.Image)
		if err != nil {
			return "", err
		}
		req["input_reference"] = map[string]string{"image_url": src}
	}
	if len(a.References) > 0 {
		var refs []map[string]string
		for _, ref := range a.References {
			src, err := reference(project, ref)
			if err != nil {
				return "", err
			}
			refs = append(refs, map[string]string{"image_url": src})
		}
		req["reference_images"] = refs
	}
	body, _ := json.Marshal(req)
	data, code, err := s.call(c, http.MethodPost, "/v1/videos", body)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("no video: %s", gatewaySaid(data))
	}
	var v struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Model   string `json:"model"`
		Seconds string `json:"seconds"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &v) != nil || v.ID == "" {
		return "", fmt.Errorf("no video: the gateway's answer has no id: %s", gatewaySaid(data))
	}
	if used != nil {
		defer func() { *used = v.Model }()
	}
	id, deadline := v.ID, time.Now().Add(videoWait)
	for v.Status != "completed" {
		if v.Status == "failed" {
			msg := "it gave no reason"
			if v.Error != nil && v.Error.Message != "" {
				msg = v.Error.Message
			}
			return "", fmt.Errorf("the video failed: %s", msg)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the video (%s) wasn't ready after %s; ask the gateway for GET /v1/videos/%s later", id, videoWait, id)
		}
		time.Sleep(videoPollEvery)
		data, code, err = s.call(c, http.MethodGet, "/v1/videos/"+id, nil)
		if err != nil {
			return "", err
		}
		if code != 200 {
			return "", fmt.Errorf("asking after the video (%s): %s", id, gatewaySaid(data))
		}
		prev := v.Model
		v.Error = nil
		if json.Unmarshal(data, &v) != nil {
			return "", fmt.Errorf("asking after the video (%s): the answer isn't JSON", id)
		}
		if v.Model == "" {
			v.Model = prev
		}
	}
	mp4, code, err := s.call(c, http.MethodGet, "/v1/videos/"+id+"/content", nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("fetching the video (%s): %s", id, gatewaySaid(mp4))
	}
	if ct := http.DetectContentType(mp4); !strings.HasPrefix(ct, "video/") {
		return "", fmt.Errorf("what came back for the video (%s) isn't a video (%s)", id, ct)
	}
	path, err := target(project, a.Path, a.Prompt, ".mp4", 0, 1)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, mp4, 0o644); err != nil {
		return "", err
	}
	length := ""
	if v.Seconds != "" {
		length = v.Seconds + "-second "
	}
	return fmt.Sprintf("Generated a %svideo with %s, saved to:\n- %s\n", length, v.Model, shownPath(path)), nil
}

// The model a generate tool takes is the gateway's to say: it knows the
// providers, which this process may not read (an agent in WSL, a gateway
// elsewhere), and the models its caller may use. Its image MCP server's
// tools list them (gateway.MediaModelMethod checks one).

// modelAsk bounds asking the gateway for the models.
const modelAsk = 10 * time.Second

// ask asks the gateway's image MCP server method with params: its result,
// or its JSON-RPC error's code and message.
func (s *server) ask(method string, params any) (json.RawMessage, int, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	ctx, cancel := context.WithTimeout(context.Background(), modelAsk)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.gateway+gateway.ImageMCPPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Authorization", "Bearer "+gateway.Token)
	s.mu.Lock()
	r.Header.Set("User-Agent", s.agent)
	s.mu.Unlock()
	res, err := s.client.Do(r)
	if err != nil {
		return nil, 0, fmt.Errorf("magpie isn't answering at %s (is it running?): %w", s.gateway, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("magpie at %s answered %d: %s", s.gateway, res.StatusCode, gatewaySaid(data))
	}
	var m struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, 0, fmt.Errorf("magpie at %s answered what isn't JSON-RPC: %w", s.gateway, err)
	}
	if m.Error != nil {
		return nil, m.Error.Code, errors.New(m.Error.Message)
	}
	return m.Result, 0, nil
}

// modelProps are the model argument of each generate tool, by its name, as
// the gateway's image MCP server gives it; a plain one where it can't be
// asked.
func (s *server) modelProps() map[string]any {
	out := map[string]any{
		tool:      map[string]any{"type": "string", "description": "The image model to use, as provider/model. Leave it out to use the one set in Magpie (Settings → Models → Images → Image generation)."},
		videoTool: map[string]any{"type": "string", "description": "The video model to use, as provider/model. Leave it out to use the video model set up in Magpie."},
	}
	res, _, err := s.ask("tools/list", map[string]any{})
	if err != nil {
		return out
	}
	var l struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	json.Unmarshal(res, &l)
	for _, t := range l.Tools {
		var prop map[string]any
		if _, ok := out[t.Name]; ok && json.Unmarshal(t.InputSchema.Properties["model"], &prop) == nil && prop != nil {
			out[t.Name] = prop
		}
	}
	return out
}

// withModel is sc, a tool's schema, with prop as its model argument.
func withModel(sc map[string]any, prop any) map[string]any {
	out := maps.Clone(sc)
	in := maps.Clone(sc["inputSchema"].(map[string]any))
	props := maps.Clone(in["properties"].(map[string]any))
	props["model"] = prop
	in["properties"] = props
	out["inputSchema"] = in
	return out
}

// mediaModel is the model of the gateway's that asked stands for in a call
// of name, or why there is none.
func (s *server) mediaModel(name, asked string) (string, error) {
	res, code, err := s.ask(gateway.MediaModelMethod, map[string]any{"tool": name, "model": asked})
	if code == -32601 {
		// a magpie older than this one: it takes the model as named
		return strings.TrimSpace(asked), nil
	}
	if err != nil {
		return "", err
	}
	var m struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(res, &m) != nil || m.Model == "" {
		return "", fmt.Errorf("magpie at %s didn't say which model %q is", s.gateway, asked)
	}
	return m.Model, nil
}
