package uilang

import "golang.org/x/sys/windows"

// System is the user's first display language, the one WebView2 hands
// the page as navigator.language.
func System() string {
	if l, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME); err == nil && len(l) > 0 {
		return l[0]
	}
	return ""
}
