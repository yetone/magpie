package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Ollama's /v1/models lists no window. What a model runs with is in its
// /api/show: the Modelfile's `PARAMETER num_ctx N` in "parameters" (a
// text, one "name value" per line) is the context Ollama allocates for it,
// and the architecture's own maximum is model_info."<arch>.context_length".
// A request asks for no more than num_ctx, since Ollama truncates the
// prompt to it, and a bigger window told to an agent only overruns the
// memory the user sized the Modelfile to keep (maohy915 on X). So num_ctx
// is the window when the Modelfile sets it, and the architecture's maximum
// is only the fallback (a model with no num_ctx runs on the server's
// default, but its maximum is the most it can be asked for).
//
// A name that says its window ("qwen-3.8-27b-128k", catalog.NameWindow) is
// the window over the architecture's maximum, and under a num_ctx the
// Modelfile sets.
//
// The window lands in catalog.Model.Context, under what the user typed
// (Provider.Contexts, windowIn) and over models.dev's guess. The reply
// limit is a quarter of it up to 32K when neither the user nor models.dev
// say, and compaction follows the window, which Codex and Claude Code
// already hold their threshold under (settings.Compact).

// ollamaShowFor bounds the whole of the /api/show round, so a server with
// hundreds of models, or one that hangs, doesn't hold the list back.
const ollamaShowFor = 6 * time.Second

// ollamaShowEach is how many models are asked at once.
const ollamaShowEach = 8

// ollamaOutputCap is the most reply magpie assumes for a local model it
// knows no limit for.
const ollamaOutputCap = 32_768

// ollamaWindow reads a /api/show reply: the Modelfile's num_ctx, else the
// architecture's context_length. 0 is not said.
func ollamaWindow(b []byte) (window int, fromModelfile bool) {
	var r struct {
		Parameters string         `json:"parameters"`
		ModelInfo  map[string]any `json:"model_info"`
	}
	if json.Unmarshal(b, &r) != nil {
		return 0, false
	}
	for _, line := range strings.Split(r.Parameters, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "num_ctx" {
			if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
				return n, true
			}
		}
	}
	for k, v := range r.ModelInfo {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		if n, ok := v.(float64); ok && n > 0 {
			return int(n), false
		}
	}
	return 0, false
}

// withOllamaWindows gives the models of a local Ollama list the window each
// runs with, asked of its /api/show. A model whose show fails is left as
// the list had it. Nothing is written for one whose list already said a
// window.
func (p Provider) withOllamaWindows(ctx context.Context, base string, ms []catalog.Model) []catalog.Model {
	if p.Preset != "ollama" || len(ms) == 0 || base == "" {
		return ms
	}
	root := strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	ctx, cancel := context.WithTimeout(ctx, ollamaShowFor)
	defer cancel()
	out := append([]catalog.Model(nil), ms...)
	var wg sync.WaitGroup
	sem := make(chan struct{}, ollamaShowEach)
	for i := range out {
		if out[i].Context > 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			n, set := p.ollamaShow(ctx, root, out[i].ID)
			// a name that says its window ("qwen-3.8-27b-128k") is heard over
			// the architecture's maximum, which is only the fallback, but
			// not over a num_ctx the Modelfile sets (maohy915 on X)
			if named := catalog.NameWindow(out[i].ID); named > 0 && !set {
				n = named
			}
			if n <= 0 {
				return
			}
			out[i].Context = n
			if out[i].Output == 0 && catalog.OutputOf(out[i].ID) == 0 {
				out[i].Output = min(n/4, ollamaOutputCap)
			}
		}()
	}
	wg.Wait()
	return out
}

func (p Provider) ollamaShow(ctx context.Context, root, model string) (window int, fromModelfile bool) {
	body, _ := json.Marshal(map[string]string{"model": model, "name": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, root+"/api/show", bytes.NewReader(body))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Content-Type", "application/json")
	if err := p.Sign(ctx, req, Chat, body); err != nil {
		return 0, false
	}
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return 0, false
	}
	return ollamaWindow(b)
}
