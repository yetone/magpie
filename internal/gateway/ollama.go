package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// The Ollama API's model routes, for a client that takes a custom endpoint
// only as an Ollama server (Visual Studio's Copilot BYOK, wani on Discord;
// VS Code's Copilot "Ollama" provider): the user enters the gateway's own
// address (http://127.0.0.1:3425) as the Ollama endpoint. VS Code's
// Copilot (vscode-copilot-chat, ollamaProvider.ts) asks /api/version and
// refuses a server older than 0.6.4, lists /api/tags and takes each
// model's "model", asks /api/show of each (a model whose show fails or
// has no capabilities is left out), and chats on the server's
// OpenAI-compatible /v1/chat/completions, which magpie serves already.
// These routes sit on Handler's mux, so lanGuard, corsGuard and the key
// limits judge them as every route.

// ollamaVersion is the Ollama release magpie answers /api/version as: one
// past every client's minimum (VS Code's Copilot asks 0.6.4).
const ollamaVersion = "0.12.6"

func (s *Server) ollamaVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"version": ollamaVersion})
}

// ollamaTags lists the models the agent asking is shown, as Ollama's
// /api/tags lists the pulled ones: name and model are magpie's id, which
// the client sends back as the model to chat with.
func (s *Server) ollamaTags(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range catalogFor(r) {
		models = append(models, map[string]any{
			"name": e.ID, "model": e.ID, "modified_at": ollamaModified, "size": 0,
			"digest": ollamaDigest(e.ID), "details": ollamaDetails(e), "capabilities": ollamaCapabilities(e),
		})
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// ollamaName is the model id a request names: the ollama CLI names a
// model with no tag as name:latest, which is the bare id unless a model is
// really called that.
func ollamaName(r *http.Request, id string) string {
	if _, ok := ollamaEntry(r, id); ok {
		return id
	}
	if bare, cut := strings.CutSuffix(id, ":latest"); cut {
		if _, ok := ollamaEntry(r, bare); ok {
			return bare
		}
	}
	return id
}

// ollamaShow is one model as Ollama's /api/show tells it. A client reads
// what it takes from capabilities, its window from model_info's
// "<general.architecture>.context_length" (VS Code's Copilot takes 32768
// without it) and its name from "general.basename".
func (s *Server) ollamaShow(w http.ResponseWriter, r *http.Request) {
	body, ok := s.requestBody(w, r, provider.Chat)
	if !ok {
		return
	}
	var req struct {
		Model string `json:"model"`
		Name  string `json:"name"` // what clients before Ollama 0.5 send
	}
	if err := json.Unmarshal(body, &req); err != nil {
		ollamaError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	id := strings.TrimSpace(req.Model)
	if id == "" {
		id = strings.TrimSpace(req.Name)
	}
	if id == "" {
		ollamaError(w, http.StatusBadRequest, "model is required")
		return
	}
	id = ollamaName(r, id)
	e, ok := ollamaEntry(r, id)
	if !ok {
		ollamaError(w, http.StatusNotFound, "model '"+id+"' not found")
		return
	}
	// every key here is sent: the ollama CLI before 0.7 reads
	// parameter_count, context_length and embedding_length without
	// checking them and panics on one missing ("ollama show", 0.6.8)
	window := e.Context
	if window <= 0 {
		// what VS Code's Copilot assumes without one; a 0 it would take
		// as the window
		window = ollamaDefaultContext
	}
	info := map[string]any{
		"general.architecture": ollamaArch, "general.basename": provider.LabelsFor(agentOf(r), []provider.Entry{e})[0],
		"general.parameter_count": 0, ollamaArch + ".context_length": window, ollamaArch + ".embedding_length": 0,
	}
	writeJSON(w, 200, map[string]any{
		"modelfile": "", "parameters": "", "template": "",
		"details": ollamaDetails(e), "model_info": info, "capabilities": ollamaCapabilities(e),
		"modified_at": ollamaModified,
	})
}

// ollamaCapabilities are what the model does, in Ollama's words: it
// completes and calls tools (every model magpie serves is asked with
// tools), sees when it takes images, and thinks when it reasons.
func ollamaCapabilities(e provider.Entry) []string {
	caps := []string{"completion", "tools"}
	if e.Images {
		caps = append(caps, "vision")
	}
	if e.Thinks() {
		caps = append(caps, "thinking")
	}
	return caps
}

// ollamaArch is the architecture every model is told as: model_info's
// keys are named after it.
const ollamaArch = "magpie"

// ollamaDefaultContext is the window told for a model whose own magpie
// doesn't know: the one VS Code's Copilot takes when none is told.
const ollamaDefaultContext = 32768

// ollamaModified stands in for when a model was pulled, which no model
// here was.
const ollamaModified = "2025-01-01T00:00:00Z"

// ollamaEntry is the model id names among those the caller is shown (a
// gateway key held to some models is shown those alone): by its catalog
// id, or its bare id when the user lists bare ids.
func ollamaEntry(r *http.Request, id string) (provider.Entry, bool) {
	shown := catalogFor(r)
	id = unprefixed(id)
	for _, e := range shown {
		if e.ID == id {
			return e, true
		}
	}
	if provider.BareModelIDs() {
		return provider.BareEntry(shown, id)
	}
	return provider.Entry{}, false
}

func ollamaDetails(e provider.Entry) map[string]any {
	d := map[string]any{"parent_model": "", "format": "", "family": ollamaArch, "families": []string{ollamaArch},
		"parameter_size": "", "quantization_level": ""}
	if e.Context > 0 {
		d["context_length"] = e.Context
	}
	return d
}

// ollamaDigest stands in for a model file's digest: the same id always
// gives the same one.
func ollamaDigest(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// ollamaError is an error as Ollama's API says one.
func ollamaError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
