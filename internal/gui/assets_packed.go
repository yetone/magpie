//go:build packedassets

package gui

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"sync"
	"time"

	_ "embed"
)

// A release build embeds the UI's files as one zip (build/assetpack, run by
// the Makefile before it builds): the scripts and styles are a quarter of
// their size, where the Windows and Linux downloads are the raw binary. A
// file is unpacked the first time it is asked for and kept, as the page's
// short list of files is all there is.
//
//go:embed assets.zip
var assetsZip []byte

var assets = &packedAssets{}

type packedAssets struct {
	once  sync.Once
	zr    *zip.Reader
	err   error
	mu    sync.Mutex
	files map[string][]byte
}

func (p *packedAssets) reader() (*zip.Reader, error) {
	p.once.Do(func() { p.zr, p.err = zip.NewReader(bytes.NewReader(assetsZip), int64(len(assetsZip))) })
	return p.zr, p.err
}

// load unpacks a file, once; dir is true for a folder.
func (p *packedAssets) load(name string) (b []byte, st fs.FileInfo, dir bool, err error) {
	zr, err := p.reader()
	if err != nil {
		return nil, nil, false, err
	}
	f, err := zr.Open(name)
	if err != nil {
		return nil, nil, false, err
	}
	defer f.Close()
	if st, err = f.Stat(); err != nil || st.IsDir() {
		return nil, st, err == nil, err
	}
	p.mu.Lock()
	b, ok := p.files[name]
	p.mu.Unlock()
	if ok {
		return b, st, false, nil
	}
	if b, err = io.ReadAll(f); err != nil {
		return nil, nil, false, err
	}
	p.mu.Lock()
	if p.files == nil {
		p.files = map[string][]byte{}
	}
	p.files[name] = b
	p.mu.Unlock()
	return b, st, false, nil
}

// Open gives a file that seeks (a range request for a video needs it) and,
// as an embedded file does, has no modification time.
func (p *packedAssets) Open(name string) (fs.File, error) {
	b, st, dir, err := p.load(name)
	if err != nil {
		return nil, err
	}
	if dir {
		zr, _ := p.reader()
		return zr.Open(name)
	}
	return &packedFile{Reader: bytes.NewReader(b), info: packedInfo{st, int64(len(b))}}, nil
}

func (p *packedAssets) ReadFile(name string) ([]byte, error) {
	b, _, dir, err := p.load(name)
	if err != nil {
		return nil, err
	}
	if dir {
		return nil, &fs.PathError{Op: "read", Path: name, Err: errors.New("is a directory")}
	}
	return bytes.Clone(b), nil
}

type packedFile struct {
	*bytes.Reader
	info packedInfo
}

func (f *packedFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *packedFile) Close() error               { return nil }

type packedInfo struct {
	fs.FileInfo
	size int64
}

func (i packedInfo) Size() int64        { return i.size }
func (i packedInfo) ModTime() time.Time { return time.Time{} }
