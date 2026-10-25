// Package ecmaregex is regular expressions with the grammar and the answers of ECMA-262, the RegExp of JavaScript.
//
//	re := ecmaregex.MustCompile(`(?<year>\d{4})-(?<month>\d{2})`, "u")
//	re.MatchString("on 2026-10-25")                    // true
//	re.ReplaceAllString("2026-10-25", "$<month>/$<year>") // "10/2026-25"
//
// A pattern is read as new RegExp(pattern, flags) reads it: with the u or v flag by the grammar of the u flag and
// matched by code point; with neither by the Annex B grammar and matched by UTF-16 code unit, as ECMA-262 matches a
// pattern with no u flag. The i, m and s flags are the modifiers in force at the start of the pattern, which a modifier
// group of ECMAScript 2025 ((?i:...), (?-m:...)) changes for part of it.
//
// Text is UTF-8 (a string) or UTF-16 ([]uint16, which may hold lone surrogates, with the methods ending in UTF16).
// Positions are in the units of the text: bytes of UTF-8, or UTF-16 code units. Nothing is transcoded. Text that is not
// UTF-8 is refused: a Try method returns an error, and any other panics.
//
// A pattern has a match timeout, as a Regex of .NET's System.Text.RegularExpressions has: the time each match may
// take, which brings a cap on the memory of its backtracking with it. A pattern compiled with one should be matched
// with the Try methods, which return ErrTime or ErrMemory when a match reaches a limit; the others panic then. A
// pattern without a timeout never reaches a limit. SetDefaultMatchTimeout gives every pattern compiled after it one.
//
// The engine is a port of the Rust crate corvus-regex-ecma262, file for file (parse.go is parse.rs, compile.go is
// compile.rs, and so on), held to the same ECMA-262 test data.
package ecmaregex

//go:generate go run ./internal/expand

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// Version is the version of the module. A release writes its tag's version here; in the repository it is 0.0.0.
const Version = "0.0.0"

// Flags are the flags of a pattern, as new RegExp(pattern, flags) reads them.
type Flags struct {
	HasIndices  bool // d
	Global      bool // g
	IgnoreCase  bool // i
	Multiline   bool // m
	DotAll      bool // s
	Unicode     bool // u
	UnicodeSets bool // v
	Sticky      bool // y
}

// ParseFlags reads flags written as new RegExp reads them, such as "giu". A flag that is not one of dgimsuvy, a flag
// given twice, or u with v, is an error of kind ErrFlags.
func ParseFlags(flags string) (Flags, error) {
	var f Flags
	for _, c := range flags {
		var flag *bool
		switch c {
		case 'd':
			flag = &f.HasIndices
		case 'g':
			flag = &f.Global
		case 'i':
			flag = &f.IgnoreCase
		case 'm':
			flag = &f.Multiline
		case 's':
			flag = &f.DotAll
		case 'u':
			flag = &f.Unicode
		case 'v':
			flag = &f.UnicodeSets
		case 'y':
			flag = &f.Sticky
		default:
			return Flags{}, &Error{Kind: ErrFlags, msg: fmt.Sprintf("invalid flag %q in %q", c, flags)}
		}
		if *flag {
			return Flags{}, &Error{Kind: ErrFlags, msg: fmt.Sprintf("the flag %q is given twice in %q", c, flags)}
		}
		*flag = true
	}
	if f.Unicode && f.UnicodeSets {
		return Flags{}, &Error{Kind: ErrFlags, msg: fmt.Sprintf("the flags u and v are given together in %q", flags)}
	}
	return f, nil
}

// String returns the flags as RegExp writes them, in the order dgimsuvy.
func (f Flags) String() string {
	var b strings.Builder
	for _, x := range []struct {
		on bool
		c  byte
	}{{f.HasIndices, 'd'}, {f.Global, 'g'}, {f.IgnoreCase, 'i'}, {f.Multiline, 'm'}, {f.DotAll, 's'}, {f.Unicode, 'u'},
		{f.UnicodeSets, 'v'}, {f.Sticky, 'y'}} {
		if x.on {
			b.WriteByte(x.c)
		}
	}
	return b.String()
}

func (f Flags) syntax() syntax {
	return syntax{unicode: f.Unicode || f.UnicodeSets, sets: f.UnicodeSets, icase: f.IgnoreCase, multiline: f.Multiline,
		dotAll: f.DotAll}
}

// ErrorKind says why a pattern was not compiled.
type ErrorKind uint8

const (
	// ErrInvalid says the pattern is not valid under the grammar its flags choose.
	ErrInvalid ErrorKind = iota
	// ErrFlags says the flags are not valid.
	ErrFlags
	// ErrTooLarge says the pattern is over a limit of size (see TooLargeError), which says nothing of whether it is
	// valid.
	ErrTooLarge
	// ErrEncoding says the pattern or a text is not UTF-8.
	ErrEncoding
)

// Error is why a pattern was not compiled, or why a text was refused.
type Error struct {
	Kind ErrorKind
	// TooLarge says which limit a pattern of kind ErrTooLarge is over.
	TooLarge *TooLargeError
	msg      string
}

func (e *Error) Error() string { return e.msg }

// Unwrap returns the *TooLargeError of a pattern over a limit of size.
func (e *Error) Unwrap() error {
	if e.TooLarge != nil {
		return e.TooLarge
	}
	return nil
}

var errNotUTF8 = &Error{Kind: ErrEncoding, msg: "ecmaregex: the text is not UTF-8"}

var defaultMatchTimeout atomic.Int64

// SetDefaultMatchTimeout gives every pattern compiled after it, without a timeout of its own, the match timeout d. Zero
// is no timeout.
func SetDefaultMatchTimeout(d time.Duration) { defaultMatchTimeout.Store(int64(max(d, 0))) }

// DefaultMatchTimeout returns the match timeout a pattern compiled now without one of its own takes, or 0 for none.
func DefaultMatchTimeout() time.Duration { return time.Duration(defaultMatchTimeout.Load()) }

// groupName is the name of one or more groups, in each encoding.
type groupInfoName struct {
	name   string
	name16 []uint16
	groups []int
}

// groupInfo is what a pattern says of its groups: how many, and their names.
type groupInfo struct {
	groups int
	names  []groupInfoName
}

func (g *groupInfo) named8(name []byte) []int {
	for k := range g.names {
		if g.names[k].name == string(name) {
			return g.names[k].groups
		}
	}
	return nil
}

func (g *groupInfo) named16(name []uint16) []int {
	for k := range g.names {
		if equal16(g.names[k].name16, name) {
			return g.names[k].groups
		}
	}
	return nil
}

// Regexp is a compiled pattern. It is safe for concurrent use.
type Regexp struct {
	pattern string
	flags   Flags
	prog    *program
	// capsOnce builds caps, the program that records every group, on the first match that reports them.
	capsOnce sync.Once
	caps     *program
	info     *groupInfo
	timeout  time.Duration
	// regular says the pattern needs no backtracking and is small enough for a linear matcher (re2Cost), so a test from
	// the start of a text can be answered by the automaton over ASCII (see dfa.go), built on the first such test.
	regular bool
	dfaOnce sync.Once
	dfa     *dfa
}

// Compile compiles a pattern with flags written as new RegExp reads them, such as "giu", and the default match
// timeout.
func Compile(pattern, flags string) (*Regexp, error) {
	f, err := ParseFlags(flags)
	if err != nil {
		return nil, err
	}
	return CompileFlags(pattern, f, DefaultMatchTimeout())
}

// MustCompile is Compile, which panics when the pattern is not compiled.
func MustCompile(pattern, flags string) *Regexp {
	re, err := Compile(pattern, flags)
	if err != nil {
		panic(err)
	}
	return re
}

// CompileFlags compiles a pattern with the flags and the match timeout (0 for none).
func CompileFlags(pattern string, flags Flags, matchTimeout time.Duration) (*Regexp, error) {
	if !utf8.ValidString(pattern) {
		return nil, &Error{Kind: ErrEncoding, msg: fmt.Sprintf("ecmaregex: the pattern %q is not UTF-8", pattern)}
	}
	root, ps, err := parseWith(pattern, flags.syntax())
	if err != nil {
		return nil, compileError(pattern, err)
	}
	info := &groupInfo{groups: int(ps.groups)}
	for _, n := range ps.groupNames() {
		name := string(n.name)
		info.names = append(info.names, groupInfoName{name: name, name16: utf16.Encode([]rune(name)), groups: n.groups})
	}
	regular := !ps.needsBacktracking && re2Cost(root) <= maxRe2Cost
	return &Regexp{
		pattern: pattern,
		flags:   flags,
		prog:    compileProgram(root, flags.Unicode || flags.UnicodeSets, -1),
		info:    info,
		timeout: max(matchTimeout, 0),
		regular: regular,
	}, nil
}

// IsValid reports whether a pattern is valid with the flags.
func IsValid(pattern, flags string) bool {
	f, err := ParseFlags(flags)
	if err != nil || !utf8.ValidString(pattern) {
		return false
	}
	_, _, err = parseWith(pattern, f.syntax())
	return err == nil
}

func compileError(pattern string, err error) error {
	switch e := err.(type) {
	case *TooLargeError:
		return &Error{Kind: ErrTooLarge, TooLarge: e, msg: "ecmaregex: " + e.Error()}
	case *syntaxError:
		return &Error{Kind: ErrInvalid, msg: fmt.Sprintf("ecmaregex: %s at offset %d in %q", e.msg, e.offset, pattern)}
	}
	return err
}

// String returns the pattern as a RegExp literal writes it, /pattern/flags.
func (re *Regexp) String() string { return "/" + re.pattern + "/" + re.flags.String() }

// Pattern returns the pattern as it was compiled.
func (re *Regexp) Pattern() string { return re.pattern }

// Flags returns the pattern's flags.
func (re *Regexp) Flags() Flags { return re.flags }

// MatchTimeout returns the time each match may take, or 0 for no limit.
func (re *Regexp) MatchTimeout() time.Duration { return re.timeout }

// NumSubexp returns the number of capturing groups of the pattern.
func (re *Regexp) NumSubexp() int { return re.info.groups }

// SubexpNames returns the name of each group, the whole match first (always ""). A group with no name has "". Two
// groups in separate alternatives can share a name.
func (re *Regexp) SubexpNames() []string {
	names := make([]string, re.info.groups+1)
	for _, n := range re.info.names {
		for _, g := range n.groups {
			names[g] = n.name
		}
	}
	return names
}

// SubexpIndex returns the groups with the name, which is more than one where groups in separate alternatives share
// it, or nil.
func (re *Regexp) SubexpIndex(name string) []int {
	for _, n := range re.info.names {
		if n.name == name {
			return append([]int(nil), n.groups...)
		}
	}
	return nil
}

func (re *Regexp) unicode() bool { return re.flags.Unicode || re.flags.UnicodeSets }

// capsProgram returns the program that records every group.
func (re *Regexp) capsProgram() *program {
	if re.prog.groups == re.info.groups {
		return re.prog
	}
	re.capsOnce.Do(func() {
		root, _, err := parseWith(re.pattern, re.flags.syntax())
		if err != nil {
			panic("ecmaregex: a compiled pattern was not read again: " + err.Error())
		}
		re.caps = compileProgram(root, re.unicode(), re.info.groups)
	})
	return re.caps
}

// automaton returns the automaton over ASCII of a regular pattern, or nil when it has none.
func (re *Regexp) automaton() *dfa {
	re.dfaOnce.Do(func() {
		root, _, err := parseWith(re.pattern, re.flags.syntax())
		if err == nil {
			re.dfa = buildDfa(root)
		}
	})
	return re.dfa
}

// test8 reports whether the pattern matches at or after start (at start alone with the y flag): from the start of an
// ASCII text by the automaton where the pattern has one, and otherwise by the matcher.
func (re *Regexp) test8(input []byte, start int) (bool, error) {
	if start == 0 && !re.flags.Sticky && re.regular {
		if d := re.automaton(); d != nil {
			if matched, decided := d.isMatch8(input); decided {
				return matched, nil
			}
		}
	}
	_, _, found, err := re.exec8(input, start, re.flags.Sticky, nil)
	return found, err
}

// test16 is test8 over UTF-16 text.
func (re *Regexp) test16(input []uint16, start int) (bool, error) {
	if start == 0 && !re.flags.Sticky && re.regular {
		if d := re.automaton(); d != nil {
			if matched, decided := d.isMatch16(input); decided {
				return matched, nil
			}
		}
	}
	_, _, found, err := re.exec16(input, start, re.flags.Sticky, nil)
	return found, err
}

// bytesOf returns the bytes of a string, without a copy. Nothing writes to them.
func bytesOf(s string) []byte { return unsafe.Slice(unsafe.StringData(s), len(s)) }

// stringOf returns bytes as a string, without a copy. The bytes are never written to after.
func stringOf(b []byte) string { return unsafe.String(unsafe.SliceData(b), len(b)) }

// must panics with a match's error, for the methods that return none.
func must[V any](v V, err error) V {
	if err != nil {
		panic(err)
	}
	return v
}

// Piece is one item of a split: a piece of the text, or what a group that did not take part leaves, which ECMA-262
// gives as undefined.
type Piece struct {
	Text    string
	Defined bool
}

// Piece16 is a Piece of UTF-16 text.
type Piece16 struct {
	Text    []uint16
	Defined bool
}

// The UTF-8 methods. Each checks that the text is UTF-8; NewText makes a text that is checked once.

// TryMatchString reports whether the pattern matches the text.
func (re *Regexp) TryMatchString(s string) (bool, error) { return re.TryMatchStringAt(s, 0) }

// TryMatchStringAt reports whether the pattern matches the text from the byte offset start.
func (re *Regexp) TryMatchStringAt(s string, start int) (bool, error) {
	if !utf8.ValidString(s) {
		return false, errNotUTF8
	}
	return re.test8(bytesOf(s), start)
}

// MatchString is TryMatchString, which panics when a match reaches a limit or the text is not UTF-8.
func (re *Regexp) MatchString(s string) bool { return must(re.TryMatchString(s)) }

// TryFindStringIndex returns the start and end of the first match, or nil.
func (re *Regexp) TryFindStringIndex(s string) ([]int, error) { return re.TryFindStringIndexAt(s, 0) }

// TryFindStringIndexAt returns the start and end of the first match at or after the byte offset start (at it alone
// with the y flag), or nil.
func (re *Regexp) TryFindStringIndexAt(s string, start int) ([]int, error) {
	if !utf8.ValidString(s) {
		return nil, errNotUTF8
	}
	from, to, found, err := re.exec8(bytesOf(s), start, re.flags.Sticky, nil)
	if !found || err != nil {
		return nil, err
	}
	return []int{from, to}, nil
}

// FindStringIndex is TryFindStringIndex, which panics when a match reaches a limit or the text is not UTF-8.
func (re *Regexp) FindStringIndex(s string) []int { return must(re.TryFindStringIndex(s)) }

// TryFindStringSubmatchIndex returns the start and end of the first match and of each group, -1 for a group that did
// not take part, or nil.
func (re *Regexp) TryFindStringSubmatchIndex(s string) ([]int, error) {
	return re.TryFindStringSubmatchIndexAt(s, 0)
}

// TryFindStringSubmatchIndexAt is TryFindStringSubmatchIndex from the byte offset start.
func (re *Regexp) TryFindStringSubmatchIndexAt(s string, start int) ([]int, error) {
	if !utf8.ValidString(s) {
		return nil, errNotUTF8
	}
	slots := make([]int, 2*(re.info.groups+1))
	_, _, found, err := re.exec8(bytesOf(s), start, re.flags.Sticky, slots)
	if !found || err != nil {
		return nil, err
	}
	return slots, nil
}

// FindStringSubmatchIndex is TryFindStringSubmatchIndex, which panics when a match reaches a limit or the text is not
// UTF-8.
func (re *Regexp) FindStringSubmatchIndex(s string) []int {
	return must(re.TryFindStringSubmatchIndex(s))
}

// TryFindAllStringIndex returns the start and end of each match of a global search, at most n of them (all when n is
// below 0). After an empty match the search moves on one character, as matchAll does.
func (re *Regexp) TryFindAllStringIndex(s string, n int) ([][]int, error) {
	if !utf8.ValidString(s) {
		return nil, errNotUTF8
	}
	return re.findAll8(bytesOf(s), n, false)
}

// FindAllStringIndex is TryFindAllStringIndex, which panics when a match reaches a limit or the text is not UTF-8.
func (re *Regexp) FindAllStringIndex(s string, n int) [][]int {
	return must(re.TryFindAllStringIndex(s, n))
}

// TryFindAllStringSubmatchIndex is TryFindAllStringIndex with the start and end of each group of each match.
func (re *Regexp) TryFindAllStringSubmatchIndex(s string, n int) ([][]int, error) {
	if !utf8.ValidString(s) {
		return nil, errNotUTF8
	}
	return re.findAll8(bytesOf(s), n, true)
}

// FindAllStringSubmatchIndex is TryFindAllStringSubmatchIndex, which panics when a match reaches a limit or the text
// is not UTF-8.
func (re *Regexp) FindAllStringSubmatchIndex(s string, n int) [][]int {
	return must(re.TryFindAllStringSubmatchIndex(s, n))
}

// TryReplaceString replaces the first match, or every match with the g flag, as String.prototype.replace does, with the
// template's $ forms ($$, $&, $`, $', $n, $nn and $<name>) replaced.
func (re *Regexp) TryReplaceString(src, template string) (string, error) {
	if !utf8.ValidString(src) || !utf8.ValidString(template) {
		return "", errNotUTF8
	}
	out, err := re.replace8(bytesOf(src), bytesOf(template), re.flags.Global)
	return stringOf(out), err
}

// ReplaceString is TryReplaceString, which panics when a match reaches a limit or a text is not UTF-8.
func (re *Regexp) ReplaceString(src, template string) string {
	return must(re.TryReplaceString(src, template))
}

// TryReplaceAllString replaces every match, as String.prototype.replaceAll does.
func (re *Regexp) TryReplaceAllString(src, template string) (string, error) {
	if !utf8.ValidString(src) || !utf8.ValidString(template) {
		return "", errNotUTF8
	}
	out, err := re.replace8(bytesOf(src), bytesOf(template), true)
	return stringOf(out), err
}

// ReplaceAllString is TryReplaceAllString, which panics when a match reaches a limit or a text is not UTF-8.
func (re *Regexp) ReplaceAllString(src, template string) string {
	return must(re.TryReplaceAllString(src, template))
}

// TrySplit splits the text around the matches, as String.prototype.split does: the groups of each match come between
// the pieces, and at most limit items (all when limit is below 0).
func (re *Regexp) TrySplit(s string, limit int) ([]Piece, error) {
	if !utf8.ValidString(s) {
		return nil, errNotUTF8
	}
	spans, err := re.split8(bytesOf(s), limit)
	pieces := make([]Piece, len(spans))
	for k, sp := range spans {
		if sp.from >= 0 {
			pieces[k] = Piece{Text: piece8(s, sp.from, sp.to), Defined: true}
		}
	}
	return pieces, err
}

// Split is TrySplit, which panics when a match reaches a limit or the text is not UTF-8.
func (re *Regexp) Split(s string, limit int) []Piece { return must(re.TrySplit(s, limit)) }

// The UTF-16 methods, over text that may hold lone surrogates. Positions are code units.

// TryMatchUTF16 reports whether the pattern matches the text.
func (re *Regexp) TryMatchUTF16(u []uint16) (bool, error) { return re.TryMatchUTF16At(u, 0) }

// TryMatchUTF16At reports whether the pattern matches the text from the code unit start.
func (re *Regexp) TryMatchUTF16At(u []uint16, start int) (bool, error) { return re.test16(u, start) }

// MatchUTF16 is TryMatchUTF16, which panics when a match reaches a limit.
func (re *Regexp) MatchUTF16(u []uint16) bool { return must(re.TryMatchUTF16(u)) }

// TryFindUTF16Index returns the start and end of the first match at or after the code unit start, or nil.
func (re *Regexp) TryFindUTF16Index(u []uint16, start int) ([]int, error) {
	from, to, found, err := re.exec16(u, start, re.flags.Sticky, nil)
	if !found || err != nil {
		return nil, err
	}
	return []int{from, to}, nil
}

// TryFindUTF16SubmatchIndex returns the start and end of the first match at or after the code unit start and of each
// group, -1 for a group that did not take part, or nil.
func (re *Regexp) TryFindUTF16SubmatchIndex(u []uint16, start int) ([]int, error) {
	slots := make([]int, 2*(re.info.groups+1))
	_, _, found, err := re.exec16(u, start, re.flags.Sticky, slots)
	if !found || err != nil {
		return nil, err
	}
	return slots, nil
}

// TryFindAllUTF16Index returns the start and end of each match of a global search, at most n of them (all when n is
// below 0).
func (re *Regexp) TryFindAllUTF16Index(u []uint16, n int) ([][]int, error) {
	return re.findAll16(u, n, false)
}

// TryFindAllUTF16SubmatchIndex is TryFindAllUTF16Index with the start and end of each group of each match.
func (re *Regexp) TryFindAllUTF16SubmatchIndex(u []uint16, n int) ([][]int, error) {
	return re.findAll16(u, n, true)
}

// TryReplaceUTF16 replaces the first match, or every match with the g flag, as String.prototype.replace does.
func (re *Regexp) TryReplaceUTF16(src, template []uint16) ([]uint16, error) {
	return re.replace16(src, template, re.flags.Global)
}

// TryReplaceAllUTF16 replaces every match, as String.prototype.replaceAll does.
func (re *Regexp) TryReplaceAllUTF16(src, template []uint16) ([]uint16, error) {
	return re.replace16(src, template, true)
}

// TrySplitUTF16 splits the text around the matches, as String.prototype.split does.
func (re *Regexp) TrySplitUTF16(u []uint16, limit int) ([]Piece16, error) {
	spans, err := re.split16(u, limit)
	pieces := make([]Piece16, len(spans))
	for k, sp := range spans {
		if sp.from >= 0 {
			pieces[k] = Piece16{Text: u[sp.from:sp.to:sp.to], Defined: true}
		}
	}
	return pieces, err
}

// piece8 returns s[from:to], or a copy with U+FFFD for a half character at a bound between the halves of a character
// of four bytes, which only a pattern without the u flag can make.
func piece8(s string, from, to int) string {
	if insideFour(bytesOf(s), from) || (to < len(s) && insideFour(bytesOf(s), to)) {
		return stringOf(pushLossy8(nil, bytesOf(s), from, to))
	}
	return s[from:to]
}

// span is a piece of a text, or from -1 for a group that did not take part.
type span struct{ from, to int }
