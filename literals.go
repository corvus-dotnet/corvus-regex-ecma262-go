package ecmaregex

// The literals of a search, over UTF-8 and over UTF-16 (Literals of search.rs): a search for the leftmost occurrence
// of any of them, the first in the pattern's order of those that start there. And the sets of UTF-16 code units the
// searches look for (simd16.rs, which the Java engine has as UnitSearch).

import (
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	// packedLongest is the most units of a literal the packed search takes, so that the chunks' overlap stays short.
	packedLongest = 64
	// indexAlone is the most share of text (in parts per ten thousand, share16) a unit may have for the UTF-16 search
	// to look for it alone.
	indexAlone = 40
	// packChunk is the units packed into bytes at a time for the packed search, and firstPack the first chunk packed,
	// so that a search whose occurrence is near packs little.
	packChunk = 4096
	firstPack = 256
)

type literals struct {
	// utf8 is the search over UTF-8, and single8 the one literal over UTF-8 when there is one.
	utf8    byteFinder
	single8 []byte
	// firsts holds the bytes the UTF-8 literals start with.
	firsts [256]bool
	// longest is the most code units of a literal.
	longest int
	// all16 is every literal over UTF-16, in the pattern's order.
	all16 [][]uint16
	// one16 is one literal over UTF-16 with a rare unit, oneUnit, at oneAt in it: the search for that unit, then a
	// compare of the literal. Nil otherwise.
	one16   []uint16
	oneUnit uint16
	oneAt   int
	// shared16 is the rare unit up to four literals over UTF-16 all start with, when hasShared16.
	shared16    uint16
	hasShared16 bool
	// packed16 is the search of the low bytes of several literals over UTF-16 (see findPacked).
	packed16 byteFinder
}

// newLiterals returns the search for the literals, each a sequence of code points, with the most bytes of each written
// out over UTF-8 (see utf8Cuts).
func newLiterals(lits [][]rune, cuts []int) *literals {
	// A literal cut to fewer bytes finds the same starts, and one that an earlier literal begins with finds no start
	// the earlier one does not.
	l := &literals{}
	var u8 [][]byte
	for k, r := range lits {
		units := utf16.Encode(r)
		l.all16 = append(l.all16, units)
		l.longest = max(l.longest, len(units))
		b := []byte(string(r))
		// A cut inside a character is not made: measured in the Rust crate, the Cyrillic letters with a three-byte
		// form cut so made the search 1.2 times slower.
		if cut := cuts[k]; cut < len(b) && utf8.RuneStart(b[cut]) {
			b = b[:cut]
		}
		covered := false
		for _, e := range u8 {
			covered = covered || len(b) >= len(e) && string(b[:len(e)]) == string(e)
		}
		if !covered {
			u8 = append(u8, b)
		}
	}
	for _, b := range u8 {
		l.firsts[b[0]] = true
	}
	l.utf8 = newByteFinder(u8, &byteRank)
	if len(u8) == 1 {
		l.single8 = u8[0]
	}
	if len(l.all16) == 1 {
		s, at := l.all16[0], -1
		for k, u := range s {
			if !isSurrogate(u) && (at < 0 || share16(u) < share16(s[at])) {
				at = k
			}
		}
		if at >= 0 && share16(s[at]) <= indexAlone {
			l.one16, l.oneAt, l.oneUnit = s, at, s[at]
		}
	}
	first := l.all16[0][0]
	shared := len(l.all16) > 1 && len(l.all16) <= 4 && !isSurrogate(first) && share16(first) <= indexAlone
	for _, s := range l.all16 {
		shared = shared && s[0] == first
	}
	l.hasShared16, l.shared16 = shared, first
	if l.longest < packedLongest {
		// The low byte of each unit, as the packed text holds it.
		var low [][]byte
		for _, s := range l.all16 {
			b := make([]byte, len(s))
			for i, u := range s {
				b[i] = byte(u)
			}
			if !slices.ContainsFunc(low, func(e []byte) bool { return string(e) == string(b) }) {
				low = append(low, b)
			}
		}
		l.packed16 = newByteFinder(low, packedRanks(l.all16))
	}
	return l
}

func isSurrogate(u uint16) bool { return 0xD800 <= u && u <= 0xDFFF }

// find8 returns the leftmost occurrence at or after from in UTF-8 text, as its start and end, or -1.
func (l *literals) find8(h []byte, from int) (int, int) {
	if from > len(h) {
		return -1, -1
	}
	return l.utf8.find(h, from, len(h))
}

// find16 returns the leftmost occurrence at or after from in UTF-16 text, as its start and end, or -1.
func (l *literals) find16(s []uint16, from int) (int, int) {
	from = max(from, 0)
	if l.one16 != nil {
		for at := from + l.oneAt; ; {
			p := indexUnit(s, at, len(s), l.oneUnit)
			if p < 0 {
				return -1, -1
			}
			if start := p - l.oneAt; hasUnits16(s, start, l.one16) {
				return start, start + len(l.one16)
			}
			at = p + 1
		}
	}
	if l.hasShared16 {
		for at := from; ; {
			p := indexUnit(s, at, len(s), l.shared16)
			if p < 0 {
				return -1, -1
			}
			for _, lit := range l.all16 {
				if hasUnits16(s, p, lit) {
					return p, p + len(lit)
				}
			}
			at = p + 1
		}
	}
	if l.packed16 != nil {
		return l.findPacked(s, from)
	}
	for p := from; p < len(s); p++ {
		if e := l.literalAt16(s, p); e >= 0 {
			return p, e
		}
	}
	return -1, -1
}

// literalAt16 returns where the first literal in order that starts at p of UTF-16 text ends, or -1.
func (l *literals) literalAt16(s []uint16, p int) int {
	c := s[p]
	for _, lit := range l.all16 {
		if lit[0] == c && hasUnits16(s, p, lit) {
			return p + len(lit)
		}
	}
	return -1
}

// findPacked returns the leftmost occurrence at or after from of one of the literals (in the pattern's order, the
// first of those that start there): the low bytes of a chunk of units packed into bytes and searched for the low bytes
// of the literals, each candidate then compared with the literals themselves (a unit beyond ASCII packs to a byte an
// ASCII unit has too). The .NET reference engine's packed search, which the crate makes with the ASCII units alone; the
// low byte keeps the search one for every literal. An occurrence that starts so near the end of a chunk that a literal
// could start before it and end beyond the chunk is looked for again in the next chunk, which starts that far back.
// The first chunk is small, and each after it is the most, so a search whose occurrence is near packs little more
// than it reads.
func (l *literals) findPacked(s []uint16, from int) (int, int) {
	units := len(s)
	if from > units {
		return -1, -1
	}
	overlap := max(l.longest-1, 0)
	var buf [packChunk]byte
	at, end := from, min(from+firstPack, units)
	for {
		n := end - at
		for i, u := range s[at:end] {
			buf[i] = byte(u)
		}
		last := end == units
		limit := n
		if !last {
			limit = n - overlap
		}
		for k := 0; ; {
			c, _ := l.packed16.find(buf[:n], k, n)
			if c < 0 || c >= limit {
				break
			}
			start := at + c
			for _, lit := range l.all16 {
				if hasUnits16(s, start, lit) {
					return start, start + len(lit)
				}
			}
			k = c + 1
		}
		if last {
			return -1, -1
		}
		at = end - overlap
		end = min(at+packChunk, units)
	}
}

// findWithin8 returns the leftmost occurrence that starts at or after from and at or before last, in UTF-8 text, or
// -1. The few bytes between are looked at one at a time for a literal's first byte before a search is called, since a
// call on so short a text costs more than reading it.
func (l *literals) findWithin8(h []byte, from, last int) int {
	last = min(last, len(h)-1)
	if from > last {
		return -1
	}
	if l.single8 != nil {
		for p := from; p <= last; p++ {
			if h[p] == l.single8[0] && startsWith(h, p, len(h), l.single8) {
				return p
			}
		}
		return -1
	}
	p := from
	for p <= last && !l.firsts[h[p]] {
		p++
	}
	if p > last {
		return -1
	}
	reach := min(last+3*l.longest, len(h))
	if q, _ := l.utf8.find(h, p, reach); q >= 0 && q <= last {
		return q
	}
	return -1
}

// findWithin16 is findWithin8 in UTF-16 text: each start between is looked at, as they are few.
func (l *literals) findWithin16(s []uint16, from, last int) int {
	last = min(last, len(s)-1)
	for p := from; p <= last; p++ {
		if l.literalAt16(s, p) >= 0 {
			return p
		}
	}
	return -1
}

// unitSet is a set of code units as ranges (inclusive pairs), for the units a match can start with and for the units
// that end a run of a loop: one unit by the search for it, one range by a compare, and any other set by a table of bits
// a unit at a time: a block of 256 bits for each high byte of a unit, the blocks the set has nothing in shared, so that
// a test is two loads and a shift.
type unitSet struct {
	ranges []int
	// one is the one unit of a set of one, or -1.
	one int
	// blocks are the blocks of bits by high byte, for a set of more than one range; nil otherwise.
	blocks *[256]*[4]uint64
}

var noUnits = &[4]uint64{}

func newUnitSet(ranges []int) *unitSet {
	s := &unitSet{ranges: ranges, one: -1}
	if len(ranges) == 2 && ranges[0] == ranges[1] {
		s.one = ranges[0]
	}
	if len(ranges) > 2 {
		var b [256]*[4]uint64
		for k := range b {
			b[k] = noUnits
		}
		for k := 0; k < len(ranges); k += 2 {
			for u := ranges[k]; u <= min(ranges[k+1], 0xFFFF); u++ {
				if b[u>>8] == noUnits {
					b[u>>8] = &[4]uint64{}
				}
				b[u>>8][(u>>6)&3] |= 1 << (u & 63)
			}
		}
		s.blocks = &b
	}
	return s
}

func (s *unitSet) contains(u uint16) bool {
	if s.blocks != nil {
		return s.blocks[u>>8][(u>>6)&3]>>(u&63)&1 != 0
	}
	return int(u) >= s.ranges[0] && int(u) <= s.ranges[1]
}

// find returns the first unit at or after from in the set, or -1.
func (s *unitSet) find(u []uint16, from int) int {
	if s.one >= 0 {
		return indexUnit(u, from, len(u), uint16(s.one))
	}
	if b := s.blocks; b != nil {
		for at := from; at < len(u); at++ {
			c := u[at]
			if b[c>>8][(c>>6)&3]>>(c&63)&1 != 0 {
				return at
			}
		}
		return -1
	}
	lo, width := s.ranges[0], s.ranges[1]-s.ranges[0]
	for at := from; at < len(u); at++ {
		if d := int(u[at]) - lo; d >= 0 && d <= width {
			return at
		}
	}
	return -1
}
