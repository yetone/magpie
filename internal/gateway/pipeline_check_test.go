package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Settings › Models' check of each pipeline (EV_John_Liu on X) makes one
// real call through the provider set up for it, and says ok, or what the
// vendor said.

func TestCheckImageDrawsOneDot(t *testing.T) {
	s, e := easeled(t)
	st := settings.Load()
	st.ImageGen = "art/gpt-image-1"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	c := s.CheckPipeline(context.Background(), PipelineImage)
	if !c.OK || c.Model != "art/gpt-image-1" || c.Status != 200 || c.Error != "" {
		t.Fatalf("%+v", c)
	}
	sent := e.got("/v1/images/generations")
	if len(sent) != 1 || !strings.Contains(sent[0], `"prompt":"a dot"`) || !strings.Contains(sent[0], `"n":1`) || !strings.Contains(sent[0], `"quality":"low"`) {
		t.Fatalf("vendor was sent %v", sent)
	}
	st.ImageGen = "off"
	settings.Save(st)
	if c := s.CheckPipeline(context.Background(), PipelineImage); c.OK || !c.Unset || len(e.got("/v1/images/generations")) != 1 {
		t.Fatalf("off: %+v", c)
	}
}

func TestCheckSpeechSaysAWord(t *testing.T) {
	s, sp := speakerUp(t)
	c := s.CheckPipeline(context.Background(), PipelineSpeech)
	if !c.OK || c.Model != "voice/gpt-4o-mini-tts" || c.Status != 200 {
		t.Fatalf("%+v", c)
	}
	sent, _ := sp.got()
	if len(sent) != 1 || !bytes.Contains(sent[0], []byte(`"model":"gpt-4o-mini-tts"`)) || !bytes.Contains(sent[0], []byte(`"input":"Hi."`)) {
		t.Fatalf("vendor was sent %q", sent)
	}
}

// The vendor's refusal comes back in its own words.
func TestCheckSpeechSaysTheVendorsError(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"voice alloy isn't one of this model's"}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "voice", Name: "Voice", Chat: up.URL + "/v1", Key: "key", Models: []string{"tts-1"}}); err != nil {
		t.Fatal(err)
	}
	c := New().CheckPipeline(context.Background(), PipelineSpeech)
	if c.OK || c.Unset || c.Status != 400 || !strings.Contains(c.Error, "voice alloy isn't one of this model's") {
		t.Fatalf("%+v", c)
	}
}

func TestCheckTranscriptionSendsASecondOfSilence(t *testing.T) {
	s, sc := scribed(t)
	c := s.CheckPipeline(context.Background(), PipelineTranscription)
	if !c.OK || c.Model != "ears/whisper-1" || c.Status != 200 {
		t.Fatalf("%+v", c)
	}
	sent, types := sc.got("/v1/audio/transcriptions")
	if len(sent) != 1 || !strings.HasPrefix(types[0], "multipart/form-data") || !bytes.Contains(sent[0], []byte("RIFF")) || !bytes.Contains(sent[0], []byte("whisper-1")) {
		t.Fatalf("vendor was sent %d %v", len(sent), types)
	}
	if n := len(silentWAV(1e9)); n != 44+32000 {
		t.Errorf("a second of 16 kHz 16-bit mono is %d bytes", n)
	}
}

// With no provider for a pipeline the check says so, as unset, and calls
// nothing.
func TestCheckUnsetPipelines(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "chat", Name: "Chat", Chat: "http://127.0.0.1:1/v1", Key: "key", Models: []string{"gpt-5"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, k := range []string{PipelineTranscription, PipelineSpeech} {
		if c := s.CheckPipeline(context.Background(), k); c.OK || !c.Unset || c.Error == "" || c.Status != 0 {
			t.Errorf("%s: %+v", k, c)
		}
	}
}

// A disabled model is none a check takes.
func TestCheckSkipsADisabledModel(t *testing.T) {
	speakerUp(t)
	if err := provider.SetDisabledModels([]string{"voice/gpt-4o-mini-tts"}); err != nil {
		t.Fatal(err)
	}
	if got := FirstServing("speech"); got != "" {
		t.Fatalf("a disabled model was taken: %q", got)
	}
}

func TestCheckSearchAsksOnce(t *testing.T) {
	fresh(t)
	st := settings.Load()
	st.Searcher = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	if c := s.CheckPipeline(context.Background(), PipelineSearch); c.OK || !c.Unset {
		t.Fatalf("no search API: %+v", c)
	}
	f := newSearchAPIs(t)
	if err := provider.SetSearchAPI(f.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	if c := s.CheckPipeline(context.Background(), PipelineSearch); !c.OK || c.Error != "" {
		t.Fatalf("%+v", c)
	}
	f.mu.Lock()
	asked := f.asked
	f.mu.Unlock()
	if len(asked) != 1 || asked[0] != "tavily: magpie" {
		t.Fatalf("asked %v", asked)
	}
	f.mu.Lock()
	f.broken = true
	f.mu.Unlock()
	if c := s.CheckPipeline(context.Background(), PipelineSearch); c.OK || c.Unset || c.Error == "" {
		t.Fatalf("broken: %+v", c)
	}
}

// With several search APIs, the check asks each of them once and says what
// each found, read as Settings reads it: a search stops at the first that
// answers, so a check made as a search said ok while the second was down
// (heyiwei5 on X).
func TestCheckSearchAsksEveryAPI(t *testing.T) {
	fresh(t)
	st := settings.Load()
	st.Searcher = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	f := newSearchAPIs(t)
	for _, a := range []provider.SearchAPI{f.api("exa", "exa-k"), f.api("tavily", "tvly-k")} {
		if err := provider.SetSearchAPI(a); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	f.broken = true // tavily, the second
	f.mu.Unlock()
	b, _ := json.Marshal(New().CheckPipeline(context.Background(), PipelineSearch))
	var got struct {
		OK       bool
		Unset    bool
		Searches []struct {
			Name  string
			OK    bool
			Error string
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Unset {
		t.Errorf("a broken API reads as ok: %s", b)
	}
	if len(got.Searches) != 2 || got.Searches[0].Name != "Exa" || !got.Searches[0].OK ||
		got.Searches[1].Name != "Tavily" || got.Searches[1].OK || got.Searches[1].Error == "" {
		t.Fatalf("each API's own row: %s", b)
	}
	f.mu.Lock()
	asked := slices.Clone(f.asked)
	f.mu.Unlock()
	slices.Sort(asked)
	if !slices.Equal(asked, []string{"exa: magpie", "tavily: magpie"}) {
		t.Fatalf("one call each: asked %v", asked)
	}
}
