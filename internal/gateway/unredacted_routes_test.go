package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// CrazyMouse_TT on X: masking turned off for a provider of the user's own
// model (Send requests unmasked, provider.Unredacted) holds on every route
// that reaches it, a chat's and a token count's (unredacted_test.go) and a
// picture's, a video's and Ark's own API's here. A provider left on, or a
// group with a member left on, stays masked.

const unmaskWord = "acmecorp"

func maskWordOn(t *testing.T) {
	t.Helper()
	if err := settings.Save(settings.Settings{Redact: true, RedactWords: []string{unmaskWord}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
}

func setUnredacted(t *testing.T, id string, on bool) {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	p.Unredacted = on
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
}

func TestUnredactedProviderDrawsWhole(t *testing.T) {
	for _, c := range []struct {
		name         string
		set, group   bool
		other, whole bool
	}{
		{name: "set", set: true, whole: true},
		{name: "unset"},
		{name: "a group of it alone", set: true, group: true, whole: true},
		{name: "a group with one left on", set: true, group: true, other: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, e := easeled(t)
			setUnredacted(t, "art", c.set)
			model := "art/gpt-image-1"
			if c.group {
				members := []string{"art/gpt-image-1"}
				if c.other {
					art, _ := provider.Find("art")
					o := *art
					o.ID, o.Name, o.Unredacted = "vendor", "Vendor", false
					if err := provider.Save(o); err != nil {
						t.Fatal(err)
					}
					members = append(members, "vendor/gpt-image-1")
				}
				if err := provider.SaveGroup(provider.Group{ID: "pics", Name: "pics", Members: members, Routing: "order"}); err != nil {
					t.Fatal(err)
				}
				model = "group/pics"
			}
			maskWordOn(t)
			code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+model+`","prompt":"the `+unmaskWord+` logo"}`)
			if code != 200 {
				t.Fatalf("%d %s", code, raw)
			}
			sent := e.got("/v1/images/generations")
			if len(sent) != 1 {
				t.Fatalf("sent %v", sent)
			}
			if whole := strings.Contains(sent[0], unmaskWord); whole != c.whole {
				t.Fatalf("drawn whole %v, want %v: %s", whole, c.whole, sent[0])
			}
		})
	}
}

func TestUnredactedProviderMakesVideosWhole(t *testing.T) {
	for _, c := range []struct {
		name       string
		set, whole bool
	}{
		{name: "set", set: true, whole: true},
		{name: "unset"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := newOpenAIVideos(t)
			p := saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "sk-studio", Unredacted: c.set})
			if p.Unredacted != c.set {
				t.Fatalf("saved %+v", p)
			}
			maskWordOn(t)
			h := New().Handler()
			code, _, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"studio/vid-1","prompt":"the `+unmaskWord+` logo","negative_prompt":"`+unmaskWord+`"}`)
			if code != 200 {
				t.Fatalf("%d %s", code, raw)
			}
			var sent map[string]any
			json.Unmarshal([]byte(up.bodies[0]), &sent)
			whole := sent["prompt"] == "the "+unmaskWord+" logo" && sent["negative_prompt"] == unmaskWord
			if whole != c.whole {
				t.Fatalf("made whole %v, want %v: %s", whole, c.whole, up.bodies[0])
			}
		})
	}
}

func TestUnredactedProviderArkNativeWhole(t *testing.T) {
	for _, c := range []struct {
		name       string
		set, whole bool
	}{
		{name: "set", set: true, whole: true},
		{name: "unset"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := newArkNative(t)
			was := isArk
			isArk = func(p provider.Provider) bool { return p.ID == "ark" }
			t.Cleanup(func() { isArk = was })
			if err := provider.Save(provider.Provider{ID: "ark", Name: "Volcengine Ark", Chat: up.URL + "/api/v3", Key: "key", Models: []string{"doubao-seedream-4-5-251128"}, Unredacted: c.set}); err != nil {
				t.Fatal(err)
			}
			maskWordOn(t)
			rec := arkNativeAsk(t, arkNativeMux(New()), access.Identity{KeyID: "desk", KeyName: "Desk"}, "POST", NativePath+"ark/images/generations", `{"model":"doubao-seedream-4-5-251128","prompt":"the `+unmaskWord+` logo"}`)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			up.mu.Lock()
			seen := strings.Join(up.seen, "\n")
			up.mu.Unlock()
			if whole := strings.Contains(seen, unmaskWord); whole != c.whole {
				t.Fatalf("sent whole %v, want %v: %s", whole, c.whole, seen)
			}
		})
	}
}

// Another magpie set to go unmasked is sent the request as written, told
// nothing was masked (no MaskedAsHeader), so it masks by its own settings:
// its vendor is still sent the word masked, and only once.
func TestUnredactedRemoteMagpieMasksOnItsOwnSide(t *testing.T) {
	remote, seen, masked := maskOnceSetup(t)
	setUnredacted(t, "office", true)
	// what the other magpie is sent, on its way there
	var hopHeader, hopBody string
	tap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hopHeader, hopBody = r.Header.Get(MaskedAsHeader), string(b)
		req, _ := http.NewRequest(r.Method, remote.URL+r.URL.RequestURI(), bytes.NewReader(b))
		req.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer res.Body.Close()
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
	}))
	t.Cleanup(tap.Close)
	office, _ := provider.Find("office")
	office.Chat = tap.URL + "/v1"
	if err := provider.Save(*office); err != nil {
		t.Fatal(err)
	}
	code, reply := postAs(t, New(), "", maskOnceBody)
	if code != 200 || !strings.Contains(reply, "done") {
		t.Fatalf("%d %s", code, reply)
	}
	if !strings.Contains(hopBody, unmaskWord) || hopHeader != "" {
		t.Fatalf("the other magpie was sent %q with %s %q, want the word as written and no header", hopBody, MaskedAsHeader, hopHeader)
	}
	body, _ := seen()
	if strings.Contains(body, unmaskWord) {
		t.Fatalf("the other magpie's vendor was sent the word unmasked: %s", body)
	}
	if n := masked.Load(); n != 1 {
		t.Errorf("masked %d times, want once (on the other magpie)", n)
	}
}
