package h264

import "encoding/binary"

// MP4 is the pictures added so far as an MP4 file, its index first so a
// player can start before the whole file is read.
func (e *Encoder) MP4() []byte {
	var total uint64
	for _, d := range e.ms {
		total += uint64(d)
	}
	sps, pps := e.sps(), e.pps()
	ftyp := box("ftyp", []byte("isom"), u32(512), []byte("isomiso2avc1mp41"))

	var stts []byte
	runs := 0
	for i := 0; i < len(e.ms); {
		j := i
		for j < len(e.ms) && e.ms[j] == e.ms[i] {
			j++
		}
		stts = append(stts, u32(uint32(j-i))...)
		stts = append(stts, u32(e.ms[i])...)
		runs++
		i = j
	}
	sizes := make([]byte, 0, 4*len(e.samples))
	var mdatLen uint64
	for _, s := range e.samples {
		sizes = append(sizes, u32(uint32(len(s)))...)
		mdatLen += uint64(len(s))
	}
	avcC := box("avcC", []byte{1, 66, 0xc0, byte(e.level), 0xff, 0xe1},
		u16(uint16(len(sps))), sps, []byte{1}, u16(uint16(len(pps))), pps)
	avc1 := box("avc1", make([]byte, 6), u16(1), make([]byte, 16),
		u16(uint16(e.w)), u16(uint16(e.h)), u32(0x00480000), u32(0x00480000), u32(0),
		u16(1), make([]byte, 32), u16(0x18), u16(0xffff), avcC)
	moov := func(chunk uint32) []byte {
		stbl := box("stbl",
			full("stsd", 0, 0, u32(1), avc1),
			full("stts", 0, 0, u32(uint32(runs)), stts),
			full("stss", 0, 0, u32(1), u32(1)),
			full("stsc", 0, 0, u32(1), u32(1), u32(uint32(len(e.samples))), u32(1)),
			full("stsz", 0, 0, u32(0), u32(uint32(len(e.samples))), sizes),
			full("stco", 0, 0, u32(1), u32(chunk)))
		minf := box("minf",
			full("vmhd", 0, 1, make([]byte, 8)),
			box("dinf", full("dref", 0, 0, u32(1), full("url ", 0, 1))),
			stbl)
		mdia := box("mdia",
			full("mdhd", 0, 0, u32(0), u32(0), u32(1000), u32(uint32(total)), u16(0x55c4), u16(0)),
			full("hdlr", 0, 0, u32(0), []byte("vide"), make([]byte, 12), []byte("VideoHandler\x00")),
			minf)
		tkhd := full("tkhd", 0, 3, u32(0), u32(0), u32(1), u32(0), u32(uint32(total)),
			make([]byte, 8), u16(0), u16(0), u16(0), u16(0), matrix(),
			u32(uint32(e.w)<<16), u32(uint32(e.h)<<16))
		mvhd := full("mvhd", 0, 0, u32(0), u32(0), u32(1000), u32(uint32(total)),
			u32(0x00010000), u16(0x0100), make([]byte, 10), matrix(), make([]byte, 24), u32(2))
		return box("moov", mvhd, box("trak", tkhd, mdia))
	}
	head := len(ftyp) + len(moov(0)) + 8
	out := make([]byte, 0, uint64(head)+mdatLen)
	out = append(out, ftyp...)
	out = append(out, moov(uint32(head))...)
	out = append(out, u32(uint32(8+mdatLen))...)
	out = append(out, "mdat"...)
	for _, s := range e.samples {
		out = append(out, s...)
	}
	return out
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }

func box(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	out = binary.BigEndian.AppendUint32(out, uint32(n))
	out = append(out, typ...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// full is a full box: its version and flags before its parts.
func full(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	vf := u32(uint32(version)<<24 | flags)
	return box(typ, append([][]byte{vf}, parts...)...)
}

// matrix is the identity transformation.
func matrix() []byte {
	var m []byte
	for _, v := range []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000} {
		m = append(m, u32(v)...)
	}
	return m
}
