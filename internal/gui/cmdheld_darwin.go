//go:build !nogui

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static int commandHeld(void) {
	return ([NSEvent modifierFlags] & NSEventModifierFlagCommand) != 0;
}
*/
import "C"

// cmdClick says whether Command was held as the icon was clicked: the Mac
// drags a menu bar icon with Command, so the click is the system's, not
// magpie's (BennettChina, #792: Hidden Bar's users move icons so).
func cmdClick() bool { return C.commandHeld() != 0 }
