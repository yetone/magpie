package gui

import "github.com/yetone/magpie/internal/settings"

// The main window opens as it was last left: at the size it was last given
// (settings.Window) and maximised again if it was (settings.WindowMaximised).

// windowW, windowH is the main window's size, in points, before it is first
// given one.
const windowW, windowH = 660, 600

// openSize is the size, in points, the main window opens at: the one it was
// last given (saved), else windowW×windowH; no smaller than its least (minW,
// minH).
func openSize(saved []int, minW, minH int) (int, int) {
	w, h := windowW, windowH
	if len(saved) == 2 && saved[0] >= windowMinW && saved[1] >= windowMinH {
		w, h = saved[0], saved[1]
	}
	return max(w, minW), max(h, minH)
}

// fitRoom is w×h no larger than the work area of the screen it is on (sw,
// sh; 0 when unknown), and whether that changed it: a size kept on a larger
// screen, or before the display was scaled up, still fits, title bar in
// sight.
func fitRoom(w, h, sw, sh int) (int, int, bool) {
	fw, fh := w, h
	if sw > 0 {
		fw = min(fw, sw)
	}
	if sh > 0 {
		fh = min(fh, sh)
	}
	return fw, fh, fw != w || fh != h
}

// settle keeps in s the main window as it has settled, w×h and maximised or
// not, on a screen whose work area is sw×sh (0s when unknown), and says
// whether s changed. A maximised window is the screen's size, not one the
// user gave it: the size it is restored to stays as it was. So does one
// under the least, or within 2 points of the size kept — macOS opens a
// window a point short of the size it was given, and kept as it is the
// window would shrink a point at every start. A side as long as the screen's
// keeps a longer one kept: the window was fitted to a smaller screen
// (fitRoom), and opens at its own size again on the larger one.
func settle(s *settings.Settings, w, h, sw, sh int, maximised bool) bool {
	changed := s.WindowMaximised != maximised
	s.WindowMaximised = maximised
	if maximised || w < windowMinW || h < windowMinH {
		return changed
	}
	if len(s.Window) == 2 {
		w, h = fitted(s.Window[0], w, sw), fitted(s.Window[1], h, sh)
		if abs(s.Window[0]-w) <= 2 && abs(s.Window[1]-h) <= 2 {
			return changed
		}
	}
	s.Window = []int{w, h}
	return true
}

// fitted is the side kept for one now as long as the screen's, room: the
// longer one kept before, if it was.
func fitted(kept, now, room int) int {
	if room > 0 && kept > now && abs(now-room) <= 2 {
		return kept
	}
	return now
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
