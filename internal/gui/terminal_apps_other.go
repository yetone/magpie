//go:build !darwin

package gui

import "errors"

func discoverTerminals() (terminalDiscovery, error) {
	return terminalDiscovery{}, errors.New("session terminals are only available in the Mac app")
}
