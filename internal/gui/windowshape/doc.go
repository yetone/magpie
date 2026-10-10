// Package windowshape measures a Mac window for the GUI's tests: the
// radius of its corners, where its traffic lights are, whether it has a
// toolbar, where a sheet on it begins and whether a first press on it
// begins a window drag. It can also flip Increase contrast for its own
// process (on where the system has it off, off where it is on), to give
// the windows a new system appearance. It sends no event to the system and
// changes no setting. Only tests import it.
package windowshape
