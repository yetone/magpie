package gui

import (
	"errors"
	"fmt"
)

const terminalBundleID = "com.apple.Terminal"

// terminalApp is an installed app that explicitly claims .command files.
type terminalApp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type terminalChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type terminalDiscovery struct {
	Apps    []terminalApp `json:"apps"`
	Default string        `json:"default"`
}

// terminalOpenArgs chooses the app for one already validated resume script.
func terminalOpenArgs(choice string, found terminalDiscovery) ([]string, error) {
	if choice == terminalBundleID {
		return []string{"-a", "Terminal"}, nil
	}
	if choice == "" || choice == "system" {
		for _, app := range found.Apps {
			if app.ID == found.Default {
				return []string{"-a", app.Path}, nil
			}
		}
		return nil, errors.New("the default app for .command files is not a supported terminal")
	}
	for _, app := range found.Apps {
		if app.ID == choice {
			return []string{"-a", app.Path}, nil
		}
	}
	return nil, fmt.Errorf("the selected terminal app %q is no longer available", choice)
}
