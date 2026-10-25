package ecmaregex

// Sets of code points, the classes ECMA-262 defines, and the sets of the Unicode properties (charset.rs).

import (
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/corvus-dotnet/corvus-regex-ecma262-go/internal/ucd"
)

// maxRune is the last Unicode code point.
const maxRune = ucd.MaxRune

// charSet is an immutable set of code points. The ranges are sorted, inclusive, and neither overlap nor touch. The two
// words of ascii repeat the membership of the code points below 128 so that the common case is one bit test.
type charSet struct {
	ranges []rune
	ascii  [2]uint64
}

// newCharSet builds a set from inclusive pairs in any order, which may overlap. With negate it builds the complement.
func newCharSet(pairs []rune, negate bool) *charSet {
	idx := make([]int, len(pairs)/2)
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return int(pairs[2*a] - pairs[2*b]) })
	merged := make([]rune, 0, len(pairs))
	for _, i := range idx {
		lo, hi := pairs[2*i], pairs[2*i+1]
		if n := len(merged); n > 0 && lo <= merged[n-1]+1 {
			if hi > merged[n-1] {
				merged[n-1] = hi
			}
			continue
		}
		merged = append(merged, lo, hi)
	}
	if negate {
		inverse := make([]rune, 0, len(merged)+2)
		var next rune
		for i := 0; i+1 < len(merged); i += 2 {
			lo, hi := merged[i], merged[i+1]
			if lo > next {
				inverse = append(inverse, next, lo-1)
			}
			next = hi + 1
		}
		if next <= maxRune {
			inverse = append(inverse, next, maxRune)
		}
		merged = inverse
	}
	s := &charSet{ranges: merged}
	for i := 0; i+1 < len(merged); i += 2 {
		lo, hi := merged[i], merged[i+1]
		if lo >= 128 {
			break
		}
		for c := lo; c <= hi && c < 128; c++ {
			s.ascii[c>>6] |= 1 << (c & 63)
		}
	}
	return s
}

// hasASCII reports whether the set holds the ASCII character.
func (s *charSet) hasASCII(c byte) bool {
	return (s.ascii[(c>>6)&1]>>(c&63))&1 != 0
}

// contains reports whether the set holds the code point.
func (s *charSet) contains(r rune) bool {
	if 0 <= r && r < 128 {
		return (s.ascii[r>>6]>>(r&63))&1 != 0
	}
	ranges := s.ranges
	lo, hi := 0, len(ranges)/2
	for lo < hi {
		mid := (lo + hi) >> 1
		if r > ranges[2*mid+1] {
			lo = mid + 1
		} else if r < ranges[2*mid] {
			hi = mid
		} else {
			return true
		}
	}
	return false
}

// isEmpty reports whether the set holds no code point.
func (s *charSet) isEmpty() bool {
	return len(s.ranges) == 0
}

// The classes ECMA-262 defines. \d and \w are ASCII only, \s is WhiteSpace and LineTerminator, and . is everything but
// the four line terminators.
var (
	digitPairs          = []rune{0x30, 0x39}
	wordPairs           = []rune{0x30, 0x39, 0x41, 0x5A, 0x5F, 0x5F, 0x61, 0x7A}
	spacePairs          = []rune{0x9, 0xD, 0x20, 0x20, 0xA0, 0xA0, 0x1680, 0x1680, 0x2000, 0x200A, 0x2028, 0x2029, 0x202F, 0x202F, 0x205F, 0x205F, 0x3000, 0x3000, 0xFEFF, 0xFEFF}
	lineTerminatorPairs = []rune{0xA, 0xA, 0xD, 0xD, 0x2028, 0x2029}
)

// dotSet is everything but the four line terminators.
var dotSet = sync.OnceValue(func() *charSet { return newCharSet(lineTerminatorPairs, true) })

// isWordByte reports whether a byte is an ECMA-262 word character. Every word character is ASCII, so a word boundary
// can be decided from the bytes on either side of a position.
func isWordByte(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || b == '_'
}

// categoryName returns the short name of a General_Category value, from its long name, an alias or the short name
// itself, and "" for any other name.
func categoryName(value string) string {
	switch value {
	case "Letter", "L":
		return "L"
	case "Lowercase_Letter", "Ll":
		return "Ll"
	case "Uppercase_Letter", "Lu":
		return "Lu"
	case "Titlecase_Letter", "Lt":
		return "Lt"
	case "Cased_Letter", "LC":
		return "LC"
	case "Modifier_Letter", "Lm":
		return "Lm"
	case "Other_Letter", "Lo":
		return "Lo"
	case "Mark", "Combining_Mark", "M":
		return "M"
	case "Nonspacing_Mark", "Mn":
		return "Mn"
	case "Spacing_Mark", "Mc":
		return "Mc"
	case "Enclosing_Mark", "Me":
		return "Me"
	case "Number", "N":
		return "N"
	case "Decimal_Number", "digit", "Nd":
		return "Nd"
	case "Letter_Number", "Nl":
		return "Nl"
	case "Other_Number", "No":
		return "No"
	case "Punctuation", "punct", "P":
		return "P"
	case "Connector_Punctuation", "Pc":
		return "Pc"
	case "Dash_Punctuation", "Pd":
		return "Pd"
	case "Open_Punctuation", "Ps":
		return "Ps"
	case "Close_Punctuation", "Pe":
		return "Pe"
	case "Initial_Punctuation", "Pi":
		return "Pi"
	case "Final_Punctuation", "Pf":
		return "Pf"
	case "Other_Punctuation", "Po":
		return "Po"
	case "Symbol", "S":
		return "S"
	case "Math_Symbol", "Sm":
		return "Sm"
	case "Currency_Symbol", "Sc":
		return "Sc"
	case "Modifier_Symbol", "Sk":
		return "Sk"
	case "Other_Symbol", "So":
		return "So"
	case "Separator", "Z":
		return "Z"
	case "Space_Separator", "Zs":
		return "Zs"
	case "Line_Separator", "Zl":
		return "Zl"
	case "Paragraph_Separator", "Zp":
		return "Zp"
	case "Other", "C":
		return "C"
	case "Control", "cntrl", "Cc":
		return "Cc"
	case "Format", "Cf":
		return "Cf"
	case "Surrogate", "Cs":
		return "Cs"
	case "Private_Use", "Co":
		return "Co"
	case "Unassigned", "Cn":
		return "Cn"
	}
	return ""
}

// binaryName returns the canonical name of a binary property ECMA-262 lists, from the name or an alias, and "" for any
// other name.
func binaryName(name string) string {
	switch name {
	case "ASCII":
		return "ASCII"
	case "ASCII_Hex_Digit", "AHex":
		return "ASCII_Hex_Digit"
	case "Alphabetic", "Alpha":
		return "Alphabetic"
	case "Any":
		return "Any"
	case "Assigned":
		return "Assigned"
	case "Bidi_Control", "Bidi_C":
		return "Bidi_Control"
	case "Bidi_Mirrored", "Bidi_M":
		return "Bidi_Mirrored"
	case "Case_Ignorable", "CI":
		return "Case_Ignorable"
	case "Cased":
		return "Cased"
	case "Changes_When_Casefolded", "CWCF":
		return "Changes_When_Casefolded"
	case "Changes_When_Casemapped", "CWCM":
		return "Changes_When_Casemapped"
	case "Changes_When_Lowercased", "CWL":
		return "Changes_When_Lowercased"
	case "Changes_When_NFKC_Casefolded", "CWKCF":
		return "Changes_When_NFKC_Casefolded"
	case "Changes_When_Titlecased", "CWT":
		return "Changes_When_Titlecased"
	case "Changes_When_Uppercased", "CWU":
		return "Changes_When_Uppercased"
	case "Dash":
		return "Dash"
	case "Default_Ignorable_Code_Point", "DI":
		return "Default_Ignorable_Code_Point"
	case "Deprecated", "Dep":
		return "Deprecated"
	case "Diacritic", "Dia":
		return "Diacritic"
	case "Emoji":
		return "Emoji"
	case "Emoji_Component", "EComp":
		return "Emoji_Component"
	case "Emoji_Modifier", "EMod":
		return "Emoji_Modifier"
	case "Emoji_Modifier_Base", "EBase":
		return "Emoji_Modifier_Base"
	case "Emoji_Presentation", "EPres":
		return "Emoji_Presentation"
	case "Extended_Pictographic", "ExtPict":
		return "Extended_Pictographic"
	case "Extender", "Ext":
		return "Extender"
	case "Grapheme_Base", "Gr_Base":
		return "Grapheme_Base"
	case "Grapheme_Extend", "Gr_Ext":
		return "Grapheme_Extend"
	case "Hex_Digit", "Hex":
		return "Hex_Digit"
	case "IDS_Binary_Operator", "IDSB":
		return "IDS_Binary_Operator"
	case "IDS_Trinary_Operator", "IDST":
		return "IDS_Trinary_Operator"
	case "ID_Continue", "IDC":
		return "ID_Continue"
	case "ID_Start", "IDS":
		return "ID_Start"
	case "Ideographic", "Ideo":
		return "Ideographic"
	case "Join_Control", "Join_C":
		return "Join_Control"
	case "Logical_Order_Exception", "LOE":
		return "Logical_Order_Exception"
	case "Lowercase", "Lower":
		return "Lowercase"
	case "Math":
		return "Math"
	case "Noncharacter_Code_Point", "NChar":
		return "Noncharacter_Code_Point"
	case "Pattern_Syntax", "Pat_Syn":
		return "Pattern_Syntax"
	case "Pattern_White_Space", "Pat_WS":
		return "Pattern_White_Space"
	case "Quotation_Mark", "QMark":
		return "Quotation_Mark"
	case "Radical":
		return "Radical"
	case "Regional_Indicator", "RI":
		return "Regional_Indicator"
	case "Sentence_Terminal", "STerm":
		return "Sentence_Terminal"
	case "Soft_Dotted", "SD":
		return "Soft_Dotted"
	case "Terminal_Punctuation", "Term":
		return "Terminal_Punctuation"
	case "Unified_Ideograph", "UIdeo":
		return "Unified_Ideograph"
	case "Uppercase", "Upper":
		return "Uppercase"
	case "Variation_Selector", "VS":
		return "Variation_Selector"
	case "White_Space", "space":
		return "White_Space"
	case "XID_Continue", "XIDC":
		return "XID_Continue"
	case "XID_Start", "XIDS":
		return "XID_Start"
	}
	return ""
}

// propertyCache holds the sets already built for property expressions. A property set can have hundreds of ranges,
// and schemas tend to repeat the same few. Only an expression ECMA-262 defines is kept, and those are a fixed few
// hundred, so the cache is bounded.
var propertyCache sync.Map // map[string]*charSet

// propertySet resolves the expression between the braces of \p{...} to its set, or nil for a name or value that
// ECMA-262 does not define.
func propertySet(expr string) *charSet {
	if cached, ok := propertyCache.Load(expr); ok {
		return cached.(*charSet)
	}
	pairs, ok := propertyPairs(expr)
	if !ok {
		return nil
	}
	set := newCharSet(pairs, false)
	propertyCache.Store(expr, set)
	return set
}

// propertyPairs returns the ranges of a property expression. Every range comes from the tables of ucd, which hold one
// fixed version of Unicode, so the answer does not depend on the toolchain.
func propertyPairs(expr string) ([]rune, bool) {
	if name, value, ok := strings.Cut(expr, "="); ok {
		switch name {
		case "General_Category", "gc":
			return categoryPairs(value)
		case "Script", "sc":
			return pairsOf(ucd.Script(value))
		case "Script_Extensions", "scx":
			return pairsOf(ucd.ScriptExtensions(value))
		}
		return nil, false
	}
	if pairs, ok := categoryPairs(expr); ok {
		return pairs, true
	}
	switch canonical := binaryName(expr); canonical {
	case "":
		return nil, false
	case "ASCII":
		return []rune{0, 0x7F}, true
	case "Any":
		return []rune{0, maxRune}, true
	default:
		return pairsOf(ucd.Binary(canonical))
	}
}

func categoryPairs(value string) ([]rune, bool) {
	name := categoryName(value)
	if name == "" {
		return nil, false
	}
	return pairsOf(ucd.Category(name))
}

func pairsOf(r ucd.Ranges, ok bool) ([]rune, bool) {
	if !ok {
		return nil, false
	}
	return r.Pairs(), true
}

// intersect returns the code points in both sets.
func (s *charSet) intersect(other *charSet) *charSet {
	a, b := s.ranges, other.ranges
	i, j := 0, 0
	var pairs []rune
	for i+1 < len(a) && j+1 < len(b) {
		lo, hi := max(a[i], b[j]), min(a[i+1], b[j+1])
		if lo <= hi {
			pairs = append(pairs, lo, hi)
		}
		if a[i+1] < b[j+1] {
			i += 2
		} else {
			j += 2
		}
	}
	return newCharSet(pairs, false)
}

// subtract returns the code points of this set that are not in the other.
func (s *charSet) subtract(other *charSet) *charSet {
	return s.intersect(newCharSet(other.ranges, true))
}

// union returns the code points of either set.
func (s *charSet) union(other *charSet) *charSet {
	pairs := make([]rune, 0, len(s.ranges)+len(other.ranges))
	pairs = append(pairs, s.ranges...)
	pairs = append(pairs, other.ranges...)
	return newCharSet(pairs, false)
}

// complement returns the code points not in the set.
func (s *charSet) complement() *charSet {
	return newCharSet(s.ranges, true)
}

// stringProperty is the value of a property of strings, which only the v flag can name: its single code points and its
// sequences.
type stringProperty struct {
	set     *charSet
	strings [][]rune
}

var allStringProperties = sync.OnceValue(func() []stringProperty {
	all := make([]stringProperty, 0, len(stringProperties)+1)
	var union []rune
	var every [][]rune
	for _, row := range stringProperties {
		var pairs []rune
		for _, item := range strings.Fields(row[1]) {
			lo, hi, ok := strings.Cut(item, "..")
			if !ok {
				hi = lo
			}
			pairs = append(pairs, hexRune(lo), hexRune(hi))
		}
		var sequences [][]rune
		for _, s := range strings.Split(row[2], ";") {
			if s == "" {
				continue
			}
			fields := strings.Fields(s)
			seq := make([]rune, len(fields))
			for i, c := range fields {
				seq[i] = hexRune(c)
			}
			sequences = append(sequences, seq)
		}
		union = append(union, pairs...)
		every = append(every, sequences...)
		all = append(all, stringProperty{set: newCharSet(pairs, false), strings: sequences})
	}
	return append(all, stringProperty{set: newCharSet(union, false), strings: every})
})

func hexRune(s string) rune {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		panic("strings_gen.go has a code point that is not hexadecimal: " + s)
	}
	return rune(v)
}

// stringPropertyOf returns the property of strings of the name (RGI_Emoji and the six it is the union of), or nil.
func stringPropertyOf(name string) *stringProperty {
	all := allStringProperties()
	if name == "RGI_Emoji" {
		return &all[len(all)-1]
	}
	for k, row := range stringProperties {
		if row[0] == name {
			return &all[k]
		}
	}
	return nil
}

// ucdTable is a generated table of code points.
type ucdTable = ucd.Table

// mustBinaryTable returns the generated table of a binary property the engine relies on. Its absence means the
// generated tables are broken, so it panics.
func mustBinaryTable(name string) *ucdTable {
	t := ucd.BinaryTable(name)
	if t == nil {
		panic("the Unicode tables have no " + name)
	}
	return t
}
