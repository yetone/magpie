package claudetui

// waiting is not read on Windows: ok is false, and a run that drew
// nothing says only that.
func (s *shim) waiting() (cmds []running, rssKiB int64, ok bool) {
	return nil, 0, false
}
