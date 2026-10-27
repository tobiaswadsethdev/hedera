package session

// Ring is a fixed-capacity scrollback buffer addressed by absolute stream
// offsets. Offsets count every byte ever written; the ring keeps the most
// recent Cap() bytes, i.e. offsets [Base(), Head()).
//
// Ring is not safe for concurrent use.
type Ring struct {
	buf  []byte
	head uint64
}

func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]byte, capacity)}
}

func (r *Ring) Cap() int { return len(r.buf) }

// Head is the offset of the next byte to be written.
func (r *Ring) Head() uint64 { return r.head }

// Base is the offset of the oldest byte still held.
func (r *Ring) Base() uint64 {
	if c := uint64(len(r.buf)); r.head > c {
		return r.head - c
	}
	return 0
}

// Write appends p and returns the offset of its first byte.
func (r *Ring) Write(p []byte) uint64 {
	start := r.head
	c := len(r.buf)
	src := p
	if len(src) > c {
		src = src[len(src)-c:]
	}
	pos := int((r.head + uint64(len(p)-len(src))) % uint64(c))
	n := copy(r.buf[pos:], src)
	copy(r.buf, src[n:])
	r.head += uint64(len(p))
	return start
}

// ReadFrom returns a copy of the bytes in [off, Head()). off is clamped into
// [Base(), Head()].
func (r *Ring) ReadFrom(off uint64) []byte {
	if b := r.Base(); off < b {
		off = b
	}
	if off > r.head {
		off = r.head
	}
	n := int(r.head - off)
	out := make([]byte, n)
	if n == 0 {
		return out
	}
	c := uint64(len(r.buf))
	pos := int(off % c)
	k := copy(out, r.buf[pos:])
	copy(out[k:], r.buf[:n-k])
	return out
}

// utf8Align returns how many leading bytes of b are UTF-8 continuation bytes
// (at most 3), so a replay that starts mid-character can skip them.
func utf8Align(b []byte) int {
	i := 0
	for i < len(b) && i < 3 && b[i]&0xC0 == 0x80 {
		i++
	}
	return i
}
