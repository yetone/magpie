package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
)

// A plugin's page shares a picture it drew (magpie.image.copy and
// magpie.image.save in pagebridge.js): its frame is sandboxed to scripts
// alone, so it can neither download a file nor reach the clipboard, and the
// app's window can't put a picture on the clipboard either. The window
// hands the page's PNG to POST /api/plugin-pages/image, which puts it on
// the system's clipboard or saves it in Downloads. In magpie web the
// browser's window does both itself (pluginpages.js): this server's
// clipboard and Downloads are another computer's.

// maxPageImage is the largest PNG a page can hand over, and
// maxPageImageSide the most pixels a side of it has.
const (
	maxPageImage     = 16 << 20
	maxPageImageSide = 8192
)

// pageImage is what a page's PNG is checked to be: a PNG, whole in its
// header, of a size a card can be.
func pageImage(b []byte) error {
	if len(b) == 0 || len(b) > maxPageImage {
		return fmt.Errorf("a picture is a PNG of %d MB at most", maxPageImage>>20)
	}
	c, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return errors.New("not a PNG")
	}
	if c.Width < 1 || c.Height < 1 || c.Width > maxPageImageSide || c.Height > maxPageImageSide {
		return fmt.Errorf("a picture is %d pixels a side at most", maxPageImageSide)
	}
	return nil
}

// pageImageName is a file name for a page's picture: what the page asked
// for, without what a file name can't hold on any system, ending .png.
func pageImageName(asked string) string {
	asked = strings.TrimSuffix(strings.TrimSpace(asked), ".png")
	var b strings.Builder
	for _, r := range asked {
		switch {
		case r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Trim(b.String(), " .-")
	if r := []rune(name); len(r) > 80 {
		name = strings.TrimRight(string(r[:80]), " .-")
	}
	if name == "" {
		name = "magpie"
	}
	return name
}

// savePageImage writes b to dir as name.png, or name-2.png and on, never
// over a file there: the path it wrote.
func savePageImage(dir, name string, b []byte) (string, error) {
	base := pageImageName(name)
	p := filepath.Join(dir, base+".png")
	for i := 2; ; i++ {
		if _, err := os.Lstat(p); err != nil {
			break
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d.png", base, i))
	}
	return p, edit.WriteAtomic(p, b)
}

// copyImage puts a PNG on the system's clipboard (clipimage_*.go); tests
// replace it.
var copyImage = copyImageOS

// pageImageDir is where a saved picture goes; tests replace it.
var pageImageDir = downloads

// pageImages lets a page share a picture twice a second at most: a page
// that asks over and over fills nobody's Downloads.
var pageImages = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

func pageImageAllowed(spec string, now time.Time) bool {
	pageImages.Lock()
	defer pageImages.Unlock()
	if last, ok := pageImages.at[spec]; ok && now.Sub(last) < 500*time.Millisecond {
		return false
	}
	pageImages.at[spec] = now
	return true
}

func pageImageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/plugin-pages/image", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec   string `json:"spec"`
			Action string `json:"action"` // copy | save
			Name   string `json:"name"`
			PNG    []byte `json:"png"` // base64
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxPageImage*4/3+64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if _, _, ok := windowOf(in.Spec); !ok {
			fail(rw, errors.New("no plugin page "+in.Spec))
			return
		}
		if in.Action != "copy" && in.Action != "save" {
			fail(rw, fmt.Errorf("no image action %q: copy or save", in.Action))
			return
		}
		if err := pageImage(in.PNG); err != nil {
			fail(rw, err)
			return
		}
		if !pageImageAllowed(in.Spec, time.Now()) {
			http.Error(rw, "a page shares a picture twice a second at most", http.StatusTooManyRequests)
			return
		}
		if in.Action == "copy" {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			if err := copyImage(ctx, in.PNG); err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]bool{"ok": true})
			return
		}
		p, err := savePageImage(pageImageDir(), in.Name, in.PNG)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"path": tilde(p)})
	})
}
