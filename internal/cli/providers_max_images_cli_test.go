package cli

import (
	"strings"
	"testing"
)

// magpie provider set max-images=8 is the user's word on how many images
// one request to a provider may carry (#6849, ID-VerNe: 100milionseek's and
// Coarena's APIs take 8 and refuse the ninth with their own words, and
// Copilot's wording alone is learned), show says so, max-images=0 hands it
// back to what a Copilot refusal teaches, and a value out of range is
// refused and leaves the saved one alone.
func TestProviderSetMaxImages(t *testing.T) {
	groupsHome(t)
	if _, err := printed(t, func() error { return providerCmd([]string{"provider", "set", "a", "max-images=8"}) }); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, "a"); p.MaxImages["*"] != 8 {
		t.Fatalf("max-images=8: %v", p.MaxImages)
	}
	out, err := printed(t, func() error { return providerCmd([]string{"provider", "a"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "max-images") || !strings.Contains(out, "8") {
		t.Fatalf("show doesn't say the cap:\n%s", out)
	}
	for _, v := range []string{"-1", "1001", "abc", ""} {
		err := providerCmd([]string{"provider", "set", "a", "max-images=" + v})
		if err == nil || !strings.Contains(err.Error(), "max-images:") {
			t.Errorf("max-images=%q: %v", v, err)
		}
		if mustFind(t, "a").MaxImages["*"] != 8 {
			t.Errorf("a refused max-images=%q changed it", v)
		}
	}
	if _, err := printed(t, func() error { return providerCmd([]string{"provider", "set", "a", "max-images=0"}) }); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, "a"); len(p.MaxImages) != 0 {
		t.Fatalf("max-images=0 left %v", p.MaxImages)
	}
}

// max-images.<model>=n caps that model alone, beside the one for all
// models, and its 0 clears it alone.
func TestProviderSetMaxImagesPerModel(t *testing.T) {
	groupsHome(t)
	for _, kv := range []string{"max-images=8", "max-images.gemini-3-flash=10"} {
		if _, err := printed(t, func() error { return providerCmd([]string{"provider", "set", "a", kv}) }); err != nil {
			t.Fatal(err)
		}
	}
	if p := mustFind(t, "a"); p.MaxImages["*"] != 8 || p.MaxImages["gemini-3-flash"] != 10 || len(p.MaxImages) != 2 {
		t.Fatalf("both set: %v", p.MaxImages)
	}
	if p := mustFind(t, "a"); p.ImageCapOf("gemini-3-flash") != 10 || p.ImageCapOf("other") != 8 {
		t.Fatalf("ImageCapOf: %d %d", p.ImageCapOf("gemini-3-flash"), p.ImageCapOf("other"))
	}
	out, err := printed(t, func() error { return providerCmd([]string{"provider", "a"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "gemini-3-flash=10") {
		t.Fatalf("show doesn't say the model's cap:\n%s", out)
	}
	if _, err := printed(t, func() error { return providerCmd([]string{"provider", "set", "a", "max-images.gemini-3-flash=0"}) }); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, "a"); p.MaxImages["*"] != 8 || len(p.MaxImages) != 1 {
		t.Fatalf("its 0 cleared more than it: %v", p.MaxImages)
	}
}
