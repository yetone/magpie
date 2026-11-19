package update

import (
	"os"
	"regexp"
	"testing"
)

// The image a magpie in a container names for a release is that
// release's own tag, the one docker.yml writes, never a floating tag
// (latest, daily, stable) that may hold another release (fishmagic80 on
// X: Daily found 0.1.4479 and the hint said :latest).
func TestImageIsTheReleasesOwnTag(t *testing.T) {
	for in, want := range map[string]string{
		"0.1.4479":  "ghcr.io/yetone/magpie:0.1.4479",
		"v0.1.4479": "ghcr.io/yetone/magpie:0.1.4479",
	} {
		if got := Image(in); got != want {
			t.Errorf("Image(%q) = %q, want %q", in, got, want)
		}
	}
	// docker.yml tags a release's image with its version, "v" taken off
	y, err := os.ReadFile("../../.github/workflows/docker.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`V="\$\{GITHUB_REF_NAME#v\}"`).Match(y) || !regexp.MustCompile(`TAGS=\(-t "\$IMAGE:\$V"\)`).Match(y) {
		t.Error("docker.yml no longer tags each release's image with its version; Image names a tag that isn't written")
	}
	if !regexp.MustCompile(`IMAGE: ghcr\.io/\$\{\{ github\.repository_owner \}\}/magpie`).Match(y) || ImageRepo != "ghcr.io/yetone/magpie" {
		t.Error("the image docker.yml pushes isn't ImageRepo")
	}
}
