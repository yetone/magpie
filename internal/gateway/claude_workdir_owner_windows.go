package gateway

import "os"

// ownedByMe says the file belongs to the user magpie runs as: on Windows
// the folder is in the user's own %LocalAppData%, which no one else writes.
func ownedByMe(os.FileInfo) bool { return true }
