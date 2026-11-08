// Package h264 makes small H.264 videos of screen pictures, in pure Go:
// Constrained Baseline, every changed macroblock coded Intra 16x16 with
// CAVLC, every unchanged one skipped. It is built for a series of
// screenshots shown a second each, where most of a picture stays as it
// was, not for camera video.
package h264

import (
	"errors"
	"image"
	"image/draw"
)

// An Encoder takes pictures of one size, each with how long it is shown,
// and makes them a video.
type Encoder struct {
	w, h     int // the size shown, even
	mbw, mbh int // in macroblocks
	qp       int
	level    int

	ref, cur, prev planes // the last picture as decoded, this one, the last source
	n              int    // pictures encoded
	tcY            []uint8
	tcC            [2][]uint8

	samples [][]byte // each picture's NAL units, each after its 4-byte length
	ms      []uint32
}

// planes is a picture as 4:2:0 YCbCr, padded to whole macroblocks.
type planes struct{ y, cb, cr []byte }

// NewEncoder is an encoder of w×h pictures at quantiser qp (0–51; lower is
// sharper and bigger).
func NewEncoder(w, h, qp int) (*Encoder, error) {
	if w <= 0 || h <= 0 {
		return nil, errors.New("h264: an empty picture")
	}
	if qp < 0 || qp > 51 {
		return nil, errors.New("h264: qp is 0 to 51")
	}
	w, h = (w+1)&^1, (h+1)&^1
	e := &Encoder{w: w, h: h, mbw: (w + 15) / 16, mbh: (h + 15) / 16, qp: qp}
	fs := e.mbw * e.mbh
	for _, l := range []struct{ idc, fs int }{{31, 3600}, {32, 5120}, {40, 8192}, {42, 8704}, {50, 22080}, {51, 36864}} {
		if fs <= l.fs && e.mbw*e.mbw <= 8*l.fs && e.mbh*e.mbh <= 8*l.fs {
			e.level = l.idc
			break
		}
	}
	if e.level == 0 {
		return nil, errors.New("h264: the picture is too big")
	}
	e.ref, e.cur = e.newPlanes(), e.newPlanes()
	e.tcY = make([]uint8, fs*16)
	e.tcC = [2][]uint8{make([]uint8, fs*4), make([]uint8, fs*4)}
	return e, nil
}

func (e *Encoder) newPlanes() planes {
	n := e.mbw * e.mbh * 256
	return planes{make([]byte, n), make([]byte, n/4), make([]byte, n/4)}
}

// Add encodes img, shown for ms milliseconds. A picture of another size
// is cut or padded at its right and bottom.
func (e *Encoder) Add(img image.Image, ms int) {
	src := e.convert(img)
	e.encode(src)
	e.prev = src
	e.ms = append(e.ms, uint32(max(ms, 1)))
}

// convert is img as BT.709 limited-range YCbCr, the edge repeated into the
// padding.
func (e *Encoder) convert(img image.Image) planes {
	b := img.Bounds()
	rgba, ok := img.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(b)
		draw.Draw(rgba, b, img, b.Min, draw.Src)
	}
	p := e.newPlanes()
	W, H := e.mbw*16, e.mbh*16
	lw, lh := min(e.w, b.Dx()), min(e.h, b.Dy())
	if lw == 0 || lh == 0 {
		for i := range p.y {
			p.y[i] = 16
		}
		for i := range p.cb {
			p.cb[i], p.cr[i] = 128, 128
		}
		return p
	}
	at := func(x, y int) (int32, int32, int32) {
		o := rgba.PixOffset(b.Min.X+min(x, lw-1), b.Min.Y+min(y, lh-1))
		return int32(rgba.Pix[o]), int32(rgba.Pix[o+1]), int32(rgba.Pix[o+2])
	}
	for y := 0; y < H; y += 2 {
		for x := 0; x < W; x += 2 {
			var sr, sg, sb int32
			for k := range 4 {
				px, py := x+k&1, y+k>>1
				r, g, bl := at(px, py)
				p.y[py*W+px] = uint8((47*r+157*g+16*bl+128)>>8 + 16)
				sr, sg, sb = sr+r, sg+g, sb+bl
			}
			c := (y/2)*(W/2) + x/2
			p.cb[c] = uint8(clamp((-26*sr-86*sg+112*sb+512)>>10 + 128))
			p.cr[c] = uint8(clamp((112*sr-102*sg-10*sb+512)>>10 + 128))
		}
	}
	return p
}

func clamp(v int32) int32 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// encode codes src as the next picture: the first as an IDR picture, each
// later one as a P picture whose unchanged macroblocks are skipped.
func (e *Encoder) encode(src planes) {
	idr := e.n == 0
	var w bits
	w.ue(0) // first_mb_in_slice
	if idr {
		w.ue(7) // I
	} else {
		w.ue(5) // P
	}
	w.ue(0)                     // pic_parameter_set_id
	w.u(16, uint32(e.n&0xffff)) // frame_num
	if idr {
		w.ue(0) // idr_pic_id
	} else {
		w.u(1, 0) // num_ref_idx_active_override_flag
		w.u(1, 0) // ref_pic_list_modification_flag_l0
	}
	if idr {
		w.u(1, 0) // no_output_of_prior_pics_flag
		w.u(1, 0) // long_term_reference_flag
	} else {
		w.u(1, 0) // adaptive_ref_pic_marking_mode_flag
	}
	w.se(int32(e.qp - 26)) // slice_qp_delta
	w.ue(1)                // disable_deblocking_filter_idc

	skip := 0
	for my := range e.mbh {
		for mx := range e.mbw {
			if !idr && e.same(src, mx, my) {
				skip++
				e.copyRef(mx, my)
				e.setTC(mx, my, nil, nil)
				continue
			}
			if !idr {
				w.ue(uint32(skip))
				skip = 0
			}
			e.intra(&w, src, mx, my, !idr)
		}
	}
	if skip > 0 {
		w.ue(uint32(skip))
	}
	rbsp := w.trailing()
	unit := nal(2, 1, rbsp)
	if idr {
		unit = nal(3, 5, rbsp)
	}
	s := make([]byte, 4, 4+len(unit))
	s[0], s[1], s[2], s[3] = byte(len(unit)>>24), byte(len(unit)>>16), byte(len(unit)>>8), byte(len(unit))
	e.samples = append(e.samples, append(s, unit...))
	e.ref, e.cur = e.cur, e.ref
	e.n++
}

// same reports whether macroblock (mx, my) of src is as it was in the
// last source picture.
func (e *Encoder) same(src planes, mx, my int) bool {
	W := e.mbw * 16
	for y := my * 16; y < my*16+16; y++ {
		o := y*W + mx*16
		if string(src.y[o:o+16]) != string(e.prev.y[o:o+16]) {
			return false
		}
	}
	for y := my * 8; y < my*8+8; y++ {
		o := y*W/2 + mx*8
		if string(src.cb[o:o+8]) != string(e.prev.cb[o:o+8]) || string(src.cr[o:o+8]) != string(e.prev.cr[o:o+8]) {
			return false
		}
	}
	return true
}

// copyRef is a skipped macroblock: the last picture's, unmoved.
func (e *Encoder) copyRef(mx, my int) {
	W := e.mbw * 16
	for y := my * 16; y < my*16+16; y++ {
		o := y*W + mx*16
		copy(e.cur.y[o:o+16], e.ref.y[o:o+16])
	}
	for y := my * 8; y < my*8+8; y++ {
		o := y*W/2 + mx*8
		copy(e.cur.cb[o:o+8], e.ref.cb[o:o+8])
		copy(e.cur.cr[o:o+8], e.ref.cr[o:o+8])
	}
}

// setTC records the TotalCoeff of each 4x4 block of macroblock (mx, my),
// for later blocks' nC; nil is none coded.
func (e *Encoder) setTC(mx, my int, y *[16]uint8, c *[2][4]uint8) {
	gw := e.mbw * 4
	for b := range 16 {
		v := uint8(0)
		if y != nil {
			v = y[b]
		}
		e.tcY[(my*4+b>>2)*gw+mx*4+b&3] = v
	}
	cw := e.mbw * 2
	for k := range 2 {
		for b := range 4 {
			v := uint8(0)
			if c != nil {
				v = c[k][b]
			}
			e.tcC[k][(my*2+b>>1)*cw+mx*2+b&1] = v
		}
	}
}

// nC is the predicted TotalCoeff of the 4x4 block at (x, y) of grid g,
// gw blocks wide, from its left and upper neighbours.
func nC(g []uint8, gw, x, y int) int {
	a, b := x > 0, y > 0
	switch {
	case a && b:
		return (int(g[y*gw+x-1]) + int(g[(y-1)*gw+x]) + 1) >> 1
	case a:
		return int(g[y*gw+x-1])
	case b:
		return int(g[(y-1)*gw+x])
	}
	return 0
}

// pred16 is Intra 16x16 prediction of the block at (x0, y0) of plane p
// (stride s), in mode 0 (vertical), 1 (horizontal) or 2 (DC).
func pred16(p []byte, s, x0, y0, mode int, left, top bool, out *[256]int32) {
	switch mode {
	case 0:
		for y := range 16 {
			for x := range 16 {
				out[y*16+x] = int32(p[(y0-1)*s+x0+x])
			}
		}
	case 1:
		for y := range 16 {
			v := int32(p[(y0+y)*s+x0-1])
			for x := range 16 {
				out[y*16+x] = v
			}
		}
	default:
		var sum, n int32
		if top {
			for x := range 16 {
				sum += int32(p[(y0-1)*s+x0+x])
			}
			n += 16
		}
		if left {
			for y := range 16 {
				sum += int32(p[(y0+y)*s+x0-1])
			}
			n += 16
		}
		v := int32(128)
		if n > 0 {
			v = (sum + n/2) / n
		}
		for i := range out {
			out[i] = v
		}
	}
}

// pred8 is chroma prediction of the 8x8 block at (x0, y0), in mode 0 (DC),
// 1 (horizontal) or 2 (vertical).
func pred8(p []byte, s, x0, y0, mode int, left, top bool, out *[64]int32) {
	switch mode {
	case 1:
		for y := range 8 {
			v := int32(p[(y0+y)*s+x0-1])
			for x := range 8 {
				out[y*8+x] = v
			}
		}
	case 2:
		for y := range 8 {
			for x := range 8 {
				out[y*8+x] = int32(p[(y0-1)*s+x0+x])
			}
		}
	default:
		for b := range 4 {
			xo, yo := (b&1)*4, (b>>1)*4
			var st, sl int32
			if top {
				for x := range 4 {
					st += int32(p[(y0-1)*s+x0+xo+x])
				}
			}
			if left {
				for y := range 4 {
					sl += int32(p[(y0+yo+y)*s+x0-1])
				}
			}
			v := int32(128)
			switch {
			case xo > 0 && yo == 0: // the upper right block leans on the row above
				if top {
					v = (st + 2) >> 2
				} else if left {
					v = (sl + 2) >> 2
				}
			case xo == 0 && yo > 0: // the lower left one on the column at its left
				if left {
					v = (sl + 2) >> 2
				} else if top {
					v = (st + 2) >> 2
				}
			default:
				switch {
				case top && left:
					v = (st + sl + 4) >> 3
				case left:
					v = (sl + 2) >> 2
				case top:
					v = (st + 2) >> 2
				}
			}
			for y := range 4 {
				for x := range 4 {
					out[(yo+y)*8+xo+x] = v
				}
			}
		}
	}
}

// fdct is the forward 4x4 core transform, in place, raster order.
func fdct(d *[16]int32) {
	for i := range 4 {
		r := d[i*4 : i*4+4]
		s03, d03, s12, d12 := r[0]+r[3], r[0]-r[3], r[1]+r[2], r[1]-r[2]
		r[0], r[1], r[2], r[3] = s03+s12, 2*d03+d12, s03-s12, d03-2*d12
	}
	for j := range 4 {
		s03, d03 := d[j]+d[12+j], d[j]-d[12+j]
		s12, d12 := d[4+j]+d[8+j], d[4+j]-d[8+j]
		d[j], d[4+j], d[8+j], d[12+j] = s03+s12, 2*d03+d12, s03-s12, d03-2*d12
	}
}

// idct is §8.5.12.2: rows, then columns, then (x+32)>>6.
func idct(d *[16]int32) {
	for i := range 4 {
		r := d[i*4 : i*4+4]
		e0, e1, e2, e3 := r[0]+r[2], r[0]-r[2], (r[1]>>1)-r[3], r[1]+(r[3]>>1)
		r[0], r[1], r[2], r[3] = e0+e3, e1+e2, e1-e2, e0-e3
	}
	for j := range 4 {
		f0, f1, f2, f3 := d[j], d[4+j], d[8+j], d[12+j]
		g0, g1, g2, g3 := f0+f2, f0-f2, (f1>>1)-f3, f1+(f3>>1)
		d[j], d[4+j], d[8+j], d[12+j] = g0+g3, g1+g2, g1-g2, g0-g3
	}
	for i := range d {
		d[i] = (d[i] + 32) >> 6
	}
}

// hadamard4 is H·c·H for the 4x4 luma DC, exact; H is symmetric, rows
// 1111, 11-1-1, 1-1-11, 1-11-1.
func hadamard4(c *[16]int32) {
	for i := range 4 {
		r := c[i*4 : i*4+4]
		a, b, cc, d := r[0]+r[1], r[0]-r[1], r[2]+r[3], r[2]-r[3]
		r[0], r[1], r[2], r[3] = a+cc, a-cc, b-d, b+d
	}
	for j := range 4 {
		a, b := c[j]+c[4+j], c[j]-c[4+j]
		cc, d := c[8+j]+c[12+j], c[8+j]-c[12+j]
		c[j], c[4+j], c[8+j], c[12+j] = a+cc, a-cc, b-d, b+d
	}
}

// most bounds a level so that CAVLC's 12-bit escape can carry it.
const most = 2000

func quant(w, mf int32, qbits uint, f int64) int32 {
	neg := w < 0
	if neg {
		w = -w
	}
	z := int32((int64(w)*int64(mf) + f) >> qbits)
	z = min(z, most)
	if neg {
		return -z
	}
	return z
}

// scaleAC is §8.5.12.1 for a coefficient other than a DC.
func scaleAC(c int32, qp, pos int) int32 {
	ls := 16 * normAdjust[qp%6][posClass(pos)]
	if qp >= 24 {
		return c * ls << (qp/6 - 4)
	}
	return (c*ls + 1<<(3-qp/6)) >> (4 - qp/6)
}

func sad(a, b []int32) int32 {
	var s int32
	for i := range a {
		d := a[i] - b[i]
		if d < 0 {
			d = -d
		}
		s += d
	}
	return s
}

// intra codes macroblock (mx, my) as Intra 16x16 and writes its
// reconstruction into e.cur.
func (e *Encoder) intra(w *bits, src planes, mx, my int, inP bool) {
	S := e.mbw * 16
	x0, y0 := mx*16, my*16
	left, top := mx > 0, my > 0

	// luma: the prediction nearest the source
	var orig [256]int32
	for y := range 16 {
		for x := range 16 {
			orig[y*16+x] = int32(src.y[(y0+y)*S+x0+x])
		}
	}
	var pred, try [256]int32
	modeY, best := 2, int32(-1)
	for _, m := range []int{2, 0, 1} {
		if m == 0 && !top || m == 1 && !left {
			continue
		}
		pred16(e.cur.y, S, x0, y0, m, left, top, &try)
		if s := sad(orig[:], try[:]); best < 0 || s < best {
			modeY, best, pred = m, s, try
		}
	}

	qp := e.qp
	qbits := uint(15 + qp/6)
	f := int64(1<<qbits) / 3
	var dc [16]int32
	var ac [16][15]int32
	anyAC := false
	for b := range 16 {
		bx, by := b&3, b>>2
		var d [16]int32
		for y := range 4 {
			for x := range 4 {
				i := (by*4+y)*16 + bx*4 + x
				d[y*4+x] = orig[i] - pred[i]
			}
		}
		fdct(&d)
		dc[b] = d[0]
		for k := 1; k < 16; k++ {
			pos := zigzag[k]
			ac[b][k-1] = quant(d[pos], quantMF[qp%6][posClass(pos)], qbits, f)
			anyAC = anyAC || ac[b][k-1] != 0
		}
	}
	hadamard4(&dc)
	var dcLev [16]int32
	for i := range dc {
		dcLev[i] = quant(dc[i]/2, quantMF[qp%6][0], qbits+1, 2*f)
	}
	cbpL := 0
	if anyAC {
		cbpL = 15
	} else {
		ac = [16][15]int32{}
	}

	// luma reconstruction, as a decoder makes it
	dcY := dcLev
	hadamard4(&dcY)
	ls := 16 * normAdjust[qp%6][0]
	for i := range dcY {
		if qp >= 36 {
			dcY[i] = dcY[i] * ls << (qp/6 - 6)
		} else {
			dcY[i] = (dcY[i]*ls + 1<<(5-qp/6)) >> (6 - qp/6)
		}
	}
	for b := range 16 {
		bx, by := b&3, b>>2
		var d [16]int32
		d[0] = dcY[b]
		for k := 1; k < 16; k++ {
			d[zigzag[k]] = scaleAC(ac[b][k-1], qp, zigzag[k])
		}
		idct(&d)
		for y := range 4 {
			for x := range 4 {
				i := (by*4+y)*16 + bx*4 + x
				e.cur.y[(y0+by*4+y)*S+x0+bx*4+x] = uint8(clamp(pred[i] + d[y*4+x]))
			}
		}
	}

	// chroma
	C := S / 2
	cx, cy := mx*8, my*8
	var corig [2][64]int32
	for k, pl := range [2][]byte{src.cb, src.cr} {
		for y := range 8 {
			for x := range 8 {
				corig[k][y*8+x] = int32(pl[(cy+y)*C+cx+x])
			}
		}
	}
	var cpred [2][64]int32
	modeC, bestC := 0, -1
	for _, m := range []int{0, 1, 2} {
		if m == 2 && !top || m == 1 && !left {
			continue
		}
		var t [2][64]int32
		pred8(e.cur.cb, C, cx, cy, m, left, top, &t[0])
		pred8(e.cur.cr, C, cx, cy, m, left, top, &t[1])
		if s := int(sad(corig[0][:], t[0][:]) + sad(corig[1][:], t[1][:])); bestC < 0 || s < bestC {
			modeC, bestC, cpred = m, s, t
		}
	}
	qpc := qpChroma(qp)
	cbits := uint(15 + qpc/6)
	cf := int64(1<<cbits) / 3
	var cdc [2][4]int32
	var cac [2][4][15]int32
	anyCDC, anyCAC := false, false
	for k := range 2 {
		var c [4]int32
		for b := range 4 {
			bx, by := b&1, b>>1
			var d [16]int32
			for y := range 4 {
				for x := range 4 {
					i := (by*4+y)*8 + bx*4 + x
					d[y*4+x] = corig[k][i] - cpred[k][i]
				}
			}
			fdct(&d)
			c[b] = d[0]
			for j := 1; j < 16; j++ {
				pos := zigzag[j]
				cac[k][b][j-1] = quant(d[pos], quantMF[qpc%6][posClass(pos)], cbits, cf)
				anyCAC = anyCAC || cac[k][b][j-1] != 0
			}
		}
		h := [4]int32{c[0] + c[1] + c[2] + c[3], c[0] - c[1] + c[2] - c[3], c[0] + c[1] - c[2] - c[3], c[0] - c[1] - c[2] + c[3]}
		for b := range 4 {
			cdc[k][b] = quant(h[b], quantMF[qpc%6][0], cbits+1, 2*cf)
			anyCDC = anyCDC || cdc[k][b] != 0
		}
	}
	cbpC := 0
	switch {
	case anyCAC:
		cbpC = 2
	case anyCDC:
		cbpC = 1
	}
	if cbpC < 2 {
		cac = [2][4][15]int32{}
	}
	cls := 16 * normAdjust[qpc%6][0]
	for k, pl := range [2][]byte{e.cur.cb, e.cur.cr} {
		c := cdc[k]
		h := [4]int32{c[0] + c[1] + c[2] + c[3], c[0] - c[1] + c[2] - c[3], c[0] + c[1] - c[2] - c[3], c[0] - c[1] - c[2] + c[3]}
		for b := range 4 {
			bx, by := b&1, b>>1
			var d [16]int32
			d[0] = (h[b] * cls << (qpc / 6)) >> 5
			for j := 1; j < 16; j++ {
				d[zigzag[j]] = scaleAC(cac[k][b][j-1], qpc, zigzag[j])
			}
			idct(&d)
			for y := range 4 {
				for x := range 4 {
					i := (by*4+y)*8 + bx*4 + x
					pl[(cy+by*4+y)*C+cx+bx*4+x] = uint8(clamp(cpred[k][i] + d[y*4+x]))
				}
			}
		}
	}

	// the macroblock's syntax
	mbType := 1 + modeY + 4*cbpC
	if cbpL != 0 {
		mbType += 12
	}
	if inP {
		mbType += 5
	}
	w.ue(uint32(mbType))
	w.ue(uint32(modeC)) // intra_chroma_pred_mode
	w.se(0)             // mb_qp_delta
	gw := e.mbw * 4
	var dcScan [16]int32
	for k := range 16 {
		dcScan[k] = dcLev[zigzag[k]]
	}
	w.residual(dcScan[:], nC(e.tcY, gw, mx*4, my*4))
	var tcY [16]uint8
	var tcC [2][4]uint8
	e.setTC(mx, my, &tcY, &tcC) // none yet: later blocks see only earlier ones
	if cbpL != 0 {
		for i := range 16 { // in decoding order: by 8x8 quadrant
			bx := (i>>2&1)*2 + i&1
			by := (i>>3&1)*2 + i>>1&1
			gx, gy := mx*4+bx, my*4+by
			tc := w.residual(ac[by*4+bx][:], nC(e.tcY, gw, gx, gy))
			e.tcY[gy*gw+gx] = uint8(tc)
		}
	}
	if cbpC > 0 {
		w.residual(cdc[0][:], -1)
		w.residual(cdc[1][:], -1)
	}
	if cbpC == 2 {
		cw := e.mbw * 2
		for k := range 2 {
			for b := range 4 {
				gx, gy := mx*2+b&1, my*2+b>>1
				tc := w.residual(cac[k][b][:], nC(e.tcC[k], cw, gx, gy))
				e.tcC[k][gy*cw+gx] = uint8(tc)
			}
		}
	}
}

// residual writes a block's coefficients, in scan order, with CAVLC
// (§9.2) at nC (-1 for chroma DC), and is its TotalCoeff.
func (w *bits) residual(coef []int32, nc int) int {
	maxNum := len(coef)
	var lv [16]int32
	var pos [16]int
	total := 0
	for i := maxNum - 1; i >= 0; i-- {
		if coef[i] != 0 {
			lv[total], pos[total] = coef[i], i
			total++
		}
	}
	t1 := 0
	for t1 < total && t1 < 3 && (lv[t1] == 1 || lv[t1] == -1) {
		t1++
	}
	if nc == -1 {
		i := total*4 + t1
		w.u(uint(chromaDCTokenLen[i]), uint32(chromaDCTokenBits[i]))
	} else {
		t := 3
		switch {
		case nc < 2:
			t = 0
		case nc < 4:
			t = 1
		case nc < 8:
			t = 2
		}
		i := total*4 + t1
		w.u(uint(coeffTokenLen[t][i]), uint32(coeffTokenBits[t][i]))
	}
	if total == 0 {
		return 0
	}
	suffix := uint(0)
	if total > 10 && t1 < 3 {
		suffix = 1
	}
	for i := range total {
		l := lv[i]
		if i < t1 {
			w.flag(l < 0)
			continue
		}
		code := 2*l - 2
		if l < 0 {
			code = -2*l - 1
		}
		if i == t1 && t1 < 3 {
			code -= 2
		}
		switch {
		case suffix == 0 && code < 14:
			w.u(uint(code), 0)
			w.u(1, 1)
		case suffix == 0 && code < 30:
			w.u(14, 0)
			w.u(1, 1)
			w.u(4, uint32(code-14))
		case suffix == 0:
			w.u(15, 0)
			w.u(1, 1)
			w.u(12, uint32(code-30))
		case code>>suffix < 15:
			w.u(uint(code>>suffix), 0)
			w.u(1, 1)
			w.u(suffix, uint32(code))
		default:
			w.u(15, 0)
			w.u(1, 1)
			w.u(12, uint32(code-15<<suffix))
		}
		if suffix == 0 {
			suffix = 1
		}
		a := l
		if a < 0 {
			a = -a
		}
		if a > 3<<(suffix-1) && suffix < 6 {
			suffix++
		}
	}
	if total < maxNum {
		tz := pos[0] + 1 - total
		if maxNum == 4 {
			w.u(uint(chromaDCZerosLen[total-1][tz]), uint32(chromaDCZerosBits[total-1][tz]))
		} else {
			w.u(uint(totalZerosLen[total-1][tz]), uint32(totalZerosBits[total-1][tz]))
		}
		left := tz
		for i := 0; i < total-1 && left > 0; i++ {
			run := pos[i] - pos[i+1] - 1
			t := min(left, 7) - 1
			w.u(uint(runLen[t][run]), uint32(runBits[t][run]))
			left -= run
		}
	}
	return total
}

// sps is the sequence parameter set's RBSP.
func (e *Encoder) sps() []byte {
	var w bits
	w.u(8, 66)   // profile_idc: Baseline
	w.u(8, 0xc0) // constraint_set0 and set1: Constrained Baseline
	w.u(8, uint32(e.level))
	w.ue(0)  // seq_parameter_set_id
	w.ue(12) // log2_max_frame_num_minus4: 16 bits
	w.ue(2)  // pic_order_cnt_type: in decoding order
	w.ue(1)  // max_num_ref_frames
	w.u(1, 0)
	w.ue(uint32(e.mbw - 1))
	w.ue(uint32(e.mbh - 1))
	w.u(1, 1) // frame_mbs_only_flag
	w.u(1, 1) // direct_8x8_inference_flag
	cr, cb := (e.mbw*16-e.w)/2, (e.mbh*16-e.h)/2
	w.flag(cr > 0 || cb > 0)
	if cr > 0 || cb > 0 {
		w.ue(0)
		w.ue(uint32(cr))
		w.ue(0)
		w.ue(uint32(cb))
	}
	w.u(1, 1) // vui_parameters_present_flag
	w.u(1, 0) // aspect_ratio_info_present_flag
	w.u(1, 0) // overscan_info_present_flag
	w.u(1, 1) // video_signal_type_present_flag
	w.u(3, 5) // video_format: unspecified
	w.u(1, 0) // video_full_range_flag
	w.u(1, 1) // colour_description_present_flag
	w.u(8, 1) // BT.709 primaries,
	w.u(8, 1) // transfer
	w.u(8, 1) // and matrix
	w.u(1, 0) // chroma_loc_info_present_flag
	w.u(1, 0) // timing_info_present_flag
	w.u(1, 0) // nal_hrd_parameters_present_flag
	w.u(1, 0) // vcl_hrd_parameters_present_flag
	w.u(1, 0) // pic_struct_present_flag
	w.u(1, 1) // bitstream_restriction_flag
	w.u(1, 1) // motion_vectors_over_pic_boundaries_flag
	w.ue(0)   // max_bytes_per_pic_denom
	w.ue(0)   // max_bits_per_mb_denom
	w.ue(15)  // log2_max_mv_length_horizontal
	w.ue(15)  // log2_max_mv_length_vertical
	w.ue(0)   // max_num_reorder_frames: each picture shows when decoded
	w.ue(1)   // max_dec_frame_buffering
	return nal(3, 7, w.trailing())
}

// pps is the picture parameter set.
func (e *Encoder) pps() []byte {
	var w bits
	w.ue(0)   // pic_parameter_set_id
	w.ue(0)   // seq_parameter_set_id
	w.u(1, 0) // CAVLC
	w.u(1, 0) // bottom_field_pic_order_in_frame_present_flag
	w.ue(0)   // num_slice_groups_minus1
	w.ue(0)   // num_ref_idx_l0_default_active_minus1
	w.ue(0)   // num_ref_idx_l1_default_active_minus1
	w.u(1, 0) // weighted_pred_flag
	w.u(2, 0) // weighted_bipred_idc
	w.se(0)   // pic_init_qp_minus26
	w.se(0)   // pic_init_qs_minus26
	w.se(0)   // chroma_qp_index_offset
	w.u(1, 1) // deblocking_filter_control_present_flag
	w.u(1, 0) // constrained_intra_pred_flag
	w.u(1, 0) // redundant_pic_cnt_present_flag
	return nal(3, 8, w.trailing())
}
