package agent

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/gateway"
)

// zedWindowsCredential matches CREDENTIALW. Zed's blob is UTF-8, not UTF-16.
type zedWindowsCredential struct {
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

func saveZedCredential(url string) error {
	target, err := windows.UTF16PtrFromString("zed:url=" + url)
	if err != nil {
		return err
	}
	user, _ := windows.UTF16PtrFromString("Bearer")
	key := []byte(gateway.TokenFor("zed"))
	credential := zedWindowsCredential{
		Type: 1, TargetName: target, UserName: user, // CRED_TYPE_GENERIC
		CredentialBlobSize: uint32(len(key)), CredentialBlob: &key[0],
		Persist: 2, // CRED_PERSIST_LOCAL_MACHINE
	}
	write := windows.NewLazySystemDLL("advapi32.dll").NewProc("CredWriteW")
	ok, _, callErr := write.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(credential)
	runtime.KeepAlive(key)
	if ok == 0 {
		return fmt.Errorf("CredWriteW: %w", callErr)
	}
	return nil
}
