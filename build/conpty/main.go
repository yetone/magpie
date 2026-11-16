// conpty fetches the ConPTY a Windows release carries for Windows 10
// (internal/conptypair, #5205): Microsoft.Windows.Console.ConPTY from
// nuget.org, checked against the SHA-512 nuget.org publishes for it, and
// writes each architecture's conpty.dll and OpenConsole.exe, each checked
// against its pinned SHA-256, to internal/conptypair/<arch>/ for -tags
// conptypair to embed. Files there already whole are kept, with no
// download.
//
// Usage: go run ./build/conpty [package.nupkg]
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/conptypair"
)

const out = "internal/conptypair"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "conpty:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if whole() {
		return nil
	}
	var pkg []byte
	var err error
	if len(args) > 0 {
		pkg, err = os.ReadFile(args[0])
	} else {
		pkg, err = fetch()
	}
	if err != nil {
		return err
	}
	got := sha512.Sum512(pkg)
	if h := base64.StdEncoding.EncodeToString(got[:]); h != conptypair.SHA512 {
		return fmt.Errorf("package SHA-512 %s, nuget.org publishes %s", h, conptypair.SHA512)
	}
	z, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		return err
	}
	for arch, files := range conptypair.Files {
		for _, f := range files {
			b, err := read(z, f.Zip)
			if err != nil {
				return err
			}
			path := filepath.Join(out, arch, f.Name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, b, 0o644); err != nil {
				return err
			}
			if err := conptypair.Check(path, f.SHA256); err != nil {
				_ = os.Remove(path)
				return err
			}
		}
	}
	fmt.Println("conpty: ConPTY", conptypair.Version, "written to", out)
	return nil
}

// whole is whether every file is there and pinned already.
func whole() bool {
	for arch, files := range conptypair.Files {
		for _, f := range files {
			if conptypair.Check(filepath.Join(out, arch, f.Name), f.SHA256) != nil {
				return false
			}
		}
	}
	return true
}

func fetch() ([]byte, error) {
	c := &http.Client{Timeout: 5 * time.Minute}
	var last error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(time.Duration(try) * 5 * time.Second)
		}
		resp, err := c.Get(conptypair.URL)
		if err != nil {
			last = err
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("%s: %s", conptypair.URL, resp.Status)
		}
		if err == nil {
			return b, nil
		}
		last = err
	}
	return nil, last
}

func read(z *zip.Reader, name string) ([]byte, error) {
	f, err := z.Open(name)
	if err != nil {
		return nil, fmt.Errorf("package has no %s: %w", name, err)
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 16<<20))
}
