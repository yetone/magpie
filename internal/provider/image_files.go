package provider

// DeepSeekHost is DeepSeek's own API. A relay or another base serving
// DeepSeek's models is not it.
const DeepSeekHost = "api.deepseek.com"

// ImageFilesAt says whether p's requests on to reach DeepSeek's own API
// with a key of the user's, where an agent's inline images can go up once
// through DeepSeek's Files API and be named by file id (gateway's
// deepseek_files.go, #5407). ImageFilesOff turns that off; this says only
// where it can apply.
func (p Provider) ImageFilesAt(to Protocol) bool {
	if p.Account != nil || p.IsPlugin() {
		return false
	}
	if to != Chat && to != Responses && to != Anthropic {
		return false
	}
	return HostOf(p.Base(to)) == DeepSeekHost
}

// TakesImageFiles says whether ImageFilesOff has any effect on p: one of
// its Chat, Responses or Anthropic URLs is DeepSeek's own API. The
// editor offers image-files only then.
func (p Provider) TakesImageFiles() bool {
	for _, to := range []Protocol{Chat, Responses, Anthropic} {
		if p.ImageFilesAt(to) {
			return true
		}
	}
	return false
}
