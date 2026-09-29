//go:build darwin && cgo

package gui

/*
#cgo LDFLAGS: -framework AppKit -framework UniformTypeIdentifiers
#include <stdlib.h>
char *magpieTerminalAppsJSON(void);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"unsafe"
)

// discoverTerminals asks Launch Services for apps that claim .command files.
func discoverTerminals() (terminalDiscovery, error) {
	raw := C.magpieTerminalAppsJSON()
	if raw == nil {
		return terminalDiscovery{}, errors.New("could not find apps for .command files")
	}
	defer C.free(unsafe.Pointer(raw))
	var found terminalDiscovery
	if err := json.Unmarshal([]byte(C.GoString(raw)), &found); err != nil {
		return terminalDiscovery{}, err
	}
	return found, nil
}
