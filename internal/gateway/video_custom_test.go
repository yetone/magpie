package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// openAIVideos is an upstream serving OpenAI's Videos API under /v1, as a
// provider set up with a base URL does (#1446): its model list marks
// vid-1 "kind": "video" and lists agnes-video-2.5-flash with no kind, a
// video takes two polls to be made, and what each request was sent and
// signed with is kept.
type openAIVideos struct {
	*httptest.Server
	mu     sync.Mutex
	asked  []string // "METHOD path key"
	bodies []string // bodies of POST /v1/videos
	heads  []http.Header
	polls  int
	lacks  string // a key answered 404 that it serves no such model
}

func newOpenAIVideos(t *testing.T) *openAIVideos {
	t.Helper()
	u := &openAIVideos{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		defer u.mu.Unlock()
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		u.asked = append(u.asked, r.Method+" "+r.URL.Path+" "+key)
		u.heads = append(u.heads, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		// the vendor's id has a dot, which magpie's own ids can't carry bare
		const vid = "video_68d7.abc"
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/models":
			io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5.5","object":"model"},{"id":"vid-1","object":"model","kind":"video"},{"id":"agnes-video-2.5-flash","object":"model"}]}`)
		case r.Method == "POST" && r.URL.Path == "/v1/videos" && key == u.lacks:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"Model \"vid-1\" is not supported by any configured account in this group","type":"invalid_request_error"}}`)
		case r.Method == "POST" && r.URL.Path == "/v1/videos":
			u.bodies = append(u.bodies, string(b))
			io.WriteString(w, `{"id":"`+vid+`","object":"video","created_at":1790000000,"status":"queued","progress":0,"model":"vid-1","seconds":"4","size":"1280x720"}`)
		case r.Method == "GET" && r.URL.Path == "/v1/videos/"+vid:
			u.polls++
			switch u.polls {
			case 1:
				io.WriteString(w, `{"id":"`+vid+`","object":"video","status":"in_progress","progress":40,"model":"vid-1"}`)
			default:
				io.WriteString(w, `{"id":"`+vid+`","object":"video","status":"completed","progress":100,"model":"vid-1","seconds":"4"}`)
			}
		case r.Method == "GET" && r.URL.Path == "/v1/videos/"+vid+"/content":
			w.Header().Set("Content-Type", "video/mp4")
			io.WriteString(w, "\x00\x00\x00\x18ftypmp42fake-mp4")
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"no route `+r.Method+" "+r.URL.Path+`","type":"invalid_request_error"}}`)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *openAIVideos) log() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.asked)
}

// saveFetched saves p and fetches its model list, as adding it does.
func saveFetched(t *testing.T, p provider.Provider) provider.Provider {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	found, err := provider.Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := found.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	return *found
}

func asKey(t *testing.T, h http.Handler, secret, method, path, body string) (int, map[string]any, []byte) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var obj map[string]any
	json.Unmarshal(w.Body.Bytes(), &obj)
	return w.Code, obj, w.Body.Bytes()
}

// A provider set up with a base URL whose model list marks a model "kind":
// "video" makes videos with it: started, polled and fetched at its
// OpenAI-shaped videos API (#1446). The old gateway turned it away 400 as
// not a Grok or Volcengine model.
func TestCustomProviderMakesVideos(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	p := saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "sk-studio"})
	ms := Videomakers(p)
	if len(ms) != 1 || ms[0].ID != "vid-1" || ms[0].Provider != "studio" {
		t.Fatalf("videomakers %v: the list marks vid-1 alone a video model", ms)
	}
	h := New().Handler()
	code, obj, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"studio/vid-1","prompt":"a cat on a fence","seconds":4,"size":"1280x720"}`)
	id, _ := obj["id"].(string)
	if code != 200 || !strings.HasPrefix(id, "video_studio.") || obj["model"] != "studio/vid-1" || obj["status"] != "queued" {
		t.Fatalf("start: %d %s", code, raw)
	}
	var sent map[string]any
	json.Unmarshal([]byte(up.bodies[0]), &sent)
	if sent["model"] != "vid-1" || sent["prompt"] != "a cat on a fence" || sent["seconds"] != "4" || sent["size"] != "1280x720" {
		t.Fatalf("sent %v", sent)
	}
	for i, want := range []string{"in_progress", "completed"} {
		code, obj, raw = asKey(t, h, "", "GET", "/v1/videos/"+id, "")
		if code != 200 || obj["id"] != id || obj["status"] != want || obj["model"] != "studio/vid-1" {
			t.Fatalf("poll %d: %d %s", i, code, raw)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/videos/"+id+"/content", nil))
	if rec.Code != 200 || rec.Body.String() != "\x00\x00\x00\x18ftypmp42fake-mp4" || rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("content: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	want := []string{"GET /v1/models sk-studio", "POST /v1/videos sk-studio", "GET /v1/videos/video_68d7.abc sk-studio", "GET /v1/videos/video_68d7.abc sk-studio", "GET /v1/videos/video_68d7.abc/content sk-studio"}
	if got := up.log(); !slices.Equal(got, want) {
		t.Fatalf("asked %v\nwant %v", got, want)
	}
	// a vendor is told nothing of who is calling
	for _, hd := range up.heads {
		if hd.Get(AgentHeader) != "" || hd.Get(SessionHeader) != "" {
			t.Fatalf("caller headers went to the vendor: %v", hd)
		}
	}
	// by its bare name too, and with none named when it is the only maker
	if code, obj, raw = asKey(t, h, "", "POST", "/v1/videos", `{"model":"vid-1","prompt":"a dog"}`); code != 200 || obj["model"] != "studio/vid-1" {
		t.Fatalf("bare name: %d %s", code, raw)
	}
	if code, obj, raw = asKey(t, h, "", "POST", "/v1/videos", `{"prompt":"a dog"}`); code != 200 || obj["model"] != "studio/vid-1" {
		t.Fatalf("no model named: %d %s", code, raw)
	}
	// another magpie asking this one for its video models is told of it
	if objs := videomakerObjects(httptest.NewRequest("GET", "/v1/models", nil)); len(objs) != 1 || objs[0]["id"] != "studio/vid-1" {
		t.Fatalf("videomakerObjects %v", objs)
	}
}

// A model's name alone makes no video model: one the provider's list
// doesn't mark "kind": "video" is still turned away before any request,
// and so is every model of a provider whose list marks none.
func TestCustomProviderVideoNeedsTheListsWord(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "sk-studio"})
	h := New().Handler()
	for _, model := range []string{"studio/agnes-video-2.5-flash", "studio/gpt-5.5", "studio/grok-imagine-video"} {
		code, obj, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"`+model+`","prompt":"a cat"}`)
		if code != 400 || !strings.Contains(errorOf(obj), "can't make videos") {
			t.Fatalf("%s: %d %s", model, code, raw)
		}
	}
	for _, a := range up.log() {
		if strings.Contains(a, "/videos") {
			t.Fatalf("a video was asked for: %v", up.log())
		}
	}
	if ms := Videomakers(provider.Provider{ID: "unlisted", Name: "Unlisted", Chat: up.URL + "/v1", Key: "k"}); len(ms) != 0 {
		t.Fatalf("a provider with no fetched list makes videos: %v", ms)
	}
}

// A Responses-only relay has no Chat base: its video is started, polled
// and fetched under its Responses base all the same, as its images are.
func TestCustomProviderVideoOnAResponsesBase(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "relay", Name: "Relay", Responses: up.URL + "/v1", Key: "sk-relay"})
	h := New().Handler()
	code, obj, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"relay/vid-1","prompt":"a kite"}`)
	id, _ := obj["id"].(string)
	if code != 200 || id == "" {
		t.Fatalf("start: %d %s", code, raw)
	}
	asKey(t, h, "", "GET", "/v1/videos/"+id, "")
	if code, obj, raw = asKey(t, h, "", "GET", "/v1/videos/"+id, ""); code != 200 || obj["status"] != "completed" {
		t.Fatalf("poll: %d %s", code, raw)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/videos/"+id+"/content", nil))
	if rec.Code != 200 || !strings.HasSuffix(rec.Body.String(), "fake-mp4") {
		t.Fatalf("content: %d %s", rec.Code, rec.Body.String())
	}
}

// A video started on a provider's later key is asked after on that key,
// whichever is first: the vendor keeps it for the key that started it.
// A gateway key held to some accounts (#905) asks after it only on one it
// may use, and one held to some models (#882) only after a model it may
// use, on the poll and the content as on the start.
func TestCustomProviderVideoKeys(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}})
	keys, secrets := newCaller(t, "OnB", "OnA", "OtherModels", "ThisModel")
	for i, c := range []access.Change{
		{Key: keys[0].ID, Accounts: []string{"studio/" + provider.KeyID("kB")}},
		{Key: keys[1].ID, Accounts: []string{"studio/" + provider.KeyID("kA")}},
		{Key: keys[2].ID, Models: []string{"elsewhere/*"}},
		{Key: keys[3].ID, Models: []string{"studio/vid-1"}},
	} {
		if _, err := access.Update([]string{"accounts-key", "accounts-key", "models-key", "models-key"}[i], c); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	code, obj, raw := asKey(t, h, secrets[0], "POST", "/v1/videos", `{"model":"studio/vid-1","prompt":"a cat"}`)
	id, _ := obj["id"].(string)
	if code != 200 || id == "" {
		t.Fatalf("start: %d %s", code, raw)
	}
	if code, _, raw = asKey(t, h, secrets[0], "GET", "/v1/videos/"+id, ""); code != 200 {
		t.Fatalf("poll on kB: %d %s", code, raw)
	}
	// a caller with no restriction asks on kB too, not on the first key
	if code, _, raw = asKey(t, h, "", "GET", "/v1/videos/"+id, ""); code != 200 {
		t.Fatalf("poll: %d %s", code, raw)
	}
	if code, _, raw = asKey(t, h, secrets[0], "GET", "/v1/videos/"+id+"/content", ""); code != 200 || !strings.HasSuffix(string(raw), "fake-mp4") {
		t.Fatalf("content on kB: %d %s", code, raw)
	}
	for _, a := range up.log()[1:] {
		if !strings.HasSuffix(a, " kB") {
			t.Fatalf("asked on another key than kB: %v", up.log())
		}
	}
	before := len(up.log())
	// held to kA: kB's video is not its to ask after
	for _, path := range []string{"/v1/videos/" + id, "/v1/videos/" + id + "/content"} {
		if code, obj, raw = asKey(t, h, secrets[1], "GET", path, ""); code != 403 || !strings.Contains(errorOf(obj), "not allowed to use the accounts") {
			t.Fatalf("held to kA, %s: %d %s", path, code, raw)
		}
	}
	if len(up.log()) != before {
		t.Fatalf("a key held from the account had the vendor asked: %v", up.log()[before:])
	}
	// held to other models: neither the poll nor the bytes
	for _, path := range []string{"/v1/videos/" + id, "/v1/videos/" + id + "/content"} {
		if code, obj, raw = asKey(t, h, secrets[2], "GET", path, ""); code != 403 || !strings.Contains(errorOf(obj), "may not use studio/vid-1") {
			t.Fatalf("held to other models, %s: %d %s", path, code, raw)
		}
	}
	for _, a := range up.log()[before:] {
		if strings.HasSuffix(strings.Fields(a)[1], "/content") {
			t.Fatalf("a key held from the model was sent the video: %v", up.log()[before:])
		}
	}
	// held to this model: both
	if code, _, raw = asKey(t, h, secrets[3], "GET", "/v1/videos/"+id, ""); code != 200 {
		t.Fatalf("held to vid-1, poll: %d %s", code, raw)
	}
	if code, _, raw = asKey(t, h, secrets[3], "GET", "/v1/videos/"+id+"/content", ""); code != 200 || !strings.HasSuffix(string(raw), "fake-mp4") {
		t.Fatalf("held to vid-1, content: %d %s", code, raw)
	}
	// and a key taken off, its videos say so rather than ask on another
	if err := provider.SetKeyOn("studio", provider.KeyID("kB"), false); err != nil {
		t.Fatal(err)
	}
	if code, obj, raw = asKey(t, h, "", "GET", "/v1/videos/"+id, ""); code != 404 || !strings.Contains(errorOf(obj), "no longer on") {
		t.Fatalf("kB off: %d %s", code, raw)
	}
}

// A Grok subscription's video is held by a gateway key's models on its
// poll and content as on its start; one the key may use is answered as
// before.
func TestGrokVideoPollHeldByKeyModels(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	keys, secrets := newCaller(t, "OtherModels", "Grok")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"elsewhere/*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"grok/*"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	code, obj, raw := asKey(t, h, secrets[1], "POST", "/v1/videos", `{"model":"grok/grok-imagine-video","prompt":"a kite"}`)
	id, _ := obj["id"].(string)
	if code != 200 || id == "" {
		t.Fatalf("start: %d %s", code, raw)
	}
	for _, path := range []string{"/v1/videos/" + id, "/v1/videos/" + id + "/content"} {
		if code, obj, raw = asKey(t, h, secrets[0], "GET", path, ""); code != 403 || !strings.Contains(errorOf(obj), "may not use grok/") {
			t.Fatalf("held, %s: %d %s", path, code, raw)
		}
	}
	// the two held asks read the vendor's state, as a poll does: the
	// third ask finds it made
	if code, obj, raw = asKey(t, h, secrets[1], "GET", "/v1/videos/"+id, ""); code != 200 || obj["status"] != "completed" || obj["model"] != "grok/grok-imagine-video" {
		t.Fatalf("poll: %d %s", code, raw)
	}
	if code, _, raw = asKey(t, h, secrets[1], "GET", "/v1/videos/"+id+"/content", ""); code != 200 || string(raw) != "fake-mp4-bytes" {
		t.Fatalf("content: %d %s", code, raw)
	}
}

// A model the provider's list doesn't mark, the user says makes videos
// (shen-joe, #1399: a relay's agnes-video-2.5-flash turned away "can't make
// videos" with no way to say it does): it is started, polled and fetched at
// the provider's videos API as a listed one is, one typed in by hand too.
// One the user says draws leaves the video models for the image ones, and
// a chat model said to draw is drawn with. Said of nothing, it goes back.
func TestCustomProviderVideoTheUserMarks(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	p := saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "sk-studio", Models: []string{"gpt-5.5", "agnes-video-2.5-flash", "typed-video-1"}})
	for ref, kind := range map[string]string{"studio/agnes-video-2.5-flash": "video", "studio/typed-video-1": "video", "studio/vid-1": "image", "studio/gpt-5.5": "image"} {
		if err := provider.SetModelKind(ref, kind); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	ids := func(ms []catalog.Model) []string {
		var out []string
		for _, m := range ms {
			if m.Provider != "studio" {
				t.Fatalf("%v: not studio's", m)
			}
			out = append(out, m.ID)
		}
		slices.Sort(out)
		return out
	}
	if got := ids(Videomakers(p)); !slices.Equal(got, []string{"agnes-video-2.5-flash", "typed-video-1"}) {
		t.Fatalf("videomakers %v", got)
	}
	if got := ids(Drawers(p)); !slices.Equal(got, []string{"gpt-5.5", "vid-1"}) {
		t.Fatalf("drawers %v", got)
	}
	h := New().Handler()
	for i, model := range []string{"agnes-video-2.5-flash", "typed-video-1"} {
		code, obj, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"studio/`+model+`","prompt":"waves on a rock"}`)
		if id, _ := obj["id"].(string); code != 200 || !strings.HasPrefix(id, "video_studio.") {
			t.Fatalf("%s: %d %s", model, code, raw)
		}
		var sent map[string]any
		json.Unmarshal([]byte(up.bodies[i]), &sent)
		if sent["model"] != model || sent["prompt"] != "waves on a rock" {
			t.Fatalf("%s: sent %v", model, sent)
		}
	}
	if code, obj, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"studio/vid-1","prompt":"x"}`); code != 400 || !strings.Contains(errorOf(obj), "can't make videos") {
		t.Fatalf("vid-1, said to draw: %d %s", code, raw)
	}
	// a model the provider doesn't have can't be said to make anything
	if err := provider.SetModelKind("studio/nope-9", "video"); err == nil {
		t.Fatal("a model studio doesn't have was marked")
	}
	if err := provider.SetModelKind("studio/agnes-video-2.5-flash", "audio"); err == nil {
		t.Fatal("a kind magpie doesn't know was taken")
	}
	for _, ref := range []string{"studio/agnes-video-2.5-flash", "studio/typed-video-1", "studio/vid-1", "studio/gpt-5.5"} {
		if err := provider.SetModelKind(ref, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := ids(Videomakers(p)); !slices.Equal(got, []string{"vid-1"}) {
		t.Fatalf("videomakers after unmarking %v", got)
	}
	if got := ids(Drawers(p)); len(got) != 0 {
		t.Fatalf("drawers after unmarking %v", got)
	}
}

// #1399 (shen-joe): Agnes's videos API answers 400 "mode 不能为空" to a
// body without mode, and magpie passed on only model, prompt, seconds and
// size. The reporter's own request now reaches the vendor with mode and
// aspect_ratio as sent; a text field passed on so is masked as the prompt
// is.
func TestCustomProviderVideoKeepsTheCallersFields(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "agnes-cn", Name: "Agnes", Chat: up.URL + "/v1", Key: "sk-agnes", Models: []string{"agnes-video-2.5-flash"}})
	if err := provider.SetModelKind("agnes-cn/agnes-video-2.5-flash", "video"); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	code, _, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model": "agnes-cn/agnes-video-2.5-flash","mode": "text","aspect_ratio": "16:9","size":"720P","seconds":"10","prompt": "海浪拍打礁石，慢镜头"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	var sent map[string]any
	json.Unmarshal([]byte(up.bodies[0]), &sent)
	want := map[string]any{"model": "agnes-video-2.5-flash", "mode": "text", "aspect_ratio": "16:9", "size": "720P", "seconds": "10", "prompt": "海浪拍打礁石，慢镜头"}
	for k, v := range want {
		if sent[k] != v {
			t.Fatalf("%s: sent %v, want %v (%s)", k, sent[k], v, up.bodies[0])
		}
	}

	st := settings.Load()
	st.RedactWords = []string{"Nightjar"}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, _, raw = asKey(t, h, "", "POST", "/v1/videos", `{"model":"agnes-cn/agnes-video-2.5-flash","mode":"text","prompt":"waves","negative_prompt":"no Nightjar","extra":{"notes":["Nightjar"]}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if b := up.bodies[1]; strings.Contains(b, "Nightjar") || !strings.Contains(b, `"mode":"text"`) {
		t.Fatalf("sent %s", b)
	}
}
