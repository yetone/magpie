package provider

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// MLXServePreset is the MLX-Serve preset's id.
const MLXServePreset = "mlx-serve"

// GPUStackPreset is GPUStack's preset id.
const GPUStackPreset = "gpustack"

// MLX-Serve (github.com/ddalcu/mlx-serve) serves decision models —
// Clef, Clef-Flash, Kev, Laya, D1 — at POST /v1/systemone (and /v1/decisions),
// System One's questions and answers. Its /v1/models lists them alongside
// chat models, marking them with capabilities: ["decisions"]. That list
// is the provider's decision models, as oMLX's and OpenRouter's are
// (listsDecisions), and a model without that capability stays a chat model.
func (p Provider) mlxServeDecides() bool {
	return p.Preset == MLXServePreset && p.Decide != "" && !p.DecideOnly()
}

// mlxServeDecisions is where MLX-Serve lists its models and capabilities.
func mlxServeDecisions(base string) string {
	return strings.TrimRight(base, "/") + "/models"
}

type mlxServeModel struct {
	ID              string   `json:"id"`
	ContextLength   int      `json:"context_length"`
	MaxModelLen     int      `json:"max_model_len"`
	Capabilities    []string `json:"capabilities"`
	InputModalities []string `json:"input_modalities"`
}

// listedMLXServeDecide are the decision models in MLX-Serve's /v1/models:
// those whose capabilities include "decisions", with the window and input
// modalities MLX-Serve serves them with.
func listedMLXServeDecide(b []byte) ([]catalog.Model, error) {
	var out struct {
		Data   *[]mlxServeModel `json:"data"`
		Models *[]mlxServeModel `json:"models"`
	}
	if err := json.Unmarshal(b, &out); err != nil || (out.Data == nil && out.Models == nil) {
		return nil, fmt.Errorf("not MLX-Serve's model list")
	}
	rows := out.Data
	if rows == nil {
		rows = out.Models
	}
	ms := []catalog.Model{}
	for _, m := range *rows {
		if m.ID == "" || !slices.Contains(m.Capabilities, "decisions") {
			continue
		}
		ctx := m.MaxModelLen
		if ctx <= 0 {
			ctx = m.ContextLength
		}
		var imageInput *bool
		images := false
		if len(m.InputModalities) > 0 {
			images = slices.Contains(m.InputModalities, "image")
			imageInput = &images
		}
		ms = append(ms, catalog.Model{
			ID:              m.ID,
			Name:            m.ID,
			Context:         ctx,
			ImageInput:      imageInput,
			Images:          images,
			InputModalities: m.InputModalities,
		})
	}
	return ms, nil
}
