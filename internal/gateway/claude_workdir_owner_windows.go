package gateway

import "os"

// ownedByMe says the file belongs to the user magpie runs as: on Windows
// the folder is in the user's own %TEMP%, which no one else writes.
func ownedByMe(os.FileInfo) bool { return true }

// othersCanWrite is false on Windows: Go reports every folder that isn't
// read-only as 0777 there, ACLs not being mode bits, and the folder is in
// the user's own %TEMP%.
func othersCanWrite(os.FileInfo) bool { return false }

// claudeWorkName is the work folder's name in the temp folder, the user's
// own on Windows.
func claudeWorkName() string { return "magpie-claude-work" }
