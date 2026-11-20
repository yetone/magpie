package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// caa536733's rule for a remote magpie's quota, credit and rate refusals,
// on the two faces it left out: a restricted account, and one its vendor
// wants verified first. Both are the remote's own account, on the model
// that account serves there — the remote rests that account itself, and
// its other models keep answering. The refusal passes here in magpie's own
// words (restrictedSaid reads them whoever answered), and rested the whole
// remote for the two hours restrictedRest names (verifyRest's half hour for
// a verify), so the group's next request went to the member on this
// computer instead of the remote's model that was fine.
func TestRemoteMagpieRestrictedRestsTheModel(t *testing.T) {
	fresh(t)
	var asked []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req.Model)
		w.Header().Set("Content-Type", "application/json")
		if req.Model == "workbuddy/glm-5.3-flash" {
			// the plugin's restricted account, as it says it (#1849: the
			// 403 11140 ends in magpie's own hint words)
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"message":"403 11140: `+provider.WBRestrictedHint+`"}}`)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+req.Model+`"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"workbuddy/glm-5.3-flash", "kimi/kimi-k3"}}); err != nil {
		t.Fatal(err)
	}
	other := &keyed{}
	serveOn(t, "other", "ko", []string{"m"}, other)
	// the remote's restricted model first, this computer's member behind
	// it: the refusal is noted with someone left, and other answers
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"office/workbuddy/glm-5.3-flash", "other/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "R", Members: []string{"office/kimi/kimi-k3", "other/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from ko") {
		t.Fatalf("glm restricted on the remote: %d %s", code, body)
	}
	if r, ok := restOf("office"); ok {
		t.Errorf("the whole remote magpie rests for one model's restricted account: %+v", r)
	}
	r, ok := restOf("office/workbuddy/glm-5.3-flash")
	if !ok || r.Why != failRestricted || r.By != "restricted" || !r.Until.After(time.Now()) {
		t.Errorf("the restricted model doesn't rest as restricted: %+v (%v)", r, rests())
	}
	// the remote's kimi, with nothing wrong with it there, is asked where
	// the group puts it: first, ahead of the member on this computer
	if code, body := postAs(t, s, "", `{"model":"group/r","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from kimi/kimi-k3") {
		t.Fatalf("kimi on the remote after glm was restricted: %d %s", code, body)
	}
	if len(asked) != 2 || asked[0] != "workbuddy/glm-5.3-flash" || asked[1] != "kimi/kimi-k3" {
		t.Errorf("the remote was asked %v, want glm once and kimi once", asked)
	}
}

// A verify refusal of the remote's own account is the model's too, as its
// quota, credit and rate refusals are; the key's own limit there still
// rests the whole remote (#2077).
func TestRemoteMagpieVerifyAndKeyLimitScoping(t *testing.T) {
	c := candidate{p: provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Key: "k"}, model: "gemini/gemini-3-flash", rest: "office"}
	if !remoteModelRest(c, failRestricted, http.Header{}) || !remoteModelRest(c, failVerify, http.Header{}) {
		t.Error("a restricted or verify refusal from the other magpie rests the whole remote, not the model")
	}
	h := http.Header{}
	h.Set(remoteKeyLimitHeader, "2026-10-15T00:00:00Z")
	if remoteModelRest(c, failRestricted, h) || remoteModelRest(c, failVerify, h) || remoteModelRest(c, failQuota, h) {
		t.Error("the key's own limit there rests only the model, not the key")
	}
	if remoteModelRest(candidate{p: provider.Provider{ID: "office", Key: "k"}, model: "m", rest: "office"}, failRestricted, http.Header{}) {
		t.Error("a provider that isn't a remote magpie rests the model on a restricted refusal")
	}
}

// A verify refusal from the remote, now resting only its model, is still
// given again without asking while it is held (#152): the agent's
// reconnects aren't sent on to the remote that just refused, and the
// remote's other models are still asked.
func TestRemoteMagpieVerifyHeldOnTheModel(t *testing.T) {
	fresh(t)
	var asked []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req.Model)
		w.Header().Set("Content-Type", "application/json")
		if req.Model == "antigravity/gemini-3-pro" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, verifyLinked)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+req.Model+`"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"antigravity/gemini-3-pro", "kimi/kimi-k3"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	req := `{"model":"office/antigravity/gemini-3-pro","messages":[{"role":"user","content":"hi"}]}`
	code, body := postAs(t, s, "", req)
	msg := provider.APIError([]byte(body), "")
	if code != 403 || !strings.Contains(msg, "Verify your account to continue.") {
		t.Fatalf("%d %s", code, body)
	}
	for range 2 {
		if code, again := postAs(t, s, "", req); code != 403 || !sameVerify(provider.APIError([]byte(again), ""), msg) {
			t.Fatalf("held: %d %s", code, again)
		}
	}
	if code, body := postAs(t, s, "", `{"model":"office/kimi/kimi-k3","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from kimi/kimi-k3") {
		t.Fatalf("the remote's other model: %d %s", code, body)
	}
	if len(asked) != 2 || asked[0] != "antigravity/gemini-3-pro" || asked[1] != "kimi/kimi-k3" {
		t.Errorf("the remote was asked %v, want the verify once and kimi once", asked)
	}
}
