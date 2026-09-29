package dimagent

// The model listing: /v1/models?type=dim answers the account's models in
// OpenAI's envelope with a "dim" object of its own on each one, where the
// context window, the image input and the reasoning levels are kept.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// ModelInfo is one entry of the listing.
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
	// Dim is what the upstream says about the model beyond its id; nil on
	// an entry that carries nothing.
	Dim   *DimInfo `json:"dim,omitempty"`
	Rate  float64  `json:"rate,omitempty"`
	Price any      `json:"price,omitempty"`
}

// DimInfo is the "dim" object: the name to show, the limits, and what the
// model can think about.
type DimInfo struct {
	ID          string    `json:"id,omitempty"`
	Name        string    `json:"name,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	Status      string    `json:"status,omitempty"`
	Limit       *DimLimit `json:"limit,omitempty"`
	// Modalities arrives either flat (["text","image"]) or split
	// ({"input":[…],"output":[…]}); raw keeps both shapes readable.
	Modalities json.RawMessage `json:"modalities,omitempty"`
	Vision     *bool           `json:"vision,omitempty"`
	Reasoning  *DimReasoning   `json:"reasoning,omitempty"`
}

// DimLimit is a model's context and output size, in tokens.
type DimLimit struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// DimReasoning is what a model thinks, and how hard it can be asked to.
type DimReasoning struct {
	Supported        bool     `json:"supported,omitempty"`
	DefaultEnabled   bool     `json:"defaultEnabled,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	Effort           string   `json:"effort,omitempty"`
	EffortOptions    []string `json:"effortOptions,omitempty"`
	Interleaved      bool     `json:"interleaved,omitempty"`
	InterleavedField string   `json:"interleavedField,omitempty"`
}

// DisplayName is the name to show: the account's, or the id.
func (m *ModelInfo) DisplayName() string {
	if m == nil {
		return ""
	}
	if m.Dim != nil {
		if m.Dim.DisplayName != "" {
			return m.Dim.DisplayName
		}
		if m.Dim.Name != "" {
			return m.Dim.Name
		}
	}
	return m.ID
}

// Context is the model's window, 0 when the listing doesn't say.
func (m *ModelInfo) Context() int {
	if m == nil || m.Dim == nil || m.Dim.Limit == nil {
		return 0
	}
	return m.Dim.Limit.Context
}

// Output is the model's reply limit, 0 when the listing doesn't say.
func (m *ModelInfo) Output() int {
	if m == nil || m.Dim == nil || m.Dim.Limit == nil {
		return 0
	}
	return m.Dim.Limit.Output
}

// Images is whether the model takes pictures, said either by "vision" or
// by a modality list that names "image".
func (m *ModelInfo) Images() bool {
	if m == nil || m.Dim == nil {
		return false
	}
	if m.Dim.Vision != nil {
		return *m.Dim.Vision
	}
	if len(m.Dim.Modalities) == 0 {
		return false
	}
	var flat []string
	if json.Unmarshal(m.Dim.Modalities, &flat) == nil {
		return slicesHas(flat, "image")
	}
	var split struct {
		Input []string `json:"input"`
	}
	if json.Unmarshal(m.Dim.Modalities, &split) == nil {
		return slicesHas(split.Input, "image")
	}
	return false
}

// Efforts are the reasoning levels the model offers, lowest first, as
// magpie names them ("none" is asked for but never listed).
func (m *ModelInfo) Efforts() []string {
	if m == nil || m.Dim == nil || m.Dim.Reasoning == nil {
		return nil
	}
	r := m.Dim.Reasoning
	if !r.Supported || len(r.EffortOptions) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.EffortOptions))
	for _, e := range r.EffortOptions {
		if s := effortName(e); s != "" {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return effortRank(out[i]) < effortRank(out[j]) })
	return out
}

// effortOrder is the reasoning levels magpie knows, lowest first.
var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// effortRank is where a level sits in that order; one it doesn't know goes
// last.
func effortRank(s string) int {
	for i, e := range effortOrder {
		if e == s {
			return i
		}
	}
	return len(effortOrder)
}

// effortName is magpie's spelling of an effort the upstream names: its
// "medium" and magpie's agree, and so on down to "none".
func effortName(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto":
		return ""
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "extra_high", "extrahigh":
		return "xhigh"
	case "max":
		return "max"
	case "none", "disabled", "off":
		return "none"
	}
	return ""
}

func slicesHas(ss []string, want string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// IsRoutableModel is whether an id names one model an agent can pick. The
// upstream lists "auto" and "default" as aggregate entries that route
// between models inside it; neither is one model.
func IsRoutableModel(id string) bool {
	switch strings.TrimSpace(id) {
	case "", "auto", "default":
		return false
	}
	return true
}

// ParseModels reads the listing into magpie's catalog entries.
func ParseModels(body []byte) ([]catalog.Model, error) {
	ms, err := ModelInfos(body)
	if err != nil {
		return nil, err
	}
	out := make([]catalog.Model, 0, len(ms))
	for i := range ms {
		m := &ms[i]
		cm := catalog.Model{ID: m.ID, Name: m.DisplayName(), Provider: ProviderKey,
			Context: m.Context(), Output: m.Output(), Images: m.Images(),
			Efforts: m.Efforts(), APIs: []string{string(catalogAPIChat)}}
		if cm.Name == "" {
			cm.Name = m.ID
		}
		out = append(out, cm)
	}
	return out, nil
}

// catalogAPIChat is catalog.Model's "chat" tag, spelled here so this file
// is where the upstream's one API is said.
const catalogAPIChat = "chat"

// ErrNoModels is an account that lists nothing: the reply was well-formed
// and empty, which is what an ended subscription answers with. A caller can
// say that apart from a reply it couldn't read.
var ErrNoModels = errors.New("dimagent: the account lists no models; its subscription may have ended")

// ModelInfos reads the listing and keeps every routable entry, as the
// upstream said it, for a caller that wants more than the catalog.
func ModelInfos(body []byte) ([]ModelInfo, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("dimagent: no model list saved yet")
	}
	var resp struct {
		Success *bool       `json:"success"`
		Data    []ModelInfo `json:"data"`
		Object  string      `json:"object"`
		// A plain OpenAI list arrives with no envelope at all.
		Models []ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("dimagent: invalid models json: %w", err)
	}
	if resp.Success != nil && !*resp.Success {
		return nil, fmt.Errorf("dimagent: models response said success=false")
	}
	list := resp.Data
	if len(list) == 0 {
		list = resp.Models
	}
	// a bare OpenAI list: {"object":"list","data":[…]} was read as Data
	// above; nothing else to unwrap.
	if len(list) == 0 {
		// The upstream answered well and said the account may call nothing:
		// a subscription that has ended leaves no model to list. That is the
		// account's state, not a broken reply, so it says so apart.
		return nil, ErrNoModels
	}
	var out []ModelInfo
	for _, m := range list {
		if !IsRoutableModel(m.ID) {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("dimagent: the model list held %d entries, none a model magpie can route", len(list))
	}
	return out, nil
}
