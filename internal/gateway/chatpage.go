package gateway

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// ChatPagePath is DeepSeek's open-source web chat page (deepseek-ai/
// chat-demo, MIT, its release v0.1.1 as built) served by the gateway, so
// any of magpie's models chats in it, with magpie's own web search and
// page reading behind its search and link tools (@yanghonghang on X).
// The release's bundle names https://api.deepseek.com once, as the
// origin it calls; it is served with the gateway's address and this path
// in its place, so the page calls the routes below at the address it was
// opened at. Nothing is built: the zip is embedded as released, and
// TestChatPageZip holds it to the release's own sha256.
const ChatPagePath = "/deepseek-chat"

//go:embed chatpage/chat-demo-v0.1.1.zip
var chatDemoZip []byte

//go:embed chatpage/magpie.js
var chatPageJS []byte

// chatDemoSHA256 is the release asset's digest, as GitHub lists it for
// chat-demo-v0.1.1.zip.
const chatDemoSHA256 = "a7881be425c5c6ba63bc4909fb4c5cc8af4350ec7a9bd50486908f264d370b17"

// chatDemoOrigin is the one place the bundle names the API it calls, and
// chatOrigin what it is served as instead: the address the page was
// opened at, this path after it.
const (
	chatDemoMain   = "static/js/main.9820cd3a.js"
	chatDemoOrigin = `let m="https://api.deepseek.com"`
	chatOrigin     = `let m=location.origin+"` + ChatPagePath + `"`
)

// chatPageKeyAgent is the key the page is given on this computer, so its
// calls are counted under a name of their own (TokenFor).
const chatPageKeyAgent = "deepseek-chat"

// chatPageFiles are the release's files by path, the main script and
// index.html as served; an error when the embedded zip isn't the
// release's.
var chatPageFiles = sync.OnceValues(func() (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(chatDemoZip), int64(len(chatDemoZip)))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, 32<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[path.Clean(f.Name)] = b
	}
	main := files[chatDemoMain]
	if n := bytes.Count(main, []byte(chatDemoOrigin)); n != 1 {
		return nil, fmt.Errorf("the page's script names its API origin %d times, not once", n)
	}
	files[chatDemoMain] = bytes.Replace(main, []byte(chatDemoOrigin), []byte(chatOrigin), 1)
	index := files["index.html"]
	at := bytes.Index(index, []byte(`<script defer src="`+chatDemoMain))
	if at < 0 {
		return nil, errors.New("the page's index.html loads no " + chatDemoMain)
	}
	files["index.html"] = slices.Concat(index[:at], []byte(`<script defer src="magpie.js"></script>`), index[at:])
	files["magpie.js"] = chatPageJS
	return files, nil
})

// chatPageFile is the release's file r asks for ("" for none): a GET or
// HEAD of the page's own path, clean, of a file the release has.
func chatPageFile(r *http.Request) string {
	if r.Method != http.MethodGet && r.Method != http.MethodHead || r.URL.RawPath != "" {
		return ""
	}
	rest, ok := strings.CutPrefix(r.URL.Path, ChatPagePath+"/")
	if !ok {
		return ""
	}
	if rest == "" {
		rest = "index.html"
	}
	if path.Clean(rest) != rest || strings.HasPrefix(rest, "/") {
		return ""
	}
	files, err := chatPageFiles()
	if err != nil {
		return ""
	}
	if _, ok := files[rest]; !ok {
		return ""
	}
	return rest
}

// chatPageOpen: r asks for one of the page's files, which lanGuard serves
// to another machine without a key while magpie is shared, as it does the
// key page: the same bytes for everyone, with nothing of the user's in
// them. Every call the page makes needs the key, which its holder types
// into the page.
func chatPageOpen(r *http.Request) bool {
	return r.URL.Path == ChatPagePath && r.URL.RawPath == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) || chatPageFile(r) != ""
}

// chatPageCSP keeps the page to its own files and its own origin: no
// script but the release's and magpie.js runs (the markdown it renders
// from a model's answer can't carry one), nothing is fetched from
// elsewhere, and it can't be framed.
const chatPageCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' blob:; worker-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// chatPage serves the page's files.
func (s *Server) chatPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == ChatPagePath {
		http.Redirect(w, r, ChatPagePath+"/", http.StatusFound)
		return
	}
	files, err := chatPageFiles()
	if err != nil {
		writeError(w, provider.Chat, http.StatusInternalServerError, "the chat page can't be served: "+err.Error())
		return
	}
	name := chatPageFile(r)
	if name == "" {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	ct := mime.TypeByExtension(path.Ext(name))
	switch path.Ext(name) {
	case ".js":
		ct = "text/javascript; charset=utf-8"
	case ".html":
		ct = "text/html; charset=utf-8"
	case ".woff2":
		ct = "font/woff2"
	case ".woff":
		ct = "font/woff"
	case ".ttf":
		ct = "font/ttf"
	case "":
		ct = "text/plain; charset=utf-8"
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", chatPageCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(name, "static/") {
		// hashed names: a new release is new names
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	b := files[name]
	h.Set("Content-Length", fmt.Sprint(len(b)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(b)
}

// chatModelCookie is the model the page's picker (magpie.js) chose; the
// page itself always asks for deepseek-flash.
const chatModelCookie = "magpie_chat_model"

// chatPageModels lists the models the caller may chat with, for the
// page's picker, and checks the key the page was given (the page's own
// GET /models).
func (s *Server) chatPageModels(w http.ResponseWriter, r *http.Request) {
	shown := catalogFor(r)
	data := make([]map[string]any, 0, len(shown))
	for _, e := range shown {
		data = append(data, map[string]any{"id": e.ID, "object": "model", "display_name": e.PlainName()})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "default": chatDefaultModel(shown)})
}

var chatFlash = regexp.MustCompile(`(?i)deepseek.*flash`)

// chatDefaultModel is the model the page chats with when its picker chose
// none: a DeepSeek flash model, as the page asks for, else the first.
func chatDefaultModel(shown []provider.Entry) string {
	for _, e := range shown {
		if chatFlash.MatchString(e.ID) {
			return e.ID
		}
	}
	if len(shown) > 0 {
		return shown[0].ID
	}
	return ""
}

// chatModel is the model a request of the page's goes to: the picker's,
// while the caller may still use it, else chatDefaultModel.
func chatModel(r *http.Request) string {
	shown := catalogFor(r)
	if c, err := r.Cookie(chatModelCookie); err == nil {
		if id, err := url.QueryUnescape(c.Value); err == nil && slices.ContainsFunc(shown, func(e provider.Entry) bool { return e.ID == id }) {
			return id
		}
	}
	return chatDefaultModel(shown)
}

// chatError answers the page in the shape it reads an error's words from
// ({"error":{"message"}}).
func chatError(w http.ResponseWriter, status int, msg string) {
	writeError(w, provider.Anthropic, status, msg)
}

// ---- files ------------------------------------------------------------------

// The page uploads each attachment once (POST /anthropic/v1/files) and
// names it by id in its messages. magpie keeps them in memory, for the
// caller that uploaded them, until it restarts: an id it no longer has
// becomes a line asking for the file again, never a failed reply.
const (
	chatFileMax   = 20 << 20
	chatFilesMax  = 200 << 20
	chatFileTextN = 400_000 // a text file's characters a model is given
)

type chatUpload struct {
	owner, name, mime string
	kind              string // "image", "pdf" or "text"
	data              []byte
	at                time.Time
}

var chatFiles = struct {
	sync.Mutex
	m     map[string]*chatUpload
	order []string
	size  int
}{m: map[string]*chatUpload{}}

// chatImageTypes are the images every vendor's API takes.
var chatImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// chatFileKind is how a file is given to a model, by its bytes:
// an image, a PDF or text; "" for what no model is given (a Word or
// PowerPoint file, an EPUB, a TIFF).
func chatFileKind(b []byte) (kind, mt string) {
	sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(b))
	switch {
	case chatImageTypes[sniffed]:
		return "image", sniffed
	case sniffed == "application/pdf":
		return "pdf", sniffed
	case utf8.Valid(b) && !bytes.ContainsRune(b, 0):
		return "text", "text/plain"
	}
	return "", ""
}

// chatFileUpload is the page's POST /anthropic/v1/files.
func (s *Server) chatFileUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, chatFileMax+1<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			chatError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than %d MB", chatFileMax>>20))
			return
		}
		chatError(w, http.StatusBadRequest, "the upload has no file: send it as the form's \"file\"")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, chatFileMax+1))
	if err != nil {
		chatError(w, http.StatusBadRequest, "the file couldn't be read: "+err.Error())
		return
	}
	if len(data) > chatFileMax {
		chatError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than %d MB", chatFileMax>>20))
		return
	}
	kind, mt := chatFileKind(data)
	if kind == "" {
		chatError(w, http.StatusUnsupportedMediaType, hdr.Filename+" can't be given to a model as it is: attach images (PNG, JPEG, GIF, WebP), PDFs or text files; save a Word or PowerPoint file as PDF first")
		return
	}
	id := "file_" + randomHex(12)
	cf := &chatUpload{owner: chatOwner(r), name: hdr.Filename, mime: mt, kind: kind, data: data, at: time.Now()}
	chatFiles.Lock()
	chatFiles.m[id] = cf
	chatFiles.order = append(chatFiles.order, id)
	chatFiles.size += len(data)
	for chatFiles.size > chatFilesMax && len(chatFiles.order) > 1 {
		old := chatFiles.order[0]
		chatFiles.order = chatFiles.order[1:]
		if o := chatFiles.m[old]; o != nil {
			chatFiles.size -= len(o.data)
			delete(chatFiles.m, old)
		}
	}
	chatFiles.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "type": "file", "size_bytes": len(data),
		"created_at": cf.at.UTC().Format(time.RFC3339), "filename": hdr.Filename, "mime_type": mt})
}

// chatFileDelete is the page's DELETE /anthropic/v1/files/{id}.
func (s *Server) chatFileDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	chatFiles.Lock()
	if f := chatFiles.m[id]; f != nil && f.owner == chatOwner(r) {
		delete(chatFiles.m, id)
		chatFiles.order = slices.DeleteFunc(chatFiles.order, func(o string) bool { return o == id })
		chatFiles.size -= len(f.data)
	}
	chatFiles.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "type": "file_deleted"})
}

// chatFileOf is the caller's file id, nil when magpie hasn't it.
func chatFileOf(r *http.Request, id string) *chatUpload {
	chatFiles.Lock()
	defer chatFiles.Unlock()
	if f := chatFiles.m[id]; f != nil && f.owner == chatOwner(r) {
		return f
	}
	return nil
}

// chatOwner is whose a file is: the gateway key that uploaded it, "" for
// this computer's calls.
func chatOwner(r *http.Request) string {
	return access.Caller(r.Context()).KeyID
}

// chatFileBlock is the block a file is given to a model as.
func chatFileBlock(f *chatUpload) map[string]any {
	switch f.kind {
	case "image":
		return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": f.mime, "data": base64.StdEncoding.EncodeToString(f.data)}}
	case "pdf":
		return map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": base64.StdEncoding.EncodeToString(f.data)}, "title": f.name}
	}
	text := string(f.data)
	if r := []rune(text); len(r) > chatFileTextN {
		text = string(r[:chatFileTextN]) + fmt.Sprintf("\n[%d more characters not included]", len(r)-chatFileTextN)
	}
	return map[string]any{"type": "text", "text": "File " + f.name + ":\n\n" + text}
}

// ---- speech -----------------------------------------------------------------

// chatASR is the page's voice input (POST /v1/tools/asr, a form with the
// recording as "file"): the first speech-to-text model a provider that is
// on serves (FirstServing), answering {"text"} as the page reads it.
func (s *Server) chatASR(w http.ResponseWriter, r *http.Request) {
	model := FirstServing(catalog.ServesTranscription)
	if model == "" {
		chatError(w, http.StatusNotImplemented, "voice input needs a speech-to-text model (whisper, gpt-4o-transcribe…): add a provider that serves one in magpie")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 30<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		chatError(w, http.StatusBadRequest, "the recording is missing: send it as the form's \"file\"")
		return
	}
	defer f.Close()
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	mw.WriteField("model", model)
	mw.WriteField("response_format", "json")
	name := hdr.Filename
	if name == "" {
		name = "recording.wav"
	}
	part, _ := mw.CreateFormFile("file", name)
	if _, err := io.Copy(part, f); err != nil {
		chatError(w, http.StatusBadRequest, "the recording couldn't be read: "+err.Error())
		return
	}
	mw.Close()
	s.transcribe("/audio/transcriptions", "transcriptions")(w, chatInner(r, "/v1/audio/transcriptions", mw.FormDataContentType(), form.Bytes()))
}

// chatVoices are the page's two voices as OpenAI's.
var chatVoices = map[string]string{"stella": "nova", "tide": "onyx"}

// chatTTS is the page's read-aloud (POST /v1/tools/tts): the first
// text-to-speech model a provider that is on serves, asked for raw PCM
// (24 kHz, 16-bit, mono on OpenAI's API, which is what the page plays).
func (s *Server) chatTTS(w http.ResponseWriter, r *http.Request) {
	model := FirstServing(catalog.ServesSpeech)
	if model == "" {
		chatError(w, http.StatusNotImplemented, "reading aloud needs a text-to-speech model (tts-1, gpt-4o-mini-tts…): add a provider that serves one in magpie")
		return
	}
	var in struct {
		Text           string `json:"text"`
		Voice          string `json:"voice_id"`
		FilterMarkdown bool   `json:"filter_markdown"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		chatError(w, http.StatusBadRequest, "send {\"text\": …} to read aloud")
		return
	}
	text := in.Text
	if in.FilterMarkdown {
		text = plainOfMarkdown(text)
	}
	voice := chatVoices[in.Voice]
	if voice == "" {
		voice = "nova"
	}
	b, _ := json.Marshal(map[string]any{"model": model, "input": text, "voice": voice, "response_format": "pcm"})
	s.speak(w, chatInner(r, "/v1/audio/speech", "application/json", b))
}

var (
	mdLink   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdMarks  = regexp.MustCompile("(?m)^\\s{0,3}(#{1,6}\\s+|>\\s?|[-*+]\\s+|\\d+\\.\\s+)|[*_`~]+")
	mdFences = regexp.MustCompile("(?s)```[^\n]*\n(.*?)```")
)

// plainOfMarkdown is markdown as it is read aloud: no marks, a link's
// words without its address.
func plainOfMarkdown(s string) string {
	s = mdFences.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllString(s, "$1")
	return strings.TrimSpace(mdMarks.ReplaceAllString(s, ""))
}

// chatInner is r as a call to the gateway's own route at p, with body:
// the caller and everything lanGuard and the key checks put on it kept.
func chatInner(r *http.Request, p, ctype string, body []byte) *http.Request {
	in := r.Clone(r.Context())
	in.URL.Path, in.URL.RawPath = p, ""
	in.RequestURI = p
	in.Pattern = "POST " + p
	in.Header.Set("Content-Type", ctype)
	in.Header.Del("Content-Length")
	in.Header.Del("Accept-Encoding")
	in.Header.Del("Anthropic-Beta")
	in.Header.Del("Cookie")
	in.Body = io.NopCloser(bytes.NewReader(body))
	in.ContentLength = int64(len(body))
	return in
}
