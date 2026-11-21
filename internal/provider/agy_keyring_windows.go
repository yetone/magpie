package provider

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// agyCredential is CREDENTIALW, as go-keyring's wincred fills it.
type agyCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	agyAdvapi     = windows.NewLazySystemDLL("advapi32.dll")
	agyCredReadW  = agyAdvapi.NewProc("CredReadW")
	agyCredWriteW = agyAdvapi.NewProc("CredWriteW")
	agyCredFree   = agyAdvapi.NewProc("CredFree")
)

// agyCredRead is the blob of the generic credential target,
// errAgyNoEntry when there is none.
func agyCredRead(target string) ([]byte, error) {
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, err
	}
	var c *agyCredential
	ok, _, callErr := agyCredReadW.Call(uintptr(unsafe.Pointer(t)), 1, 0, uintptr(unsafe.Pointer(&c))) // CRED_TYPE_GENERIC
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return nil, errAgyNoEntry
		}
		return nil, fmt.Errorf("CredReadW: %w", callErr)
	}
	defer agyCredFree.Call(uintptr(unsafe.Pointer(c)))
	if c.CredentialBlobSize == 0 || c.CredentialBlob == nil {
		return []byte{}, nil
	}
	return append([]byte(nil), unsafe.Slice(c.CredentialBlob, c.CredentialBlobSize)...), nil
}

// agyCredWrite writes the generic credential target over in place, as
// go-keyring does: user as its UserName, kept on this machine.
func agyCredWrite(target, user string, blob []byte) error {
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	u, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return err
	}
	if len(blob) == 0 {
		return errors.New("no sign-in to write")
	}
	c := agyCredential{
		Type: 1, TargetName: t, UserName: u, // CRED_TYPE_GENERIC
		CredentialBlobSize: uint32(len(blob)), CredentialBlob: &blob[0],
		Persist: 2, // CRED_PERSIST_LOCAL_MACHINE
	}
	ok, _, callErr := agyCredWriteW.Call(uintptr(unsafe.Pointer(&c)), 0)
	runtime.KeepAlive(c)
	runtime.KeepAlive(blob)
	if ok == 0 {
		return fmt.Errorf("CredWriteW: %w", callErr)
	}
	return nil
}
