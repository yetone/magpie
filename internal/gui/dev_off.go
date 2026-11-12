//go:build !dev

package gui

import (
	"io/fs"
	"net/http"
)

// staticFixed: the UI's files are the binary's, the same for its life.
const staticFixed = true

// The shipped app serves the UI from the binary, in one process.
func staticFS() fs.FS                             { f, _ := fs.Sub(assets, "assets"); return f }
func devRoutes(*http.ServeMux)                    {}
func devListen(http.Handler)                      {}
func devPage(h http.Handler) http.Handler         { return h }
func devRole() string                             { return "" }
func devBackend(func(Windows) http.Handler) error { return nil }
func stash(link string) string                    { return stashImport(link) }
