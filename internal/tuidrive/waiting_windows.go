package tuidrive

// Waiting is not read on Windows: ok is false, and a run that drew
// nothing says only that.
func (t *Term) Waiting() (cmds []Running, rssKiB int64, ok bool) {
	return nil, 0, false
}
