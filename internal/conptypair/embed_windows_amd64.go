//go:build conptypair

package conptypair

import "embed"

// The pair go run ./build/conpty fetched, checked and wrote here.
//
//go:embed amd64/conpty.dll amd64/OpenConsole.exe
var amd64 embed.FS

func init() { bundled, arch = amd64, "amd64" }
