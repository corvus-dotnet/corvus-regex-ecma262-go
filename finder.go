package ecmaregex

// A search for one of a set of literals in a text's bytes (Finder of search.rs). Its answer is the leftmost occurrence
// at or after a position, the first literal in their order of those that start there, or -1 for none.
//
// The Rust crate takes memchr::memmem, aho-corasick and its packed searcher Teddy; Go has none of them, so the searches
// here are written on the searches of swar.go, as the Java engine writes them: a byte rare in text by
// bytes.IndexByte, a few literals by two of their byte positions (pairFinder, PCRE2's search, which the crate has as
// pair8.rs and which here takes one literal too, in place of memmem's search for its two rarest bytes), and any other
// set by a table of the bytes at its rarest position (tableFinder).

import (
	"math/bits"
	"slices"
	"strings"
	"unicode"
)

type byteFinder interface {
	// find returns the leftmost occurrence that starts at or after from and ends at or before end, as its start and
	// end, or -1 for none.
	find(h []byte, from, end int) (int, int)
}

// byteRank is how common each byte is in text, from 0 (rare) to 255: the table memchr::memmem ranks a needle's bytes
// by (memchr 2.8, arch/all/packedpair/default_rank.rs; MIT or Unlicense), which pair8.rs takes.
var byteRank = [256]int{
	55, 52, 51, 50, 49, 48, 47, 46, 45, 103, 242, 66, 67, 229, 44, 43, 42, 41, 40, 39, 38, 37, 36, 35, 34, 33, 56, 32,
	31, 30, 29, 28, 255, 148, 164, 149, 136, 160, 155, 173, 221, 222, 134, 122, 232, 202, 215, 224, 208, 220, 204, 187,
	183, 179, 177, 168, 178, 200, 226, 195, 154, 184, 174, 126, 120, 191, 157, 194, 170, 189, 162, 161, 150, 193, 142,
	137, 171, 176, 185, 167, 186, 112, 175, 192, 188, 156, 140, 143, 123, 133, 128, 147, 138, 146, 114, 223, 151, 249,
	216, 238, 236, 253, 227, 218, 230, 247, 135, 180, 241, 233, 246, 244, 231, 139, 245, 243, 251, 235, 201, 196, 240,
	214, 152, 182, 205, 181, 127, 27, 212, 211, 210, 213, 228, 197, 169, 159, 131, 172, 105, 80, 98, 96, 97, 81, 207,
	145, 116, 115, 144, 130, 153, 121, 107, 132, 109, 110, 124, 111, 82, 108, 118, 141, 113, 129, 119, 125, 165, 117,
	92, 106, 83, 72, 99, 93, 65, 79, 166, 237, 163, 199, 190, 225, 209, 203, 198, 217, 219, 206, 234, 248, 158, 239,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
}

// rareByte reports whether a byte is rare enough in text for a search for it alone to pay: not a lower case letter
// (but the rarest five), a space or the commonest punctuation, nor a byte beyond ASCII, where the lead bytes of a
// script are common.
func rareByte(b byte) bool {
	switch {
	case 'A' <= b && b <= 'Z', '0' <= b && b <= '9':
		return true
	case 0x20 < b && b < 0x7F && !('a' <= b && b <= 'z'):
		return !strings.ContainsRune(",.'\"-", rune(b))
	}
	return strings.ContainsRune("jqxzk", rune(b))
}

// aloneRank is the rank under which a byte is searched for alone (the capital letters, the digits and the rarest
// lower case letters): the search for one byte reads several times as fast as the pair search, so a byte this rare
// pays for the compares where it is found.
const aloneRank = 200

// rarestAlone returns the position of the byte of a literal with the least share of text (unitShare) of those rare
// enough to be searched for alone, or -1 for none.
func rarestAlone(l []byte, rank *[256]int) int {
	best := -1
	for k, b := range l {
		if alone(rank, b) && (best < 0 || unitShare[b] < unitShare[l[best]]) {
			best = k
		}
	}
	return best
}

// alone reports whether a byte is rare enough to be searched for alone: one rareByte names (the Rust crate's rule),
// and one the ranks do not mark common (a byte a UTF-16 literal's unit beyond ASCII packs to, which packedRanks
// marks).
func alone(rank *[256]int, b byte) bool { return b < 0x80 && rareByte(b) && rank[b] <= aloneRank }

// unitShare is each ASCII unit's share of English text in parts per ten thousand: the small letters by their
// frequency (the table of the .NET reference engine's needle), the capitals as measured over the Industry texts, the
// space and the commonest punctuation, and ten for any other.
var unitShare = func() [128]int {
	var r [128]int
	for k := range r {
		r[k] = 10
	}
	perMille := []int{102, 73, 66, 60, 56, 54, 50, 49, 48, 34, 32, 22, 22, 19, 19, 18, 16, 16, 15, 12, 8, 6, 2, 2, 1, 1}
	for k, c := range "etaoinshrdlcumwfgypbvkjxqz" {
		r[c] = perMille[k] * 10
	}
	perTenThousand := []int{65, 28, 21, 18, 16, 13, 13, 12, 10, 10, 9, 9, 8, 8, 7, 6, 6, 5, 5, 4, 2, 2, 2, 1, 1, 1}
	for k, c := range "ITHASWMECBNOYRLDPGFJUVKXQZ" {
		r[c] = perTenThousand[k]
	}
	r[' '], r['\n'], r['\r'], r['.'], r[','] = 1700, 200, 200, 70, 80
	return r
}()

// share16 is a UTF-16 code unit's share of text in parts per ten thousand, for the choice of the unit a search over
// UTF-16 looks for: an ASCII unit as unitShare has it; a capital of another alphabet (Latin, Greek, Cyrillic,
// Armenian) as rare as an ASCII capital and its small letters as common as the commonest ASCII ones, since a text in
// that script is mostly them; anything else rare.
func share16(u uint16) int {
	if u < 0x80 {
		return unitShare[u]
	}
	if u < 0x250 || u >= 0x370 && u < 0x590 {
		switch r := rune(u); {
		case unicode.IsUpper(r):
			return 20
		case unicode.IsLetter(r):
			return 500
		}
	}
	return 10
}

// newByteFinder returns the search for the literals, in the pattern's order, with how common each byte is (byteRank
// over UTF-8, and the ranks of packedRanks for the low bytes of UTF-16 units).
func newByteFinder(literals [][]byte, rank *[256]int) byteFinder {
	if len(literals) == 1 {
		l := literals[0]
		if len(l) == 1 {
			return oneFinder(l[0])
		}
		// The byte with the least share of text of those rare enough to be searched for alone; the pair search where
		// every byte is common. The share, not byteRank, chooses: byteRank ranks Sherlock's k rarest, which the Sherlock
		// Holmes text holds 3,595 times to its S's 837 (Sherlock 16.1 us by its S, 46.6 by its k). bytes.IndexByte
		// reads a vector at a time and the pair search a word, so any rare byte is searched for alone, as the Java
		// engine does with SWAR: Twain as a pair, as the Java engine's rule with vectors has it, took 2.4 times as
		// long as by its T.
		if best := rarestAlone(l, rank); best >= 0 {
			return &rareFinder{literal: l, k: best}
		}
		if p := newPairFinder(literals, rank); p != nil {
			return p
		}
		return newTableFinder(literals, rank)
	}
	if f := newFirstsFinder(literals, rank); f != nil {
		return f
	}
	if p := newPairFinder(literals, rank); p != nil {
		return p
	}
	return newTableFinder(literals, rank)
}

// packedRanks returns how common each low byte of the units of UTF-16 literals is in the packed text: an ASCII unit's
// byte as byteRank has it, and a byte any of the literals' units beyond ASCII packs to as common (a Cyrillic letter in
// Russian text) or nearly so (a Han character, whose low byte any of thousands share), so that neither is searched for
// alone or taken for a rare position.
func packedRanks(literals [][]uint16) *[256]int {
	rank := byteRank
	for _, l := range literals {
		for _, u := range l {
			if u >= 0x80 {
				b := byte(u)
				v := 230
				if u >= 0x400 && u <= 0x4FF {
					v = 245
				}
				rank[b] = max(rank[b], v)
			}
		}
	}
	return &rank
}

// oneFinder is a literal of one byte.
type oneFinder byte

func (f oneFinder) find(h []byte, from, end int) (int, int) {
	if p := indexByte(h, from, end, byte(f)); p >= 0 {
		return p, p + 1
	}
	return -1, -1
}

// rareFinder is one literal with a byte rare enough in text to be searched for alone: the search for it at its
// offset, then the literal compared, as V8 searches a literal pattern for its first character.
type rareFinder struct {
	literal []byte
	k       int
}

func (f *rareFinder) find(h []byte, from, end int) (int, int) {
	b := f.literal[f.k]
	for at := from + f.k; at < end; {
		p := indexByte(h, at, end, b)
		if p < 0 {
			return -1, -1
		}
		if start := p - f.k; startsWith(h, start, end, f.literal) {
			return start, start + len(f.literal)
		}
		at = p + 1
	}
	return -1, -1
}

// firstsFinder is several literals whose bytes at one position are up to four values, or more within a range, each
// byte rare enough to be searched for alone: the search for those bytes (or that range) at that position, then the
// literals with the byte found there compared in order. Sherlock|Holmes is found by an S or an H, as the regex crate
// finds Sherlock|Street by its S; the position is the one whose values are rarest.
type firstsFinder struct {
	k int
	// count is the number of values a, b, c and d (the last repeated), or -1 for the range lo to hi.
	count      int
	a, b, c, d byte
	lo, hi     byte
	// byValue holds the literals by their byte at k, in the pattern's order.
	byValue [256][][]byte
}

// positionValues returns the values a position of the literals holds, sorted.
func positionValues(literals [][]byte, k int) []byte {
	var seen [256]bool
	for _, l := range literals {
		seen[l[k]] = true
	}
	var out []byte
	for b := range 256 {
		if seen[b] {
			out = append(out, byte(b))
		}
	}
	return out
}

func shortestOf(literals [][]byte) int {
	shortest := len(literals[0])
	for _, l := range literals {
		shortest = min(shortest, len(l))
	}
	return shortest
}

func newFirstsFinder(literals [][]byte, rank *[256]int) *firstsFinder {
	bestK, bestRange, bestCost := -1, false, int64(1<<62)
	for k := 0; k < shortestOf(literals); k++ {
		v := positionValues(literals, k)
		// Up to four values are tested for exactly; more as the range from the least to the greatest, where every byte
		// in it is rare (the capital letters of a list of names), which is one test where the values would be as many.
		isRange := len(v) > 4
		cost, rare := int64(0), true
		if isRange {
			for x := int(v[0]); x <= int(v[len(v)-1]); x++ {
				rare = rare && alone(rank, byte(x))
				cost += 1 << (rank[x] / 16)
			}
		} else {
			for _, x := range v {
				rare = rare && alone(rank, x)
				cost += 1 << (rank[x] / 16)
			}
		}
		// Exact values before a range, then the rarer.
		if isRange {
			cost += 2 << 20
		} else {
			cost += 1 << 20
		}
		if rare && cost < bestCost {
			bestK, bestRange, bestCost = k, isRange, cost
		}
	}
	if bestK < 0 {
		return nil
	}
	v := positionValues(literals, bestK)
	f := &firstsFinder{k: bestK, count: len(v)}
	for _, x := range v {
		for _, l := range literals {
			if l[bestK] == x {
				f.byValue[x] = append(f.byValue[x], l)
			}
		}
	}
	if bestRange {
		f.count, f.lo, f.hi = -1, v[0], v[len(v)-1]
	} else {
		at := func(i int) byte { return v[min(i, len(v)-1)] }
		f.a, f.b, f.c, f.d = at(0), at(1), at(2), at(3)
	}
	return f
}

func (f *firstsFinder) find(h []byte, from, end int) (int, int) {
	for at := from + f.k; at < end; {
		var p int
		switch f.count {
		case 1:
			p = indexByte(h, at, end, f.a)
		case 2:
			p = indexByte2(h, at, end, f.a, f.b)
		case 3, 4:
			p = indexByte4(h, at, end, f.a, f.b, f.c, f.d)
		default:
			p = indexBetween(h, at, end, f.lo, f.hi)
		}
		if p < 0 {
			return -1, -1
		}
		start := p - f.k
		// A byte of the range no literal holds there has no literals.
		for _, l := range f.byValue[h[p]] {
			if startsWith(h, start, end, l) {
				return start, start + len(l)
			}
		}
		at = p + 1
	}
	return -1, -1
}

// positionTests returns the tests of a position's values as pairs of a mask and a value, a byte x passing one where
// x|mask == value: two values that differ in one bit (the cases of an ASCII letter, the lead bytes of the cases of a
// Cyrillic letter) make one test with that bit set, any other value a test of its own. Nil where that makes more than
// most.
func positionTests(values []byte, most int) []byte {
	used := make([]bool, len(values))
	var out []byte
	for a := range values {
		if used[a] {
			continue
		}
		used[a] = true
		mask := byte(0)
		for b := a + 1; b < len(values); b++ {
			if bit := values[a] ^ values[b]; !used[b] && bits.OnesCount8(bit) == 1 {
				used[b] = true
				mask = bit
				break
			}
		}
		if len(out) == 2*most {
			return nil
		}
		out = append(out, mask, values[a]|mask)
	}
	return out
}

const (
	// pairMostTests is the most tests of a position of the pair search.
	pairMostTests = 4
	// pairChecked is the most bytes of the literals looked at before they are compared one by one.
	pairChecked = 16
)

// pairFinder is the search for a few literals by two of their byte positions, each of whose values are a few tests
// (positionTests): PCRE2's search before a match (fast_forward_char_pair_simd in its JIT compiler), which finds
// Sher[a-z]+|Hol[a-z]+ by an S or an H with an e or an l two bytes on, and a case-insensitive Holmes by both cases of
// two of its letters. Eight starts are tested at a time; each candidate is compared with the literals out of that loop,
// so that the loop keeps everything in registers.
type pairFinder struct {
	literals [][]byte
	// shortest is the fewest bytes of a literal.
	shortest int
	// i and j are the two positions, and mi, vi, mj and vj the masks and values of each one's tests, broadcast.
	i, j           int
	mi, vi, mj, vj []uint64
	// sets holds, for each of the first bytes of the literals up to pairChecked, the values they hold there.
	sets [][256]bool
	// every says the literals are every sequence of the values sets holds, all of one length (a word's cases written
	// out): a candidate whose bytes pass sets is then a literal, with nothing more to compare.
	every bool
}

// newPairFinder returns the search for the literals (of at least two bytes each, in the pattern's order), or nil
// where no two of their positions take pairMostTests tests or fewer each.
func newPairFinder(literals [][]byte, ranks *[256]int) *pairFinder {
	shortest := shortestOf(literals)
	if shortest < 2 {
		return nil
	}
	// Each position of few enough tests, with how common its commonest value is.
	tests := make([][]byte, shortest)
	values := make([][]byte, shortest)
	rank := make([]int, shortest)
	for k := range shortest {
		values[k] = positionValues(literals, k)
		tests[k] = positionTests(values[k], pairMostTests)
		for _, v := range values[k] {
			rank[k] = max(rank[k], ranks[v])
		}
	}
	// The pair of the rarest positions, their values apart where any pair has them apart (a run of one byte would
	// pass both otherwise), as PCRE2 chooses; of those as rare, the one of fewer tests.
	bi, bj, best := -1, -1, int64(1<<62)
	for _, apart := range []bool{true, false} {
		for x := range shortest {
			for y := x + 1; y < shortest; y++ {
				if tests[x] == nil || tests[y] == nil || apart && overlaps(values[x], values[y]) {
					continue
				}
				if cost := int64(rank[x]+rank[y])<<8 | int64(len(tests[x])+len(tests[y])); cost < best {
					best, bi, bj = cost, x, y
				}
			}
		}
		if bi >= 0 {
			break
		}
	}
	if bi < 0 {
		return nil
	}
	checked := min(shortest, pairChecked)
	sets := make([][256]bool, checked)
	for k := range checked {
		for _, l := range literals {
			sets[k][l[k]] = true
		}
	}
	every := shortest <= pairChecked
	for _, l := range literals {
		every = every && len(l) == shortest
	}
	product := 1
	for k := 0; k < shortest && every; k++ {
		product *= len(values[k])
		every = product <= len(literals)
	}
	every = every && product == len(literals)
	broadcastEach := func(t []byte, which int) []uint64 {
		out := make([]uint64, len(t)/2)
		for k := range out {
			out[k] = broadcast(t[2*k+which])
		}
		return out
	}
	return &pairFinder{
		literals: literals, shortest: shortest, i: bi, j: bj,
		mi: broadcastEach(tests[bi], 0), vi: broadcastEach(tests[bi], 1),
		mj: broadcastEach(tests[bj], 0), vj: broadcastEach(tests[bj], 1),
		sets: sets, every: every,
	}
}

func overlaps(a, b []byte) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// literalAt returns where the literal that starts at at ends (the first in the pattern's order), or -1.
func (f *pairFinder) literalAt(h []byte, at, end int) int {
	n := min(len(f.sets), end-at)
	for k := range n {
		if !f.sets[k][h[at+k]] {
			return -1
		}
	}
	if f.every {
		return at + f.shortest
	}
	for _, l := range f.literals {
		if startsWith(h, at, end, l) {
			return at + len(l)
		}
	}
	return -1
}

// pass returns the lanes of a word that pass the tests, by the cheap test of hasZero, which may flag a lane that does
// not pass but never misses one: every candidate is compared with the literals.
func pass(x uint64, m, v []uint64) uint64 {
	out := hasZero((x | m[0]) ^ v[0])
	for k := 1; k < len(m); k++ {
		out |= hasZero((x | m[k]) ^ v[k])
	}
	return out
}

func (f *pairFinder) find(h []byte, from, end int) (int, int) {
	last := end - f.shortest
	if from > last {
		return -1, -1
	}
	at := from
	// The last start whose two words are within the text.
	blocks := end - 8 - max(f.i, f.j)
	if len(f.mi) == 1 && len(f.mj) == 1 {
		mi, vi, mj, vj := f.mi[0], f.vi[0], f.mj[0], f.vj[0]
		for ; at <= blocks; at += 8 {
			m := hasZero((word(h, at+f.i)|mi)^vi) & hasZero((word(h, at+f.j)|mj)^vj)
			for ; m != 0; m &= m - 1 {
				if s := at + lane(m); s <= last {
					if e := f.literalAt(h, s, end); e >= 0 {
						return s, e
					}
				}
			}
		}
	} else {
		for ; at <= blocks; at += 8 {
			m := pass(word(h, at+f.i), f.mi, f.vi) & pass(word(h, at+f.j), f.mj, f.vj)
			for ; m != 0; m &= m - 1 {
				if s := at + lane(m); s <= last {
					if e := f.literalAt(h, s, end); e >= 0 {
						return s, e
					}
				}
			}
		}
	}
	for ; at <= last; at++ {
		if e := f.literalAt(h, at, end); e >= 0 {
			return at, e
		}
	}
	return -1, -1
}

// tableFinder is any set of literals: a table of the bytes the literals hold at their rarest position, read a byte at a
// time, and the literals that start with the byte found compared in order. The search the crate makes with Teddy and
// with aho-corasick's automaton, where no two positions take few enough tests for pairFinder.
type tableFinder struct {
	k        int
	at       [256]bool
	shortest int
	// byFirst holds the literals by their first byte, in the pattern's order.
	byFirst [256][][]byte
}

func newTableFinder(literals [][]byte, rank *[256]int) *tableFinder {
	shortest := shortestOf(literals)
	bestK, bestCost := 0, int64(1<<62)
	for k := range shortest {
		cost := int64(0)
		for _, v := range positionValues(literals, k) {
			cost += 1 << (rank[v] / 16)
		}
		if cost < bestCost {
			bestCost, bestK = cost, k
		}
	}
	f := &tableFinder{k: bestK, shortest: shortest}
	for _, l := range literals {
		f.at[l[bestK]] = true
		f.byFirst[l[0]] = append(f.byFirst[l[0]], l)
	}
	return f
}

func (f *tableFinder) find(h []byte, from, end int) (int, int) {
	last, k := end-f.shortest, f.k
	for s := from; s <= last; s++ {
		if f.at[h[s+k]] {
			for _, l := range f.byFirst[h[s]] {
				if startsWith(h, s, end, l) {
					return s, s + len(l)
				}
			}
		}
	}
	return -1, -1
}
