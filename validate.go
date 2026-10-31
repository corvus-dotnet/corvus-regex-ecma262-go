package ecmaregex

// Whether a pattern is valid in the grammar, decided as the parser decides it but without its limits of size, and
// with nothing allocated for any pattern of ordinary size: no tree, no sets, no string (the design of the .NET
// engine's EcmaRegexValidator, and of validate.rs of the Rust crate).
//
// It reads the pattern where it lies, decoding a character only where the grammar needs it. It keeps the groups and
// the nested classes of the v flag on stacks of its own rather than recursing, so that no pattern can overflow the
// stack, and it keeps a group name as its place in the pattern. Each stack starts on arrays of the caller's and moves
// to the heap only beyond them, which takes groups nested more deeply than 32, or more than 12 named groups (or their
// paths) at once. It asks the grammar only: the parser's limits of size (the length of the pattern, the nesting of
// groups, the size it counts) belong to compiling a pattern, and none of them applies here.
//
// Each method follows the method of the parser with the same name, and makes the same decision at each step. A
// position is twice the index of a byte of the pattern, plus one for the second half of a character beyond the Basic
// Multilingual Plane when it is read as two UTF-16 code units (without the u flag the grammar reads code units).

import (
	"strings"
	"unicode/utf8"
)

// invalidEscape is a character escape that is not valid.
const invalidEscape rune = -2

const (
	kindGroup = iota
	kindLookahead
	kindLookbehind
)

// A frame of a class of the v flag: whether it is negated, its phase, its operator, whether it may contain strings.
const (
	classFrame     = 4
	phaseStart     = 0
	phaseUnion     = 1
	phaseOperands  = 2
	waitingFirst   = 3
	waitingUnion   = 4
	waitingOperand = 5
)

// What a class escape was.
const (
	escapeNone = iota
	escapeInvalid
	escapeValid
	escapeStrings
)

// One element of a class of the v flag's grammar.
const (
	elementInvalid = iota
	// elementNested is a nested class, whose frame is now open.
	elementNested
	elementOperand
)

type validator struct {
	s       string
	end     int
	unicode bool
	sets    bool
	es2020  bool
	uc      *unicodeData

	i            int
	hasNames     bool
	groupCount   int64
	disjunctions int
	// path is the alternatives that enclose the position, as disjunction and alternative (the parser's path).
	path stack
	// frames is the groups open at the position: the kind of each.
	frames stack
	// names holds each named group: the start and the end of its name, the start and the length of its path in
	// namePaths, and the hash of its name.
	names     stack
	namePaths stack
	// nameTable holds the places of the named groups by the hash of their names: one more than the place in names of
	// the last group with each name (0 for none). Open addressing, a power of two long and at most half full.
	nameTable     nameTable
	distinctNames int
	// references holds each named reference: the start and the end of its name.
	references stack
	// classes is the classes of the v flag open at the position (see classFrame).
	classes stack
}

// stack is a stack of integers whose first entries live in place, and which moves to the heap when it outgrows them.
// It never stores a slice of its own array, so a validator on the stack stays there.
type stack struct {
	inline [64]int
	n      int
	// heap is the whole stack once it has outgrown inline, and nil until then.
	heap []int
}

func (s *stack) push(x int) {
	if s.heap == nil {
		if s.n < len(s.inline) {
			s.inline[s.n] = x
			s.n++
			return
		}
		s.heap = make([]int, s.n, 2*len(s.inline))
		copy(s.heap, s.inline[:s.n])
	}
	s.heap = append(s.heap, x)
	s.n++
}

func (s *stack) get(k int) int {
	if s.heap != nil {
		return s.heap[k]
	}
	return s.inline[k]
}

func (s *stack) set(k, x int) {
	if s.heap != nil {
		s.heap[k] = x
	} else {
		s.inline[k] = x
	}
}

func (s *stack) truncate(n int) {
	s.n = n
	if s.heap != nil {
		s.heap = s.heap[:n]
	}
}

func (s *stack) pop() int {
	x := s.get(s.n - 1)
	s.truncate(s.n - 1)
	return x
}

// nameTable is the table of names, in place until it outgrows 32 slots.
type nameTable struct {
	inline [32]int
	heap   []int
}

func (t *nameTable) len() int {
	if t.heap != nil {
		return len(t.heap)
	}
	return len(t.inline)
}

func (t *nameTable) get(k int) int {
	if t.heap != nil {
		return t.heap[k]
	}
	return t.inline[k]
}

func (t *nameTable) set(k, x int) {
	if t.heap != nil {
		t.heap[k] = x
	} else {
		t.inline[k] = x
	}
}

// isValidPattern reports whether a pattern is valid in the grammar of the syntax (its u and v flags, its edition and
// its version of Unicode). A string that is not UTF-8 is not a pattern.
func isValidPattern(pattern string, sx syntax) bool {
	if !utf8.ValidString(pattern) {
		return false
	}
	v := validator{
		s:       pattern,
		end:     2 * len(pattern),
		unicode: sx.unicode || sx.sets,
		sets:    sx.sets,
		es2020:  sx.edition == EditionES2020,
		uc:      sx.unicodeVersion.data(),
	}
	return v.validate()
}

func isOneOf(c rune, set string) bool {
	return 0 <= c && c < 128 && strings.IndexByte(set, byte(c)) >= 0
}

// at returns the element at a position (a code point with the u flag's grammar, a UTF-16 code unit without), and the
// position after it.
func (v *validator) at(pos int) (rune, int) {
	b := pos >> 1
	c, width := rune(v.s[b]), 1
	if c >= utf8.RuneSelf {
		c, width = utf8.DecodeRuneInString(v.s[b:])
	}
	if c > 0xFFFF && !v.unicode {
		// Two code units: the high surrogate, then the low one.
		if pos&1 == 0 {
			return 0xD800 + (c-0x10000)>>10, pos | 1
		}
		return 0xDC00 + (c-0x10000)&0x3FF, (b + width) << 1
	}
	return c, (b + width) << 1
}

func (v *validator) more() bool { return v.i < v.end }

func (v *validator) peek() rune {
	if v.i < v.end {
		c, _ := v.at(v.i)
		return c
	}
	return -1
}

// peekSecond returns the element after the one at the position, or -1.
func (v *validator) peekSecond() rune {
	if v.i >= v.end {
		return -1
	}
	_, next := v.at(v.i)
	if next < v.end {
		c, _ := v.at(next)
		return c
	}
	return -1
}

func (v *validator) next() rune {
	c, next := v.at(v.i)
	v.i = next
	return c
}

func (v *validator) eat(c rune) bool {
	if v.i < v.end {
		if e, next := v.at(v.i); e == c {
			v.i = next
			return true
		}
	}
	return false
}

func (v *validator) validate() bool {
	v.scanGroups()
	v.pushDisjunction()
	for v.more() {
		c := v.peek()
		if c == '|' {
			v.next()
			v.path.set(v.path.n-1, v.path.get(v.path.n-1)+1)
			continue
		}
		if c == ')' {
			// The end of a group: an unmatched ')' when none is open.
			if v.frames.n == 0 {
				return false
			}
			v.next()
			v.path.truncate(v.path.n - 2)
			kind := v.frames.pop()
			// Annex B lets a quantifier follow a lookahead. The u flag does not, and neither lets one follow a
			// lookbehind.
			quantifiable := kind == kindGroup || (!v.unicode && kind == kindLookahead)
			if q := v.quantifier(); q < 0 || (q > 0 && !quantifiable) {
				return false
			}
			continue
		}
		if c == '(' {
			kind := v.groupOrLook()
			if kind < 0 {
				return false
			}
			v.frames.push(kind)
			v.pushDisjunction()
			continue
		}
		quantifiable, ok := v.term(c)
		if !ok {
			return false
		}
		if q := v.quantifier(); q < 0 || (q > 0 && !quantifiable) {
			return false
		}
	}
	// An unterminated group.
	if v.frames.n != 0 {
		return false
	}
	// Every named reference names a group somewhere in the pattern.
	for r := 0; r < v.references.n; r += 2 {
		start, stop := v.references.get(r), v.references.get(r+1)
		if _, found := v.findName(start, stop, v.nameHash(start, stop)); !found {
			return false
		}
	}
	return true
}

func (v *validator) pushDisjunction() {
	v.disjunctions++
	v.path.push(v.disjunctions)
	v.path.push(0)
}

// scanGroups counts the capturing groups of the pattern and notes whether any is named.
func (v *validator) scanGroups() {
	inClass := false
	for pos := 0; pos < v.end; {
		c, next := v.at(pos)
		switch {
		case c == '\\':
			if next < v.end {
				_, next = v.at(next)
			}
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '(' && !inClass:
			e1, after1 := rune(-1), v.end
			if next < v.end {
				e1, after1 = v.at(next)
			}
			if e1 != '?' {
				v.groupCount++
			} else {
				e2, after2 := rune(-1), v.end
				if after1 < v.end {
					e2, after2 = v.at(after1)
				}
				e3 := rune(-1)
				if e2 >= 0 && after2 < v.end {
					e3, _ = v.at(after2)
				}
				if e2 == '<' && e3 >= 0 && e3 != '=' && e3 != '!' {
					v.groupCount++
					v.hasNames = true
				}
			}
		}
		pos = next
	}
}

// term reads a term that is not a group: whether a quantifier may follow it, and whether it is valid.
func (v *validator) term(c rune) (quantifiable, ok bool) {
	switch c {
	case '^', '$':
		v.next()
		return false, true
	case '[':
		if v.sets {
			return true, v.classSetExpression()
		}
		return true, v.class()
	case '\\':
		return v.atomEscape()
	case '*', '+', '?':
		return false, false
	case '{':
		if v.unicode || v.looksLikeQuantifier() {
			return false, false
		}
	case ']', '}':
		if v.unicode {
			return false, false
		}
	}
	v.next()
	return true, true
}

// looksLikeQuantifier reports whether the text at the current '{' reads as {n}, {n,} or {n,m}.
func (v *validator) looksLikeQuantifier() bool {
	_, k := v.at(v.i)
	start := k
	for k < v.end {
		c, next := v.at(k)
		if !isDigit(c) {
			break
		}
		k = next
	}
	if k == start || k >= v.end {
		return false
	}
	c, afterDigits := v.at(k)
	if c == '}' {
		return true
	}
	if c != ',' {
		return false
	}
	k = afterDigits
	for k < v.end {
		c, next := v.at(k)
		if !isDigit(c) {
			break
		}
		k = next
	}
	if k >= v.end {
		return false
	}
	c, _ = v.at(k)
	return c == '}'
}

// greaterAsWritten reports whether the first of two runs of decimal digits (by position) is the greater number,
// whatever their lengths. A digit is one byte, so two positions.
func (v *validator) greaterAsWritten(a, aEnd, b, bEnd int) bool {
	for a < aEnd && v.s[a>>1] == '0' {
		a += 2
	}
	for b < bEnd && v.s[b>>1] == '0' {
		b += 2
	}
	x, y := v.s[a>>1:aEnd>>1], v.s[b>>1:bEnd>>1]
	if len(x) != len(y) {
		return len(x) > len(y)
	}
	return x > y
}

// skipNumber reads a run of decimal digits.
func (v *validator) skipNumber() {
	for v.more() && isDigit(v.peek()) {
		v.next()
	}
}

// numberValue reads the decimal digits at the position, saturating as the parser's number does.
func (v *validator) numberValue() int64 {
	var n int64
	for v.more() && isDigit(v.peek()) {
		n = min(n*10+int64(v.next()-'0'), maxCount)
	}
	return n
}

// quantifier reads a quantifier when one is here: 0 for none, 1 for one, -1 when it is not valid.
func (v *validator) quantifier() int {
	c := v.peek()
	switch {
	case c == '*' || c == '+' || c == '?':
		v.next()
	case c == '{' && v.looksLikeQuantifier():
		v.next()
		first := v.i
		v.skipNumber()
		firstEnd := v.i
		if v.eat(',') && v.peek() != '}' {
			// The counts are compared as the numbers written, which can be past what a count is kept as.
			second := v.i
			v.skipNumber()
			if v.greaterAsWritten(first, firstEnd, second, v.i) {
				return -1
			}
		}
		if !v.eat('}') {
			return -1
		}
	default:
		return 0
	}
	v.eat('?')
	return 1
}

// groupOrLook reads the start of a group or a lookaround, to its body: its kind, or -1 when it is not valid.
func (v *validator) groupOrLook() int {
	v.next()
	if !v.eat('?') || v.eat(':') {
		return kindGroup
	}
	c := v.peek()
	// Modifier groups came in ECMAScript 2025, so the 2020 grammar reads (?i as an invalid group.
	if (c == 'i' || c == 'm' || c == 's' || c == '-') && !v.es2020 {
		if v.modifierGroup() {
			return kindGroup
		}
		return -1
	}
	if v.eat('=') || v.eat('!') {
		return kindLookahead
	}
	if !v.eat('<') {
		return -1
	}
	if v.eat('=') || v.eat('!') {
		return kindLookbehind
	}
	start, stop, ok := v.groupName()
	if !ok {
		return -1
	}
	// Two groups may share a name only when they are in separate alternatives of one disjunction, so that no match
	// can go through both. The 2020 grammar refuses a name used twice, wherever the groups are. The last group with
	// the name is the only one to ask: the groups before it lie in alternatives apart from it, of a disjunction this
	// group is either outside, where its alternatives are all one to this group, or inside, in an alternative after
	// theirs.
	hash := v.nameHash(start, stop)
	slot, found := v.findName(start, stop, hash)
	if found {
		last := v.nameTable.get(slot) - 1
		if v.es2020 || !v.separateAlternatives(v.names.get(last+2), v.names.get(last+3)) {
			return -1
		}
	}
	entry := v.names.n
	for _, x := range [...]int{start, stop, v.namePaths.n, v.path.n, hash} {
		v.names.push(x)
	}
	for k := 0; k < v.path.n; k++ {
		v.namePaths.push(v.path.get(k))
	}
	if found {
		v.nameTable.set(slot, entry+1)
	} else {
		v.addName(slot, entry)
	}
	return kindGroup
}

// separateAlternatives reports whether a named group (by the start and the length of its path) and the current
// position lie in different alternatives of some disjunction.
func (v *validator) separateAlternatives(otherStart, otherLen int) bool {
	for k := 0; k+1 < otherLen && k+1 < v.path.n; k += 2 {
		if v.namePaths.get(otherStart+k) != v.path.get(k) {
			break
		}
		if v.namePaths.get(otherStart+k+1) != v.path.get(k+1) {
			return true
		}
	}
	return false
}

// modifierGroup reads the modifiers of a group such as (?i:...) or (?s-i:...), after the "(?", to its ':'.
func (v *validator) modifierGroup() bool {
	seen, removing := 0, false
	for !v.eat(':') {
		c := v.peek()
		if c == '-' {
			if removing {
				return false
			}
			v.next()
			removing = true
			continue
		}
		flag := 0
		switch c {
		case 'i':
			flag = 1
		case 'm':
			flag = 2
		case 's':
			flag = 4
		}
		if flag == 0 || seen&flag != 0 {
			return false
		}
		v.next()
		seen |= flag
	}
	return seen != 0
}

// groupName reads a group name up to and including its '>', and gives the positions of its first character and of
// the '>'. A character of the name may be written as a Unicode escape.
func (v *validator) groupName() (start, stop int, ok bool) {
	start = v.i
	count := 0
	for {
		if v.peek() == '>' {
			stop = v.i
			v.next()
			return start, stop, count != 0
		}
		if !v.more() {
			return 0, 0, false
		}
		c, ok := v.nameCharacter()
		if !ok {
			return 0, 0, false
		}
		if count == 0 {
			ok = c == '$' || c == '_' || v.uc.idStart().Contains(c)
		} else {
			ok = c == '$' || c == 0x200C || c == 0x200D || v.uc.idContinue().Contains(c)
		}
		if !ok {
			return 0, 0, false
		}
		count++
	}
}

// nameCharacter reads one character of a group name: a code point, written or as a Unicode escape.
func (v *validator) nameCharacter() (rune, bool) {
	c := v.next()
	if c == '\\' {
		if !v.eat('u') {
			return 0, false
		}
		u := v.unicodeEscape(true)
		return u, u >= 0
	}
	if 0xD800 <= c && c <= 0xDBFF {
		if low := v.peek(); 0xDC00 <= low && low <= 0xDFFF {
			// Without the u flag the pattern is code units, and a name still takes a surrogate pair as one character.
			v.next()
			return 0x10000 + (c-0xD800)<<10 + (low - 0xDC00), true
		}
	}
	return c, true
}

// nameHash returns the hash of a name, by the positions of its characters, over its code points.
func (v *validator) nameHash(start, stop int) int {
	saved := v.i
	hash := uint32(2166136261)
	for v.i = start; v.i < stop; {
		c, _ := v.nameCharacter()
		hash = (hash ^ uint32(c)) * 16777619
	}
	v.i = saved
	return int(hash)
}

// findName finds a name in the table: the slot that holds its last group and true, or the empty slot it would take
// and false.
func (v *validator) findName(start, stop, hash int) (int, bool) {
	mask := v.nameTable.len() - 1
	for slot := hash & mask; ; slot = (slot + 1) & mask {
		held := v.nameTable.get(slot)
		if held == 0 {
			return slot, false
		}
		n := held - 1
		if v.names.get(n+4) == hash && v.sameName(start, stop, v.names.get(n), v.names.get(n+1)) {
			return slot, true
		}
	}
}

// addName puts a new name's group in the empty slot found for it, and doubles the table when it is half full.
func (v *validator) addName(slot, entry int) {
	v.nameTable.set(slot, entry+1)
	v.distinctNames++
	if v.distinctNames*2 <= v.nameTable.len() {
		return
	}
	table := make([]int, 2*v.nameTable.len())
	mask := len(table) - 1
	for k := 0; k < v.nameTable.len(); k++ {
		if held := v.nameTable.get(k); held != 0 {
			s := v.names.get(held-1+4) & mask
			for table[s] != 0 {
				s = (s + 1) & mask
			}
			table[s] = held
		}
	}
	v.nameTable.heap = table
}

// sameName reports whether two names, by the positions of their characters, are the same code points.
func (v *validator) sameName(aStart, aStop, bStart, bStop int) bool {
	saved := v.i
	a, b := aStart, bStart
	same := true
	for {
		aDone, bDone := a >= aStop, b >= bStop
		if aDone || bDone {
			same = aDone && bDone
			break
		}
		v.i = a
		ca, _ := v.nameCharacter()
		a = v.i
		v.i = b
		cb, _ := v.nameCharacter()
		b = v.i
		if ca != cb {
			same = false
			break
		}
	}
	v.i = saved
	return same
}

// atomEscape reads an escape outside a class: whether a quantifier may follow it, and whether it is valid.
func (v *validator) atomEscape() (quantifiable, ok bool) {
	v.next()
	if !v.more() {
		return false, false
	}
	c := v.peek()
	if c == 'b' || c == 'B' {
		v.next()
		return false, true
	}
	if c == 'k' {
		v.next()
		// Annex B reads \k as the letter k in a pattern with no named group.
		if !v.unicode && !v.hasNames {
			return true, true
		}
		if !v.eat('<') {
			return false, false
		}
		start, stop, ok := v.groupName()
		if !ok {
			return false, false
		}
		v.references.push(start)
		v.references.push(stop)
		return true, true
	}
	if '1' <= c && c <= '9' {
		start := v.i
		if v.numberValue() <= v.groupCount {
			return true, true
		}
		if v.unicode {
			return false, false
		}
		// Annex B reads it as an octal escape, or as the digit itself.
		v.i = start
		v.legacyOctal()
		return true, true
	}
	switch v.classEscape(c) {
	case escapeValid, escapeStrings:
		return true, true
	case escapeInvalid:
		return false, false
	}
	return true, v.characterEscape(false) != invalidEscape
}

// legacyOctal reads an Annex B octal escape of up to three digits with a value below 256. A digit that cannot start
// one (8 or 9) stands for itself.
func (v *validator) legacyOctal() rune {
	var n rune
	k := 0
	for k < 3 && v.more() && '0' <= v.peek() && v.peek() <= '7' && n*8+(v.peek()-'0') <= 0o377 {
		n = n*8 + (v.next() - '0')
		k++
	}
	if k == 0 {
		return v.next()
	}
	return n
}

// classEscape reads a class escape such as \d or \p{...} whose letter is c. It reads nothing when c does not start
// one.
func (v *validator) classEscape(c rune) int {
	switch c {
	case 'd', 'D', 'w', 'W', 's', 'S':
		v.next()
		return escapeValid
	case 'p', 'P':
		// Without the u flag \p is the letter p.
		if !v.unicode {
			return escapeNone
		}
		v.next()
		if !v.eat('{') {
			return escapeInvalid
		}
		start := v.i
		for v.more() && v.peek() != '}' {
			v.next()
		}
		stop := v.i
		if !v.eat('}') {
			return escapeInvalid
		}
		// A name ECMA-262 defines is ASCII, so an expression with any other character names nothing.
		expr := v.s[start>>1 : stop>>1]
		for k := 0; k < len(expr); k++ {
			if expr[k] >= utf8.RuneSelf {
				return escapeInvalid
			}
		}
		// The 2020 grammar knows only the names and values of its own tables, whatever the version of Unicode.
		found := (!v.es2020 || es2020HasProperty(expr)) && hasProperty(v.uc.ucd, expr)
		if !found && v.sets && c == 'p' && hasStringProperty(v.uc, expr) {
			// A property of strings, which the v flag alone names, and which has no complement.
			return escapeStrings
		}
		if found {
			return escapeValid
		}
		return escapeInvalid
	}
	return escapeNone
}

// hex reads exactly n hexadecimal digits. It returns -1, reading nothing, when they are not there.
func (v *validator) hex(n int) rune {
	pos := v.i
	var value rune
	for k := 0; k < n; k++ {
		if pos >= v.end {
			return -1
		}
		c, next := v.at(pos)
		d := hexValue(c)
		if d < 0 {
			return -1
		}
		value = value*16 + d
		pos = next
	}
	v.i = pos
	return value
}

// characterEscape reads the escape after a backslash, in or out of a class: the character it names, or
// invalidEscape.
func (v *validator) characterEscape(inClass bool) rune {
	at := v.i
	c := v.next()
	switch c {
	case 'f':
		return 0x0C
	case 'n':
		return 0x0A
	case 'r':
		return 0x0D
	case 't':
		return 0x09
	case 'v':
		return 0x0B
	case 'c':
		l := v.peek()
		if isASCIILetter(l) {
			v.next()
			return l % 32
		}
		if !v.unicode && inClass && (isDigit(l) || l == '_') {
			v.next()
			return l % 32
		}
		if v.unicode {
			return invalidEscape
		}
		// Annex B reads a \c that is not a control escape as a backslash followed by the letter c.
		v.i = at
		return '\\'
	case '0':
		if v.more() && isDigit(v.peek()) {
			if v.unicode {
				return invalidEscape
			}
			v.i = at
			return v.legacyOctal()
		}
		return 0
	case 'x':
		if h := v.hex(2); h >= 0 {
			return h
		}
		if v.unicode {
			return invalidEscape
		}
		return 'x'
	case 'u':
		if u := v.unicodeEscape(v.unicode); u >= 0 {
			return u
		}
		if v.unicode {
			return invalidEscape
		}
		return 'u'
	}
	if '1' <= c && c <= '9' && !v.unicode && inClass {
		v.i = at
		return v.legacyOctal()
	}
	// Identity escapes. The u flag allows only the syntax characters and '/', and '-' in a class. Annex B allows any
	// character, but k in a pattern with a named group.
	if (v.unicode && !syntaxCharacter(c) && !(inClass && c == '-')) || (c == 'k' && v.hasNames) {
		return invalidEscape
	}
	return c
}

// unicodeEscape reads what follows the u of a Unicode escape and returns the character, or -1, reading nothing, when
// it is not a well-formed escape. With unicodeMode, as with the u flag and always in a group name, it accepts the form
// with braces and reads a surrogate pair written as two escapes as one code point.
func (v *validator) unicodeEscape(unicodeMode bool) rune {
	start := v.i
	if unicodeMode && v.eat('{') {
		digits := v.i
		var n rune
		for v.more() && hexValue(v.peek()) >= 0 {
			if n <= maxRune {
				n = n*16 + hexValue(v.peek())
			}
			v.next()
		}
		if digits == v.i || !v.eat('}') || n > maxRune {
			v.i = start
			return -1
		}
		return n
	}
	u := v.hex(4)
	if u < 0 {
		return -1
	}
	if unicodeMode && 0xD800 <= u && u <= 0xDBFF && v.peek() == '\\' && v.peekSecond() == 'u' {
		save := v.i
		v.next()
		v.next()
		if low := v.hex(4); 0xDC00 <= low && low <= 0xDFFF {
			return 0x10000 + (u-0xD800)<<10 + (low - 0xDC00)
		}
		v.i = save
	}
	return u
}

// class reads a character class from its '[' to its ']'.
func (v *validator) class() bool {
	v.next()
	v.eat('^')
	for {
		if !v.more() {
			return false
		}
		if v.eat(']') {
			return true
		}
		first, firstSet := v.classAtom()
		if first == invalidEscape {
			return false
		}
		if v.peek() == '-' && v.peekSecond() >= 0 && v.peekSecond() != ']' {
			v.next()
			second, secondSet := v.classAtom()
			if second == invalidEscape {
				return false
			}
			if firstSet || secondSet {
				// Annex B reads the '-' as itself when a class escape is at either end.
				if v.unicode {
					return false
				}
				continue
			}
			if second < first {
				return false
			}
		}
	}
}

// classAtom reads one member of a class: its character, or 0 and true for a class escape, or invalidEscape.
func (v *validator) classAtom() (rune, bool) {
	c := v.next()
	if c != '\\' {
		return c, false
	}
	if !v.more() {
		return invalidEscape, false
	}
	e := v.peek()
	if e == 'b' {
		v.next()
		return 0x08, false
	}
	switch v.classEscape(e) {
	case escapeValid, escapeStrings:
		return 0, true
	case escapeInvalid:
		return invalidEscape, true
	}
	return v.characterEscape(true), false
}

// The v flag.

// classSetExpression reads a class of the v flag's grammar from its '[' to its ']', with its nested classes on a
// stack of frames: a union of elements, or the intersection or the difference of operands, which may not be mixed. A
// negated class may not hold anything that may match a string.
func (v *validator) classSetExpression() bool {
	v.openClass()
	for {
		top := v.classes.n - classFrame
		switch v.classes.get(top + 1) {
		case phaseStart:
			if v.peek() == ']' {
				v.classes.set(top+3, 0)
				done, ok := v.closeClass()
				if !ok {
					return false
				}
				if done {
					return true
				}
				continue
			}
			switch element, strs, rng := v.classSetElement(); element {
			case elementInvalid:
				return false
			case elementNested:
				v.classes.set(top+1, waitingFirst)
			default:
				if !v.firstOperand(top, strs, rng) {
					return false
				}
			}
		case phaseUnion:
			if v.more() && v.peek() != ']' {
				if v.setOperator() != 0 {
					return false
				}
				switch element, strs, _ := v.classSetElement(); element {
				case elementInvalid:
					return false
				case elementNested:
					v.classes.set(top+1, waitingUnion)
				default:
					if strs {
						v.classes.set(top+3, 1)
					}
				}
				continue
			}
			done, ok := v.closeClass()
			if !ok {
				return false
			}
			if done {
				return true
			}
		case phaseOperands:
			if v.setOperator() == rune(v.classes.get(top+2)) {
				v.next()
				v.next()
				if v.peek() == ']' || !v.more() {
					return false
				}
				switch element, strs, rng := v.classSetElement(); {
				case element == elementInvalid || rng:
					return false
				case element == elementNested:
					v.classes.set(top+1, waitingOperand)
				default:
					v.operand(top, strs)
				}
				continue
			}
			if v.peek() != ']' {
				return false
			}
			done, ok := v.closeClass()
			if !ok {
				return false
			}
			if done {
				return true
			}
		default:
			// A nested class is open above this one; it is never the top while it waits.
			return false
		}
	}
}

func (v *validator) openClass() {
	v.next()
	negated := 0
	if v.eat('^') {
		negated = 1
	}
	for _, x := range [...]int{negated, phaseStart, 0, 0} {
		v.classes.push(x)
	}
}

// closeClass closes the class at the top at its ']', and gives what it may contain to the class it is nested in, if
// any. done reports that it was the outermost.
func (v *validator) closeClass() (done, ok bool) {
	if !v.eat(']') {
		return false, false
	}
	top := v.classes.n - classFrame
	negated, strs := v.classes.get(top) != 0, v.classes.get(top+3) != 0
	if negated && strs {
		return false, false
	}
	v.classes.truncate(top)
	if v.classes.n == 0 {
		return true, true
	}
	// A nested class is an operand, never a range; a negated one may contain no strings.
	strs = strs && !negated
	parent := v.classes.n - classFrame
	switch v.classes.get(parent + 1) {
	case waitingFirst:
		return false, v.firstOperand(parent, strs, false)
	case waitingUnion:
		v.classes.set(parent+1, phaseUnion)
		if strs {
			v.classes.set(parent+3, 1)
		}
	default:
		v.classes.set(parent+1, phaseOperands)
		v.operand(parent, strs)
	}
	return false, true
}

// firstOperand takes the first element of a class, and decides from what follows it whether the class is a union or
// operations.
func (v *validator) firstOperand(top int, strs, rng bool) bool {
	v.classes.set(top+3, 0)
	if strs {
		v.classes.set(top+3, 1)
	}
	if op := v.setOperator(); op != 0 {
		// An intersection or a difference: operands only, with the one operator between every two.
		if rng {
			return false
		}
		v.classes.set(top+1, phaseOperands)
		v.classes.set(top+2, int(op))
		return true
	}
	v.classes.set(top+1, phaseUnion)
	return true
}

// operand takes an operand of an intersection (strings only where both may hold them) or a difference (the first's).
func (v *validator) operand(top int, strs bool) {
	if v.classes.get(top+2) == '&' && !strs {
		v.classes.set(top+3, 0)
	}
}

// setOperator returns the set operator at the position ('&' for &&, '-' for --), or 0.
func (v *validator) setOperator() rune {
	if c := v.peek(); (c == '&' || c == '-') && v.peekSecond() == c {
		return c
	}
	return 0
}

// classSetElement reads one element of a class of the v flag's grammar: what it is, whether it may hold strings, and
// whether it is a range of two characters.
func (v *validator) classSetElement() (element int, strs, rng bool) {
	c := v.peek()
	if c == '[' {
		v.openClass()
		return elementNested, false, false
	}
	if c == '\\' {
		e := v.peekSecond()
		if e < 0 {
			return elementInvalid, false, false
		}
		if e == 'q' {
			v.next()
			v.next()
			strs, ok := v.classStringDisjunction()
			if !ok {
				return elementInvalid, false, false
			}
			return elementOperand, strs, false
		}
		save := v.i
		v.next()
		switch v.classEscape(e) {
		case escapeInvalid:
			return elementInvalid, false, false
		case escapeValid:
			return elementOperand, false, false
		case escapeStrings:
			return elementOperand, true, false
		}
		v.i = save
	}
	first := v.classSetCharacter()
	if first == invalidEscape {
		return elementInvalid, false, false
	}
	if v.peek() == '-' && v.setOperator() == 0 {
		v.next()
		if p := v.peek(); p == ']' || p == '[' || (p == '\\' && isOneOf(v.peekSecond(), "qdDwWsSpP")) {
			return elementInvalid, false, false
		}
		if second := v.classSetCharacter(); second == invalidEscape || second < first {
			return elementInvalid, false, false
		}
		return elementOperand, false, true
	}
	return elementOperand, false, false
}

// classSetCharacter reads one character of a class of the v flag's grammar: a character that is not a syntax
// character and does not begin a reserved double punctuator, or an escape, which may be of a reserved punctuator.
func (v *validator) classSetCharacter() rune {
	if !v.more() {
		return invalidEscape
	}
	c := v.peek()
	if c == '\\' {
		e := v.peekSecond()
		if e < 0 {
			return invalidEscape
		}
		if e == 'b' {
			v.next()
			v.next()
			return 0x08
		}
		if isOneOf(e, "&-!#%,:;<=>@`~") {
			v.next()
			v.next()
			return e
		}
		v.next()
		return v.characterEscape(true)
	}
	if isOneOf(c, `()[]{}/-\|`) {
		return invalidEscape
	}
	if v.peekSecond() == c && isOneOf(c, "&!#$%*+,.:;<=>?@^`~") {
		return invalidEscape
	}
	v.next()
	return c
}

// classStringDisjunction reads the strings of \q{...} after its \q: whether any is of other than one character, and
// whether it is valid.
func (v *validator) classStringDisjunction() (strs, ok bool) {
	if !v.eat('{') {
		return false, false
	}
	count := 0
	for {
		if !v.more() {
			return false, false
		}
		c := v.peek()
		if c == '|' || c == '}' {
			v.next()
			if count != 1 {
				strs = true
			}
			count = 0
			if c == '}' {
				return strs, true
			}
			continue
		}
		if v.classSetCharacter() == invalidEscape {
			return false, false
		}
		count++
	}
}
