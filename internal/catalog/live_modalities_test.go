package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// A list's explicit empty input remains different from one it never named,
// and a later modalities value must not keep fields from an earlier shape.
func TestFetchModalitiesShapes(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		inputs       []string
		outputs      []string
		known, image bool
		serves       string
	}{
		{"empty array", `"modalities":[]`, []string{}, nil, true, false, ""},
		{"null", `"modalities":null`, nil, nil, false, false, ""},
		{"number", `"modalities":42`, nil, nil, false, false, ""},
		{"boolean", `"modalities":true`, nil, nil, false, false, ""},
		{"mixed array", `"modalities":["text",42]`, nil, nil, false, false, ""},
		{"malformed object", `"modalities":{"input":"text","output":["video"]}`, nil, nil, false, false, ""},
		{"input alone wins", `"modalities":["text","image"],"input_modalities":["text"]`, []string{"text"}, nil, true, false, ""},
		{"empty input wins", `"modalities":["image"],"input_modalities":[]`, []string{}, nil, true, false, ""},
		{"embedding output", `"modalities":["text","image"],"output_modalities":["embeddings"]`, []string{"text", "image"}, []string{"embeddings"}, true, true, ServesEmbedding},
		{"object then array", `"modalities":{"input":["text"],"output":["video"]},"modalities":["text","image"]`, []string{"text", "image"}, nil, true, true, ""},
		{"array then null", `"modalities":["image"],"modalities":null`, nil, nil, false, false, ""},
		{"object then number", `"modalities":{"input":["image"]},"modalities":42`, nil, nil, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"data":[{"id":"reported-model",%s},{"id":"healthy-neighbor","modalities":{"input":["text"],"output":["text"]}}]}`, tc.fields)
			}))
			defer srv.Close()
			models, err := FetchURL(context.Background(), srv.URL+"/v1/models", "", false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != 2 || models[0].ID != "reported-model" || models[1].ID != "healthy-neighbor" {
				t.Fatalf("list lost a model: %+v", models)
			}
			m := models[0]
			if !slices.Equal(m.InputModalities, tc.inputs) || (m.InputModalities == nil) != (tc.inputs == nil) {
				t.Errorf("input modalities = %#v, want %#v", m.InputModalities, tc.inputs)
			}
			if !slices.Equal(m.OutputModalities, tc.outputs) || (m.OutputModalities == nil) != (tc.outputs == nil) {
				t.Errorf("output modalities = %#v, want %#v", m.OutputModalities, tc.outputs)
			}
			if (m.ImageInput != nil) != tc.known || m.ImageInput != nil && *m.ImageInput != tc.image || m.Images != tc.image {
				t.Errorf("image input = %v, Images = %v; want known=%v, image=%v", m.ImageInput, m.Images, tc.known, tc.image)
			}
			if m.Serves != tc.serves || m.Films || m.Draws {
				t.Errorf("serves = %q, video = %v, image = %v; want %q alone", m.Serves, m.Films, m.Draws, tc.serves)
			}
		})
	}
}
