// Package ucd holds the Unicode Character Database properties the engine reads, for each version of Unicode the module
// carries (ucd.rs of the Rust crate): V13, V17 and V18, each a *UCD.
//
// Nothing in the engine takes Unicode properties from the Go standard library, because those follow the toolchain.
// With its own tables the module gives the same answers whatever builds it, and the answers change only when the
// tables are generated again. A pattern is read with the tables of the version it was compiled for.
//
// The tables of a version are in tables<major>_gen.go, which tools/unicode/gen_tables.py writes from the Rust crate's
// tables<major>.rs, so every engine holds the same data. Each is a sorted list of inclusive ranges, the code points up
// to U+FFFF as 16-bit bounds and the rest as 32-bit bounds. A value that follows from other values (a General_Category
// group such as L, the Assigned property, the Unknown script) has no table of its own and is built when it is first
// asked for.
package ucd

import (
	"slices"
	"strconv"
	"strings"
	"sync"
)

// MaxRune is the last Unicode code point.
const MaxRune rune = 0x10FFFF

// Table is an immutable set of code points. r16 holds the ranges that end at or below U+FFFF and r32 the ranges that
// start above it, each as inclusive pairs in ascending order. No two ranges of one list overlap or touch.
type Table struct {
	r16 []uint16
	r32 []uint32
}

// Contains reports whether the table holds the code point.
func (t *Table) Contains(r rune) bool {
	if uint32(r) <= 0xFFFF {
		ranges, c := t.r16, uint16(r)
		lo, hi := 0, len(ranges)/2
		for lo < hi {
			mid := (lo + hi) >> 1
			if c > ranges[2*mid+1] {
				lo = mid + 1
			} else if c < ranges[2*mid] {
				hi = mid
			} else {
				return true
			}
		}
		return false
	}
	ranges, c := t.r32, uint32(r)
	lo, hi := 0, len(ranges)/2
	for lo < hi {
		mid := (lo + hi) >> 1
		if c > ranges[2*mid+1] {
			lo = mid + 1
		} else if c < ranges[2*mid] {
			hi = mid
		} else {
			return true
		}
	}
	return false
}

// AppendRanges appends the ranges of the table to dst as inclusive pairs in ascending order. The pairs neither
// overlap nor touch.
func (t *Table) AppendRanges(dst []rune) []rune {
	dst = slices.Grow(dst, len(t.r16)+len(t.r32))
	for _, c := range t.r16 {
		dst = append(dst, rune(c))
	}
	high := t.r32
	// A range that crosses U+FFFF is stored in two parts.
	if len(t.r16) > 0 && len(high) > 0 && t.r16[len(t.r16)-1] == 0xFFFF && high[0] == 0x10000 {
		dst[len(dst)-1] = rune(high[1])
		high = high[2:]
	}
	for _, c := range high {
		dst = append(dst, rune(c))
	}
	return dst
}

// Ranges returns the ranges of the table as inclusive pairs.
func (t *Table) Ranges() []rune {
	return t.AppendRanges(nil)
}

// Ranges are the ranges of a property: of a generated table, or of one that was built.
type Ranges struct {
	table *Table
	built []rune
}

// Pairs returns the ranges as inclusive pairs in ascending order. The caller may change the slice.
func (r Ranges) Pairs() []rune {
	if r.table != nil {
		return r.table.Ranges()
	}
	return slices.Clone(r.built)
}

// union is the ranges of the code points that any of the tables holds.
func union(tables ...*Table) []rune {
	var all []rune
	for _, t := range tables {
		all = t.AppendRanges(all)
	}
	order := make([]int, len(all)/2)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return int(all[2*a] - all[2*b]) })
	merged := make([]rune, 0, len(all))
	for _, i := range order {
		lo, hi := all[2*i], all[2*i+1]
		if n := len(merged); n > 0 && lo <= merged[n-1]+1 {
			if hi > merged[n-1] {
				merged[n-1] = hi
			}
			continue
		}
		merged = append(merged, lo, hi)
	}
	return merged
}

// complement is the ranges of the code points that the table does not hold.
func complement(t *Table) []rune {
	pairs := t.Ranges()
	inverse := make([]rune, 0, len(pairs)+2)
	var next rune
	for i := 0; i+1 < len(pairs); i += 2 {
		lo, hi := pairs[i], pairs[i+1]
		if lo > next {
			inverse = append(inverse, next, lo-1)
		}
		next = hi + 1
	}
	if next <= MaxRune {
		inverse = append(inverse, next, MaxRune)
	}
	return inverse
}

// data is the generated data of one version of Unicode: its lookups by name and its case mappings
// (tables<major>_gen.go).
type data struct {
	version               string
	categoryTable         func(name string) *Table
	binaryTable           func(name string) *Table
	scriptTable           func(name string) *Table
	scriptExtensionsTable func(name string) *Table
	scriptNames           [][]string
	binaryNames           []string
	caseFolds             []CaseRange
	upperCases            []CaseRange
}

// UCD is the Unicode of one version: its generated data, and the values built from it on first use and then kept.
type UCD struct {
	data *data
	// The General_Category values that are groups of other values, the Assigned property (the code points that have a
	// General_Category other than Cn) and the Unknown script (the code points that belong to no script: the unassigned,
	// private use and surrogate code points). The generator of the tables checks each definition against their source.
	letter, casedLetter, mark, number, punctuation, symbol, separator, other, assigned, unknownScript func() []rune
}

func newUCD(d *data) *UCD {
	gc := func(names ...string) func() []rune {
		return sync.OnceValue(func() []rune {
			tables := make([]*Table, len(names))
			for i, name := range names {
				tables[i] = d.categoryTable(name)
			}
			return union(tables...)
		})
	}
	return &UCD{
		data:          d,
		letter:        gc("Lu", "Ll", "Lt", "Lm", "Lo"),
		casedLetter:   gc("Lu", "Ll", "Lt"),
		mark:          gc("Mn", "Mc", "Me"),
		number:        gc("Nd", "Nl", "No"),
		punctuation:   gc("Pc", "Pd", "Ps", "Pe", "Pi", "Pf", "Po"),
		symbol:        gc("Sm", "Sc", "Sk", "So"),
		separator:     gc("Zs", "Zl", "Zp"),
		other:         gc("Cc", "Cf", "Cs", "Co", "Cn"),
		assigned:      sync.OnceValue(func() []rune { return complement(d.categoryTable("Cn")) }),
		unknownScript: gc("Cn", "Co", "Cs"),
	}
}

// The versions of Unicode the module carries.
var (
	V13 = newUCD(&data13)
	V17 = newUCD(&data17)
	V18 = newUCD(&data18)
)

// ByMajor returns the Unicode of a version by its major number, 13, 17 or 18, or nil for another.
func ByMajor(major int) *UCD {
	switch major {
	case 13:
		return V13
	case 17:
		return V17
	case 18:
		return V18
	}
	return nil
}

// Version returns the version of Unicode the tables hold, such as "17.0.0".
func (u *UCD) Version() string { return u.data.version }

// Major returns the major number of the version, such as 17.
func (u *UCD) Major() int {
	major, _ := strconv.Atoi(u.data.version[:strings.IndexByte(u.data.version, '.')])
	return major
}

// katakanaOrHiragana is the table of the Script value Katakana_Or_Hiragana (Hrkt). PropertyValueAliases.txt lists the
// value, and no code point has it, in Script or in Script_Extensions. Scripts.txt has no line for it, so the generated
// tables do not have it.
var katakanaOrHiragana = Table{}

// Category returns the table of a General_Category value by its short name, such as "Lu" or "L", and false for any
// other name.
func (u *UCD) Category(name string) (Ranges, bool) {
	var built func() []rune
	switch name {
	case "L":
		built = u.letter
	case "LC":
		built = u.casedLetter
	case "M":
		built = u.mark
	case "N":
		built = u.number
	case "P":
		built = u.punctuation
	case "S":
		built = u.symbol
	case "Z":
		built = u.separator
	case "C":
		built = u.other
	default:
		return fromTable(u.data.categoryTable(name))
	}
	return Ranges{built: built()}, true
}

// Binary returns the table of a binary property by its canonical name, such as "Alphabetic" or "White_Space", and
// false for any other name. ASCII and Any, which ECMA-262 adds to the properties of Unicode, are not here.
func (u *UCD) Binary(name string) (Ranges, bool) {
	if name == "Assigned" {
		return Ranges{built: u.assigned()}, true
	}
	return fromTable(u.data.binaryTable(name))
}

// BinaryTable returns the generated table of a binary property, for the callers that test one code point, or nil.
func (u *UCD) BinaryTable(name string) *Table {
	return u.data.binaryTable(name)
}

// Script returns the table of a Script value by its long name or an alias, such as "Greek" or "Grek", and false for
// any other name.
func (u *UCD) Script(name string) (Ranges, bool) {
	switch name {
	case "Unknown", "Zzzz":
		return Ranges{built: u.unknownScript()}, true
	case "Katakana_Or_Hiragana", "Hrkt":
		return Ranges{table: &katakanaOrHiragana}, true
	}
	return fromTable(u.data.scriptTable(name))
}

// ScriptExtensions returns the table of a Script_Extensions value by the long name of the script or an alias, and
// false for any other name.
func (u *UCD) ScriptExtensions(name string) (Ranges, bool) {
	switch name {
	case "Unknown", "Zzzz":
		return Ranges{built: u.unknownScript()}, true
	case "Katakana_Or_Hiragana", "Hrkt":
		return Ranges{table: &katakanaOrHiragana}, true
	}
	return fromTable(u.data.scriptExtensionsTable(name))
}

func fromTable(t *Table) (Ranges, bool) {
	if t == nil {
		return Ranges{}, false
	}
	return Ranges{table: t}, true
}

// ScriptNames returns the names of every script. Each element is the long name of one script followed by its
// aliases.
func (u *UCD) ScriptNames() [][]string {
	return u.data.scriptNames
}

// BinaryNames returns the canonical name of every binary property that has a table, which is every name Binary knows
// but Assigned.
func (u *UCD) BinaryNames() []string {
	return u.data.binaryNames
}

// CaseRange maps some of the code points first, first + 1, ... first + length - 1 to themselves plus delta. The ones
// it maps are those whose offset from first has none of the bits of mask, so a mask of 1 takes every other code point.
type CaseRange struct {
	first  rune
	delta  int32
	length uint16
	mask   uint16
}

func mapCase(table []CaseRange, r rune) rune {
	lo, hi := 0, len(table)
	for lo < hi {
		mid := (lo + hi) >> 1
		row := &table[mid]
		if r < row.first {
			hi = mid
		} else {
			offset := r - row.first
			if offset >= rune(row.length) {
				lo = mid + 1
			} else if offset&rune(row.mask) == 0 {
				return r + row.delta
			} else {
				return r
			}
		}
	}
	return r
}

// Fold returns the simple case folding of a code point (the C and S rows of CaseFolding.txt), which is the code point
// itself when it has none. Two code points are equal ignoring case when Fold gives the same result for both.
func (u *UCD) Fold(r rune) rune {
	if r < 0x80 {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}
	return mapCase(u.data.caseFolds, r)
}

// ToUpper returns the simple uppercase mapping of a code point, which is the code point itself when it has none.
func (u *UCD) ToUpper(r rune) rune {
	if r < 0x80 {
		if 'a' <= r && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
	return mapCase(u.data.upperCases, r)
}

// Folding returns every code point that Fold changes, in ascending order.
func (u *UCD) Folding() []rune {
	var dst []rune
	for _, row := range u.data.caseFolds {
		for offset := rune(0); offset < rune(row.length); offset++ {
			if offset&rune(row.mask) == 0 && row.delta != 0 {
				dst = append(dst, row.first+offset)
			}
		}
	}
	return dst
}
