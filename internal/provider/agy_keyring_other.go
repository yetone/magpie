//go:build !windows

package provider

import "errors"

func agyCredRead(string) ([]byte, error) {
	return nil, errors.New("Credential Manager is Windows'")
}

func agyCredWrite(string, string, []byte) error {
	return errors.New("Credential Manager is Windows'")
}
