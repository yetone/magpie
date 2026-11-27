package proc

import (
	"errors"
	"syscall"
)

// PortReserved says a listen failed because Windows keeps the port for
// itself: a port in a range excluded for Hyper-V, WSL, Docker or winnat
// ("netsh interface ipv4 show excludedportrange protocol=tcp" lists them)
// can't be bound, and bind says WSAEACCES (10013) though no program has
// it, so nothing shows in netstat (Shen Bao on Discord: the gateway's 3425
// said "taken by another program" and no program had it). A program
// bound with SO_EXCLUSIVEADDRUSE gets the same answer, so a caller that
// can read who holds the port asks that first. Only Windows' Winsock uses
// 10013; no other system's bind returns it.
func PortReserved(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == 10013
}

// ReservedFix is the way out besides another port, said after what
// couldn't listen: winnat restarted takes its ranges again, most often
// elsewhere.
const ReservedFix = "or, from an administrator prompt, run net stop winnat then net start winnat, which usually frees it (netsh interface ipv4 show excludedportrange protocol=tcp lists the reserved ranges)"
