package update

import "strings"

// ImageRepo is magpie's Docker image (.github/workflows/docker.yml).
const ImageRepo = "ghcr.io/yetone/magpie"

// Image is the Docker image of release version: its own tag, which
// docker.yml writes for every release ("0.1.4479", no "v"). A magpie in a
// container names it when the update it found is out, since the floating
// tags hold other releases: latest is the newest release, daily and
// stable the Daily and Stable channels' (docker-daily.yml,
// docker-stable.yml), so `docker pull …:latest` on the Daily channel
// fetched a version other than the one announced (fishmagic80 on X).
func Image(version string) string {
	return ImageRepo + ":" + strings.TrimPrefix(version, "v")
}
