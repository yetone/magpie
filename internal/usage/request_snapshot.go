package usage

// Cache snapshots encode the fixed request fields without reflection. The
// encoding is private to this process: no on-disk format or migration. Time
// locations are retained separately so a round trip preserves displayed times.
//
// The blocks are compressed with S2, not flate: a query over the whole
// history decodes every block, and flate's decoder was most of the 3 s a
// filter over 750k calls took; S2 decodes at memory speed for a block a
// little larger.
import (
	"encoding/binary"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/klauspost/compress/s2"
)

// rowDecodes are the buffers blocks are decoded into before their rows are
// read out of them; nothing keeps a reference to one after unpack.
var rowDecodes = sync.Pool{New: func() any { return new([]byte) }}

// rowEncodes are pack's scratch: the rows' bytes before compression and
// S2's output. s2.Encode makes its output as large as the worst case
// (MaxEncodedLen, more than the input), and an archive cut from it kept
// that whole allocation, several times the compressed bytes: the largest
// thing a started magpie held, ~17 MB for ~380k calls, and uncounted in
// Bytes. The archive is copied out of the scratch at its own length.
var rowEncodes = sync.Pool{New: func() any { return new([2][]byte) }}

func (c *rowChunk) pack() *rowChunk {
	if c == nil || len(c.Rows) == 0 || len(c.Archive) != 0 {
		return c
	}
	encoded, locations := c.encode(func(encoded []byte) bool { return int64(len(encoded)) < c.Bytes })
	if encoded == nil {
		return c
	}
	next := *c
	next.Count = len(c.Rows) // what rowsIn says of it
	next.Rows, next.Strings, next.Shapes, next.dict, next.shapeIDs = nil, nil, nil, nil, nil
	next.Archive, next.Locations = encoded, locations
	next.Bytes = int64(len(next.Archive)+len(locations)*8) + next.codexActors.bytes()
	return &next
}

// encode is c's rows as an archive and the locations their times are in,
// or nil when keep turns the compressed bytes down.
func (c *rowChunk) encode(keep func([]byte) bool) ([]byte, []*time.Location) {
	scratch := rowEncodes.Get().(*[2][]byte)
	defer rowEncodes.Put(scratch)
	data := scratch[0][:0]
	defer func() { scratch[0] = data[:0] }()
	put := func(v uint64) { data = binary.AppendUvarint(data, v) }
	put(uint64(len(c.Rows)))
	put(uint64(len(c.Strings)))
	for _, s := range c.Strings {
		put(uint64(len(s)))
		data = append(data, s...)
	}
	put(uint64(len(c.Shapes)))
	for _, shape := range c.Shapes {
		for _, n := range shape {
			put(uint64(n))
		}
	}
	var locations []*time.Location
	locs := map[*time.Location]uint64{}
	for k := range c.Rows {
		p := &c.Rows[k]
		loc := p.Time().Location()
		id, ok := locs[loc]
		if !ok {
			id = uint64(len(locations))
			locs[loc] = id
			locations = append(locations, loc)
		}
		put(uint64(p.Time().Unix()))
		put(uint64(p.Time().Nanosecond()))
		put(id)
		put(uint64(p.Shape))
		for _, n := range p.Varied {
			put(uint64(n))
		}
		for _, n := range p.Tokens {
			put(uint64(n))
		}
		for _, n := range []int64{int64(p.Millis), int64(p.TTFT), int64(p.FirstText), p.Order, p.RouteID, int64(p.Sent), int64(p.Flow), int64(p.Own), int64(p.Retried), int64(p.Queued), int64(p.FirstReasoning)} {
			put(uint64(n))
		}
		put(math.Float64bits(p.Cost))
		put(uint64(p.Status))
		put(uint64(p.Flags))
		put(uint64(p.Tries))
		put(uint64(p.Masking))
		put(uint64(p.UpBytes))
		put(uint64(p.DownBytes))
		put(uint64(int64(p.FilesSaved)))
		put(uint64(p.ImageFiles))
		if p.Flags&rowCredits != 0 {
			put(math.Float64bits(p.Credits))
		}
	}
	encoded := s2.Encode(scratch[1][:cap(scratch[1])], data)
	scratch[1] = encoded[:0]
	if !keep(encoded) {
		return nil, nil
	}
	return slices.Clone(encoded), locations
}

func (c *rowChunk) unpack() *rowChunk { return c.unpackInto(nil) }

// keptStrings copies out the strings that a result kept after a read takes
// from the rows it visited. An unpacked block's strings are cut from one
// allocation (unpackInto), so a cache holding one session id from a block
// holds every string of that block: its request and response ids and its
// errors. The Sessions and Routing caches kept a session id from nearly
// every block, and so the decoded text of the whole history: ~200 MB at
// rest for 600k calls.
type keptStrings map[string]string

func (k keptStrings) of(s string) string {
	if s == "" {
		return ""
	}
	if kept, ok := k[s]; ok {
		return kept
	}
	kept := strings.Clone(s)
	k[kept] = kept
	return kept
}

// decodeMapped is s2.Decode of src, of m's pages (readMapped). It is not
// unpackReusing's own closure: data caught by one is on the heap, and
// every read of a row's fields through it is slower.
func decodeMapped(m *diskMapping, dst, src []byte) (data []byte, ok bool) {
	var err error
	if m == nil {
		data, err = s2.Decode(dst, src)
		return data, err == nil
	}
	if !readMapped(m, func() { data, err = s2.Decode(dst, src) }) {
		return nil, false
	}
	return data, err == nil
}

// unpackInto is unpack decoding the rows into rows when it is large enough:
// every field of a row is written, so a reused slice needs no clearing.
func (c *rowChunk) unpackInto(into []packedRow) *rowChunk {
	return c.unpackReusing(nil, into, nil, nil, nil, false)
}

// unpackReusing is unpackInto decoding the strings' and shapes' slices into
// strs and shapes too, when they are large enough: for a reader that lets
// go of a chunk's rows before it opens the next (chunkRows). The block is
// decoded into *buf, when given. With lent, the strings are of *buf, not a
// copy: they are good until *buf is decoded into again. The chunk is dst,
// when given.
func (c *rowChunk) unpackReusing(dst *rowChunk, into []packedRow, strs []string, shapes [][rowTexts]uint32, buf *[]byte, lent bool) *rowChunk {
	if c == nil || len(c.Archive) == 0 {
		return c
	}
	if buf == nil {
		lent = false
		buf = rowDecodes.Get().(*[]byte)
		defer rowDecodes.Put(buf)
	}
	data, ok := decodeMapped(c.mapped, (*buf)[:cap(*buf)], c.Archive)
	if !ok {
		return nil
	}
	*buf = data
	valid := true
	take := func() uint64 {
		// most of a row's numbers are under 128, one byte: Uvarint's loop
		// for them was ~40% of a pass's decoding (#4372)
		if len(data) > 0 && data[0] < 0x80 {
			n := uint64(data[0])
			data = data[1:]
			return n
		}
		n, k := binary.Uvarint(data)
		if k <= 0 {
			valid = false
			return 0
		}
		data = data[k:]
		return n
	}
	rows, count := take(), take()
	if rows > uint64(len(data)) || count > uint64(len(data)) {
		return nil
	}
	next := dst
	if next == nil {
		next = new(rowChunk)
	}
	*next = *c
	next.Archive, next.Locations, next.mapped = nil, nil, nil
	if uint64(cap(into)) >= rows {
		next.Rows = into[:rows]
	} else {
		next.Rows = make([]packedRow, int(rows))
	}
	if uint64(cap(strs)) >= count {
		next.Strings = strs[:count]
	} else {
		next.Strings = make([]string, int(count))
	}
	next.Bytes = int64(len(next.Rows))*int64(unsafe.Sizeof(packedRow{})) + next.codexActors.bytes()
	// The strings are copied out in one piece: one allocation a block, not
	// one a string. The section is walked once to find its end, and its
	// copy again to cut the strings from it.
	section := data
	for range next.Strings {
		n := take()
		if n > uint64(len(data)) {
			return nil
		}
		data = data[n:]
	}
	if !valid {
		return nil
	}
	var text string
	if lent {
		text = unsafe.String(unsafe.SliceData(section), len(section)-len(data))
	} else {
		text = string(section[:len(section)-len(data)])
	}
	for i := range next.Strings {
		var n, shift uint64
		for k := 0; ; k++ {
			b := text[k]
			n |= uint64(b&0x7f) << shift
			if b < 0x80 {
				text = text[k+1:]
				break
			}
			shift += 7
		}
		next.Strings[i] = text[:n]
		text = text[n:]
		next.Bytes += int64(n) + 16
	}
	shapeCount := take()
	if shapeCount > uint64(len(data)) {
		return nil
	}
	if uint64(cap(shapes)) >= shapeCount {
		next.Shapes = shapes[:shapeCount]
	} else {
		next.Shapes = make([][rowTexts]uint32, int(shapeCount))
	}
	next.shapeIDs = nil
	for i := range next.Shapes {
		for j := range next.Shapes[i] {
			n := take()
			if n >= count {
				return nil
			}
			next.Shapes[i][j] = uint32(n)
		}
	}
	next.Bytes += int64(len(next.Shapes)) * int64(unsafe.Sizeof([rowTexts]uint32{}))
	var few [4]uint16
	zones := few[:0]
	for _, loc := range c.Locations {
		zones = append(zones, zoneOf(loc))
	}
	for i := range next.Rows {
		p := &next.Rows[i]
		sec, nano, loc := int64(take()), int64(take()), take()
		if loc >= uint64(len(zones)) {
			return nil
		}
		p.setTimeIn(time.Unix(sec, nano), zones[loc])
		p.Shape = uint32(take())
		if int(p.Shape) >= len(next.Shapes) {
			return nil
		}
		for j := range p.Varied {
			n := take()
			if n >= count {
				return nil
			}
			p.Varied[j] = uint32(n)
		}
		for j := range p.Tokens {
			p.Tokens[j] = tokensOf(int64(take()))
		}
		p.Millis, p.TTFT, p.FirstText, p.Order = msOf(int64(take())), msOf(int64(take())), msOf(int64(take())), int64(take())
		p.RouteID = int64(take())
		p.Sent, p.Flow = msOf(int64(take())), msOf(int64(take()))
		p.Own, p.Retried, p.Queued = msOf(int64(take())), msOf(int64(take())), msOf(int64(take()))
		p.FirstReasoning = msOf(int64(take()))
		p.Cost, p.Status, p.Flags = math.Float64frombits(take()), int32(take()), uint16(take())
		p.Tries = int16(take())
		p.Masking = int32(take())
		p.UpBytes, p.DownBytes, p.FilesSaved = int32(take()), int32(take()), int32(take())
		p.ImageFiles = int16(take())
		p.Credits = 0
		if p.Flags&rowCredits != 0 {
			p.Credits = math.Float64frombits(take())
		}
	}
	if !valid || len(data) != 0 {
		return nil
	}
	return next
}
