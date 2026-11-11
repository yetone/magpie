//go:build !darwin

package dirent

func stats(string, Listing, Listing) (Listing, bool) { return nil, false }

func eachIn(string, func(string, Stat) bool) bool { return false }
