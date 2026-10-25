package ecmaregex

import (
	"bytes"
	"testing"
)

// TestTheByteSearchesFindWhatALoopFinds compares each SWAR search, and each finder, with a loop over the text, at
// every start, over texts of every length up to a few words, so that every lane and every tail is reached.
func TestTheByteSearchesFindWhatALoopFinds(t *testing.T) {
	seed := uint32(0x9e3779b9)
	next := func() byte {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		return "abcSHxyz09 \x00\x80\xe2\xff"[seed%15]
	}
	loop := func(h []byte, from int, f func(byte) bool) int {
		for k := from; k < len(h); k++ {
			if f(h[k]) {
				return k
			}
		}
		return -1
	}
	for n := 0; n < 70; n++ {
		for round := 0; round < 20; round++ {
			h := make([]byte, n)
			for k := range h {
				h[k] = next()
			}
			for from := 0; from <= n; from++ {
				if got, want := indexByte2(h, from, n, 'a', 0x80), loop(h, from, func(b byte) bool { return b == 'a' || b == 0x80 }); got != want {
					t.Fatalf("indexByte2 over %q from %d: %d, want %d", h, from, got, want)
				}
				if got, want := indexByte4(h, from, n, 'S', 'H', 0, 0xFF), loop(h, from, func(b byte) bool { return b == 'S' || b == 'H' || b == 0 || b == 0xFF }); got != want {
					t.Fatalf("indexByte4 over %q from %d: %d, want %d", h, from, got, want)
				}
				if got, want := indexBetween(h, from, n, '0', 'c'), loop(h, from, func(b byte) bool { return b >= '0' && b <= 'c' }); got != want {
					t.Fatalf("indexBetween over %q from %d: %d, want %d", h, from, got, want)
				}
				u := make([]uint16, n)
				for k, b := range h {
					u[k] = uint16(b) | uint16(b&1)<<8
				}
				want := -1
				for k := from; k < n; k++ {
					if u[k] == 'a'|1<<8 {
						want = k
						break
					}
				}
				if got := indexUnit(u, from, n, 'a'|1<<8); got != want {
					t.Fatalf("indexUnit over %x from %d: %d, want %d", u, from, got, want)
				}
			}
		}
	}
}

// TestTheFindersFindWhatALoopFinds compares each kind of finder with a loop that compares every literal at every start.
func TestTheFindersFindWhatALoopFinds(t *testing.T) {
	sets := [][]string{{"Sherlock"}, {"the"}, {"ab"}, {"x"}, {"Sherlock", "Holmes"}, {"Huck", "Saw"},
		{"HOLMES", "HOLMEs", "HOLMeS", "holmes", "Holmes"}, {"aa", "ab", "ba"}, {"the", "she", "he"},
		{"Tom", "Sawyer", "Huckleberry", "Finn"}, {"abc", "abd", "abe", "abf", "abg", "abh", "abi"}}
	seed := uint32(12345)
	rnd := func() uint32 {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		return seed
	}
	next := func() byte { return "abehHoOlLmMesSkrTwFinSuxyz "[rnd()%27] }
	checks := 0
	defer func() { t.Logf("%d bytes of texts", checks) }()
	for _, set := range sets {
		var lits [][]byte
		for _, s := range set {
			lits = append(lits, []byte(s))
		}
		finders := map[string]byteFinder{"chosen": newByteFinder(lits, &byteRank), "table": newTableFinder(lits, &byteRank)}
		if p := newPairFinder(lits, &byteRank); p != nil {
			finders["pair"] = p
		}
		if f := newFirstsFinder(lits, &byteRank); f != nil {
			finders["firsts"] = f
		}
		for round := 0; round < 300; round++ {
			h := make([]byte, int(rnd()%90))
			checks += len(h)
			for k := range h {
				h[k] = next()
			}
			for k := 0; k+5 < len(h) && round%3 == 0; k += 13 {
				copy(h[k:], set[k%len(set)])
			}
			for from := 0; from <= len(h); from++ {
				ws, we := -1, -1
			search:
				for s := from; s < len(h); s++ {
					for _, l := range lits {
						if bytes.HasPrefix(h[s:], l) {
							ws, we = s, s+len(l)
							break search
						}
					}
				}
				for name, f := range finders {
					if s, e := f.find(h, from, len(h)); s != ws || (s >= 0 && e != we) {
						t.Fatalf("%s finder of %q over %q from %d: %d..%d, want %d..%d", name, set, h, from, s, e, ws, we)
					}
				}
			}
		}
	}
}
