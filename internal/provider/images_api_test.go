package provider

import (
	"encoding/json"
	"testing"
)

func TestOpenRouterImageModelProbeUsesImagesAPI(t *testing.T) {
	p := Provider{Chat: "https://openrouter.ai/api/v1"}
	if !p.drawsOnImages("openai/gpt-image-2") {
		t.Fatal("OpenRouter gpt-image model is not probed on its images API")
	}
	if p.drawsOnImages("openai/gpt-5-image") {
		t.Fatal("OpenRouter chat image model should stay on chat completions")
	}
	url, body := tinyDrawing(p, "openai/gpt-image-2")
	if url != "https://openrouter.ai/api/v1/images" {
		t.Fatalf("image probe URL %q", url)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "openai/gpt-image-2" || got["prompt"] != "a dot" || got["n"] != float64(1) || got["quality"] != "low" {
		t.Fatalf("image probe body %s", body)
	}
	other := Provider{Chat: "https://relay.example/v1"}
	if !other.drawsOnImages("gpt-image-2") {
		t.Fatal("OpenAI-compatible image model is not probed on its images API")
	}
	if url, _ := tinyDrawing(other, "gpt-image-2"); url != "https://relay.example/v1/images/generations" {
		t.Fatalf("other provider image probe URL %q", url)
	}
}
