package update

import "github.com/yetone/magpie/internal/settings"

// Source is Settings' update source, "" when it is automatic: the
// usemagpie.ai feed, then GitHub when the feed can't be reached
// (fallback.go). settings.UpdateSourceSite never goes to GitHub, and
// settings.UpdateSourceGitHub never to usemagpie.ai (RootSir and Sakugar_
// on X: the site's download is slow). Whichever it is, the release goes
// through Release.verify: the same ed25519 signature over SHA256SUMS, the
// version inside it and every file's hash from it. MAGPIE_UPDATE_FEED, a
// test's feed, is never overridden by it.
func Source() string {
	switch s := settings.Load().UpdateSource; s {
	case settings.UpdateSourceSite, settings.UpdateSourceGitHub:
		return s
	}
	return ""
}
