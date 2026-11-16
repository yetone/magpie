//go:build conptypair

package conptypair

import "embed"

// The pair go run ./build/conpty fetched, checked and wrote here.
//
//go:embed arm64/conpty.dll arm64/OpenConsole.exe
var arm64 embed.FS

func init() { bundled, arch = arm64, "arm64" }
