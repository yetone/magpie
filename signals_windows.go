package main

import "os"

// raise has nothing to do: Windows can't send a process a signal, and die
// exits instead.
func raise(os.Signal) {}
