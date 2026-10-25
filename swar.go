package ecmaregex

// The searches the Rust crate takes from memchr, for what the Go runtime has no search of: one byte is bytes.IndexByte,
// which the runtime runs as vector code, and two to four bytes, a range of bytes, and units of UTF-16 text are SWAR:
// eight bytes of a uint64 read at a time and tested together. A lane test is exact (no lane is flagged that does not
// pass), so the first flagged lane is the first unit that passes.

import (
	"bytes"
	"encoding/binary"
	"math/bits"
)

const (
	ones  = 0x0101010101010101
	low7  = 0x7F7F7F7F7F7F7F7F
	highs = 0x8080808080808080
)

// word returns the eight bytes at at, the first in the low byte.
func word(h []byte, at int) uint64 { return binary.LittleEndian.Uint64(h[at : at+8]) }

// broadcast returns a byte in every lane.
func broadcast(b byte) uint64 { return uint64(b) * ones }

// zeros returns the high bit of each lane of x that is zero.
func zeros(x uint64) uint64 { return ^(((x & low7) + low7) | x | low7) }

// eq returns the high bit of each lane of x that equals the broadcast byte v.
func eq(x, v uint64) uint64 { return zeros(x ^ v) }

// lane returns the lane of the lowest flagged high bit.
func lane(flags uint64) int { return bits.TrailingZeros64(flags) >> 3 }

// hasZero is the cheap test of whether some lane of x is zero: it flags every zero lane but may also flag a lane of 1
// above a zero lane. The loops use it to know that a block holds a byte they look for, and zeros to find which; a
// finder that tests candidates can take its flags as they are.
func hasZero(x uint64) uint64 { return (x - ones) &^ x & highs }

// indexByte returns the first position at or after from and before n whose byte is b, or -1.
func indexByte(h []byte, from, n int, b byte) int {
	if from >= n {
		return -1
	}
	if k := bytes.IndexByte(h[from:n], b); k >= 0 {
		return from + k
	}
	return -1
}

// indexByte2 returns the first position at or after from and before n whose byte is a or b, or -1.
func indexByte2(h []byte, from, n int, a, b byte) int {
	va, vb := broadcast(a), broadcast(b)
	at := from
	for ; at <= n-16; at += 16 {
		w0, w1 := word(h, at), word(h, at+8)
		if hasZero(w0^va)|hasZero(w0^vb)|hasZero(w1^va)|hasZero(w1^vb) != 0 {
			if z := eq(w0, va) | eq(w0, vb); z != 0 {
				return at + lane(z)
			}
			return at + 8 + lane(eq(w1, va)|eq(w1, vb))
		}
	}
	for ; at <= n-8; at += 8 {
		w := word(h, at)
		if x := eq(w, va) | eq(w, vb); x != 0 {
			return at + lane(x)
		}
	}
	for ; at < n; at++ {
		if h[at] == a || h[at] == b {
			return at
		}
	}
	return -1
}

// indexByte4 returns the first position at or after from and before n whose byte is one of four (which may repeat),
// or -1.
func indexByte4(h []byte, from, n int, a, b, c, d byte) int {
	va, vb, vc, vd := broadcast(a), broadcast(b), broadcast(c), broadcast(d)
	at := from
	for ; at <= n-8; at += 8 {
		w := word(h, at)
		if hasZero(w^va)|hasZero(w^vb)|hasZero(w^vc)|hasZero(w^vd) != 0 {
			return at + lane(eq(w, va)|eq(w, vb)|eq(w, vc)|eq(w, vd))
		}
	}
	for ; at < n; at++ {
		if x := h[at]; x == a || x == b || x == c || x == d {
			return at
		}
	}
	return -1
}

// between returns the high bit of each lane of x that is an ASCII byte from lo to hi (both below 0x80), given the
// constants of betweenLo and betweenHi: exact, since the seven low bits of a lane plus a constant below 0x80 cannot
// carry into the next lane.
func between(x, lo, hi uint64) uint64 {
	t := x & low7
	return (t + lo) &^ (t + hi) &^ x & highs
}

func betweenLo(lo byte) uint64 { return broadcast(0x80 - lo) }
func betweenHi(hi byte) uint64 { return broadcast(0x7F - hi) }

// indexBetween returns the first position at or after from and before n whose byte is from lo to hi (both below
// 0x80), or -1.
func indexBetween(h []byte, from, n int, lo, hi byte) int {
	vl, vh := betweenLo(lo), betweenHi(hi)
	at := from
	for ; at <= n-16; at += 16 {
		z0, z1 := between(word(h, at), vl, vh), between(word(h, at+8), vl, vh)
		if z0|z1 != 0 {
			if z0 != 0 {
				return at + lane(z0)
			}
			return at + 8 + lane(z1)
		}
	}
	for ; at <= n-8; at += 8 {
		if z := between(word(h, at), vl, vh); z != 0 {
			return at + lane(z)
		}
	}
	for ; at < n; at++ {
		if x := h[at]; x >= lo && x <= hi {
			return at
		}
	}
	return -1
}

// startsWith reports whether the bytes of literal are at at of h, before end.
func startsWith(h []byte, at, end int, literal []byte) bool {
	return end-at >= len(literal) && string(h[at:at+len(literal)]) == string(literal)
}

// The same over UTF-16: four units of a uint64.

const (
	ones16  = 0x0001000100010001
	low15   = 0x7FFF7FFF7FFF7FFF
	highs16 = 0x8000800080008000
)

// word16 returns the four units at at, the first in the low unit. The compiler joins the four loads into one where
// the machine can load a word from any address.
func word16(u []uint16, at int) uint64 {
	w := u[at : at+4 : at+4]
	return uint64(w[0]) | uint64(w[1])<<16 | uint64(w[2])<<32 | uint64(w[3])<<48
}

func zeros16(x uint64) uint64 { return ^(((x & low15) + low15) | x | low15) }

func hasZero16(x uint64) uint64 { return (x - ones16) &^ x & highs16 }

// indexUnit returns the first position at or after from and before n whose unit is v, or -1.
func indexUnit(u []uint16, from, n int, v uint16) int {
	vv := uint64(v) * ones16
	at := from
	for ; at <= n-16; at += 16 {
		w0, w1, w2, w3 := word16(u, at)^vv, word16(u, at+4)^vv, word16(u, at+8)^vv, word16(u, at+12)^vv
		if hasZero16(w0)|hasZero16(w1)|hasZero16(w2)|hasZero16(w3) != 0 {
			for k, w := range [4]uint64{w0, w1, w2, w3} {
				if z := zeros16(w); z != 0 {
					return at + 4*k + bits.TrailingZeros64(z)>>4
				}
			}
		}
	}
	for ; at <= n-4; at += 4 {
		if z := zeros16(word16(u, at) ^ vv); z != 0 {
			return at + bits.TrailingZeros64(z)>>4
		}
	}
	for ; at < n; at++ {
		if u[at] == v {
			return at
		}
	}
	return -1
}

// hasUnits16 reports whether the units of literal are at at of u.
func hasUnits16(u []uint16, at int, literal []uint16) bool {
	if len(u)-at < len(literal) || at < 0 {
		return false
	}
	for k, x := range literal {
		if u[at+k] != x {
			return false
		}
	}
	return true
}
