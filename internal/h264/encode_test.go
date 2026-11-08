package h264

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// screens are pictures like a recording's: text, flat colour, a photo-like
// gradient with noise, and from one to the next a few things change.
func screens(w, h, n int) []*image.RGBA {
	rnd := rand.New(rand.NewPCG(1, 2))
	var out []*image.RGBA
	base := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(base, base.Bounds(), image.White, image.Point{}, draw.Src)
	for y := h / 2; y < h; y++ {
		for x := 0; x < w/3; x++ {
			base.Set(x, y, color.RGBA{uint8(x * 255 / (w / 3)), uint8(y * 255 / h), uint8(rnd.IntN(256)), 255})
		}
	}
	draw.Draw(base, image.Rect(w/2, 10, w-10, 60), image.NewUniform(color.RGBA{0x1d, 0x9b, 0xf0, 255}), image.Point{}, draw.Src)
	for i := range n {
		img := image.NewRGBA(base.Bounds())
		copy(img.Pix, base.Pix)
		d := font.Drawer{Dst: img, Src: image.NewUniform(color.RGBA{0x20, 0x20, 0x20, 255}), Face: basicfont.Face7x13}
		for l := 0; l <= i && l < 20; l++ {
			d.Dot = fixed.P(12, 20+l*16)
			d.DrawString("The quick brown fox jumps over the lazy dog, line " + string(rune('a'+l)))
		}
		if i%3 == 1 { // a box that comes and goes
			draw.Draw(img, image.Rect(w/2+i*7, h/2, w/2+i*7+90, h/2+40), image.NewUniform(color.RGBA{0xe0, 0x30, 0x40, 255}), image.Point{}, draw.Src)
		}
		if i == n-2 { // a picture just like the one before it
			copy(img.Pix, out[len(out)-1].Pix)
		}
		out = append(out, img)
	}
	return out
}

// recon is the encoder's last picture as decoded, w×h, as yuv420p.
func recon(e *Encoder) []byte {
	var b []byte
	W := e.mbw * 16
	for y := range e.h {
		b = append(b, e.ref.y[y*W:y*W+e.w]...)
	}
	for _, p := range [][]byte{e.ref.cb, e.ref.cr} {
		for y := range e.h / 2 {
			b = append(b, p[y*W/2:y*W/2+e.w/2]...)
		}
	}
	return b
}

func encodeScreens(t *testing.T, w, h, qp, n int) (*Encoder, [][]byte, []planes) {
	t.Helper()
	e, err := NewEncoder(w, h, qp)
	if err != nil {
		t.Fatal(err)
	}
	var recs [][]byte
	var srcs []planes
	for i, img := range screens(w, h, n) {
		ms := 1000
		if i == n-1 {
			ms = 3000
		}
		e.Add(img, ms)
		recs = append(recs, recon(e))
		srcs = append(srcs, e.prev)
	}
	return e, recs, srcs
}

// TestEncodeKeepsThePicture: what a decoder makes of each picture is near
// its source (PSNR over 36 dB in luma).
func TestEncodeKeepsThePicture(t *testing.T) {
	for _, qp := range []int{18, 24, 30} {
		e, recs, srcs := encodeScreens(t, 330, 190, qp, 6)
		for i := range recs {
			W := e.mbw * 16
			var se float64
			for y := range e.h {
				for x := range e.w {
					d := float64(recs[i][y*e.w+x]) - float64(srcs[i].y[y*W+x])
					se += d * d
				}
			}
			psnr := 10 * math.Log10(255*255/(se/float64(e.w*e.h)+1e-9))
			if psnr < 36 {
				t.Errorf("qp %d picture %d: PSNR %.1f dB", qp, i, psnr)
			}
			t.Logf("qp %d picture %d: PSNR %.1f dB", qp, i, psnr)
		}
		t.Logf("qp %d: %d bytes", qp, len(e.MP4()))
	}
}

// TestDecodersMakeTheSamePicture: ffmpeg decodes the video to exactly the
// pictures the encoder predicted from. Any difference would drift from
// picture to picture.
func TestDecodersMakeTheSamePicture(t *testing.T) {
	dump := os.Getenv("H264_DUMP")
	ff, err := exec.LookPath("ffmpeg")
	if err != nil && dump == "" {
		t.Skip("no ffmpeg")
	}
	for _, c := range []struct{ w, h, qp int }{{330, 190, 24}, {800, 522, 20}, {64, 48, 10}, {48, 32, 40}, {96, 64, 0}, {96, 64, 51}, {18, 18, 23}} {
		e, recs, _ := encodeScreens(t, c.w, c.h, c.qp, 7)
		mp4 := e.MP4()
		want := bytes.Join(recs, nil)
		dir := t.TempDir()
		if dump != "" {
			dir = filepath.Join(dump, filepath.Base(t.Name())+"-"+itoa(c.w)+"x"+itoa(c.h)+"-"+itoa(c.qp))
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "want.yuv"), want, 0o644)
		}
		f := filepath.Join(dir, "v.mp4")
		os.WriteFile(f, mp4, 0o644)
		if ff == "" {
			continue
		}
		got, err := exec.Command(ff, "-v", "error", "-i", f, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-").Output()
		if err != nil {
			t.Fatalf("%dx%d: ffmpeg: %v", c.w, c.h, err)
		}
		if !bytes.Equal(got, want) {
			fs := len(want) / len(recs)
			for i := 0; i*fs < min(len(got), len(want)); i++ {
				if !bytes.Equal(got[i*fs:min(len(got), (i+1)*fs)], want[i*fs:(i+1)*fs]) {
					t.Fatalf("%dx%d qp %d: picture %d decodes otherwise (got %d bytes, want %d)", c.w, c.h, c.qp, i, len(got), len(want))
				}
			}
			t.Fatalf("%dx%d: got %d bytes, want %d", c.w, c.h, len(got), len(want))
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestMP4Boxes: the file is ftyp, moov, then mdat, each box's size its own
// and the chunk offset where the first picture starts.
func TestMP4Boxes(t *testing.T) {
	e, _, _ := encodeScreens(t, 100, 60, 26, 4)
	f := e.MP4()
	var types []string
	off := 0
	for off < len(f) {
		n := int(binary.BigEndian.Uint32(f[off:]))
		types = append(types, string(f[off+4:off+8]))
		if n < 8 || off+n > len(f) {
			t.Fatalf("box %s at %d: size %d", f[off+4:off+8], off, n)
		}
		off += n
	}
	if want := []string{"ftyp", "moov", "mdat"}; len(types) != 3 || types[0] != want[0] || types[1] != want[1] || types[2] != want[2] {
		t.Fatalf("boxes %v, want %v", types, want)
	}
	i := bytes.Index(f, []byte("stco"))
	chunk := int(binary.BigEndian.Uint32(f[i+12:]))
	if l := int(binary.BigEndian.Uint32(f[chunk:])); l != len(e.samples[0])-4 {
		t.Fatalf("stco points at a %d-byte unit, the first picture is %d", l, len(e.samples[0])-4)
	}
}
