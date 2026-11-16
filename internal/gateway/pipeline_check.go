package gateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Pipelines magpie serves besides chat, each of which Settings › Models
// can check with one real call (EV_John_Liu on X).
const (
	PipelineImage         = "image"
	PipelineTranscription = "transcription"
	PipelineSpeech        = "speech"
	PipelineSearch        = "search"
)

// Pipelines lists them in the order Settings shows them.
var Pipelines = []string{PipelineImage, PipelineTranscription, PipelineSpeech, PipelineSearch}

// CheckAgent is the agent a check's calls are counted for, so Usage and
// Routing name them apart from an agent's own.
const CheckAgent = "check"

// PipelineCheck is what one check found. Unset is that nothing is set up
// to serve the pipeline, which isn't a failure of anything set up.
type PipelineCheck struct {
	Kind   string `json:"kind"`
	OK     bool   `json:"ok"`
	Unset  bool   `json:"unset,omitempty"`
	Model  string `json:"model,omitempty"`
	Status int    `json:"status,omitempty"`
	Millis int64  `json:"ms"`
	Error  string `json:"error,omitempty"`
	// Searches is the web search check's part per searcher: the provider
	// that searches, and each search API on, each asked once
	// (heyiwei5 on X: a second API was never checked).
	Searches []SearchCheck `json:"searches,omitempty"`
}

// SearchCheck is what one searcher's search found in a check: its name,
// and ok with how long it took, or its error.
type SearchCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Millis int64  `json:"ms"`
	Error  string `json:"error,omitempty"`
}

// FirstServing is the first model of kind's API (catalog.ServesSpeech,
// catalog.ServesTranscription) a provider that is on lists, in the
// Providers order, as provider/model, which the gateway resolves to that
// provider; "" when none does. Neither API has a default in Settings, so
// a check takes this one. A model the user disabled is passed over.
func FirstServing(kind string) string {
	off := provider.DisabledModels()
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() || p.ServesNone() {
			continue
		}
		var ids []string
		for _, id := range p.Models {
			if catalog.ServesOf(id) == kind {
				ids = append(ids, id)
			}
		}
		for _, m := range catalog.LiveServing(p.ID, kind) {
			ids = append(ids, m.ID)
		}
		for _, m := range provider.KeptListing(p).Available {
			if catalog.ServesOf(m.ID) == kind {
				ids = append(ids, m.ID)
			}
		}
		for _, m := range p.PlanRetrieval(kind) {
			ids = append(ids, m.ID)
		}
		for _, id := range ids {
			if !slices.ContainsFunc(off, func(pat string) bool { return provider.DisabledBy(pat, p.ID, id) }) {
				return p.ID + "/" + id
			}
		}
	}
	return ""
}

// CheckPipeline makes the smallest real call kind's pipeline serves, as an
// agent would through this gateway: a one-dot drawing, a second of silence
// to transcribe, a word to speak, one web search. It costs the user what
// that call costs, so it is made only when they ask.
func (s *Server) CheckPipeline(ctx context.Context, kind string) PipelineCheck {
	c := PipelineCheck{Kind: kind}
	start := time.Now()
	switch kind {
	case PipelineImage:
		m, ok := drawer()
		if !ok {
			c.Unset, c.Error = true, "No model draws images"
			return c
		}
		c.Model = m
		body := map[string]any{"prompt": "a dot", "n": 1}
		if strings.Contains(m, "gpt-image") {
			body["quality"] = "low"
		}
		b, _ := json.Marshal(body)
		s.checkCall(ctx, &c, "/v1/images/generations", "application/json", b)
	case PipelineTranscription, PipelineSpeech:
		api := catalog.ServesTranscription
		if kind == PipelineSpeech {
			api = catalog.ServesSpeech
		}
		ref := FirstServing(api)
		if ref == "" {
			c.Unset = true
			c.Error = "No provider lists a speech-to-text model"
			if kind == PipelineSpeech {
				c.Error = "No provider lists a text-to-speech model"
			}
			return c
		}
		c.Model = ref
		model := ref
		if kind == PipelineSpeech {
			b, _ := json.Marshal(map[string]any{"model": model, "input": "Hi.", "voice": "alloy"})
			s.checkCall(ctx, &c, "/v1/audio/speech", "application/json", b)
			break
		}
		var form bytes.Buffer
		w := multipart.NewWriter(&form)
		_ = w.WriteField("model", model)
		f, _ := w.CreateFormFile("file", "silence.wav")
		_, _ = f.Write(silentWAV(time.Second))
		_ = w.Close()
		s.checkCall(ctx, &c, "/v1/audio/transcriptions", w.FormDataContentType(), form.Bytes())
	case PipelineSearch:
		s.checkSearch(ctx, &c)
		if c.Unset {
			return c
		}
	default:
		c.Error = "no such pipeline: " + kind
		return c
	}
	c.Millis = time.Since(start).Milliseconds()
	return c
}

// checkSearch asks every searcher a search may go to once, together: the
// provider that searches (searcher), unless provider search is off, and
// each search API on, in the order a search asks them. A search itself
// stops at the first that answers, so checking it alone said nothing of
// the rest (heyiwei5 on X). Each is asked by the path a search takes to it
// (searchBy, askSearchAPIKeys), with no other in its place, so a row's ok
// is that searcher's own. c is ok when every one is.
func (s *Server) checkSearch(ctx context.Context, c *PipelineCheck) {
	ctx = netproxy.With(context.WithValue(ctx, searchForKey{}, &CallFor{Agent: CheckAgent}), "")
	ctx = withSearchCount(ctx, 1)
	const query = "magpie"
	if !searchServed() {
		c.Unset, c.Error = true, errSearchOff.Error()
		return
	}
	type one struct {
		name string
		ask  func(context.Context) error
	}
	var ones []one
	for _, a := range provider.SearchAPIs() {
		ones = append(ones, one{a.Name(), func(ctx context.Context) error {
			q, err := maskedQuery(ctx, query, nil)
			if err != nil {
				return err
			}
			pages, err := s.askSearchAPIKeys(ctx, a, q)
			if err == nil && len(pages) == 0 {
				err = errFoundNothing
			}
			return err
		}})
	}
	if settings.Shared().Searcher != "off" {
		if p, model, ok := searcher(); ok {
			by := one{searcherName(p, model), func(ctx context.Context) error {
				ctx, cancel := sideDeadline(context.WithValue(ctx, searchingKey{}, true), "search", searchTimeout, searchWaitMost)
				defer cancel()
				_, _, err := s.searchBy(ctx, p, model, query, searchEffort(p, model))
				return err
			}}
			if settings.Shared().SearchFirst == settings.SearchFirstAPI {
				ones = append(ones, by)
			} else {
				ones = append([]one{by}, ones...)
			}
		}
	}
	if len(ones) == 0 {
		c.Unset, c.Error = true, errNoSearcher.Error()
		return
	}
	c.Searches = make([]SearchCheck, len(ones))
	var wg sync.WaitGroup
	for i, o := range ones {
		wg.Go(func() {
			start := time.Now()
			err := o.ask(ctx)
			r := SearchCheck{Name: o.name, OK: err == nil, Millis: time.Since(start).Milliseconds()}
			if err != nil {
				r.Error = err.Error()
			}
			c.Searches[i] = r
		})
	}
	wg.Wait()
	var bad []string
	for _, r := range c.Searches {
		if !r.OK {
			bad = append(bad, r.Name+": "+r.Error)
		}
	}
	c.OK, c.Error = len(bad) == 0, strings.Join(bad, "; ")
}

// checkCall posts body to the gateway's own route, as CheckAgent, and
// reads the answer into c: ok on a 200 with a body, else the vendor's
// error in its words.
func (s *Server) checkCall(ctx context.Context, c *PipelineCheck, path, ctype string, body []byte) {
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, URL()+path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:0"
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+TokenFor(CheckAgent))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	c.Status = rec.Code
	b := rec.Body.Bytes()
	if rec.Code == http.StatusOK && len(b) > 0 {
		c.OK = true
		return
	}
	if rec.Code == http.StatusOK {
		c.Error = "the answer was empty"
		return
	}
	c.Error = provider.APIError(b, http.StatusText(rec.Code))
}

// silentWAV is d of silence as a 16 kHz mono 16-bit WAV file.
func silentWAV(d time.Duration) []byte {
	const rate = 16000
	n := int(d.Seconds()*rate) * 2
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + n))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // PCM
	w(uint16(1)) // mono
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(n))
	b.Write(make([]byte, n))
	return b.Bytes()
}
