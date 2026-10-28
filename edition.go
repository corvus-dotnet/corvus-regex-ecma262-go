package ecmaregex

// The edition of the grammar and the version of Unicode a pattern is read with (the Edition and UnicodeVersion of
// lib.rs, and RegexBuilder of regex.rs). Both are read when the pattern is compiled, so a match costs the same whichever
// is chosen.

import (
	"strconv"
	"sync"
	"time"

	"github.com/corvus-dotnet/corvus-regex-ecma262-go/internal/ucd"
)

// Edition is the edition of ECMA-262 whose grammar a pattern is read with.
type Edition uint8

const (
	// EditionCurrent is today's grammar, ECMAScript 2025: the default.
	EditionCurrent Edition = iota
	// EditionES2020 is the grammar of ECMAScript 2020 (ECMA-262, 11th edition), which JSON Schema 2020-12 names. It
	// refuses what later editions added, and nothing else: the d and v flags, modifier groups such as (?i:...), a group
	// name used by two groups, and a property name or value that is not in the edition's tables. A pattern it accepts
	// has the same answers as in today's grammar, under the same version of Unicode.
	EditionES2020
)

// String returns the name of the edition: "current" or "es2020".
func (e Edition) String() string {
	if e == EditionES2020 {
		return "es2020"
	}
	return "current"
}

// UnicodeVersion is the version of Unicode whose character database a pattern is read with: the members of each
// \p{...}, the characters of a group name, and the case folding of the i flag. It is independent of the Edition.
type UnicodeVersion uint8

const (
	// UnicodeDefault is the version of the V8 the answers are held to, Unicode 17 today. It moves when that V8 moves.
	UnicodeDefault UnicodeVersion = iota
	// Unicode13 is Unicode 13.0, the version of the year of ECMAScript 2020.
	Unicode13
	// Unicode17 is Unicode 17.0.
	Unicode17
	// Unicode18 is Unicode 18.0.
	Unicode18
	// UnicodeLatest is the latest version the module carries, Unicode 18 today. It moves when a later version is added.
	UnicodeLatest
)

// Resolve returns the version this one stands for: UnicodeDefault is V8's (Unicode17) and UnicodeLatest the newest the
// module carries (Unicode18). Any other is itself.
func (v UnicodeVersion) Resolve() UnicodeVersion {
	switch v {
	case UnicodeDefault:
		return Unicode17
	case UnicodeLatest:
		return Unicode18
	}
	return v
}

// Major returns the major number of the version, resolved: 13, 17 or 18.
func (v UnicodeVersion) Major() int {
	switch v.Resolve() {
	case Unicode13:
		return 13
	case Unicode18:
		return 18
	}
	return 17
}

// String returns the name of the version: "default", "13", "17", "18" or "latest".
func (v UnicodeVersion) String() string {
	switch v {
	case UnicodeDefault:
		return "default"
	case Unicode13:
		return "13"
	case Unicode17:
		return "17"
	case Unicode18:
		return "18"
	case UnicodeLatest:
		return "latest"
	}
	return "UnicodeVersion(" + strconv.Itoa(int(v)) + ")"
}

// Options are the settings of CompileOptions and IsValidOptions: the flags, and those beyond them, each independent of
// the others. The zero value is a pattern with no flags, no match timeout, today's grammar and V8's Unicode.
type Options struct {
	// Flags are the flags, as new RegExp(pattern, flags) takes them (see ParseFlags).
	Flags Flags
	// MatchTimeout is the time each match may take before it is abandoned, which brings a cap on the memory of its
	// backtracking with it: 0 for no limit. DefaultMatchTimeout is the timeout Compile gives.
	MatchTimeout time.Duration
	// Edition is the edition of ECMA-262 whose grammar the pattern is read with.
	Edition Edition
	// UnicodeVersion is the version of Unicode whose tables the pattern is read with.
	UnicodeVersion UnicodeVersion
}

// unicodeData is the Unicode of one version as the engine reads it: the tables of ucd, the properties of strings, and
// what is built from them on first use and then kept.
type unicodeData struct {
	ucd *ucd.UCD
	// strings are the properties of strings of the version (strings<major>_gen.go).
	strings *[6][3]string
	// unicodeFold and legacyFold are the equivalence classes of Canonicalize under the u flag's grammar and under the
	// grammar of no flag (fold.go).
	unicodeFold, legacyFold func() *foldTable
	// stringProperties are the properties of strings, read from strings (charset.go).
	stringProperties func() []stringProperty
	// idStart and idContinue are the tables a group name is read with (parse.go).
	idStart, idContinue func() *ucdTable
	// propertyCache holds the sets already built for property expressions (charset.go).
	propertyCache sync.Map // map[string]*charSet
}

func newUnicodeData(u *ucd.UCD, strings *[6][3]string) *unicodeData {
	return &unicodeData{
		ucd:              u,
		strings:          strings,
		unicodeFold:      sync.OnceValue(func() *foldTable { return unicodeFoldTable(u) }),
		legacyFold:       sync.OnceValue(func() *foldTable { return legacyFoldTable(u) }),
		stringProperties: sync.OnceValue(func() []stringProperty { return readStringProperties(strings) }),
		idStart:          sync.OnceValue(func() *ucdTable { return mustBinaryTable(u, "ID_Start") }),
		idContinue:       sync.OnceValue(func() *ucdTable { return mustBinaryTable(u, "ID_Continue") }),
	}
}

// The versions of Unicode the module carries.
var (
	unicode13 = newUnicodeData(ucd.V13, &stringProperties13)
	unicode17 = newUnicodeData(ucd.V17, &stringProperties17)
	unicode18 = newUnicodeData(ucd.V18, &stringProperties18)
)

// unicodeByMajor returns the Unicode of a version by its major number: 13, 17 or 18. A backreference carries the
// number of the version it folds case with (compile.go), which is never another.
func unicodeByMajor(major int32) *unicodeData {
	switch major {
	case 13:
		return unicode13
	case 18:
		return unicode18
	}
	return unicode17
}

// data returns the Unicode of the version, resolved.
func (v UnicodeVersion) data() *unicodeData {
	return unicodeByMajor(int32(v.Major()))
}
