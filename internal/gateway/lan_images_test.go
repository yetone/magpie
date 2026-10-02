package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Another computer on the network draws with the magpie shared there
// (#545): a plain client with the gateway's key, at its images API, and a
// magpie that has it as a Remote magpie, whose image models it lists and
// draws with there. Neither gets an image only this computer can open.
func TestLANDraws(t *testing.T) {
	_, e := easeled(t)
	_, secrets := newCaller(t, "Laptop") // shares the gateway
	h := lanGuard(New().Handler())
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "192.168.1.9:5000" // another computer
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(host.Close)
	ask := func(path, key, body string) (int, imagesAnswer, string) {
		req, _ := http.NewRequest("POST", host.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var a imagesAnswer
		var raw json.RawMessage
		json.NewDecoder(res.Body).Decode(&raw)
		json.Unmarshal(raw, &a)
		return res.StatusCode, a, string(raw)
	}
	want := base64.StdEncoding.EncodeToString(pngBytes)

	// a plain client: no key, turned away; with it, the image's bytes, by
	// the model named or the one the Settings draw with
	if code, _, body := ask("/v1/images/generations", "", `{"prompt":"a magpie"}`); code != 401 {
		t.Errorf("no key: %d %s", code, body)
	}
	for _, body := range []string{`{"model":"art/gpt-image-1","prompt":"a magpie"}`, `{"prompt":"a magpie"}`} {
		code, a, raw := ask("/v1/images/generations", secrets[0], body)
		if code != 200 || len(a.Data) != 1 || a.Data[0].B64 != want || strings.Contains(raw, "127.0.0.1") || strings.Contains(raw, "localhost") {
			t.Errorf("%s: %d %s", body, code, raw)
		}
	}
	if code, a, raw := ask("/v1/images/edits", secrets[0], `{"model":"art/gpt-image-1","prompt":"a hat","image":"data:image/png;base64,`+want+`"}`); code != 200 || len(a.Data) != 1 {
		t.Errorf("edit: %d %s", code, raw)
	}

	// a magpie with it as a Remote magpie, signed with the key
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Key: secrets[0], Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(host.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var draws []string
	for _, m := range Drawers(*office) {
		draws = append(draws, m.ID)
	}
	if !slices.Contains(draws, "art/gpt-image-1") {
		t.Fatalf("the shared magpie's image models aren't listed here: %v", draws)
	}
	before := len(e.got("/v1/images/generations"))
	code, body := post(t, "/v1/images/generations", `{"model":"office/art/gpt-image-1","prompt":"a magpie"}`)
	var a imagesAnswer
	json.Unmarshal([]byte(body), &a)
	if code != 200 || len(a.Data) != 1 || a.Data[0].B64 != want || a.Model != "office/art/gpt-image-1" {
		t.Errorf("through the remote magpie: %d %s", code, body)
	}
	if n := len(e.got("/v1/images/generations")); n != before+1 {
		t.Errorf("the vendor was asked %d times", n-before)
	}
}

// A magpie with another as its Remote magpie makes videos with the video
// models shared there (#545): they are listed as such, not as image models,
// and the video is started, asked after and fetched at the other magpie's
// videos API, under an id of this magpie's.
func TestRemoteMagpieVideos(t *testing.T) {
	grokSignedIn(t)
	g := newGrokMedia(t)
	_, secrets := newCaller(t, "Laptop")
	h := lanGuard(New().Handler())
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "192.168.1.9:5000"
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(host.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Key: secrets[0], Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(host.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var films, draws []string
	for _, m := range Videomakers(*office) {
		films = append(films, m.ID)
	}
	for _, m := range Drawers(*office) {
		draws = append(draws, m.ID)
	}
	if !slices.Contains(films, "grok/grok-imagine-video") {
		t.Fatalf("the shared magpie's video models aren't listed here: %v", films)
	}
	if slices.ContainsFunc(draws, func(id string) bool { return strings.Contains(id, "video") }) || !slices.Contains(draws, "grok/grok-imagine-image") {
		t.Errorf("image models here: %v", draws)
	}

	s := New()
	code, obj, raw, _ := request(t, s, "POST", "/v1/videos", "application/json", `{"model":"office/grok/grok-imagine-video","prompt":"a magpie takes off","seconds":6,"size":"1280x720"}`)
	vid, _ := obj["id"].(string)
	if code != 200 || !strings.HasPrefix(vid, "video_office.") || obj["model"] != "office/grok/grok-imagine-video" {
		t.Fatalf("start: %d %s", code, raw)
	}
	if b := g.body("/v1/videos/generations", 0); !strings.Contains(b, `"duration":6`) || !strings.Contains(b, `"resolution":"720p"`) {
		t.Errorf("Grok was asked %s", b)
	}
	if code, obj, raw, _ := request(t, s, "GET", "/v1/videos/"+vid, "", ""); code != 200 || obj["id"] != vid || obj["status"] != "queued" {
		t.Errorf("status: %d %s", code, raw)
	}
	if code, _, raw, _ := request(t, s, "GET", "/v1/videos/"+vid+"/content", "", ""); code != 409 {
		t.Errorf("content before it is made: %d %s", code, raw)
	}
	if code, _, raw, hdr := request(t, s, "GET", "/v1/videos/"+vid+"/content", "", ""); code != 200 || string(raw) != "fake-mp4-bytes" || hdr.Get("Content-Type") != "video/mp4" {
		t.Errorf("content: %d %s %v", code, raw, hdr)
	}
}
