// Package imagemcp is `magpie mcp image`: a stdio MCP server an agent is
// given, when it is picked for it in the Library, to make images with the
// model the Settings' Image generation names. It asks the gateway, which
// knows the providers, and saves what comes back in the project.
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
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// Folder is where images go in the project when no path is given.
const Folder = "generated-images"

const tool = "generate_image"

var schema = map[string]any{
	"name": tool,
	"description": "Generate an image from a text prompt, or edit/combine images given as reference_images, with the image model set in Magpie. " +
		"The image is saved as a file in the project (" + Folder + "/ unless path says where) and its path is returned; " +
		"reference it from code or docs by that path. Write a detailed prompt: subject, style, composition, colours, any text to render exactly.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt":           map[string]any{"type": "string", "description": "What to draw, or how to change the reference images."},
			"path":             map[string]any{"type": "string", "description": "Where to save it: a file (e.g. assets/hero.png) or a folder, relative to the project or absolute. With n > 1 a number is added to the name."},
			"size":             map[string]any{"type": "string", "description": "WIDTHxHEIGHT (1024x1024, 1536x1024, 1024x1536) or an aspect ratio (16:9). Default: the model's."},
			"n":                map[string]any{"type": "integer", "minimum": 1, "maximum": 4, "description": "How many images (default 1)."},
			"reference_images": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Images to edit or draw from: file paths or http(s) URLs."},
			"quality":          map[string]any{"type": "string", "description": "low, medium, high or auto, for models that take it."},
			"background":       map[string]any{"type": "string", "description": "transparent, opaque or auto, for models that take it."},
		},
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
}

// Run serves MCP on stdin/stdout until stdin closes.
func Run(args []string) error {
	if len(args) > 0 && args[0] != "image" {
		return fmt.Errorf("unknown MCP server %q (there is: image)", args[0])
	}
	base := strings.TrimRight(os.Getenv("MAGPIE_GATEWAY"), "/")
	if base == "" {
		base = gateway.URL()
	}
	s := &server{gateway: base, client: &http.Client{Timeout: 6 * time.Minute}, enc: json.NewEncoder(os.Stdout), agent: gateway.DrawAgent, pending: map[string]chan rpc{}}
	return s.serve(os.Stdin)
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
		result = map[string]any{"tools": []any{schema}}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name != tool {
			rpcErr = map[string]any{"code": -32602, "message": "unknown tool " + p.Name}
			break
		}
		text, err := s.generate(p.Arguments)
		if err != nil {
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
			break
		}
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
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
}

type args struct {
	Prompt     string   `json:"prompt"`
	Path       string   `json:"path"`
	Size       string   `json:"size"`
	N          int      `json:"n"`
	References []string `json:"reference_images"`
	Quality    string   `json:"quality"`
	Background string   `json:"background"`
}

// generate asks the gateway for the images and saves them; what it says is
// where they are.
func (s *server) generate(raw json.RawMessage) (string, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	a.N = max(1, min(a.N, 4))
	project := s.project()
	req := map[string]any{"prompt": a.Prompt, "n": a.N}
	for k, v := range map[string]string{"size": a.Size, "quality": a.Quality, "background": a.Background} {
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
	s.mu.Lock()
	r.Header.Set("User-Agent", s.agent)
	s.mu.Unlock()
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
	var saved, revised []string
	for i, d := range out.Data {
		img, mt := []byte(nil), d.Mime
		switch {
		case d.B64 != "":
			if img, err = base64.StdEncoding.DecodeString(d.B64); err != nil {
				return "", fmt.Errorf("image %d: %w", i+1, err)
			}
		case d.URL != "":
			if img, mt, err = s.download(d.URL); err != nil {
				return "", fmt.Errorf("image %d: %w", i+1, err)
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
	var b strings.Builder
	fmt.Fprintf(&b, "Generated with %s, saved to:\n", out.Model)
	for _, p := range saved {
		fmt.Fprintf(&b, "- %s\n", p)
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
// it can say, else the folder the agent started magpie in. The home folder
// or / isn't a project: ~/Pictures/Magpie is used then.
func (s *server) project() string {
	if dir := s.root(); dir != "" {
		return dir
	}
	dir, _ := os.Getwd()
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
		for _, root := range r.Roots {
			if u, err := url.Parse(root.URI); err == nil && u.Scheme == "file" && u.Path != "" {
				return filepath.FromSlash(u.Path)
			}
		}
	case <-time.After(3 * time.Second):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}
	return ""
}

// reference is ref as the gateway takes it: a URL as it is, a file as a
// data URL.
func reference(project, ref string) (string, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
		return ref, nil
	}
	path := strings.TrimPrefix(ref, "file://")
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

func (s *server) download(u string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	res, err := s.client.Do(r)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, "", fmt.Errorf("%s: %s", u, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	return b, mt, err
}

var unslug = regexp.MustCompile(`[^a-z0-9]+`)

// target is where the i-th of n images goes: path as a file (numbered when
// n > 1) or a folder, else generated-images/<words of the prompt>-<time>.
// A name that is taken is given a number, never written over.
func target(project, path, prompt, ext string, i, n int) (string, error) {
	dir, name := filepath.Join(project, Folder), ""
	if path != "" {
		p := path
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
	case imageExt[l]:
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

// imageExt are the extensions of the formats an image comes back in.
var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true}

func extOf(mt string) string {
	switch mt {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".png"
}
