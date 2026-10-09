package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// FetchGemini lists the models at a Gemini API's base (…/v1beta), as
// Google's Gemini API lists them: GET models, the key in x-goog-api-key,
// a page at a time (nextPageToken), each named models/{id} with the
// methods it serves. Only those that serve generateContent are kept: the
// list also has embedding models (embedContent), Imagen's (predict) and
// Veo's (predictLongRunning), which no agent can talk to (#1346). It
// answers the URL it asked first.
func FetchGemini(ctx context.Context, base, key string, headers map[string]string) ([]Model, string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, "", errors.New("no base URL")
	}
	at := base + "/models"
	var out []Model
	token := ""
	for range 20 {
		u := at + "?pageSize=1000"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		page, next, err := geminiPage(ctx, u, key, headers)
		if err != nil {
			return nil, at, err
		}
		out = append(out, page...)
		if token = next; token == "" {
			break
		}
	}
	if len(out) == 0 {
		return nil, at, errors.New(at + ": no models listed")
	}
	return out, at, nil
}

// geminiModel is one model of a Gemini API's list.
type geminiModel struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Methods     []string `json:"supportedGenerationMethods"`
	Input       int      `json:"inputTokenLimit"`
	Output      int      `json:"outputTokenLimit"`
}

func geminiPage(ctx context.Context, u, key string, headers map[string]string) ([]Model, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "magpie")
	if key != "" {
		req.Header.Set("x-goog-api-key", key)
	}
	for k, v := range headers {
		req.Header[k] = []string{v}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		if msg := errorMessage(b); msg != "" {
			return nil, "", fmt.Errorf("%s: %s (%s)", u, res.Status, msg)
		}
		return nil, "", fmt.Errorf("%s: %s", u, res.Status)
	}
	var v struct {
		Models []geminiModel `json:"models"`
		Next   string        `json:"nextPageToken"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, "", fmt.Errorf("%s: not a model list", u)
	}
	var out []Model
	for _, r := range v.Models {
		id := strings.TrimPrefix(r.Name, "models/")
		if id == "" || len(r.Methods) > 0 && !slices.Contains(r.Methods, "generateContent") {
			continue
		}
		m := Model{ID: id, Name: id, Context: r.Input, Output: r.Output}
		if r.DisplayName != "" {
			m.Name = r.DisplayName
		}
		out = append(out, m)
	}
	return out, v.Next, nil
}
