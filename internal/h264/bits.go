package h264

// bits writes a bit string, most significant bit first.
type bits struct {
	buf []byte
	acc uint64
	n   uint // bits in acc not yet in buf, fewer than 8
}

// u writes v's low n bits, n ≤ 32.
func (w *bits) u(n uint, v uint32) {
	if n == 0 {
		return
	}
	w.acc = w.acc<<n | uint64(v)&(1<<n-1)
	w.n += n
	for w.n >= 8 {
		w.n -= 8
		w.buf = append(w.buf, byte(w.acc>>w.n))
	}
}

func (w *bits) flag(b bool) {
	if b {
		w.u(1, 1)
	} else {
		w.u(1, 0)
	}
}

// ue writes v as Exp-Golomb.
func (w *bits) ue(v uint32) {
	x := uint64(v) + 1
	n := uint(0)
	for x>>n > 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, uint32(x))
}

// se writes v as signed Exp-Golomb.
func (w *bits) se(v int32) {
	if v > 0 {
		w.ue(uint32(2*v - 1))
	} else {
		w.ue(uint32(-2 * v))
	}
}

// trailing ends an RBSP: a one, then zeros to the byte.
func (w *bits) trailing() []byte {
	w.u(1, 1)
	if w.n > 0 {
		w.u(8-w.n, 0)
	}
	return w.buf
}

// nal is an RBSP as a NAL unit: its header, then the payload with an
// emulation-prevention byte wherever two zeros meet a byte under four.
func nal(refIdc, typ byte, rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/64+2)
	out = append(out, refIdc<<5|typ)
	zeros := 0
	for _, b := range rbsp {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}
