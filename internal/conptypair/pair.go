// Package conptypair is the ConPTY a Windows release carries for Windows
// 10, whose own drops a paste's bracketed-paste marks and loses 1024-byte
// blocks of a long write (yetone/magpie#5205): Microsoft's
// Microsoft.Windows.Console.ConPTY package from nuget.org (MIT), its
// conpty.dll and the OpenConsole.exe it runs, both signed by Microsoft.
//
// The release fetches the package (go run ./build/conpty), checks it
// against the SHA-512 nuget.org publishes for it and each file against the
// SHA-256 pinned here, and embeds the pair (-tags conptypair). At run time
// Ready writes them to a cache folder and checks them there again before
// they are loaded. A build without the tag carries none (Bundled is
// false), and the system's ConPTY is used.
package conptypair

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Version is the package's version: the one 3leg0ocat ran on Windows 10
// 19044 with every message whole (#5205).
const Version = "1.25.260930003"

// URL is where the package is fetched, nuget.org's own.
const URL = "https://api.nuget.org/v3-flatcontainer/microsoft.windows.console.conpty/" + Version +
	"/microsoft.windows.console.conpty." + Version + ".nupkg"

// SHA512 is the package's hash as nuget.org publishes it (its catalog
// entry's packageHash, base64), checked against the download.
const SHA512 = "LuqMk9r5oTzeRne35IOlUdSQ7DkjorOIbcnODGZ81MvmUpTr/1VBQz3fAdCNCwkfuR0Mluq+H7rS3Hes/A1fOw=="

// File is one of the pair: its name, where the package holds it and its
// SHA-256.
type File struct {
	Name, Zip, SHA256 string
}

// Files are each architecture's pair.
var Files = map[string][]File{
	"amd64": {
		{"conpty.dll", "runtimes/win-x64/native/conpty.dll", "feeef341d891643c62d30b6b07800bc70f0bc148f44cb8c3bee84aa557ae805a"},
		{"OpenConsole.exe", "build/native/runtimes/x64/OpenConsole.exe", "3d66b23d0a71bb8eed2b77edc8b9df9bf54ce6c8fb74c863a30e760997f80586"},
	},
	"arm64": {
		{"conpty.dll", "runtimes/win-arm64/native/conpty.dll", "151a02355227710c20c9f8f1eaf68ac4668aa09cdc921adc7ec661804d8eb1bf"},
		{"OpenConsole.exe", "build/native/runtimes/arm64/OpenConsole.exe", "4313825a3ba634d113cc6ed3bc8ce735e3c19d360cecd985794787479469f91f"},
	},
}

// bundled is the embedded pair, under <arch>/, nil without -tags
// conptypair; arch is the one it holds.
var (
	bundled fs.FS
	arch    string
)

// Bundled is whether this build carries the pair.
func Bundled() bool { return bundled != nil }

// Ready writes the bundled pair under cache (cache/conpty/<Version>/<arch>)
// unless it is there whole already, checks each file there against its
// pin, and returns the folder, for its conpty.dll to be loaded. A file
// that doesn't match is written again; one that still doesn't is an error,
// and nothing there is to be loaded.
func Ready(cache string) (string, error) {
	if bundled == nil {
		return "", errors.New("no ConPTY in this build")
	}
	dir := filepath.Join(cache, "conpty", Version, arch)
	return dir, write(bundled, arch, Files[arch], dir)
}

// write puts src's <arch>/<name> of each file in dir, each checked against
// its pin before it is written and after.
func write(src fs.FS, arch string, files []File, dir string) error {
	if len(files) == 0 {
		return fmt.Errorf("no ConPTY pinned for %s", arch)
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Name)
		if Check(path, f.SHA256) == nil {
			continue
		}
		b, err := fs.ReadFile(src, arch+"/"+f.Name)
		if err != nil {
			return err
		}
		if err := matches(b, f.SHA256); err != nil {
			return fmt.Errorf("bundled %s: %w", f.Name, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, f.Name+".*.tmp")
		if err != nil {
			return err
		}
		_, err = tmp.Write(b)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			// a file in use (an OpenConsole.exe running) can't be
			// replaced: then what is there is checked below
			err = os.Rename(tmp.Name(), path)
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
		if err := Check(path, f.SHA256); err != nil {
			return err
		}
	}
	return nil
}

// Check is whether the file at path is the one pinned by sum.
func Check(path, sum string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := matches(b, sum); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func matches(b []byte, sum string) error {
	want, err := hex.DecodeString(sum)
	if err != nil {
		return err
	}
	got := sha256.Sum256(b)
	if !bytes.Equal(got[:], want) {
		return fmt.Errorf("SHA-256 %x, pinned %s", got, sum)
	}
	return nil
}
