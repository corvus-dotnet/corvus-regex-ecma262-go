package ecmaregex

import (
	"bytes"
	"slices"
)

// Reading UTF-8 and UTF-16, and matching by UTF-16 code unit over UTF-8 text (units.rs).
//
// ECMA-262 matches a pattern that has no u flag a code unit at a time. A character of one, two or three bytes of
// UTF-8 is one code unit. A character of four bytes is two, a high surrogate and a low one, so the matcher has to be
// able to stand between them. That position is written as the offset of the character's third byte. Each half then
// spans two bytes, every position is still an offset into the text, a group is still a pair of offsets, and no UTF-16
// copy of the text is made.
//
// The third byte of a character is a continuation byte, which is no position at all when matching by code point, so
// the two readings cannot be confused. Only a text with a character of four bytes differs between the two, and the
// instructions that compare bytes (a literal run, a word boundary, a line terminator) need no change: none of them can
// hold at the position between the halves, and all of them are right at every other.
//
// The matcher is one template expanded for each encoding (see the generate directive in regex.go), so each operation
// the Rust crate's Text trait has is a pair of functions here: the one ending in 8 reads UTF-8 bytes, and the one
// ending in 16 reads UTF-16 code units, which may hold lone surrogates. A position is an offset in the units of the
// text.

const (
	// runeError is what a byte that is not UTF-8 decodes to, one byte at a time. The public API refuses text that is
	// not UTF-8, so the matcher only meets it in a text the caller vouched for.
	runeError rune = 0xFFFD
	// runeSelf is the first byte value that is not a character by itself.
	runeSelf byte = 0x80
)

func continuation(b byte) bool {
	return b&0xC0 == 0x80
}

// decodeRune returns the code point at the start of p and its width in bytes: (runeError, 1) for bytes that are not
// UTF-8, and (runeError, 0) for no bytes (utf8.DecodeRune).
func decodeRune(p []byte) (rune, int) {
	if len(p) == 0 {
		return runeError, 0
	}
	p0 := p[0]
	if p0 < runeSelf {
		return rune(p0), 1
	}
	switch {
	case 0xC2 <= p0 && p0 <= 0xDF:
		if len(p) >= 2 && continuation(p[1]) {
			return rune(p0&0x1F)<<6 | rune(p[1]&0x3F), 2
		}
	case 0xE0 <= p0 && p0 <= 0xEF:
		if len(p) >= 3 && continuation(p[2]) {
			lo, hi := byte(0x80), byte(0xBF)
			if p0 == 0xE0 {
				lo = 0xA0
			} else if p0 == 0xED {
				hi = 0x9F
			}
			if lo <= p[1] && p[1] <= hi {
				return rune(p0&0x0F)<<12 | rune(p[1]&0x3F)<<6 | rune(p[2]&0x3F), 3
			}
		}
	case 0xF0 <= p0 && p0 <= 0xF4:
		if len(p) >= 4 && continuation(p[2]) && continuation(p[3]) {
			lo, hi := byte(0x80), byte(0xBF)
			if p0 == 0xF0 {
				lo = 0x90
			} else if p0 == 0xF4 {
				hi = 0x8F
			}
			if lo <= p[1] && p[1] <= hi {
				return rune(p0&0x07)<<18 | rune(p[1]&0x3F)<<12 | rune(p[2]&0x3F)<<6 | rune(p[3]&0x3F), 4
			}
		}
	}
	return runeError, 1
}

// decodeLastRune returns the code point at the end of p and its width in bytes (utf8.DecodeLastRune).
func decodeLastRune(p []byte) (rune, int) {
	end := len(p)
	if end == 0 {
		return runeError, 0
	}
	last := p[end-1]
	if last < runeSelf {
		return rune(last), 1
	}
	// A character is four bytes at most, so its first byte is one of the last four.
	lim := max(end-4, 0)
	start := end - 1
	for start > lim {
		start--
		if !continuation(p[start]) {
			break
		}
	}
	r, size := decodeRune(p[start:end])
	if start+size != end {
		return runeError, 1
	}
	return r, size
}

// highSurrogate and lowSurrogate are the two UTF-16 code units of a code point beyond the Basic Multilingual Plane.
func highSurrogate(c rune) rune { return 0xD800 + ((c - 0x10000) >> 10) }
func lowSurrogate(c rune) rune  { return 0xDC00 + ((c - 0x10000) & 0x3FF) }

// unitAt8 returns the UTF-16 code unit at the position of UTF-8 text and the number of bytes it spans.
func unitAt8(input []byte, pos int) (rune, int) {
	c := input[pos]
	if c >= 0xF0 {
		r, w := decodeRune(input[pos:])
		if w == 4 {
			return highSurrogate(r), 2
		}
		return runeError, 1
	}
	if 0x80 <= c && c < 0xC0 && pos >= 2 && input[pos-2] >= 0xF0 {
		// Between the halves of the character that starts two bytes back.
		r, w := decodeRune(input[pos-2:])
		if w == 4 {
			return lowSurrogate(r), 2
		}
	}
	return decodeRune(input[pos:])
}

// unitBefore8 returns the UTF-16 code unit that ends at the position of UTF-8 text and the number of bytes it spans.
func unitBefore8(input []byte, pos int) (rune, int) {
	r, w := decodeLastRune(input[:pos])
	if w == 4 {
		return lowSurrogate(r), 2
	}
	if w == 1 && r == runeError && pos >= 2 && len(input)-pos >= 2 && input[pos-2] >= 0xF0 {
		// Between the halves of the character that starts two bytes back.
		r2, w2 := decodeRune(input[pos-2:])
		if w2 == 4 {
			return highSurrogate(r2), 2
		}
	}
	return r, w
}

// decode8 returns the code point at the position and its width in bytes.
func decode8(input []byte, pos int) (rune, int) { return decodeRune(input[pos:]) }

// decodeLast8 returns the code point that ends at the position and its width.
func decodeLast8(input []byte, pos int) (rune, int) { return decodeLastRune(input[:pos]) }

// read8 returns the character at the position: a code unit with units set, and a code point otherwise.
func read8(input []byte, pos int, units bool) (rune, int) {
	if units {
		return unitAt8(input, pos)
	}
	return decodeRune(input[pos:])
}

// readLast8 is read8 for the character that ends at the position.
func readLast8(input []byte, pos int, units bool) (rune, int) {
	if units {
		return unitBefore8(input, pos)
	}
	return decodeLastRune(input[:pos])
}

// giveBack8 returns the width of the last character of input[from:pos], read as it was read going forward from from.
func giveBack8(input []byte, from, pos int, units bool) int {
	if input[pos-1] < runeSelf {
		return 1
	}
	if units {
		_, w := unitBefore8(input, pos)
		return w
	}
	_, w := decodeLastRune(input[from:pos])
	return w
}

func lineTerminatorAt8(input []byte, pos int) bool {
	switch input[pos] {
	case '\n', '\r':
		return true
	case 0xE2:
		return pos+2 < len(input) && input[pos+1] == 0x80 && (input[pos+2] == 0xA8 || input[pos+2] == 0xA9)
	}
	return false
}

// lineTerminatorBefore8 reports whether a line terminator ends at the position. U+2028 and U+2029 are E2 80 A8 and
// E2 80 A9.
func lineTerminatorBefore8(input []byte, pos int) bool {
	switch input[pos-1] {
	case '\n', '\r':
		return true
	case 0xA8, 0xA9:
		return pos >= 3 && input[pos-3] == 0xE2 && input[pos-2] == 0x80
	}
	return false
}

// whole8 reports whether the units input[from:to], compared equal to a group's, are the same code points. Over UTF-8
// they always are.
func whole8(input []byte, from, to int) bool { return true }

// pushLossy8 appends input[from:to] to out. A bound between the halves of a character (which only a pattern without
// the u flag can make) cannot be written as UTF-8, and the half character is written as U+FFFD, as a lossy conversion
// of the UTF-16 text would write it; so the result is UTF-8 when the input is.
func pushLossy8(out []byte, input []byte, from, to int) []byte {
	if from >= to {
		return out
	}
	if insideFour(input, from) {
		out = append(out, "�"...)
		from += 2
	}
	tail := to < len(input) && insideFour(input, to)
	if tail {
		to -= 2
	}
	if from < to {
		out = append(out, input[from:to]...)
	}
	if tail {
		out = append(out, "�"...)
	}
	return out
}

// normalize8 returns the position a search from start begins at. With the u flag a position inside a character means
// the character (RegExpBuiltinExec reads the text as code points); without it every code unit is a position, which
// over UTF-8 is a character's first byte or the third byte of a character of four.
func normalize8(input []byte, start int, unicode bool) int {
	if start == 0 || start >= len(input) || !continuation(input[start]) {
		return start
	}
	lead := start
	for lead > 0 && continuation(input[lead]) {
		lead--
	}
	if !unicode && input[lead] >= 0xF0 && start == lead+2 {
		return start
	}
	return lead
}

// advance8 returns the position after the character at pos (AdvanceStringIndex): a code point with the u flag and a
// code unit without it.
func advance8(input []byte, pos int, unicode bool) int {
	if pos >= len(input) {
		return pos + 1
	}
	if unicode {
		_, w := decodeRune(input[pos:])
		return pos + w
	}
	_, w := unitAt8(input, pos)
	return pos + w
}

// insideFour reports whether the position is the one between the halves of a character of four bytes: its third
// byte.
func insideFour(input []byte, pos int) bool {
	if pos < 2 || pos >= len(input) || !continuation(input[pos]) || input[pos-2] < 0xF0 {
		return false
	}
	_, w := decodeRune(input[pos-2:])
	return w == 4
}

func isHigh(u uint16) bool { return 0xD800 <= u && u <= 0xDBFF }
func isLow(u uint16) bool  { return 0xDC00 <= u && u <= 0xDFFF }

// decode16 returns the code point at the position and its width in units: with the u flag a surrogate pair is one
// code point and a lone surrogate is a code point of its own.
func decode16(input []uint16, pos int) (rune, int) {
	u := input[pos]
	if isHigh(u) && pos+1 < len(input) && isLow(input[pos+1]) {
		return 0x10000 + (rune(u)-0xD800)<<10 + (rune(input[pos+1]) - 0xDC00), 2
	}
	return rune(u), 1
}

// decodeLast16 returns the code point that ends at the position, and its width.
func decodeLast16(input []uint16, pos int) (rune, int) {
	u := input[pos-1]
	if isLow(u) && pos >= 2 && isHigh(input[pos-2]) {
		return 0x10000 + (rune(input[pos-2])-0xD800)<<10 + (rune(u) - 0xDC00), 2
	}
	return rune(u), 1
}

func unitAt16(input []uint16, pos int) (rune, int)     { return rune(input[pos]), 1 }
func unitBefore16(input []uint16, pos int) (rune, int) { return rune(input[pos-1]), 1 }

func read16(input []uint16, pos int, units bool) (rune, int) {
	if units {
		return rune(input[pos]), 1
	}
	return decode16(input, pos)
}

func readLast16(input []uint16, pos int, units bool) (rune, int) {
	if units {
		return rune(input[pos-1]), 1
	}
	return decodeLast16(input, pos)
}

func giveBack16(input []uint16, from, pos int, units bool) int {
	if !units && isLow(input[pos-1]) && pos >= from+2 && isHigh(input[pos-2]) {
		return 2
	}
	return 1
}

func lineTerminatorAt16(input []uint16, pos int) bool {
	switch input[pos] {
	case 0x0A, 0x0D, 0x2028, 0x2029:
		return true
	}
	return false
}

func lineTerminatorBefore16(input []uint16, pos int) bool {
	switch input[pos-1] {
	case 0x0A, 0x0D, 0x2028, 0x2029:
		return true
	}
	return false
}

// whole16 reports whether the units input[from:to], compared equal to a group's, are the same code points: false only
// when the comparison ends inside a surrogate pair of the text, which the u flag reads as one code point.
func whole16(input []uint16, from, to int) bool {
	startsInside := from > 0 && from < len(input) && isLow(input[from]) && isHigh(input[from-1])
	endsInside := to > from && to < len(input) && isHigh(input[to-1]) && isLow(input[to])
	return !(startsInside || endsInside)
}

func pushLossy16(out []uint16, input []uint16, from, to int) []uint16 {
	if from < to {
		out = append(out, input[from:to]...)
	}
	return out
}

func normalize16(input []uint16, start int, unicode bool) int {
	if unicode && start > 0 && start < len(input) && isLow(input[start]) && isHigh(input[start-1]) {
		return start - 1
	}
	return start
}

func advance16(input []uint16, pos int, unicode bool) int {
	if pos >= len(input) {
		return pos + 1
	}
	if unicode {
		_, w := decode16(input, pos)
		return pos + w
	}
	return pos + 1
}

// isWordUnit reports whether a code unit is one of the 63 ASCII word characters.
func isWordUnit(c uint32) bool {
	return c < 0x80 && isWordByte(byte(c))
}

// equal8 and equal16 report whether two runs of units are the same.
func equal8(a, b []byte) bool    { return string(a) == string(b) }
func equal16(a, b []uint16) bool { return slices.Equal(a, b) }

// lastOfBack8 returns the position of the last of the ASCII bytes back in input[from:to], or from when there is none:
// the give-back of a greedy single character loop (see setBack), which finds where what follows it can start.
func lastOfBack8(input []byte, from, to int, back []byte) int {
	run := input[from:to]
	var found int
	switch len(back) {
	case 1:
		found = bytes.LastIndexByte(run, back[0])
	default:
		found = bytes.LastIndexAny(run, string(back))
	}
	return from + max(found, 0)
}

// lastOfBack16 is never taken: the give-back search is over UTF-8 alone.
func lastOfBack16(input []uint16, from, to int, back []byte) int { return to }
