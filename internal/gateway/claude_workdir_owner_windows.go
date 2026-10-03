package gateway

import "os"

// ownedByMe says the file belongs to the user magpie runs as: on Windows
// the folder is in the user's own %TEMP%, which no one else writes.
func ownedByMe(os.FileInfo) bool { return true }

// claudeWorkName is the work folder's name in the temp folder, the user's
// own on Windows.
func claudeWorkName() string { return "magpie-claude-work" }
