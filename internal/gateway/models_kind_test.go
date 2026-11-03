package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// axelburks on Discord asked why /v1/models lists no image or video models.
// An agent or client that offers every id this list gives in its chat
// picker would offer them to chat with, so they are listed only when the
// caller asks: ?kind=image,video (or all), each marked by its "kind". No
// agent's list, the plain one, Claude Desktop's, Claude Science's,
// Cursor's, OpenCode's or the ids of ?format=text, gains an image or a
// video model.
func TestModelsListMediaOnlyWhenAsked(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	h := New().Handler()
	const image, video = "grok/grok-imagine-image", "grok/grok-imagine-video"
	get := func(path string, header map[string]string) (int, string) {
		r := httptest.NewRequest("GET", path, nil)
		for k, v := range header {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	type row struct{ ID, Kind string }
	list := func(path string, header map[string]string) []row {
		t.Helper()
		code, body := get(path, header)
		if code != 200 {
			t.Fatalf("%s %v: %d %s", path, header, code, body)
		}
		var l struct{ Data []row }
		if err := json.Unmarshal([]byte(body), &l); err != nil {
			t.Fatalf("%s %v: %s", path, header, body)
		}
		return l.Data
	}
	agents := map[string]map[string]string{
		"plain":          nil,
		"opencode":       {"User-Agent": "opencode/1.2.3"},
		"cherry":         {"User-Agent": "CherryStudio/1.5"},
		"claude-desktop": {"User-Agent": "Mozilla/5.0 (Macintosh) Claude/1.2.3 Chrome/130"},
		"science":        {"X-Anthropic-Surface": "operon", "User-Agent": "Anthropic/JS 0.60.0"},
		"cursor":         {"User-Agent": "Cursor-CLI/1.0"},
		"magpie-key":     {"Authorization": "Bearer magpie-acme"},
	}
	for name, header := range agents {
		rows := list("/v1/models", header)
		if len(rows) == 0 && name == "plain" {
			t.Fatalf("plain list is empty")
		}
		for _, m := range rows {
			if m.Kind == "image" || m.Kind == "video" || strings.Contains(m.ID, "imagine") {
				t.Errorf("%s's chat list offers %s (%s)", name, m.ID, m.Kind)
			}
		}
		if _, body := get("/v1/models?format=text", header); strings.Contains(body, "imagine") {
			t.Errorf("%s's text list offers a media model:\n%s", name, body)
		}
	}

	kinds := func(rows []row) (chat, img, vid []string) {
		for _, m := range rows {
			switch m.Kind {
			case "image":
				img = append(img, m.ID)
			case "video":
				vid = append(vid, m.ID)
			default:
				chat = append(chat, m.ID)
			}
		}
		return
	}
	if chat, img, vid := kinds(list("/v1/models?kind=image,video", nil)); len(chat) != 0 || !slices.Contains(img, image) || !slices.Contains(vid, video) {
		t.Fatalf("kind=image,video: chat %v image %v video %v", chat, img, vid)
	}
	if chat, img, vid := kinds(list("/v1/models?kind=video", nil)); len(chat) != 0 || len(img) != 0 || !slices.Contains(vid, video) {
		t.Fatalf("kind=video: chat %v image %v video %v", chat, img, vid)
	}
	if chat, img, vid := kinds(list("/v1/models?kind=chat&kind=Image", nil)); len(chat) == 0 || !slices.Contains(img, image) || len(vid) != 0 {
		t.Fatalf("kind=chat&kind=Image: chat %v image %v video %v", chat, img, vid)
	}
	if chat, img, vid := kinds(list("/v1/models?kind=all", nil)); len(chat) == 0 || len(img) == 0 || len(vid) == 0 {
		t.Fatalf("kind=all: chat %v image %v video %v", chat, img, vid)
	}
	if _, body := get("/v1/models?kind=image&format=text", nil); !strings.Contains(body, image) || strings.Contains(body, video) {
		t.Fatalf("kind=image&format=text:\n%s", body)
	}
	for _, bad := range []string{"images", "embedding", "", "chat,"} {
		if code, body := get("/v1/models?kind="+bad, nil); code != 400 {
			t.Errorf("kind=%q: %d %s", bad, code, body)
		}
	}
}
