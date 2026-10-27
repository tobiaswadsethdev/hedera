package session

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestRingBasic(t *testing.T) {
	r := NewRing(8)
	if r.Write([]byte("abc")) != 0 || r.Write([]byte("def")) != 3 {
		t.Fatal("bad start offsets")
	}
	if r.Base() != 0 || r.Head() != 6 {
		t.Fatalf("base %d head %d", r.Base(), r.Head())
	}
	if got := r.ReadFrom(2); string(got) != "cdef" {
		t.Fatalf("got %q", got)
	}
}

func TestRingWrap(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("abcdef"))
	r.Write([]byte("ghij")) // wraps: holds "cdefghij"
	if r.Base() != 2 || r.Head() != 10 {
		t.Fatalf("base %d head %d", r.Base(), r.Head())
	}
	if got := r.ReadFrom(0); string(got) != "cdefghij" {
		t.Fatalf("clamped read got %q", got)
	}
	if got := r.ReadFrom(7); string(got) != "hij" {
		t.Fatalf("got %q", got)
	}
	if got := r.ReadFrom(10); len(got) != 0 {
		t.Fatalf("read at head got %q", got)
	}
	if got := r.ReadFrom(99); len(got) != 0 {
		t.Fatalf("read past head got %q", got)
	}
}

func TestRingOversizeWrite(t *testing.T) {
	r := NewRing(4)
	r.Write([]byte("xy"))
	if start := r.Write([]byte("0123456789")); start != 2 {
		t.Fatalf("start %d", start)
	}
	if r.Head() != 12 || r.Base() != 8 {
		t.Fatalf("base %d head %d", r.Base(), r.Head())
	}
	if got := r.ReadFrom(0); string(got) != "6789" {
		t.Fatalf("got %q", got)
	}
}

// TestRingMatchesReference checks random writes and reads against a plain
// slice holding the whole stream.
func TestRingMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, capacity := range []int{1, 7, 64, 1000} {
		r := NewRing(capacity)
		var all []byte
		for i := 0; i < 500; i++ {
			chunk := make([]byte, rng.Intn(2*capacity+3))
			rng.Read(chunk)
			if start := r.Write(chunk); start != uint64(len(all)) {
				t.Fatalf("cap %d: start %d want %d", capacity, start, len(all))
			}
			all = append(all, chunk...)
			off := uint64(rng.Intn(len(all) + 2))
			got := r.ReadFrom(off)
			lo := max(off, r.Base())
			lo = min(lo, uint64(len(all)))
			if want := all[lo:]; !bytes.Equal(got, want) {
				t.Fatalf("cap %d: ReadFrom(%d) mismatch", capacity, off)
			}
		}
	}
}

func TestUTF8Align(t *testing.T) {
	check := "✓" // e2 9c 93
	cases := []struct {
		in   []byte
		want int
	}{
		{[]byte("abc"), 0},
		{[]byte(check), 0},
		{[]byte(check)[1:], 2},
		{[]byte(check)[2:], 1},
		{[]byte{0x80, 0x80, 0x80, 0x80}, 3},
		{nil, 0},
	}
	for _, c := range cases {
		if got := utf8Align(c.in); got != c.want {
			t.Errorf("utf8Align(%x) = %d, want %d", c.in, got, c.want)
		}
	}
}
