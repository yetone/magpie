package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// gatedEye is eyeUp with each description held until gate is closed or
// its request ends; asked counts the descriptions asked for, and arrived
// says each one has come.
type gatedEye struct {
	eyeUp
	mu      sync.Mutex
	asked   int
	arrived chan struct{}
	gate    chan struct{}
	open    func() // closes gate, once
}

func (u *gatedEye) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if strings.HasSuffix(modelOf(body), "eye") {
		u.mu.Lock()
		u.asked++
		u.mu.Unlock()
		u.arrived <- struct{}{}
		select {
		case <-u.gate:
		case <-r.Context().Done():
			return
		}
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	u.eyeUp.ServeHTTP(w, r)
}

func (u *gatedEye) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.asked
}

// gateEyed is eyed with every description held at the gate.
func gateEyed(t *testing.T) (*Server, *gatedEye) {
	t.Helper()
	fresh(t)
	u := &gatedEye{arrived: make(chan struct{}, 256), gate: make(chan struct{})}
	up := httptest.NewServer(u)
	var once sync.Once
	u.open = func() { once.Do(func() { close(u.gate) }) }
	t.Cleanup(func() {
		u.open()
		up.Close()
	})
	if err := provider.Save(provider.Provider{ID: "probe", Name: "Probe", Chat: up.URL + "/v1", Key: "key", Models: []string{"text", "eye"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
		{ID: "eye", Images: true, ImageInput: imageInputBool(true)},
	}); err != nil {
		t.Fatal(err)
	}
	return New(), u
}

// arrive waits for n descriptions to be asked for.
func (u *gatedEye) arrive(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-u.arrived:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d descriptions were asked for", i, n)
		}
	}
}

// imagesBody is a Chat request to probe/text with n images, each its own.
func imagesBody(n int) string {
	var parts []string
	for i := range n {
		data := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "picture %d", i))
		parts = append(parts, `{"type":"image_url","image_url":{"url":"data:image/png;base64,`+data+`"}}`)
	}
	return `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"text","text":"read"},` + strings.Join(parts, ",") + `]}]}`
}

// sightWaiters is how many requests wait on the description of src.
func sightWaiters(s *Server, src string) int {
	s.sightMu.Lock()
	defer s.sightMu.Unlock()
	if e := s.sights[sightKey(src)]; e != nil {
		return e.waiting
	}
	return 0
}

// servedOn answers body, posted to path on ctx, on the channel it returns.
func servedOn(s *Server, ctx context.Context, path, body string) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx))
		done <- rec
	}()
	return done
}

func recorded(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-done:
		return rec
	case <-time.After(15 * time.Second):
		t.Fatal("the request was never answered")
	}
	return nil
}

// Pkh on Discord: AutoVision's pick changed between turns, and every image
// in the conversation was described again by the new one. A description is
// kept under the image's bytes: the same picture, sent on another
// protocol, under another type, with another model picked to describe it,
// isn't described again.
func TestSameImageDescribedOnceWhateverDescribesIt(t *testing.T) {
	s, u := eyed(t)
	code, body := postAs(t, s, "", pastedImage)
	if code != 200 {
		t.Fatalf("first turn: %d %s", code, body)
	}
	// another model that sees, now picked to describe images
	other := &eyeUp{}
	srv := httptest.NewServer(other)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Chat: srv.URL + "/v1", Key: "key", Models: []string{"bigeye"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("other", srv.URL+"/v1", []catalog.Model{{ID: "bigeye", Images: true, ImageInput: imageInputBool(true)}}); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.Vision = "other/bigeye"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	// the same bytes, on Messages, called a JPEG
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"probe/text","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"aGVsbG8="}}]}]}`)))
	if rec.Code != 200 {
		t.Fatalf("second turn: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(other.sent("bigeye")); n != 0 {
		t.Fatalf("the image, described already, was described again by the new pick (%d times)", n)
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("the image was described %d times", n)
	}
	got := u.sent("text")
	if len(got) != 2 || !strings.Contains(got[1], "HELLO") {
		t.Fatalf("the second turn wasn't given the kept description: %v", got)
	}
}

// One request's agent canceling it doesn't fail the description another
// request waits on: the description is no one request's, and the next
// waiting is given it when it comes.
func TestOneRequestGoneLeavesTheDescriptionToAnother(t *testing.T) {
	s, u := gateEyed(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := servedOn(s, ctx, "/v1/chat/completions", pastedImage)
	u.arrive(t, 1)
	second := servedOn(s, context.Background(), "/v1/chat/completions", pastedImage)
	src := "data:image/png;base64,aGVsbG8="
	deadline := time.Now().Add(10 * time.Second)
	for sightWaiters(s, src) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the second request never waited on the description (%d waiting)", sightWaiters(s, src))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	recorded(t, first)
	u.open()
	rec := recorded(t, second)
	if rec.Code != 200 {
		t.Fatalf("the request still waiting: %d %s", rec.Code, rec.Body.String())
	}
	if got := u.sent("text"); len(got) != 1 || !strings.Contains(got[0], "HELLO") {
		t.Fatalf("text model was sent %v", got)
	}
	if n := u.count(); n != 1 {
		t.Fatalf("the image was asked about %d times", n)
	}
}

// canceledWhileDescribing sends a request with ten images, cancels it once
// the first visionParallel are being described, and answers once it has
// returned.
func canceledWhileDescribing(t *testing.T) (*Server, *gatedEye) {
	t.Helper()
	s, u := gateEyed(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := servedOn(s, ctx, "/v1/chat/completions", imagesBody(10))
	u.arrive(t, visionParallel)
	cancel()
	recorded(t, done)
	return s, u
}

// Pkh on Discord: one canceled request went on to ask for 63 descriptions
// in 60ms, each its own row on the Routing page. The images still waiting
// for a slot when their request ends are asked of no one, and those being
// described stop with no one left waiting on them.
func TestCanceledRequestAsksNoMoreDescriptions(t *testing.T) {
	s, u := canceledWhileDescribing(t)
	if n := u.count(); n != visionParallel {
		t.Fatalf("a canceled request asked for %d descriptions, want the %d under way", n, visionParallel)
	}
	if rs := routesOf(s, "probe/eye"); len(rs) != visionParallel {
		t.Fatalf("a canceled request left %d description rows, want %d", len(rs), visionParallel)
	}
	s.sightMu.Lock()
	left := len(s.sights)
	s.sightMu.Unlock()
	if left != 0 {
		t.Fatalf("%d descriptions no one waits for were left in the cache", left)
	}
}

// Pkh on Discord: the request whose images were being described had no row
// of its own on the Routing page once it was canceled, only its
// descriptions'. It has one, ended as the agent canceled it.
func TestCanceledWhileDescribingKeepsItsRoute(t *testing.T) {
	s, _ := canceledWhileDescribing(t)
	rs := routesOf(s, "probe/text")
	if len(rs) != 1 || !rs[0].Done || rs[0].Status != 499 || !strings.Contains(rs[0].Error, "canceled") {
		t.Fatalf("the canceled request's routes: %+v", rs)
	}
}

// A gateway restart must not describe a conversation's images all over
// again. The description cache lives in the same config directory as the
// gateway, so the next Server starts with every completed description.
func TestDescriptionSurvivesGatewayRestart(t *testing.T) {
	s, u := eyed(t)
	body := imagesBody(1)
	if code, got := postAs(t, s, "", body); code != 200 {
		t.Fatalf("before restart: %d %s", code, got)
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("before restart: %d descriptions, want 1", n)
	}

	restarted := New()
	if code, got := postAs(t, restarted, "", body); code != 200 {
		t.Fatalf("after restart: %d %s", code, got)
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("after restart: %d descriptions, want the original 1", n)
	}
	if got := u.sent("text"); len(got) != 2 || !strings.Contains(got[1], "A red square with the word HELLO") || !strings.Contains(got[1], "probe/eye describes it") {
		t.Fatalf("the restored description/by wasn't sent to the text model: %v", got)
	}
}
