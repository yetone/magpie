package usage

// Cache snapshots encode the fixed request fields without reflection. The
// encoding is private to this process: no on-disk format or migration. Time
// locations are retained separately so a round trip preserves displayed times.
import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"math"
	"sync"
	"time"
	"unsafe"
)

var rowCompressors = sync.Pool{New: func() any {
	w, _ := flate.NewWriter(io.Discard, flate.BestSpeed)
	return w
}}

func (c *rowChunk) pack() *rowChunk {
	if c == nil || len(c.Rows) < 64 || len(c.Archive) != 0 {
		return c
	}
	data := make([]byte, 0, len(c.Rows)*64)
	put := func(v uint64) { data = binary.AppendUvarint(data, v) }
	put(uint64(len(c.Rows)))
	put(uint64(len(c.Strings)))
	for _, s := range c.Strings {
		put(uint64(len(s)))
		data = append(data, s...)
	}
	var locations []*time.Location
	locs := map[*time.Location]uint64{}
	for _, p := range c.Rows {
		loc := p.Time.Location()
		id, ok := locs[loc]
		if !ok {
			id = uint64(len(locations))
			locs[loc] = id
			locations = append(locations, loc)
		}
		put(uint64(p.Time.Unix()))
		put(uint64(p.Time.Nanosecond()))
		put(id)
		for _, n := range p.Text {
			put(uint64(n))
		}
		for _, n := range p.Tokens {
			put(uint64(n))
		}
		for _, n := range []int64{p.Millis, p.TTFT, p.FirstText, p.Order, p.RouteID, p.Sent} {
			put(uint64(n))
		}
		put(math.Float64bits(p.Cost))
		put(uint64(p.Status))
		put(uint64(p.Flags))
	}
	var b bytes.Buffer
	w := rowCompressors.Get().(*flate.Writer)
	w.Reset(&b)
	_, err := w.Write(data)
	closeErr := w.Close()
	w.Reset(io.Discard)
	rowCompressors.Put(w)
	if err != nil || closeErr != nil || int64(b.Len()) >= c.Bytes {
		return c
	}
	next := *c
	next.Rows, next.Strings, next.dict = nil, nil, nil
	next.Archive, next.Locations = b.Bytes(), locations
	next.Bytes = int64(len(next.Archive) + len(locations)*8)
	return &next
}

func (c *rowChunk) unpack() *rowChunk {
	if c == nil || len(c.Archive) == 0 {
		return c
	}
	r := flate.NewReader(bytes.NewReader(c.Archive))
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return nil
	}
	valid := true
	take := func() uint64 {
		n, k := binary.Uvarint(data)
		if k <= 0 {
			valid = false
			return 0
		}
		data = data[k:]
		return n
	}
	rows, strs := take(), take()
	if rows > uint64(len(data)) || strs > uint64(len(data)) {
		return nil
	}
	next := *c
	next.Archive, next.Locations = nil, nil
	next.Rows = make([]packedRow, int(rows))
	next.Strings = make([]string, int(strs))
	next.Bytes = int64(len(next.Rows)) * int64(unsafe.Sizeof(packedRow{}))
	for i := range next.Strings {
		n := take()
		if n > uint64(len(data)) {
			return nil
		}
		next.Strings[i] = string(data[:n])
		data = data[n:]
		next.Bytes += int64(n) + 16
	}
	for i := range next.Rows {
		p := &next.Rows[i]
		sec, nano, loc := int64(take()), int64(take()), take()
		if loc >= uint64(len(c.Locations)) {
			return nil
		}
		p.Time = time.Unix(sec, nano).In(c.Locations[loc])
		for j := range p.Text {
			n := take()
			if n >= strs {
				return nil
			}
			p.Text[j] = uint32(n)
		}
		for j := range p.Tokens {
			p.Tokens[j] = int64(take())
		}
		p.Millis, p.TTFT, p.FirstText, p.Order = int64(take()), int64(take()), int64(take()), int64(take())
		p.RouteID = int64(take())
		p.Sent = int64(take())
		p.Cost, p.Status, p.Flags = math.Float64frombits(take()), int32(take()), uint8(take())
	}
	if !valid || len(data) != 0 {
		return nil
	}
	return &next
}
