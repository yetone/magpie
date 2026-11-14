package catalog

import (
	"slices"
	"testing"
)

// What a model takes, by bare id, is what most providers list it with, so
// a model a list gives bare (Vertex AI's, a relay's) is said to take audio
// when models.dev's providers say so. Nothing listed is nothing known.
func TestInputsOf(t *testing.T) {
	writeCatalog(t, `{
	  "google-vertex": {"models": {
	    "gemini-3.5-flash": {"id":"gemini-3.5-flash","modalities":{"input":["text","image","video","audio","pdf"],"output":["text"]}},
	    "openai/gpt-oss-120b-maas": {"id":"openai/gpt-oss-120b-maas","modalities":{"input":["text"],"output":["text"]}}
	  }},
	  "google": {"models": {"gemini-3.5-flash": {"id":"gemini-3.5-flash","modalities":{"input":["text","image","audio","video","pdf"],"output":["text"]}}}},
	  "relay": {"models": {"google/gemini-3.5-flash": {"id":"google/gemini-3.5-flash","modalities":{"input":["text","image"],"output":["text"]}}}},
	  "bare": {"models": {"plain": {"id":"plain"}}}
	}`)
	for id, want := range map[string][]string{
		"gemini-3.5-flash":         {"text", "image", "audio", "video", "pdf"},
		"google/gemini-3.5-flash":  {"text", "image", "audio", "video", "pdf"},
		"openai/gpt-oss-120b-maas": {"text"},
		"plain":                    nil,
		"unknown":                  nil,
	} {
		if got := InputsOf(id); !slices.Equal(got, want) {
			t.Errorf("InputsOf(%q) = %v, want %v", id, got, want)
		}
	}
}
