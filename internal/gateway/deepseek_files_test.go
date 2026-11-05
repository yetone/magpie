package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const responsesWholeOK = `{"id":"resp_1","object":"response","status":"completed","model":"m1","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

// dsFilesUp stands for api.deepseek.com and the other hosts the tests
// send to: its Files API hands out file-api-N ids, and its three APIs
// keep what they were sent.
type dsFilesUp struct {
	t           *testing.T
	mu          sync.Mutex
	uploads     []string // each upload's key
	sent        map[string][]string
	betas       []string
	failUploads bool
	slowUpload  chan struct{} // an upload waits for it to close
	gone        int           // requests naming a file answered 404, at most
	n           int
}

func (u *dsFilesUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/files" {
		if u.slowUpload != nil {
			<-u.slowUpload
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			u.t.Errorf("upload not multipart: %v", err)
		}
		f := r.MultipartForm
		if f == nil || f.Value["purpose"][0] != "user_data" || f.Value["expires_after[anchor]"][0] != "created_at" || f.Value["expires_after[seconds]"][0] != "86400" || len(f.File["file"]) != 1 {
			u.t.Errorf("upload fields: %+v", f)
		}
		u.mu.Lock()
		u.uploads = append(u.uploads, r.Header.Get("Authorization"))
		u.n++
		n, fail := u.n, u.failUploads
		u.mu.Unlock()
		if fail {
			http.Error(w, `{"error":{"message":"busy"}}`, 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"file-api-%d","object":"file","bytes":10,"created_at":%d,"filename":"image.png","purpose":"user_data","expires_at":%d}`, n, time.Now().Unix(), time.Now().Add(24*time.Hour).Unix())
		return
	}
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	if u.sent == nil {
		u.sent = map[string][]string{}
	}
	u.sent[r.URL.Path] = append(u.sent[r.URL.Path], string(b))
	if r.URL.Path == "/anthropic/v1/messages" {
		u.betas = append(u.betas, r.Header.Get("anthropic-beta"))
	}
	gone := u.gone > 0 && strings.Contains(string(b), "file-api-")
	if gone {
		u.gone--
	}
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if gone {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"message":"File not found","type":"invalid_request_error"}}`))
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		w.Write([]byte(chatWholeOK))
	case strings.HasSuffix(r.URL.Path, "/responses"):
		w.Write([]byte(responsesWholeOK))
	default:
		w.Write([]byte(anthropicWholeOK))
	}
}

func (u *dsFilesUp) last(path string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.sent[path]
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

func (u *dsFilesUp) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.sent[path])
}

func (u *dsFilesUp) uploaded() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.uploads...)
}

// dsSetup saves DeepSeek's own API, a relay serving its model, and
// another vendor, all answered by one fake that every host dials.
func dsSetup(t *testing.T, ps ...provider.Provider) (*Server, *dsFilesUp) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &dsFilesUp{t: t}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if len(ps) == 0 {
		ps = []provider.Provider{
			{ID: "deepseek", Name: "DeepSeek", Key: "k1", Models: []string{"deepseek-flash", "deepseek-v4-pro"}, Chat: "http://api.deepseek.com/v1", Responses: "http://api.deepseek.com/v1", Anthropic: "http://api.deepseek.com/anthropic"},
			{ID: "relay", Name: "Relay", Key: "k1", Models: []string{"deepseek-flash"}, Chat: "http://relay.example.com/v1", Responses: "http://relay.example.com/v1", Anthropic: "http://relay.example.com/anthropic"},
			{ID: "kimi", Name: "Kimi", Key: "k1", Models: []string{"kimi-k2.5-flash"}, Chat: "http://api.moonshot.ai/v1", Anthropic: "http://api.moonshot.ai/anthropic"},
		}
	}
	for _, p := range ps {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	return s, u
}

var (
	dsPNG1 = base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfirst image"))
	dsPNG2 = base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nsecond image"))
)

// dsBodies are a request with an image on each API, by its path.
func dsBody(api, model, img string) (path, upPath, body string) {
	switch api {
	case "chat":
		return "/v1/chat/completions", "/v1/chat/completions", `{"model":"` + model + `","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"what is it"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + img + `"}}]}]}`
	case "responses":
		return "/v1/responses", "/v1/responses", `{"model":"` + model + `","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"what is it"},{"type":"input_image","image_url":"data:image/png;base64,` + img + `","detail":"auto"}]}]}`
	}
	return "/v1/messages", "/anthropic/v1/messages", `{"model":"` + model + `","max_tokens":100,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"what is it"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + img + `"},"cache_control":{"type":"ephemeral"}}]}]}`
}

// On DeepSeek's own API an inline image goes up once and every turn
// after names it by file id, in each API's shape (#5407); the Anthropic
// request carries the Files API's beta.
func TestDeepSeekImagesByFile(t *testing.T) {
	for _, c := range []struct{ api, want string }{
		{"chat", `{"type":"file","file_id":"file-api-1"}`},
		{"responses", `"file_id":"file-api-1"`},
		{"anthropic", `"source":{"type":"file","file_id":"file-api-1"}`},
	} {
		t.Run(c.api, func(t *testing.T) {
			s, u := dsSetup(t)
			path, upPath, body := dsBody(c.api, "deepseek/deepseek-flash", dsPNG1)
			for turn := range 2 {
				if code, reply := postTo(t, s, path, body); code != 200 {
					t.Fatalf("turn %d: %d %s", turn, code, reply)
				}
				got := u.last(upPath)
				if !strings.Contains(got, c.want) || strings.Contains(got, dsPNG1) {
					t.Fatalf("turn %d: the image not named by file: %s", turn, got)
				}
			}
			if ups := u.uploaded(); len(ups) != 1 || ups[0] != "Bearer k1" {
				t.Errorf("uploads %v, want one with the key", ups)
			}
			if c.api == "responses" && strings.Contains(u.last(upPath), `"image_url"`) {
				t.Errorf("image_url kept beside file_id: %s", u.last(upPath))
			}
			if c.api == "anthropic" {
				if !strings.Contains(u.last(upPath), `"cache_control"`) {
					t.Errorf("cache_control lost: %s", u.last(upPath))
				}
				if b := u.betas[len(u.betas)-1]; !strings.Contains(b, dsFilesBeta) {
					t.Errorf("anthropic-beta %q, want %s", b, dsFilesBeta)
				}
			}
		})
	}
}

// A relay serving DeepSeek's model, another vendor, DeepSeek's model that
// doesn't see images, and an image at detail low all go inline as sent,
// and nothing is uploaded for them.
func TestDeepSeekImagesInlineElsewhere(t *testing.T) {
	s, u := dsSetup(t)
	for _, c := range []struct{ api, model string }{
		{"chat", "relay/deepseek-flash"},
		{"anthropic", "relay/deepseek-flash"},
		{"responses", "relay/deepseek-flash"},
		{"chat", "kimi/kimi-k2.5-flash"},
		{"anthropic", "kimi/kimi-k2.5-flash"},
		{"chat", "deepseek/deepseek-v4-pro"},
	} {
		path, upPath, body := dsBody(c.api, c.model, dsPNG1)
		if code, reply := postTo(t, s, path, body); code != 200 {
			t.Fatalf("%s %s: %d %s", c.api, c.model, code, reply)
		}
		if got := u.last(upPath); !strings.Contains(got, dsPNG1) || strings.Contains(got, "file-api-") {
			t.Errorf("%s %s: not sent inline: %s", c.api, c.model, got)
		}
	}
	low := `{"model":"deepseek/deepseek-flash","stream":false,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + dsPNG1 + `","detail":"low"}}]}]}`
	if code, reply := postTo(t, s, "/v1/chat/completions", low); code != 200 {
		t.Fatalf("low: %d %s", code, reply)
	}
	if got := u.last("/v1/chat/completions"); !strings.Contains(got, dsPNG1) {
		t.Errorf("detail low not inline: %s", got)
	}
	if ups := u.uploaded(); len(ups) != 0 {
		t.Errorf("uploaded %d, want none", len(ups))
	}
	if b := u.betas; len(b) > 0 && strings.Contains(strings.Join(b, ","), dsFilesBeta) {
		t.Errorf("files beta sent off DeepSeek: %v", b)
	}
}

// image-files=off sends DeepSeek its images inline.
func TestDeepSeekImageFilesOff(t *testing.T) {
	s, u := dsSetup(t, provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k1", Models: []string{"deepseek-flash"}, Chat: "http://api.deepseek.com/v1", ImageFilesOff: true})
	path, upPath, body := dsBody("chat", "deepseek/deepseek-flash", dsPNG1)
	if code, reply := postTo(t, s, path, body); code != 200 {
		t.Fatalf("%d %s", code, reply)
	}
	if got := u.last(upPath); !strings.Contains(got, dsPNG1) || len(u.uploaded()) != 0 {
		t.Errorf("not inline with the switch off: %s, %d uploads", got, len(u.uploaded()))
	}
}

// A failed upload leaves the image inline and the turn answered, and the
// key's uploads aren't tried again on the next turns for a while.
func TestDeepSeekUploadFailsInline(t *testing.T) {
	s, u := dsSetup(t)
	u.failUploads = true
	for turn := range 3 {
		path, upPath, body := dsBody("chat", "deepseek/deepseek-flash", []string{dsPNG1, dsPNG2, dsPNG1}[turn])
		if code, reply := postTo(t, s, path, body); code != 200 {
			t.Fatalf("turn %d: %d %s", turn, code, reply)
		}
		if got := u.last(upPath); strings.Contains(got, "file-api-") || !strings.Contains(got, "base64,") {
			t.Errorf("turn %d: not inline: %s", turn, got)
		}
	}
	if n := len(u.uploaded()); n != 1 {
		t.Errorf("%d uploads, want 1 before the key's uploads rest", n)
	}
}

// An upload that hangs doesn't hold the turn: past dsWaitMax the image
// goes inline, and once the upload is done the next turn names it.
func TestDeepSeekUploadSlowGoesInline(t *testing.T) {
	s, u := dsSetup(t)
	u.slowUpload = make(chan struct{})
	var release sync.Once
	letGo := func() { release.Do(func() { close(u.slowUpload) }) }
	defer letGo()
	old := dsWaitMax
	dsWaitMax = 100 * time.Millisecond
	t.Cleanup(func() { dsWaitMax = old })
	path, upPath, body := dsBody("chat", "deepseek/deepseek-flash", dsPNG1)
	start := time.Now()
	if code, reply := postTo(t, s, path, body); code != 200 {
		t.Fatalf("%d %s", code, reply)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the turn waited %v on the upload", d)
	}
	if got := u.last(upPath); strings.Contains(got, "file-api-") || !strings.Contains(got, dsPNG1) {
		t.Errorf("not inline while the upload hangs: %s", got)
	}
	letGo()
	for deadline := time.Now().Add(10 * time.Second); len(dsWaitingFlights()) > 0 || len(u.uploaded()) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the upload never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, reply := postTo(t, s, path, body); code != 200 {
		t.Fatalf("%d %s", code, reply)
	}
	if got := u.last(upPath); !strings.Contains(got, "file-api-1") {
		t.Errorf("not named by file once uploaded: %s", got)
	}
}

// A file DeepSeek says is gone is uploaded again and the request asked
// once more with the new id; gone again, the request goes inline.
func TestDeepSeekFileGoneUploadsAgain(t *testing.T) {
	s, u := dsSetup(t)
	path, upPath, body := dsBody("chat", "deepseek/deepseek-flash", dsPNG1)
	if code, reply := postTo(t, s, path, body); code != 200 {
		t.Fatalf("%d %s", code, reply)
	}
	u.mu.Lock()
	u.gone = 1
	u.mu.Unlock()
	if code, reply := postTo(t, s, path, body); code != 200 {
		t.Fatalf("after gone: %d %s", code, reply)
	}
	if got := u.last(upPath); !strings.Contains(got, `"file_id":"file-api-2"`) {
		t.Errorf("not asked again with a new upload: %s", got)
	}
	u.mu.Lock()
	u.gone = 5
	u.mu.Unlock()
	if code, reply := postTo(t, s, path, body); code != 200 {
		t.Fatalf("gone twice: %d %s", code, reply)
	}
	if got := u.last(upPath); strings.Contains(got, "file-api-") || !strings.Contains(got, dsPNG1) {
		t.Errorf("not inline after the file was gone twice: %s", got)
	}
}

// Each key uploads its own: an id never goes with another key.
func TestDeepSeekFilesPerKey(t *testing.T) {
	s, u := dsSetup(t,
		provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k1", Models: []string{"deepseek-flash"}, Chat: "http://api.deepseek.com/v1"},
		provider.Provider{ID: "deepseek-2", Name: "DeepSeek 2", Key: "k2", Models: []string{"deepseek-flash"}, Chat: "http://api.deepseek.com/v1"},
	)
	for _, m := range []string{"deepseek/deepseek-flash", "deepseek-2/deepseek-flash", "deepseek/deepseek-flash"} {
		path, _, body := dsBody("chat", m, dsPNG1)
		if code, reply := postTo(t, s, path, body); code != 200 {
			t.Fatalf("%s: %d %s", m, code, reply)
		}
	}
	if ups := u.uploaded(); len(ups) != 2 || ups[0] != "Bearer k1" || ups[1] != "Bearer k2" {
		t.Errorf("uploads %v, want one per key", ups)
	}
	if got := u.last("/v1/chat/completions"); !strings.Contains(got, "file-api-1") {
		t.Errorf("the first key not sent its own id: %s", got)
	}
}

// Requests wanting one image at once upload it once.
func TestDeepSeekFilesOneUpload(t *testing.T) {
	s, u := dsSetup(t)
	u.slowUpload = make(chan struct{})
	var wg sync.WaitGroup
	var release sync.Once
	letGo := func() { release.Do(func() { close(u.slowUpload) }) }
	// a red run doesn't leave its requests waiting on the upload
	defer func() {
		letGo()
		wg.Wait()
	}()
	path, upPath, body := dsBody("chat", "deepseek/deepseek-flash", dsPNG1)
	for range 4 {
		wg.Go(func() {
			if code, reply := postTo(t, s, path, body); code != 200 {
				t.Errorf("%d %s", code, reply)
			}
		})
	}
	// the other three wait on the first one's upload
	joined := dsJoined.Load()
	for deadline := time.Now().Add(20 * time.Second); dsJoined.Load()-joined < 3 || len(dsWaitingFlights()) != 1; {
		if time.Now().After(deadline) {
			t.Fatalf("joined %d, flights %d", dsJoined.Load()-joined, len(dsWaitingFlights()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	letGo()
	wg.Wait()
	if n := len(u.uploaded()); n != 1 {
		t.Errorf("%d uploads, want 1", n)
	}
	if u.count(upPath) != 4 || !strings.Contains(u.last(upPath), "file-api-1") {
		t.Errorf("requests %d: %s", u.count(upPath), u.last(upPath))
	}
}

// The ids are kept on disk: after a restart the image isn't uploaded
// again, and an expired one is. A file that can't be read is set aside.
func TestDeepSeekFilesKeptOnDisk(t *testing.T) {
	s, u := dsSetup(t)
	path, upPath, body := dsBody("chat", "deepseek/deepseek-flash", dsPNG1)
	postTo(t, s, path, body)
	forgetStores := func() {
		dsStores.Lock()
		delete(dsStores.m, dsFilesPath())
		dsStores.Unlock()
	}
	forgetStores()
	postTo(t, s, path, body)
	if n := len(u.uploaded()); n != 1 || !strings.Contains(u.last(upPath), "file-api-1") {
		t.Errorf("after a restart: %d uploads, %s", n, u.last(upPath))
	}
	// a day on, the file has expired: uploaded again
	dsNow = func() time.Time { return time.Now().Add(23*time.Hour + 30*time.Minute) }
	t.Cleanup(func() { dsNow = time.Now })
	postTo(t, s, path, body)
	if n := len(u.uploaded()); n != 2 || !strings.Contains(u.last(upPath), "file-api-2") {
		t.Errorf("near expiry: %d uploads, %s", n, u.last(upPath))
	}
	dsNow = time.Now
	forgetStores()
	if err := os.WriteFile(dsFilesPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	postTo(t, s, path, body)
	if b, err := os.ReadFile(dsFilesPath() + ".bad"); err != nil || string(b) != "{not json" {
		t.Errorf("unreadable file not set aside: %q %v", b, err)
	}
	if !strings.Contains(u.last(upPath), "file-api-3") {
		t.Errorf("after an unreadable file: %s", u.last(upPath))
	}
}

func TestDsFileGone(t *testing.T) {
	for _, c := range []struct {
		code int
		body string
		want bool
	}{
		{404, `{"error":{"message":"File not found"}}`, true},
		{400, `{"error":{"message":"file-api-12 does not exist"}}`, true},
		{400, `{"error":{"message":"The file has expired"}}`, true},
		{400, `{"error":{"message":"max_tokens too large"}}`, false},
		{429, `{"error":{"message":"file not found"}}`, false},
		{500, `{"error":{"message":"file not found"}}`, false},
	} {
		if got := dsFileGone(c.code, []byte(c.body)); got != c.want {
			t.Errorf("%d %s: %v", c.code, c.body, got)
		}
	}
}

func dsWaitingFlights() map[string]*dsFlight {
	st := dsStore()
	st.mu.Lock()
	defer st.mu.Unlock()
	return maps.Clone(st.flights)
}
