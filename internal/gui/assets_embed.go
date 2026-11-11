//go:build !packedassets

package gui

import "embed"

// The UI's files, as they are in the tree. A release build packs them
// (assets_packed.go).
//
//go:embed assets
var assets embed.FS
