package ecmaregex

// The parser: a pattern to its syntax tree, in either grammar, with the limits of size (parse.rs).

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

type nodeKind uint8

const (
	nodeEmpty nodeKind = iota
	nodeChar
	nodeSet
	nodeCat
	nodeAlt
	nodeGroup
	nodeRepeat
	nodeBol
	nodeEol
	// nodeLineStart and nodeLineEnd are ^ and $ inside a group with the m modifier.
	nodeLineStart
	nodeLineEnd
	nodeWordBoundary
	nodeNotWordBoundary
	nodeLook
	nodeBackref
)

// unbounded is the max of a quantifier with no upper bound.
const unbounded int32 = -1

// maxCount caps the bounds of a counted quantifier. No text that fits in memory reaches a larger count.
const maxCount int64 = 1<<31 - 1

// The limits on the size of one pattern. A pattern may come from outside, so reading and compiling one must take
// bounded time and memory whatever it holds. A pattern over a limit is refused with a *TooLargeError, which is not
// the error of a pattern that is not valid.
const (
	// MaxPatternLength is the longest pattern that is read, in bytes of UTF-8.
	MaxPatternLength = 1 << 20
	// MaxNesting caps the depth of groups, which bounds the recursion of the parser and of the compilers.
	MaxNesting = 1000
	// MaxSize is the largest size a pattern may have, in the bytes the parser counts for it: nodeSize for each node of
	// its syntax tree, and rangeSize for each range of a class as its members are read, for each range a case
	// insensitive group or a negated property adds, for each alternative that encloses a named group and for each
	// group that shares its name. The tree, the program compiled from it and the time to make both are proportional
	// to that size, so it bounds them all: a pattern at the limit takes a few milliseconds and a few megabytes.
	MaxSize = 1 << 20

	nodeSize  = 32
	rangeSize = 8
)

// Limit says which limit of size a pattern is over.
type Limit uint8

const (
	// LimitLength is MaxPatternLength.
	LimitLength Limit = iota
	// LimitNesting is MaxNesting.
	LimitNesting
	// LimitSize is MaxSize.
	LimitSize
)

// String returns the name of the limit: "length", "nesting" or "size".
func (l Limit) String() string {
	switch l {
	case LimitLength:
		return "length"
	case LimitNesting:
		return "nesting"
	}
	return "size"
}

// TooLargeError reports a pattern over one of the limits of size. Limit names the limit and Value is its value. The
// pattern was not read to its end, so the error says nothing of whether it is valid.
type TooLargeError struct {
	Limit Limit
	Value int
}

func (e *TooLargeError) Error() string {
	switch e.Limit {
	case LimitLength:
		return fmt.Sprintf("the pattern is longer than %d bytes", e.Value)
	case LimitNesting:
		return fmt.Sprintf("the groups of the pattern nest more than %d deep", e.Value)
	}
	return fmt.Sprintf("the pattern has a size of more than %d bytes when it is read", e.Value)
}

// syntaxError reports a pattern that is not valid under the chosen grammar: the reason, and the offset in the
// pattern's characters.
type syntaxError struct {
	msg    string
	offset int
}

func (e *syntaxError) Error() string { return e.msg }

// node is one element of the syntax tree of a pattern.
type node struct {
	kind nodeKind
	// r is the code point of a nodeChar.
	r rune
	// set is the class of a nodeSet.
	set *charSet
	// subs are the children of nodeCat and nodeAlt, and the one child of nodeGroup, nodeRepeat and nodeLook.
	subs []*node
	// min, max and lazy describe a nodeRepeat.
	min, max int32
	lazy     bool
	// group is the number of the capturing group of a nodeGroup, counted from 1.
	group int32
	// groups are the groups a nodeBackref names. A name can belong to several groups in separate alternatives, of
	// which at most one has taken part at any time.
	groups []int32
	// name is the group name of a named backreference until it is resolved.
	name []rune
	// fold marks a nodeBackref, nodeWordBoundary or nodeNotWordBoundary inside a case-insensitive group.
	fold bool
	// foldVersion is the major number of the version of Unicode whose case folding a nodeBackref matches with.
	foldVersion int32
	// behind and negative describe a nodeLook.
	behind, negative bool
}

func newNode(kind nodeKind) *node                 { return &node{kind: kind} }
func withSubs(kind nodeKind, subs ...*node) *node { return &node{kind: kind, subs: subs} }
func charNode(r rune) *node                       { return &node{kind: nodeChar, r: r} }
func setNode(set *charSet) *node                  { return &node{kind: nodeSet, set: set} }

// branch names one alternative of one disjunction.
type branch struct {
	disjunction, alternative int
}

// namedGroup is a capturing group with a name, and the alternatives that enclose it.
type namedGroup struct {
	group int32
	path  []branch
}

// parser reads a pattern by recursive descent. With unicode set it applies the grammar of the u flag. Without it, it
// applies the Annex B grammar of patterns with no flag.
type parser struct {
	// p is the pattern as code points with the u flag, and as UTF-16 code units without it (see codeUnits).
	p       []rune
	i       int
	unicode bool
	// sets is the grammar of the v flag.
	sets bool
	// escapeStrings are the sequences of the property of strings the last class escape named (the v flag), or nil.
	escapeStrings [][]rune
	// uc is the Unicode of the version the pattern is read with.
	uc *unicodeData
	// es2020 is the grammar of ECMAScript 2020, which refuses what later editions added.
	es2020 bool
	depth  int
	// groupCount and hasNames come from a scan of the whole pattern before parsing, because the meaning of \1 and \k
	// depends on groups that may follow them.
	groupCount int64
	hasNames   bool
	groups     int32
	// names holds the groups of each name, keyed by the name (whose characters are never surrogates, so the string
	// keeps them all).
	names     map[string][]namedGroup
	namedRefs bool
	// path is the alternatives that enclose the current position, innermost last.
	path         []branch
	disjunctions int
	// icase, multiline and dotAll are the modifiers in force, which only a group such as (?i:...) changes.
	icase, multiline, dotAll bool
	// needsBacktracking records that the pattern has a construct only the backtracking matcher can run.
	needsBacktracking bool
	// size is the size of what has been read so far (see MaxSize).
	size int
}

var (
	anySet      = sync.OnceValue(func() *charSet { return newCharSet([]rune{0, maxRune}, false) })
	digitSet    = sync.OnceValue(func() *charSet { return newCharSet(digitPairs, false) })
	notDigitSet = sync.OnceValue(func() *charSet { return newCharSet(digitPairs, true) })
	wordSet     = sync.OnceValue(func() *charSet { return newCharSet(wordPairs, false) })
	notWordSet  = sync.OnceValue(func() *charSet { return newCharSet(wordPairs, true) })
	spaceSet    = sync.OnceValue(func() *charSet { return newCharSet(spacePairs, false) })
	notSpaceSet = sync.OnceValue(func() *charSet { return newCharSet(spacePairs, true) })
)

// syntax is the grammar a pattern is read with and the flags that change what its atoms mean.
type syntax struct {
	// unicode is the grammar of the u flag. Without it, the Annex B grammar of a pattern with no u or v flag.
	unicode bool
	// sets is the grammar of the v flag inside a class, and the properties of strings. It comes with unicode.
	sets bool
	// icase, multiline and dotAll are the i, m and s flags: the modifiers in force at the start of the pattern.
	icase, multiline, dotAll bool
	// edition is the edition of ECMA-262 whose grammar the pattern is read with.
	edition Edition
	// unicodeVersion is the version of Unicode whose tables the pattern is read with.
	unicodeVersion UnicodeVersion
}

// parseWith reads the pattern. It returns a *syntaxError for a pattern that is not valid under the chosen grammar,
// and a *TooLargeError for a pattern over a limit of size, which it stops reading at the limit.
func parseWith(pattern string, sx syntax) (*node, *parser, error) {
	if len(pattern) > MaxPatternLength {
		return nil, nil, &TooLargeError{Limit: LimitLength, Value: MaxPatternLength}
	}
	p := []rune(pattern)
	if !sx.unicode {
		p = codeUnits(p)
	}
	ps := &parser{
		p:         p,
		unicode:   sx.unicode,
		sets:      sx.sets,
		uc:        sx.unicodeVersion.data(),
		es2020:    sx.edition == EditionES2020,
		names:     map[string][]namedGroup{},
		icase:     sx.icase,
		multiline: sx.multiline,
		dotAll:    sx.dotAll,
	}
	ps.scanGroups()
	root, err := ps.disjunction()
	if err != nil {
		return nil, nil, err
	}
	if ps.i < len(ps.p) {
		if ps.p[ps.i] == ')' {
			return nil, nil, ps.fail("unmatched ')'")
		}
		return nil, nil, ps.fail("unexpected character")
	}
	if ps.namedRefs {
		if err := ps.resolve(root); err != nil {
			return nil, nil, err
		}
	}
	return root, ps, nil
}

// codeUnits returns the UTF-16 code units of a pattern. Without the u flag ECMA-262 reads a pattern, and matches a
// text, a code unit at a time. A character beyond the Basic Multilingual Plane is then two atoms, a quantifier after it
// repeats the second, and in a class it is two members, so a range between two such characters runs from a low
// surrogate to a high one and is an error.
func codeUnits(p []rune) []rune {
	n := 0
	for _, c := range p {
		if c > 0xFFFF {
			n++
		}
	}
	if n == 0 {
		return p
	}
	units := make([]rune, 0, len(p)+n)
	for _, c := range p {
		if c > 0xFFFF {
			units = append(units, highSurrogate(c), lowSurrogate(c))
		} else {
			units = append(units, c)
		}
	}
	return units
}

func isDigit(c rune) bool { return '0' <= c && c <= '9' }

// greaterAsWritten reports whether the first of two runs of decimal digits is the greater number. The runs are
// compared as written, because a count has no limit of size in the grammar and number stops at maxCount.
func greaterAsWritten(a, b []rune) bool {
	for len(a) > 0 && a[0] == '0' {
		a = a[1:]
	}
	for len(b) > 0 && b[0] == '0' {
		b = b[1:]
	}
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	for k := range a {
		if a[k] != b[k] {
			return a[k] > b[k]
		}
	}
	return false
}

// separateAlternatives reports whether two positions lie in different alternatives of some disjunction.
func separateAlternatives(a, b []branch) bool {
	for k := 0; k < len(a) && k < len(b); k++ {
		if a[k].disjunction != b[k].disjunction {
			break
		}
		if a[k].alternative != b[k].alternative {
			return true
		}
	}
	return false
}

func hexValue(c rune) rune {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10
	}
	return -1
}

func isASCIILetter(c rune) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// inASCII reports whether c is one of the ASCII characters of set.
func inASCII(c rune, set string) bool {
	return 0 <= c && c < 0x80 && strings.IndexByte(set, byte(c)) >= 0
}

// foldVersion is the major number of the version of Unicode the pattern is read with, which a backreference folds case
// with.
func (ps *parser) foldVersion() int32 { return int32(ps.uc.ucd.Major()) }

// syntaxCharacter reports whether c is one of the characters the u flag lets an identity escape name.
func syntaxCharacter(c rune) bool { return inASCII(c, `^$\.*+?()[]{}|/`) }

func appendClassAtom(pairs []rune, c rune, set *charSet) []rune {
	if set != nil {
		return append(pairs, set.ranges...)
	}
	return append(pairs, c, c)
}

// groupNames returns the names of the groups, each with the groups that have it, in the order of their first group.
func (ps *parser) groupNames() []groupName {
	names := make([]groupName, 0, len(ps.names))
	for name, groups := range ps.names {
		g := make([]int, len(groups))
		for k, ng := range groups {
			g[k] = int(ng.group)
		}
		names = append(names, groupName{name: []rune(name), groups: g})
	}
	slices.SortFunc(names, func(a, b groupName) int { return a.groups[0] - b.groups[0] })
	return names
}

// groupName is a group name with the groups that have it.
type groupName struct {
	name   []rune
	groups []int
}

// charge adds to the size of the pattern, and stops the parser when the size is over its limit.
func (ps *parser) charge(bytes int) error {
	ps.size += bytes
	if ps.size > MaxSize {
		return &TooLargeError{Limit: LimitSize, Value: MaxSize}
	}
	return nil
}

func (ps *parser) fail(msg string) error {
	return &syntaxError{msg: msg, offset: ps.i}
}

// resolve gives every named backreference the groups of its name. A name no group has is an error.
func (ps *parser) resolve(n *node) error {
	if n.kind == nodeBackref && len(n.name) > 0 {
		named, ok := ps.names[string(n.name)]
		if !ok {
			return ps.fail("reference to an unknown group name")
		}
		for _, g := range named {
			n.groups = append(n.groups, g.group)
		}
	}
	for _, sub := range n.subs {
		if err := ps.resolve(sub); err != nil {
			return err
		}
	}
	return nil
}

// scanGroups counts the capturing groups of the pattern and notes whether any is named.
func (ps *parser) scanGroups() {
	inClass := false
	s := ps.p
	for k := 0; k < len(s); k++ {
		switch c := s[k]; {
		case c == '\\':
			k++
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '(' && !inClass:
			if k+1 >= len(s) || s[k+1] != '?' {
				ps.groupCount++
			} else if k+3 < len(s) && s[k+2] == '<' && s[k+3] != '=' && s[k+3] != '!' {
				ps.groupCount++
				ps.hasNames = true
			}
		}
	}
}

func (ps *parser) more() bool { return ps.i < len(ps.p) }

func (ps *parser) peek() rune {
	if ps.i < len(ps.p) {
		return ps.p[ps.i]
	}
	return -1
}

func (ps *parser) eat(c rune) bool {
	if ps.i < len(ps.p) && ps.p[ps.i] == c {
		ps.i++
		return true
	}
	return false
}

func (ps *parser) disjunction() (*node, error) {
	ps.depth++
	if ps.depth > MaxNesting {
		return nil, &TooLargeError{Limit: LimitNesting, Value: MaxNesting}
	}
	ps.disjunctions++
	ps.path = append(ps.path, branch{disjunction: ps.disjunctions})
	last := len(ps.path) - 1
	first, err := ps.alternative()
	if err != nil {
		return nil, err
	}
	if ps.peek() != '|' {
		ps.path = ps.path[:last]
		ps.depth--
		return first, nil
	}
	alt := withSubs(nodeAlt, first)
	for ps.eat('|') {
		if err := ps.charge(nodeSize); err != nil {
			return nil, err
		}
		ps.path[last].alternative++
		sub, err := ps.alternative()
		if err != nil {
			return nil, err
		}
		alt.subs = append(alt.subs, sub)
	}
	ps.path = ps.path[:last]
	ps.depth--
	return alt, nil
}

func (ps *parser) alternative() (*node, error) {
	var terms []*node
	for ps.more() && ps.peek() != '|' && ps.peek() != ')' {
		t, err := ps.term()
		if err != nil {
			return nil, err
		}
		terms = append(terms, t)
	}
	switch len(terms) {
	case 0:
		return newNode(nodeEmpty), nil
	case 1:
		return terms[0], nil
	}
	return withSubs(nodeCat, terms...), nil
}

// charNodeOf returns the node for one literal character. Inside a case-insensitive group that is the class of the
// characters equivalent to it.
func (ps *parser) charNodeOf(c rune) *node {
	if ps.icase {
		set, _ := foldClosure(ps.uc, newCharSet([]rune{c, c}, false), ps.unicode)
		if len(set.ranges) != 2 || set.ranges[0] != set.ranges[1] {
			return setNode(set)
		}
	}
	return charNode(c)
}

// setNodeOf returns the node for a class. Inside a case-insensitive group the class also takes every character
// equivalent to one of its members.
func (ps *parser) setNodeOf(set *charSet) (*node, error) {
	if ps.icase {
		folded, err := ps.fold(set)
		if err != nil {
			return nil, err
		}
		set = folded
	}
	return setNode(set), nil
}

// fold returns the set with every character equivalent to one of its members. It charges the pattern for the work
// and for the set: a byte for each character with a case variant that the set holds, and its ranges.
func (ps *parser) fold(set *charSet) (*charSet, error) {
	closed, variants := foldClosure(ps.uc, set, ps.unicode)
	if err := ps.charge(variants + rangeSize*len(closed.ranges)/2); err != nil {
		return nil, err
	}
	return closed, nil
}

func (ps *parser) term() (*node, error) {
	// The atom, and the repetition or the group around it.
	if err := ps.charge(2 * nodeSize); err != nil {
		return nil, err
	}
	c := ps.peek()
	quantifiable := true
	var atom *node
	var err error
	switch c {
	case '^':
		ps.i++
		quantifiable = false
		if ps.multiline {
			ps.needsBacktracking = true
			atom = newNode(nodeLineStart)
		} else {
			atom = newNode(nodeBol)
		}
	case '$':
		ps.i++
		quantifiable = false
		if ps.multiline {
			ps.needsBacktracking = true
			atom = newNode(nodeLineEnd)
		} else {
			atom = newNode(nodeEol)
		}
	case '(':
		atom, quantifiable, err = ps.group()
	case '.':
		ps.i++
		if ps.dotAll {
			atom = setNode(anySet())
		} else {
			atom = setNode(dotSet())
		}
	case '[':
		atom, err = ps.class()
	case '\\':
		atom, quantifiable, err = ps.atomEscape()
	case '*', '+', '?':
		return nil, ps.fail("nothing to repeat")
	case '{':
		if ps.unicode || ps.looksLikeQuantifier() {
			return nil, ps.fail("nothing to repeat")
		}
		ps.i++
		atom = ps.charNodeOf('{')
	case ']', '}':
		if ps.unicode {
			return nil, ps.fail("lone bracket")
		}
		ps.i++
		atom = ps.charNodeOf(c)
	default:
		ps.i++
		atom = ps.charNodeOf(c)
	}
	if err != nil {
		return nil, err
	}
	min, max, lazy, ok, err := ps.quantifier()
	if err != nil {
		return nil, err
	}
	if !ok {
		return atom, nil
	}
	if !quantifiable {
		return nil, ps.fail("nothing to repeat")
	}
	return &node{kind: nodeRepeat, subs: []*node{atom}, min: min, max: max, lazy: lazy}, nil
}

// looksLikeQuantifier reports whether the text at the current { reads as {n}, {n,} or {n,m}.
func (ps *parser) looksLikeQuantifier() bool {
	s := ps.p
	k := ps.i + 1
	start := k
	for k < len(s) && isDigit(s[k]) {
		k++
	}
	if k == start || k >= len(s) {
		return false
	}
	if s[k] == '}' {
		return true
	}
	if s[k] != ',' {
		return false
	}
	k++
	for k < len(s) && isDigit(s[k]) {
		k++
	}
	return k < len(s) && s[k] == '}'
}

// number reads a run of decimal digits, saturating at maxCount.
func (ps *parser) number() (int64, error) {
	start := ps.i
	var v int64
	for ps.i < len(ps.p) && isDigit(ps.p[ps.i]) {
		if v < maxCount {
			v = v*10 + int64(ps.p[ps.i]-'0')
			if v > maxCount {
				v = maxCount
			}
		}
		ps.i++
	}
	if start == ps.i {
		return 0, ps.fail("expected a number")
	}
	return v, nil
}

// quantifier reads a quantifier, when one is here: its bounds, and whether it is lazy. ok is false when there is none.
func (ps *parser) quantifier() (min, max int32, lazy, ok bool, err error) {
	c := ps.peek()
	switch {
	case c == '*':
		ps.i++
		min, max = 0, unbounded
	case c == '+':
		ps.i++
		min, max = 1, unbounded
	case c == '?':
		ps.i++
		min, max = 0, 1
	case c == '{' && ps.looksLikeQuantifier():
		ps.i++
		first := ps.i
		lo, err := ps.number()
		if err != nil {
			return 0, 0, false, false, err
		}
		hi := int32(lo)
		firstEnd := ps.i
		if ps.eat(',') {
			if ps.peek() == '}' {
				hi = unbounded
			} else {
				second := ps.i
				h, err := ps.number()
				if err != nil {
					return 0, 0, false, false, err
				}
				hi = int32(h)
				// Both counts stop at maxCount, so the numbers are compared as they are written.
				if greaterAsWritten(ps.p[first:firstEnd], ps.p[second:ps.i]) {
					return 0, 0, false, false, ps.fail("numbers out of order in quantifier")
				}
			}
		}
		if !ps.eat('}') {
			return 0, 0, false, false, ps.fail("malformed quantifier")
		}
		min, max = int32(lo), hi
	default:
		return 0, 0, false, false, nil
	}
	return min, max, ps.eat('?'), true, nil
}

// group reads a group or a lookaround. It also reports whether a quantifier may follow.
func (ps *parser) group() (*node, bool, error) {
	ps.i++
	if !ps.eat('?') {
		n, err := ps.capture(nil)
		return n, true, err
	}
	if ps.eat(':') {
		body, err := ps.disjunction()
		if err != nil {
			return nil, false, err
		}
		if err := ps.close(); err != nil {
			return nil, false, err
		}
		return withSubs(nodeGroup, body), true, nil
	}
	// Modifier groups came in ECMAScript 2025, so the 2020 grammar reads (?i as an invalid group.
	if c := ps.peek(); (c == 'i' || c == 'm' || c == 's' || c == '-') && !ps.es2020 {
		n, err := ps.modifierGroup()
		return n, true, err
	}
	if ps.eat('=') || ps.eat('!') {
		negative := ps.p[ps.i-1] == '!'
		body, err := ps.disjunction()
		if err != nil {
			return nil, false, err
		}
		if err := ps.close(); err != nil {
			return nil, false, err
		}
		ps.needsBacktracking = true
		// Annex B lets a quantifier follow a lookahead. The u flag does not.
		return &node{kind: nodeLook, subs: []*node{body}, negative: negative}, !ps.unicode, nil
	}
	if ps.eat('<') {
		if ps.eat('=') || ps.eat('!') {
			negative := ps.p[ps.i-1] == '!'
			body, err := ps.disjunction()
			if err != nil {
				return nil, false, err
			}
			if err := ps.close(); err != nil {
				return nil, false, err
			}
			ps.needsBacktracking = true
			return &node{kind: nodeLook, subs: []*node{body}, negative: negative, behind: true}, false, nil
		}
		name, err := ps.groupName()
		if err != nil {
			return nil, false, err
		}
		// Two groups may share a name only when they are in separate alternatives of one disjunction, so that no
		// match can go through both. The 2020 grammar refuses a name used twice, wherever the groups are.
		if ps.es2020 && len(ps.names[string(name)]) > 0 {
			return nil, false, ps.fail("duplicate group name")
		}
		for _, other := range ps.names[string(name)] {
			if !separateAlternatives(other.path, ps.path) {
				return nil, false, ps.fail("duplicate group name")
			}
		}
		n, err := ps.capture(name)
		return n, true, err
	}
	return nil, false, ps.fail("invalid group")
}

// modifierGroup reads a group that turns modifiers on or off for its body, such as (?i:...) or (?s-i:...). The
// position is after the (?.
func (ps *parser) modifierGroup() (*node, error) {
	icase, multiline, dotAll := ps.icase, ps.multiline, ps.dotAll
	var seen [3]bool
	removing, count := false, 0
	for !ps.eat(':') {
		c := ps.peek()
		ps.i++
		var flag int
		switch c {
		case 'i':
			flag, ps.icase = 0, !removing
		case 'm':
			flag, ps.multiline = 1, !removing
		case 's':
			flag, ps.dotAll = 2, !removing
		case '-':
			if removing {
				return nil, ps.fail("invalid group modifier")
			}
			removing = true
			continue
		default:
			return nil, ps.fail("invalid group modifier")
		}
		if seen[flag] {
			return nil, ps.fail("repeated group modifier")
		}
		seen[flag] = true
		count++
	}
	if count == 0 {
		return nil, ps.fail("invalid group modifier")
	}
	body, err := ps.disjunction()
	if err != nil {
		return nil, err
	}
	if err := ps.close(); err != nil {
		return nil, err
	}
	ps.icase, ps.multiline, ps.dotAll = icase, multiline, dotAll
	return withSubs(nodeGroup, body), nil
}

func (ps *parser) capture(name []rune) (*node, error) {
	ps.groups++
	group := ps.groups
	if name != nil {
		// The path is kept, and every group of the same name is compared with it.
		key := string(name)
		same := len(ps.names[key])
		if err := ps.charge(rangeSize * (len(ps.path) + same)); err != nil {
			return nil, err
		}
		ps.names[key] = append(ps.names[key], namedGroup{group: group, path: slices.Clone(ps.path)})
	}
	body, err := ps.disjunction()
	if err != nil {
		return nil, err
	}
	if err := ps.close(); err != nil {
		return nil, err
	}
	return &node{kind: nodeGroup, subs: []*node{body}, group: group}, nil
}

func (ps *parser) close() error {
	if !ps.eat(')') {
		return ps.fail("unterminated group")
	}
	return nil
}

// groupName reads a group name up to and including its >. A character of the name may be written as a \u escape.
func (ps *parser) groupName() ([]rune, error) {
	var name []rune
	for !ps.eat('>') {
		if !ps.more() {
			return nil, ps.fail("invalid group name")
		}
		c := ps.peek()
		ps.i++
		if c == '\\' {
			if !ps.eat('u') {
				return nil, ps.fail("invalid group name")
			}
			c = ps.unicodeEscape(true)
			if c < 0 {
				return nil, ps.fail("invalid group name")
			}
		} else if 0xD800 <= c && c <= 0xDBFF && 0xDC00 <= ps.peek() && ps.peek() <= 0xDFFF {
			// Without the u flag the pattern is code units, and a name still takes a surrogate pair as one character.
			c = 0x10000 + (c-0xD800)<<10 + (ps.peek() - 0xDC00)
			ps.i++
		}
		if len(name) == 0 {
			if !(c == '$' || c == '_' || ps.uc.idStart().Contains(c)) {
				return nil, ps.fail("invalid group name")
			}
		} else if !(c == '$' || c == 0x200C || c == 0x200D || ps.uc.idContinue().Contains(c)) {
			return nil, ps.fail("invalid group name")
		}
		name = append(name, c)
	}
	if len(name) == 0 {
		return nil, ps.fail("invalid group name")
	}
	return name, nil
}

// atomEscape reads an escape outside a class. It also reports whether a quantifier may follow.
func (ps *parser) atomEscape() (*node, bool, error) {
	ps.i++
	if !ps.more() {
		return nil, false, ps.fail(`\ at end of pattern`)
	}
	c := ps.peek()
	if c == 'b' || c == 'B' {
		ps.i++
		kind := nodeWordBoundary
		if c == 'B' {
			kind = nodeNotWordBoundary
		}
		boundary := newNode(kind)
		// With the u flag, a case-insensitive word boundary counts two more characters as word characters.
		if ps.unicode && ps.icase {
			boundary.fold = true
			ps.needsBacktracking = true
		}
		return boundary, false, nil
	}
	if c == 'k' {
		// Annex B reads \k as the letter k in a pattern with no named group.
		if ps.unicode || ps.hasNames {
			ps.i++
			if !ps.eat('<') {
				return nil, false, ps.fail("invalid named reference")
			}
			name, err := ps.groupName()
			if err != nil {
				return nil, false, err
			}
			ps.namedRefs = true
			ps.needsBacktracking = true
			return &node{kind: nodeBackref, name: name, fold: ps.icase, foldVersion: ps.foldVersion()}, true, nil
		}
		ps.i++
		return ps.charNodeOf('k'), true, nil
	}
	if '1' <= c && c <= '9' {
		start := ps.i
		n, err := ps.number()
		if err != nil {
			return nil, false, err
		}
		if n <= ps.groupCount {
			ps.needsBacktracking = true
			return &node{kind: nodeBackref, groups: []int32{int32(n)}, fold: ps.icase, foldVersion: ps.foldVersion()},
				true, nil
		}
		if ps.unicode {
			return nil, false, ps.fail("reference to a group that does not exist")
		}
		// Annex B reads it as an octal escape, or as the digit itself.
		ps.i = start
		return ps.charNodeOf(ps.legacyOctal()), true, nil
	}
	set, err := ps.classEscape(c)
	if err != nil {
		return nil, false, err
	}
	if set != nil {
		if strs := ps.escapeStrings; strs != nil {
			// A property of strings (the v flag): the class of its code points and its sequences.
			folded, err := ps.foldIf(set)
			if err != nil {
				return nil, false, err
			}
			n, err := ps.classNode(ps.stringsValue(folded, strs), false)
			return n, true, err
		}
		n, err := ps.setNodeOf(set)
		return n, true, err
	}
	escaped, err := ps.characterEscape(false)
	if err != nil {
		return nil, false, err
	}
	return ps.charNodeOf(escaped), true, nil
}

// legacyOctal reads an Annex B octal escape of up to three digits with a value below 256. A digit that cannot start
// one (8 or 9) stands for itself.
func (ps *parser) legacyOctal() rune {
	var v rune
	k := 0
	for k < 3 && ps.more() && '0' <= ps.peek() && ps.peek() <= '7' && v*8+(ps.peek()-'0') <= 0o377 {
		v = v*8 + (ps.peek() - '0')
		ps.i++
		k++
	}
	if k == 0 {
		d := ps.peek()
		ps.i++
		return d
	}
	return v
}

// classEscape reads a class escape such as \d or \p{...} whose letter is c. It returns nil, consuming nothing, when
// c does not start one.
func (ps *parser) classEscape(c rune) (*charSet, error) {
	ps.escapeStrings = nil
	var set *charSet
	switch c {
	case 'd':
		set = digitSet()
	case 'D':
		set = notDigitSet()
	case 'w':
		if ps.unicode && ps.icase {
			set = foldWordSet()
		} else {
			set = wordSet()
		}
	case 'W':
		if ps.unicode && ps.icase {
			set = notFoldWordSet()
		} else {
			set = notWordSet()
		}
	case 's':
		set = spaceSet()
	case 'S':
		set = notSpaceSet()
	case 'p', 'P':
		// Without the u flag \p is the letter p.
		if !ps.unicode {
			return nil, nil
		}
		ps.i++
		if !ps.eat('{') {
			return nil, ps.fail("invalid property name")
		}
		start := ps.i
		for ps.more() && ps.peek() != '}' {
			ps.i++
		}
		// A name ECMA-262 defines is ASCII. Any other character makes a name no table has.
		expr := string(ps.p[start:ps.i])
		if !ps.eat('}') {
			return nil, ps.fail("invalid property name")
		}
		// The 2020 grammar knows only the names and values of its own tables, whatever the version of Unicode.
		var found *charSet
		if !ps.es2020 || es2020HasProperty(expr) {
			found = propertySet(ps.uc, expr)
		}
		if found == nil {
			if ps.sets && c == 'p' {
				if property := stringPropertyOf(ps.uc, expr); property != nil {
					// A property of strings, which the v flag alone names, and which has no complement.
					if err := ps.charge(rangeSize * (len(property.set.ranges)/2 + len(property.strings))); err != nil {
						return nil, err
					}
					ps.escapeStrings = property.strings
					return property.set, nil
				}
			}
			return nil, ps.fail("invalid property name")
		}
		if c == 'P' {
			if err := ps.charge(rangeSize * len(found.ranges) / 2); err != nil {
				return nil, err
			}
			// With the v flag the complement is of the case-folded set; with the u flag the fold comes after it.
			if ps.sets {
				folded, err := ps.foldIf(found)
				if err != nil {
					return nil, err
				}
				found = folded
			}
			return newCharSet(found.ranges, true), nil
		}
		return found, nil
	default:
		return nil, nil
	}
	ps.i++
	return set, nil
}

// hex reads exactly n hexadecimal digits. It returns -1, consuming nothing, when they are not there.
func (ps *parser) hex(n int) rune {
	if ps.i+n > len(ps.p) {
		return -1
	}
	var v rune
	for k := 0; k < n; k++ {
		d := hexValue(ps.p[ps.i+k])
		if d < 0 {
			return -1
		}
		v = v*16 + d
	}
	ps.i += n
	return v
}

// characterEscape reads the escape after a backslash, in or out of a class, and returns the code point it names.
func (ps *parser) characterEscape(inClass bool) (rune, error) {
	c := ps.peek()
	ps.i++
	switch c {
	case 'f':
		return 0x0C, nil
	case 'n':
		return 0x0A, nil
	case 'r':
		return 0x0D, nil
	case 't':
		return 0x09, nil
	case 'v':
		return 0x0B, nil
	case 'c':
		l := ps.peek()
		if isASCIILetter(l) {
			ps.i++
			return l % 32, nil
		}
		if !ps.unicode && inClass && (isDigit(l) || l == '_') {
			ps.i++
			return l % 32, nil
		}
		if ps.unicode {
			return 0, ps.fail("invalid control escape")
		}
		// Annex B reads a \c that is not a control escape as a backslash followed by the letter c.
		ps.i--
		return '\\', nil
	case '0':
		if ps.more() && isDigit(ps.peek()) {
			if ps.unicode {
				return 0, ps.fail("invalid decimal escape")
			}
			ps.i--
			return ps.legacyOctal(), nil
		}
		return 0, nil
	case 'x':
		h := ps.hex(2)
		if h < 0 {
			if ps.unicode {
				return 0, ps.fail(`invalid \x escape`)
			}
			return 'x', nil
		}
		return h, nil
	case 'u':
		u := ps.unicodeEscape(ps.unicode)
		if u < 0 {
			if ps.unicode {
				return 0, ps.fail(`invalid \u escape`)
			}
			return 'u', nil
		}
		return u, nil
	}
	if '1' <= c && c <= '9' && !ps.unicode && inClass {
		ps.i--
		return ps.legacyOctal(), nil
	}
	// Identity escapes. The u flag allows only the syntax characters and '/', and '-' in a class. Annex B allows any
	// character, except k in a pattern with a named group. Outside a class atomEscape reads \k before this is reached,
	// so here the \k of such a pattern is in a class, where it is not a reference either.
	if ps.unicode && !syntaxCharacter(c) && !(inClass && c == '-') {
		return 0, ps.fail("invalid escape")
	}
	if c == 'k' && ps.hasNames {
		return 0, ps.fail("invalid escape")
	}
	return c, nil
}

// unicodeEscape reads what follows a \u and returns the code point, or -1, consuming nothing, when it is not a
// well-formed escape. With unicodeMode set, as it is with the u flag and always in a group name, it accepts the
// \u{...} form and reads a surrogate pair written as two escapes as one code point. Without it each escape is one code
// unit.
func (ps *parser) unicodeEscape(unicodeMode bool) rune {
	start := ps.i
	if unicodeMode && ps.eat('{') {
		digits := ps.i
		var v rune
		for ps.more() && hexValue(ps.peek()) >= 0 {
			if v <= maxRune {
				v = v*16 + hexValue(ps.peek())
			}
			ps.i++
		}
		if digits == ps.i || !ps.eat('}') || v > maxRune {
			ps.i = start
			return -1
		}
		return v
	}
	u := ps.hex(4)
	if u < 0 {
		return -1
	}
	if unicodeMode && 0xD800 <= u && u <= 0xDBFF && ps.i+6 <= len(ps.p) && ps.p[ps.i] == '\\' && ps.p[ps.i+1] == 'u' {
		save := ps.i
		ps.i += 2
		low := ps.hex(4)
		if 0xDC00 <= low && low <= 0xDFFF {
			return 0x10000 + (u-0xD800)<<10 + (low - 0xDC00)
		}
		ps.i = save
	}
	return u
}

// class reads a character class from its [ to its ].
func (ps *parser) class() (*node, error) {
	if ps.sets {
		return ps.classSetExpression()
	}
	ps.i++
	negated := ps.eat('^')
	var pairs []rune
	// charged is the number of ranges read so far that the size of the pattern has been charged for.
	charged := 0
	for {
		if !ps.more() {
			return nil, ps.fail("unterminated character class")
		}
		if ps.eat(']') {
			break
		}
		if err := ps.charge(rangeSize * (len(pairs)/2 - charged)); err != nil {
			return nil, err
		}
		charged = len(pairs) / 2
		first, firstSet, err := ps.classAtom()
		if err != nil {
			return nil, err
		}
		if ps.peek() == '-' && ps.i+1 < len(ps.p) && ps.p[ps.i+1] != ']' {
			ps.i++
			second, secondSet, err := ps.classAtom()
			if err != nil {
				return nil, err
			}
			if firstSet != nil || secondSet != nil {
				if ps.unicode {
					return nil, ps.fail("invalid character class range")
				}
				// Annex B reads the '-' as itself when a class escape is at either end.
				pairs = appendClassAtom(pairs, first, firstSet)
				pairs = append(pairs, '-', '-')
				pairs = appendClassAtom(pairs, second, secondSet)
				continue
			}
			if second < first {
				return nil, ps.fail("range out of order in character class")
			}
			pairs = append(pairs, first, second)
			continue
		}
		pairs = appendClassAtom(pairs, first, firstSet)
	}
	// Inside a case-insensitive group a character matches the class when it is equivalent to a member, and a negated
	// class excludes exactly those characters.
	if err := ps.charge(rangeSize * (len(pairs)/2 - charged)); err != nil {
		return nil, err
	}
	set := newCharSet(pairs, false)
	if ps.icase {
		folded, err := ps.fold(set)
		if err != nil {
			return nil, err
		}
		set = folded
	}
	if negated {
		set = newCharSet(set.ranges, true)
	}
	return setNode(set), nil
}

// classAtom reads one member of a class. It returns a code point, or a set for a class escape.
func (ps *parser) classAtom() (rune, *charSet, error) {
	c := ps.peek()
	ps.i++
	if c != '\\' {
		return c, nil, nil
	}
	if !ps.more() {
		return 0, nil, ps.fail(`\ at end of pattern`)
	}
	e := ps.peek()
	if e == 'b' {
		ps.i++
		return 0x08, nil, nil
	}
	set, err := ps.classEscape(e)
	if err != nil {
		return 0, nil, err
	}
	if set != nil {
		return 0, set, nil
	}
	r, err := ps.characterEscape(true)
	return r, nil, err
}

// The v flag. Its grammar changes a class and nothing outside it. A class is read as a value of two parts, its code
// points and its strings (each of a length other than one, the empty string among them), with a flag for whether its
// syntax may hold a string, which ECMA-262's MayContainStrings decides from the syntax and not from what the strings
// turn out to be. A string of one character is a character of the class. The union, the intersection (&&) and the
// difference (--) act on both parts, strings compared whole. With the i flag each operand is case folded as it is
// read, so that the complement of a class, and of \P{...}, is of the folded set (with the u flag the fold comes after
// the complement). A class with strings is a group of an alternation: the strings longest first, each a sequence of
// characters, then the set. (This is the design of the .NET reference engine, section 3.2.)

// foldIf returns the set with every character equivalent to one of its members, inside a case-insensitive group, and
// the set itself outside one.
func (ps *parser) foldIf(set *charSet) (*charSet, error) {
	if ps.icase {
		return ps.fold(set)
	}
	return set, nil
}

// classSetExpression reads a class of the v flag's grammar from its [ to its ].
func (ps *parser) classSetExpression() (*node, error) {
	ps.i++
	negated := ps.eat('^')
	start := ps.i
	value, err := ps.classSetContents()
	if err != nil {
		return nil, err
	}
	if !ps.eat(']') {
		return nil, ps.fail("unterminated character class")
	}
	if negated && value.mayContainStrings {
		ps.i = start
		return nil, ps.fail("negated character class may contain strings")
	}
	return ps.classNode(value, negated)
}

// classSetContents reads the contents of a class of the v flag's grammar, up to its ].
func (ps *parser) classSetContents() (classSetValue, error) {
	if ps.peek() == ']' {
		return emptyClassSetValue(), nil
	}
	value, isRange, err := ps.classSetElement()
	if err != nil {
		return classSetValue{}, err
	}
	if op := ps.setOperator(); op != 0 {
		// An intersection or a difference: operands only, with the one operator between every two.
		if isRange {
			return classSetValue{}, ps.fail("invalid set operation in character class")
		}
		for ps.setOperator() == op {
			ps.i += 2
			if ps.peek() == ']' || !ps.more() {
				return classSetValue{}, ps.fail("invalid set operation in character class")
			}
			operand, operandRange, err := ps.classSetElement()
			if err != nil {
				return classSetValue{}, err
			}
			if operandRange {
				return classSetValue{}, ps.fail("invalid set operation in character class")
			}
			if err := ps.charge(rangeSize * (len(value.set.ranges) + len(operand.set.ranges)) / 2); err != nil {
				return classSetValue{}, err
			}
			if op == '&' {
				value = value.intersect(operand)
			} else {
				value = value.subtract(operand)
			}
		}
		if ps.peek() != ']' {
			return classSetValue{}, ps.fail("invalid set operation in character class")
		}
		return value, nil
	}
	// A union.
	for ps.more() && ps.peek() != ']' {
		if ps.setOperator() != 0 {
			return classSetValue{}, ps.fail("invalid set operation in character class")
		}
		operand, _, err := ps.classSetElement()
		if err != nil {
			return classSetValue{}, err
		}
		if err := ps.charge(rangeSize * (len(value.set.ranges) + len(operand.set.ranges)) / 2); err != nil {
			return classSetValue{}, err
		}
		value = value.union(operand)
	}
	return value, nil
}

// setOperator returns the set operator at the position (& for &&, - for --), or 0.
func (ps *parser) setOperator() rune {
	if ps.i+1 < len(ps.p) && ps.p[ps.i] == ps.p[ps.i+1] && (ps.p[ps.i] == '&' || ps.p[ps.i] == '-') {
		return ps.p[ps.i]
	}
	return 0
}

// classSetElement reads one element of a class of the v flag's grammar: an operand, or a range of two characters,
// which it reports.
func (ps *parser) classSetElement() (classSetValue, bool, error) {
	if err := ps.charge(nodeSize); err != nil {
		return classSetValue{}, false, err
	}
	c := ps.peek()
	if c == '[' {
		// A nested class: its strings count as strings whether or not there are any.
		start := ps.i
		ps.depth++
		if ps.depth > MaxNesting {
			return classSetValue{}, false, &TooLargeError{Limit: LimitNesting, Value: MaxNesting}
		}
		ps.i++
		negated := ps.eat('^')
		inner, err := ps.classSetContents()
		if err != nil {
			return classSetValue{}, false, err
		}
		if !ps.eat(']') {
			return classSetValue{}, false, ps.fail("unterminated character class")
		}
		ps.depth--
		if negated {
			if inner.mayContainStrings {
				ps.i = start
				return classSetValue{}, false, ps.fail("negated character class may contain strings")
			}
			return classSetValueOf(inner.set.complement()), false, nil
		}
		return inner, false, nil
	}
	if c == '\\' {
		if ps.i+1 >= len(ps.p) {
			return classSetValue{}, false, ps.fail(`\ at end of pattern`)
		}
		e := ps.p[ps.i+1]
		if e == 'q' {
			ps.i += 2
			v, err := ps.classStringDisjunction()
			return v, false, err
		}
		ps.i++
		set, err := ps.classEscape(e)
		if err != nil {
			return classSetValue{}, false, err
		}
		if set != nil {
			if strs := ps.escapeStrings; strs != nil {
				folded, err := ps.foldIf(set)
				if err != nil {
					return classSetValue{}, false, err
				}
				return ps.stringsValue(folded, strs), false, nil
			}
			// A class escape is folded as one operand; \P is already the complement of a folded set.
			if e != 'P' {
				if set, err = ps.foldIf(set); err != nil {
					return classSetValue{}, false, err
				}
			}
			return classSetValueOf(set), false, nil
		}
		ps.i--
	}
	first, err := ps.classSetCharacter()
	if err != nil {
		return classSetValue{}, false, err
	}
	if ps.peek() == '-' && ps.setOperator() == 0 {
		dash := ps.i
		ps.i++
		next := ps.peek()
		escapeClass := next == '\\' && ps.i+1 < len(ps.p) && inASCII(ps.p[ps.i+1], "qdDwWsSpP")
		if next == ']' || next == '[' || escapeClass {
			ps.i = dash
			return classSetValue{}, false, ps.fail("invalid character class range")
		}
		second, err := ps.classSetCharacter()
		if err != nil {
			return classSetValue{}, false, err
		}
		if second < first {
			ps.i = dash
			return classSetValue{}, false, ps.fail("range out of order in character class")
		}
		set, err := ps.foldIf(newCharSet([]rune{first, second}, false))
		if err != nil {
			return classSetValue{}, false, err
		}
		return classSetValueOf(set), true, nil
	}
	set, err := ps.foldIf(newCharSet([]rune{first, first}, false))
	if err != nil {
		return classSetValue{}, false, err
	}
	return classSetValueOf(set), false, nil
}

// classSetCharacter reads one character of a class of the v flag's grammar: a character that is not a syntax
// character and does not begin a reserved double punctuator, or an escape, which may be of a reserved punctuator.
func (ps *parser) classSetCharacter() (rune, error) {
	if !ps.more() {
		return 0, ps.fail("unterminated character class")
	}
	c := ps.p[ps.i]
	if c == '\\' {
		if ps.i+1 >= len(ps.p) {
			return 0, ps.fail(`\ at end of pattern`)
		}
		e := ps.p[ps.i+1]
		if e == 'b' {
			ps.i += 2
			return 0x08, nil
		}
		if inASCII(e, "&-!#%,:;<=>@`~") {
			ps.i += 2
			return e, nil
		}
		ps.i++
		return ps.characterEscape(true)
	}
	if inASCII(c, `()[]{}/-\|`) {
		return 0, ps.fail("invalid character in character class")
	}
	if ps.i+1 < len(ps.p) && ps.p[ps.i+1] == c && inASCII(c, "&!#$%*+,.:;<=>?@^`~") {
		return 0, ps.fail("invalid set operation in character class")
	}
	ps.i++
	return c, nil
}

// classStringDisjunction reads the strings of \q{...} after its \q. A string of one character is a character of the
// class.
func (ps *parser) classStringDisjunction() (classSetValue, error) {
	if !ps.eat('{') {
		return classSetValue{}, ps.fail("invalid escape")
	}
	var current []rune
	value := emptyClassSetValue()
	for {
		if !ps.more() {
			return classSetValue{}, ps.fail("unterminated character class")
		}
		c := ps.peek()
		if c == '|' || c == '}' {
			ps.i++
			if len(current) == 1 {
				set, err := ps.foldIf(newCharSet([]rune{current[0], current[0]}, false))
				if err != nil {
					return classSetValue{}, err
				}
				value.set = value.set.union(set)
			} else {
				value.mayContainStrings = true
				if err := ps.charge(nodeSize * max(len(current), 1)); err != nil {
					return classSetValue{}, err
				}
				s := ps.canonicalString(current)
				if !containsString(value.strings, s) {
					value.strings = append(value.strings, s)
				}
			}
			current = current[:0]
			if c == '}' {
				break
			}
			continue
		}
		r, err := ps.classSetCharacter()
		if err != nil {
			return classSetValue{}, err
		}
		current = append(current, r)
	}
	return value, nil
}

// stringsValue returns the value of a property of strings: its code points, and its sequences (case folded in a
// case-insensitive group).
func (ps *parser) stringsValue(set *charSet, strs [][]rune) classSetValue {
	out := make([][]rune, len(strs))
	for k, s := range strs {
		out[k] = ps.canonicalString(s)
	}
	return classSetValue{set: set, strings: out, mayContainStrings: true}
}

// canonicalString returns a string as its code points, each canonicalized in a case-insensitive group so that strings
// are compared as ECMA-262 compares them.
func (ps *parser) canonicalString(codePoints []rune) []rune {
	out := make([]rune, len(codePoints))
	for k, c := range codePoints {
		if ps.icase {
			c = canonicalize(ps.uc.ucd, c, true)
		}
		out[k] = c
	}
	return out
}

// classNode returns the node of a class of the v flag's grammar: its set, or, where it holds strings, the
// alternatives of its strings longest first and then its set, as ECMA-262 orders them, in a group.
func (ps *parser) classNode(value classSetValue, negated bool) (*node, error) {
	if negated {
		return setNode(value.set.complement()), nil
	}
	if len(value.strings) == 0 {
		return setNode(value.set), nil
	}
	ordered := slices.Clone(value.strings)
	slices.SortStableFunc(ordered, func(a, b []rune) int { return len(b) - len(a) })
	alternatives := make([]*node, 0, len(ordered)+1)
	for _, s := range ordered {
		if err := ps.charge(nodeSize * (len(s) + 1)); err != nil {
			return nil, err
		}
		if len(s) == 0 {
			alternatives = append(alternatives, newNode(nodeEmpty))
			continue
		}
		chars := make([]*node, len(s))
		for k, c := range s {
			chars[k] = ps.charNodeOf(c)
		}
		alternatives = append(alternatives, withSubs(nodeCat, chars...))
	}
	if len(value.set.ranges) > 0 {
		alternatives = append(alternatives, setNode(value.set))
	}
	return withSubs(nodeGroup, withSubs(nodeAlt, alternatives...)), nil
}

// classSetValue is the value of a class of the v flag's grammar: its code points, its strings (each of a length other
// than one, the empty string among them), and whether its syntax may hold a string.
type classSetValue struct {
	set               *charSet
	strings           [][]rune
	mayContainStrings bool
}

func emptyClassSetValue() classSetValue          { return classSetValue{set: newCharSet(nil, false)} }
func classSetValueOf(set *charSet) classSetValue { return classSetValue{set: set} }

func containsString(strs [][]rune, s []rune) bool {
	return slices.ContainsFunc(strs, func(t []rune) bool { return slices.Equal(t, s) })
}

func (a classSetValue) union(b classSetValue) classSetValue {
	strs := slices.Clone(a.strings)
	for _, s := range b.strings {
		if !containsString(strs, s) {
			strs = append(strs, s)
		}
	}
	return classSetValue{set: a.set.union(b.set), strings: strs, mayContainStrings: a.mayContainStrings || b.mayContainStrings}
}

func (a classSetValue) intersect(b classSetValue) classSetValue {
	var strs [][]rune
	for _, s := range a.strings {
		if containsString(b.strings, s) {
			strs = append(strs, s)
		}
	}
	return classSetValue{set: a.set.intersect(b.set), strings: strs, mayContainStrings: a.mayContainStrings && b.mayContainStrings}
}

func (a classSetValue) subtract(b classSetValue) classSetValue {
	var strs [][]rune
	for _, s := range a.strings {
		if !containsString(b.strings, s) {
			strs = append(strs, s)
		}
	}
	return classSetValue{set: a.set.subtract(b.set), strings: strs, mayContainStrings: a.mayContainStrings}
}
