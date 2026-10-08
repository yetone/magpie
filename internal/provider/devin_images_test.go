package provider

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// Devin's own ids models.dev doesn't know (swe-2, gpt-6-1-sol) take images,
// as Devin tells its CLI in GetCliModelConfigs; `devin models list` says
// nothing of images, so they were served as text-only and agents dropped
// the images (#417). A model Devin says takes none (GLM) is text-only
// whatever models.dev says, and one Devin doesn't name is left to it.
func TestDevinImagesFromModelConfigs(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	creds := DevinCredentialsPath()
	os.MkdirAll(filepath.Dir(creds), 0o700)
	os.WriteFile(creds, []byte("windsurf_api_key = \"devin-session-token$k\"\n"), 0o600)

	// a ClientModelConfig: model_uid (22), and supports_images (5) when true
	config := func(uid string, images bool) []byte {
		var b []byte
		b = binary.AppendUvarint(b, 22<<3|2)
		b = binary.AppendUvarint(b, uint64(len(uid)))
		b = append(b, uid...)
		if images {
			b = append(b, 5<<3, 1)
		}
		return b
	}
	var asked []byte
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exa.api_server_pb.ApiServerService/GetCliModelConfigs" || r.Header.Get("Content-Type") != "application/proto" {
			http.Error(w, "no", 404)
			return
		}
		asked, _ = io.ReadAll(r.Body)
		var out []byte
		for _, c := range [][]byte{config("swe-2-high", true), config("swe-2-medium", true), config("glm-5-2", false), config("glm-5-2-max", false)} {
			out = binary.AppendUvarint(out, 1<<3|2)
			out = binary.AppendUvarint(out, uint64(len(c)))
			out = append(out, c...)
		}
		w.Header().Set("Content-Type", "application/proto")
		w.Write(out)
	}))
	defer fake.Close()
	t.Setenv("WINDSURF_API_SERVER_URL", fake.URL)

	exe := filepath.Join(home, "devin")
	testenv.Program(t, exe, `#!/bin/sh
cat <<'X'
{"families":[
 {"family_label":"SWE-2","family_uid":"swe-2","aliases":["swe"],"variants":[
  {"model_uid":"swe-2-high","label":"SWE-2 High"},{"model_uid":"swe-2-medium","label":"SWE-2 Medium"}]},
 {"family_label":"GLM-5.2","family_uid":"glm-5.2","variants":[
  {"model_uid":"glm-5-2","label":"GLM-5.2 High"},{"model_uid":"glm-5-2-max","label":"GLM-5.2 Max"}]},
 {"family_label":"New","family_uid":"new-model","variants":[{"model_uid":"new-model-high","label":"New High"}]}]}
X
`)
	fakeDevin(t, exe)

	families, err := askDevinFamiliesAt(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(asked, []byte("devin-session-token$k")) || !bytes.Contains(asked, []byte("chisel")) {
		t.Fatalf("GetCliModelConfigs was asked with %q", asked)
	}
	images := map[string]Entry{}
	for _, m := range devinModels(families) {
		images[m.ID] = entryFor(Provider{ID: "devin"}, m, settings.Settings{})
	}
	for id, want := range map[string]bool{"swe-2": true, "glm-5.2": false} {
		e, ok := images[id]
		if !ok {
			t.Fatalf("%s isn't listed: %v", id, images)
		}
		if e.Images != want || e.ImageInput == nil || *e.ImageInput != want {
			t.Errorf("%s: images %v (said %v), want %v", id, e.Images, e.ImageInput, want)
		}
	}
	if e := images["new-model"]; e.ImageInput != nil {
		t.Errorf("a model Devin didn't name was said to take images: %v", *e.ImageInput)
	}

	// a list saved before Devin was asked takes what it said
	devinFamiliesCached(families)
	t.Cleanup(func() { devinFamiliesCached(nil) })
	for _, m := range withDevinContexts([]catalog.Model{{ID: "swe-2", Provider: "devin"}, {ID: "swe", Provider: "devin"}}) {
		if m.ImageInput == nil || !*m.ImageInput {
			t.Errorf("saved %s: images %v", m.ID, m.ImageInput)
		}
	}
}
