package claudetui

import "golang.org/x/sys/unix"

// the requests that read and set a terminal's modes (the tests' fake
// Claude Code sets its own raw, as the real one does)
const ioctlGetTermios, ioctlSetTermios = unix.TIOCGETA, unix.TIOCSETA
