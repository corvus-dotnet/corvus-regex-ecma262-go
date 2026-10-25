package ecmaregex

// Case-insensitive matching (fold.rs).
//
// It exists only inside a modifier group such as (?i:...), or under the i flag. ECMA-262 defines it through a
// Canonicalize function. Two characters match when they canonicalize to the same character. The function differs
// between the two grammars. With the u flag it is Unicode simple case folding. With no flag it is the single
// character uppercase mapping of a UTF-16 code unit, which never maps a character outside ASCII into ASCII.
//
// The parser applies the equivalence to every literal and class as it reads them, so a case-insensitive group is
// matched with plain classes. Only a backreference needs the function when matching.
//
// The folding and the uppercase mapping come from the tables of ucd, never from the standard library, so a pattern
// matches the same texts whichever toolchain built the program.

import (
	"slices"
	"sort"
	"sync"

	"github.com/corvus-dotnet/corvus-regex-ecma262-go/internal/ucd"
)

// multiUpper is the characters of the Basic Multilingual Plane whose uppercase form is more than one character, as
// inclusive pairs. Canonicalize leaves them alone.
var multiUpper = sync.OnceValue(func() *charSet {
	return newCharSet([]rune{
		0xDF, 0xDF, 0x149, 0x149, 0x1F0, 0x1F0, 0x390, 0x390, 0x3B0, 0x3B0, 0x587, 0x587, 0x1E96, 0x1E9A, 0x1F50,
		0x1F50, 0x1F52, 0x1F52, 0x1F54, 0x1F54, 0x1F56, 0x1F56, 0x1F80, 0x1FAF, 0x1FB2, 0x1FB4, 0x1FB6, 0x1FB7,
		0x1FBC, 0x1FBC, 0x1FC2, 0x1FC4, 0x1FC6, 0x1FC7, 0x1FCC, 0x1FCC, 0x1FD2, 0x1FD3, 0x1FD6, 0x1FD7, 0x1FE2,
		0x1FE4, 0x1FE6, 0x1FE7, 0x1FF2, 0x1FF4, 0x1FF6, 0x1FF7, 0x1FFC, 0x1FFC, 0xFB00, 0xFB06, 0xFB13, 0xFB17,
	}, false)
})

// canonicalize is the Canonicalize function of ECMA-262 for case-insensitive matching.
func canonicalize(r rune, unicodeMode bool) rune {
	if unicodeMode {
		// Every character of a simple case folding class folds to the same character.
		return ucd.Fold(r)
	}
	// A character beyond the Basic Multilingual Plane is two code units, and neither has an uppercase form.
	if r > 0xFFFF || multiUpper().contains(r) {
		return r
	}
	upper := ucd.ToUpper(r)
	if r >= 128 && upper < 128 || upper > 0xFFFF {
		return r
	}
	return upper
}

// foldTable holds the characters that are equivalent to some other character, each with all the members of its
// equivalence class.
type foldTable struct {
	points []rune
	// classOf is, for each point, the number of its class in classes.
	classOf []uint32
	classes [][]rune
}

func newFoldTable(classes map[rune][]rune) *foldTable {
	t := &foldTable{}
	byMember := map[rune]uint32{}
	for _, class := range classes {
		if len(class) < 2 {
			continue
		}
		for _, member := range class {
			t.points = append(t.points, member)
			byMember[member] = uint32(len(t.classes))
		}
		t.classes = append(t.classes, class)
	}
	slices.Sort(t.points)
	t.classOf = make([]uint32, len(t.points))
	for i, point := range t.points {
		t.classOf[i] = byMember[point]
	}
	return t
}

var unicodeFoldTable = sync.OnceValue(func() *foldTable {
	// A class is a character that others fold to, with those others.
	classes := map[rune][]rune{}
	for _, r := range ucd.Folding() {
		key := ucd.Fold(r)
		if classes[key] == nil {
			classes[key] = []rune{key}
		}
		classes[key] = append(classes[key], r)
	}
	return newFoldTable(classes)
})

var legacyFoldTable = sync.OnceValue(func() *foldTable {
	classes := map[rune][]rune{}
	for r := rune(0); r <= 0xFFFF; r++ {
		key := canonicalize(r, false)
		classes[key] = append(classes[key], r)
	}
	return newFoldTable(classes)
})

// foldClasses returns the table of the grammar in use. It is built on first use, which only a pattern with a
// case-insensitive group reaches.
func foldClasses(unicodeMode bool) *foldTable {
	if unicodeMode {
		return unicodeFoldTable()
	}
	return legacyFoldTable()
}

// foldClosure returns the set of every character equivalent to some member of the set, and the number of characters
// with a case variant that the set holds, which is the work it did.
func foldClosure(set *charSet, unicodeMode bool) (*charSet, int) {
	table := foldClasses(unicodeMode)
	var extra []rune
	variants := 0
	for i := 0; i+1 < len(set.ranges); i += 2 {
		lo, hi := set.ranges[i], set.ranges[i+1]
		k := sort.Search(len(table.points), func(j int) bool { return table.points[j] >= lo })
		for ; k < len(table.points) && table.points[k] <= hi; k++ {
			variants++
			for _, member := range table.classes[table.classOf[k]] {
				if !set.contains(member) {
					extra = append(extra, member, member)
				}
			}
		}
	}
	if len(extra) == 0 {
		return set, variants
	}
	extra = append(extra, set.ranges...)
	return newCharSet(extra, false), variants
}

// foldWordPairs are, with the u flag and case-insensitive matching, the characters \w and \b count as word
// characters: the ASCII ones and the two characters that fold to an ASCII letter (U+017F, the long s, and U+212A, the
// Kelvin sign).
var foldWordPairs = []rune{0x30, 0x39, 0x41, 0x5A, 0x5F, 0x5F, 0x61, 0x7A, 0x17F, 0x17F, 0x212A, 0x212A}

var (
	foldWordSet    = sync.OnceValue(func() *charSet { return newCharSet(foldWordPairs, false) })
	notFoldWordSet = sync.OnceValue(func() *charSet { return newCharSet(foldWordPairs, true) })
)

func isFoldWordRune(r rune) bool {
	return 0 <= r && r < 128 && isWordByte(byte(r)) || r == 0x17F || r == 0x212A
}
