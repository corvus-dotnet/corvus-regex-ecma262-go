package ecmaregex

// Finding where a match can start (section 4.3 of the design of the .NET reference engine), worked out once from the
// syntax tree when a pattern is compiled (search.rs).
//
// A search on a long text spends most of its time looking for where to start, so before the matcher runs the program
// applies what it learned at compile time. What is searched for (an "anchor") is a literal, each of whose characters
// may have several forms (a letter's cases under the i flag, or the members of a small class), that every match holds
// at a known number of characters from its start: one number, or a bounded range. A pattern that is an alternation, or
// that holds one after a bounded prefix (.{0,2}(Tom|Sawyer)), has an anchor in each alternative, and all of them are
// searched for at once (literals.go). Each occurrence opens the window of starts from which some alternative could
// reach it; the starts are tried in order and never twice (the alternation windows). Where every alternative starts
// with its literal, the matcher runs at the occurrence alone.
//
// A pattern that starts with loops with no upper bound, then a literal ([a-zA-Z]+ing, \w+\s+Holmes), has the literal
// searched for and the runs of the loops' sets before it walked back to the earliest start, which is the only one
// tried for that occurrence: a later start in the same run can only reach a subset of the places the loops can end at.

import (
	"slices"
	"sync/atomic"
)

// How the search finds where a match can start, beyond the first set.
type searchPlan uint8

const (
	// planScan tries every position the first set allows.
	planScan searchPlan = iota
	// planWindows: every match holds one of the literals lo to hi characters after its start. rest is the anchored
	// automaton of the anchors and the terms after each, run at an occurrence first where a window has more than one
	// start: where nothing from the occurrence on can match, its window is not tried.
	planWindows
	// planAfterLoops: every match is runs of the loops' sets, then the literal. rest is the anchored automaton of the
	// literal and the terms after it, run at each occurrence first.
	planAfterLoops
)

// The shape of a pattern whose matches the search finds without running the matcher (the .NET reference engine's
// simple shapes). Only for a pattern with no group.
type searchShape uint8

const (
	shapeNone searchShape = iota
	// shapeLiterals is literals and nothing else, an alternation of them or one, with every form of each written out:
	// the search's own match, which is the first literal in the pattern's order of those that start leftmost.
	shapeLiterals
	// shapeForms is an alternation of literals each of whose characters has several forms: at each occurrence the
	// search finds, the alternatives are compared in order.
	shapeForms
	// shapeSet is one class and nothing else: every member is a match.
	shapeSet
	// shapeLoop is one greedy loop of a class with no upper bound and a least count of zero or one: a match is the run
	// of members from where it starts.
	shapeLoop
)

// How the end of a run of a loop's members is found (RunEnd of search.rs).
type runEndKind uint8

const (
	// runEach finds it a member at a time.
	runEach runEndKind = iota
	// runAll: the set holds every character, and the run is the rest of the text.
	runAll
	// runStops: the set holds every character but up to three ASCII ones, found by the searches for bytes.
	runStops
	// runLine: the set holds every character but the four line terminators (the dot): LF and CR are found by the
	// search for bytes, with the lead byte of U+2028 and U+2029, which is looked at further where it is found.
	runLine
)

const (
	// maxForms is the most literals the forms of one anchor are expanded to.
	maxForms = 64
	// maxLiterals is the most literals of all the anchors of a pattern.
	maxLiterals = 512
	// maxWindow is the most characters a window of starts may span.
	maxWindow = 64
)

// search is what a pattern's search knows of it.
type search struct {
	plan  searchPlan
	shape searchShape
	// literals are the literals of the windows and of the search after loops, and lo and hi the distances of the
	// windows.
	literals *literals
	lo, hi   int
	// reverse is the reverse automaton of the windows' prefixes (see buildReverseDfa).
	reverse *reverseDfa
	// rest is the check of what follows an occurrence (see restCheck).
	rest *restCheck
	// sets and mins are the loops of the search after loops, and their least counts.
	sets []*charSet
	mins []int
	// forms holds the alternatives of shapeForms: for each, the forms of each of its characters.
	forms [][][]rune
	// shapeSet is the class of shapeSet and shapeLoop, shapeMin the loop's least count, and runEnd how its run ends.
	shapeSet *charSet
	shapeMin int
	runEnd   runEndKind
	// stops are the bytes of runStops (the last repeated to three), and stops16 the units that end a run over UTF-16,
	// for runStops and runLine.
	stops   [3]byte
	nStops  int
	stops16 *unitSet
	// skipRun is the set of a loop every match starts with, when a start that fails may move to the end of the run of
	// its set: every start in the rest of the run can only reach a subset of the places the loop can end at.
	skipRun *charSet
	// leadingWordBoundary says the pattern starts with \b and every character a match can start with is an ASCII word
	// character, so a start inside a word fails and the search passes over the rest of the word.
	leadingWordBoundary bool
	// firstBytes are the bytes a match can start with over UTF-8, searched for together when they are few: the first
	// set's ASCII members and the lead bytes of its other members.
	firstBytes *firstBytes
	// firstUnits are the code units a match can start with over UTF-16.
	firstUnits *unitSet
}

// noSearch makes every pattern compiled while it is set try every position its first set allows, which the tests
// compare the searches with.
var noSearch atomic.Bool

// restTrials is the checks a restCheck makes before it decides whether it pays.
const restTrials = 64

// restCheck is the anchored automaton of what follows an occurrence (an anchor and the terms after it), run at the
// occurrence before any start is walked back to. A check that seldom rules an occurrence out costs more than it saves
// (an e-mail address's @ is nearly always followed by a domain), so the check keeps count over its first trials and is
// dropped for good when fewer than one in eight of them ruled the occurrence out. The counts change only how fast a
// search is, never what it finds.
type restCheck struct {
	dfa             *anchoredDfa
	checks, rejects atomic.Int32
}

// pays reports whether the check is still made.
func (r *restCheck) pays() bool {
	return r.checks.Load() < restTrials || r.rejects.Load()*8 >= restTrials
}

// count counts a check while it is still on trial: each of the first restTrials checks once, whichever goroutines
// make them, and none after (the Load keeps the counter from being written on every check once the trial is over).
func (r *restCheck) count(out bool) {
	if r.checks.Load() < restTrials && r.checks.Add(1) <= restTrials && out {
		r.rejects.Add(1)
	}
}

// firstBytes is a search for any of a few bytes: the searches for one to three bytes, and a table for up to
// sixty-four, which is the set the crate finds with the fingerprint search of one-byte literals.
type firstBytes struct {
	count   int
	a, b, c byte
	table   [256]bool
}

func newFirstBytes(set *charSet, units bool) *firstBytes {
	f := &firstBytes{}
	r := set.ranges
	spans := [4][4]rune{{0, 0x7F, 0, 0x00}, {0x80, 0x7FF, 6, 0xC0}, {0x800, 0xFFFF, 12, 0xE0},
		{0x10000, 0x10FFFF, 18, 0xF0}}
	for k := 0; k < len(r); k += 2 {
		lo, hi := r[k], r[k+1]
		// A surrogate without the u flag is half of a character of four bytes, and U+FFFD is also what a byte that is
		// not UTF-8 reads as: neither starts at a byte the set can name.
		if units && lo <= 0xDFFF && hi >= 0xD800 || lo <= 0xFFFD && hi >= 0xFFFD {
			return nil
		}
		for _, s := range spans {
			x, y := max(lo, s[0]), min(hi, s[1])
			if x <= y {
				for b := s[3] | x>>s[2]; b <= s[3]|y>>s[2]; b++ {
					f.table[b] = true
				}
			}
		}
	}
	var list []byte
	for b := range 256 {
		if f.table[b] {
			list = append(list, byte(b))
		}
	}
	if len(list) == 0 || len(list) > maxForms {
		return nil
	}
	f.count, f.a = len(list), list[0]
	f.b, f.c = list[min(1, len(list)-1)], list[min(2, len(list)-1)]
	return f
}

// find returns the first position at or after from whose byte is one of the bytes, or -1.
func (f *firstBytes) find(h []byte, from int) int {
	switch f.count {
	case 1:
		return indexByte(h, from, len(h), f.a)
	case 2:
		return indexByte2(h, from, len(h), f.a, f.b)
	case 3:
		return indexByte4(h, from, len(h), f.a, f.b, f.c, f.c)
	}
	for at := from; at < len(h); at++ {
		if f.table[h[at]] {
			return at
		}
	}
	return -1
}

// newFirstUnits returns the test for the code units a match can start with over UTF-16: the first set's units as up
// to eight ranges (a member beyond the BMP as its lead surrogate), where more are merged across the narrowest gaps, the
// ASCII and Latin ones last since those units are common in text. A candidate is then tested against the first set
// itself.
func newFirstUnits(set *charSet) *unitSet {
	lead := func(cp int) int { return 0xD800 + (cp-0x10000)>>10 }
	var units [][2]int
	r := set.ranges
	for k := 0; k < len(r); k += 2 {
		lo, hi := int(r[k]), int(r[k+1])
		if lo <= 0xFFFF {
			units = append(units, [2]int{lo, min(hi, 0xFFFF)})
		}
		if hi > 0xFFFF {
			units = append(units, [2]int{lead(max(lo, 0x10000)), lead(hi)})
		}
	}
	slices.SortFunc(units, func(x, y [2]int) int {
		if x[0] != y[0] {
			return x[0] - y[0]
		}
		return x[1] - y[1]
	})
	var merged [][2]int
	for _, u := range units {
		if n := len(merged); n > 0 && u[0] <= merged[n-1][1]+1 {
			merged[n-1][1] = max(merged[n-1][1], u[1])
		} else {
			merged = append(merged, u)
		}
	}
	for len(merged) > 8 {
		// The gap whose units are least likely in text: its width, a thousand times over below U+0300.
		best, bestCost := 0, int64(1<<62)
		for k := 0; k < len(merged)-1; k++ {
			a, b := merged[k][1]+1, merged[k+1][0]
			cost := int64(b - a)
			if a < 0x300 {
				cost *= 1000
			}
			if cost < bestCost {
				bestCost, best = cost, k
			}
		}
		merged[best][1] = merged[best+1][1]
		merged = slices.Delete(merged, best+1, best+2)
	}
	if len(merged) == 0 {
		return nil
	}
	ranges := make([]int, 0, 2*len(merged))
	for _, m := range merged {
		ranges = append(ranges, m[0], m[1])
	}
	return newUnitSet(ranges)
}

// anchor is one literal every match of an alternative holds, lo to hi characters after its start.
type anchor struct {
	// chars holds the forms of each of its characters.
	chars  [][]rune
	lo, hi int64
	// prefix is the terms a match goes through before the anchor, and rest the terms from the anchor on, to the end of
	// the pattern.
	prefix, rest []*node
}

// forms returns the forms of a character the search can look for: a literal code point, or the members of a small
// class.
func forms(n *node) []rune {
	if n.kind == nodeChar {
		if literalRune(n.r) {
			return []rune{n.r}
		}
		return nil
	}
	if n.kind != nodeSet {
		return nil
	}
	r := n.set.ranges
	var out []rune
	for k := 0; k < len(r); k += 2 {
		lo, hi := r[k], r[k+1]
		if hi-lo >= 4 || len(out)+int(hi-lo+1) > 4 {
			return nil
		}
		for c := lo; c <= hi; c++ {
			if !literalRune(c) {
				return nil
			}
			out = append(out, c)
		}
	}
	return out
}

// terms returns the terms of a node in order: a sequence and a group whose body is a sequence are spliced in.
func terms(n *node) []*node {
	var out []*node
	var add func(n *node)
	add = func(n *node) {
		switch n.kind {
		case nodeCat:
			for _, s := range n.subs {
				add(s)
			}
		case nodeGroup:
			if n.subs[0].kind != nodeAlt {
				add(n.subs[0])
			} else {
				out = append(out, n)
			}
		case nodeEmpty:
		default:
			out = append(out, n)
		}
	}
	add(n)
	return out
}

// far is a bound past every window, so that sums of widths saturate there.
const far = int64(1) << 40

func addFar(a, b int64) int64 { return min(a+b, far) }

func mulFar(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if min(a, far)*min(b, far) >= far {
		return far
	}
	return a * b
}

// width returns the least and the greatest number of characters a node consumes (the greatest -1 for no bound), or
// false when it is not known (a backreference).
func width(n *node) (int64, int64, bool) {
	switch n.kind {
	case nodeChar, nodeSet:
		return 1, 1, true
	case nodeCat:
		lo, hi := int64(0), int64(0)
		for _, sub := range n.subs {
			a, b, ok := width(sub)
			if !ok {
				return 0, 0, false
			}
			lo = addFar(lo, a)
			if hi < 0 || b < 0 {
				hi = -1
			} else {
				hi = addFar(hi, b)
			}
		}
		return lo, hi, true
	case nodeAlt:
		first := true
		var lo, hi int64
		for _, sub := range n.subs {
			a, b, ok := width(sub)
			if !ok {
				return 0, 0, false
			}
			if first {
				lo, hi, first = a, b, false
			} else {
				lo = min(lo, a)
				if hi < 0 || b < 0 {
					hi = -1
				} else {
					hi = max(hi, b)
				}
			}
		}
		return lo, hi, true
	case nodeGroup:
		return width(n.subs[0])
	case nodeRepeat:
		a, b, ok := width(n.subs[0])
		if !ok {
			return 0, 0, false
		}
		lo := mulFar(a, int64(n.min))
		var hi int64
		switch {
		case n.max == unbounded && b == 0:
			hi = 0
		case n.max == unbounded, b < 0:
			hi = -1
		default:
			hi = mulFar(b, int64(n.max))
		}
		return lo, hi, true
	case nodeBackref:
		return 0, 0, false
	}
	return 0, 0, true
}

// anchors returns the anchors of a node whose match starts lo to hi characters after the start of the whole match:
// the first run of literal characters of each of its alternatives, or nil when some alternative has none at a bounded
// distance.
func anchors(n *node, lo, hi int64) []anchor { return anchorsAfter(n, lo, hi, nil, nil) }

// anchorsAfter returns the anchors of n, which the terms before precede and the terms after follow.
func anchorsAfter(n *node, lo, hi int64, before, after []*node) []anchor {
	ts := terms(n)
	prefix := slices.Clone(before)
	for k := 0; k < len(ts); {
		t := ts[k]
		if forms(t) != nil {
			rest := append(slices.Clone(ts[k:]), after...)
			var chars [][]rune
			for ; k < len(ts); k++ {
				f := forms(ts[k])
				if f == nil {
					break
				}
				chars = append(chars, f)
			}
			return []anchor{{chars: chars, lo: lo, hi: hi, prefix: prefix, rest: rest}}
		}
		var alt *node
		if t.kind == nodeAlt {
			alt = t
		} else if t.kind == nodeGroup && t.subs[0].kind == nodeAlt {
			alt = t.subs[0]
		}
		if alt != nil {
			following := append(slices.Clone(ts[k+1:]), after...)
			var all []anchor
			for _, sub := range alt.subs {
				found := anchorsAfter(sub, lo, hi, prefix, following)
				if found == nil {
					return nil
				}
				all = append(all, found...)
			}
			return all
		}
		a, b, ok := width(t)
		if !ok || b < 0 {
			return nil
		}
		lo, hi = addFar(lo, a), addFar(hi, b)
		if hi > maxWindow {
			return nil
		}
		prefix = append(prefix, t)
		k++
	}
	return nil
}

// expand returns the literals of an anchor: the forms of its first count characters written out.
func expand(a *anchor, count int) [][]rune {
	out := [][]rune{{}}
	for _, f := range a.chars[:count] {
		next := make([][]rune, 0, len(out)*len(f))
		for _, prefix := range out {
			for _, r := range f {
				next = append(next, append(slices.Clone(prefix), r))
			}
		}
		out = next
	}
	return out
}

// utf8Cuts returns the most bytes of each of an anchor's literals written out over UTF-8: where the anchor was cut
// short, every literal is cut to the bytes of its shortest, so that the forms of a character longer than the others
// (the long s, two bytes beside the one of s) make no more literals than the others. The regex crate's rule.
func utf8Cuts(cutShort bool, lits [][]rune) []int {
	cut := int(^uint(0) >> 1)
	if cutShort {
		for _, l := range lits {
			n := 0
			for _, r := range l {
				n += runeLen8(r)
			}
			cut = min(cut, n)
		}
	}
	out := make([]int, len(lits))
	for k := range out {
		out[k] = cut
	}
	return out
}

func runeLen8(r rune) int {
	switch {
	case r < 0x80:
		return 1
	case r < 0x800:
		return 2
	case r < 0x10000:
		return 3
	}
	return 4
}

// totalLiterals returns the number of literals the anchors make with the first k characters of each written out.
func totalLiterals(as []anchor, k int) int64 {
	t := int64(0)
	for _, a := range as {
		p := int64(1)
		for c := 0; c < min(len(a.chars), k); c++ {
			p = mulFar(p, int64(len(a.chars[c])))
		}
		t = addFar(t, p)
	}
	return t
}

// literalCounts returns how many of each anchor's characters to write out, and whether the anchors were cut short.
// Exact anchors are written out in full. Where a character has several forms (a letter under the i flag), every
// anchor is cut to one length by the rule of the regex crate's literal optimisation (Seq::optimize_by_preference):
// the full literals if they are ten or fewer, else five characters if that makes ten or fewer, else four if that makes
// sixty-four or fewer, else three if sixty-four or fewer, else two if ten or fewer, else one.
func literalCounts(as []anchor) ([]int, bool) {
	exact := true
	for _, a := range as {
		for _, f := range a.chars {
			exact = exact && len(f) == 1
		}
	}
	const all = int(^uint(0) >> 1)
	var k int
	switch {
	case exact || totalLiterals(as, all) <= 10:
		k = all
	case totalLiterals(as, 5) <= 10:
		k = 5
	case totalLiterals(as, 4) <= maxForms:
		k = 4
	case totalLiterals(as, 3) <= maxForms:
		k = 3
	case totalLiterals(as, 2) <= 10:
		k = 2
	default:
		k = 1
	}
	out := make([]int, len(as))
	for i, a := range as {
		out[i] = min(len(a.chars), k)
	}
	return out, !exact && k != all
}

// asciiWordSet reports whether a set holds ASCII word characters alone.
func asciiWordSet(f *charSet) bool {
	if f == nil || len(f.ranges) == 0 || f.ranges[len(f.ranges)-1] >= 0x80 {
		return false
	}
	for c := byte(0); c < 0x80; c++ {
		if f.hasASCII(c) && !isWordByte(c) {
			return false
		}
	}
	return true
}

// alternatives returns the top-level alternatives of a tree: the alternatives of an alternation (in a group or not), or
// the tree.
func alternatives(root *node) []*node {
	if root.kind == nodeAlt {
		return root.subs
	}
	if root.kind == nodeGroup && root.group == 0 && root.subs[0].kind == nodeAlt {
		return root.subs[0].subs
	}
	return []*node{root}
}

// holdsGroup reports whether a node holds a capturing group.
func holdsGroup(n *node) bool {
	if n.kind == nodeGroup && n.group > 0 {
		return true
	}
	return slices.ContainsFunc(n.subs, holdsGroup)
}

// shapeOfLiterals sets the literal shapes of a tree whose anchors are found: literals and nothing else, exact or with
// forms.
func (s *search) shapeOfLiterals(root *node, found []anchor, counts []int, cutShort bool) {
	if holdsGroup(root) {
		return
	}
	alts := alternatives(root)
	all := make([][][]rune, len(alts))
	for a, alt := range alts {
		ts := terms(alt)
		if len(ts) == 0 {
			return
		}
		all[a] = make([][]rune, len(ts))
		for k, t := range ts {
			if all[a][k] = forms(t); all[a][k] == nil {
				return
			}
		}
	}
	// Literals written out in full, every form of each, are the search's own literals, in the pattern's order (an
	// alternative's forms together), so the search's leftmost-first match is the pattern's.
	literal := !cutShort && len(found) == len(all)
	for a := 0; a < len(all) && literal; a++ {
		literal = found[a].lo == 0 && found[a].hi == 0 && counts[a] == len(all[a])
	}
	if literal {
		s.shape = shapeLiterals
	} else {
		s.shape, s.forms = shapeForms, all
	}
}

// openLoop returns the set of a loop of one character or class with no upper bound, or nil.
func openLoop(t *node) *charSet {
	if t.kind != nodeRepeat || t.max != unbounded {
		return nil
	}
	switch body := t.subs[0]; body.kind {
	case nodeSet:
		return body.set
	case nodeChar:
		return newCharSet([]rune{body.r, body.r}, false)
	}
	return nil
}

// shapeOfOne sets the class and loop shapes of a tree that is one term.
func (s *search) shapeOfOne(root *node) {
	if holdsGroup(root) {
		return
	}
	ts := terms(root)
	if len(ts) != 1 {
		return
	}
	t := ts[0]
	if t.kind == nodeSet && !t.set.isEmpty() {
		s.shape, s.shapeSet = shapeSet, t.set
	} else if t.kind == nodeRepeat && !t.lazy && t.min <= 1 {
		if set := openLoop(t); set != nil && !set.isEmpty() {
			s.shape, s.shapeSet, s.shapeMin = shapeLoop, set, int(t.min)
			s.setRunEnd(set)
		}
	}
}

// setRunEnd sets how the end of a run of the set's members is found.
func (s *search) setRunEnd(set *charSet) {
	c := set.complement().ranges
	if len(c) == 0 {
		s.runEnd = runAll
		return
	}
	asInts := func() []int {
		out := make([]int, len(c))
		for k, r := range c {
			out[k] = int(r)
		}
		return out
	}
	if slices.Equal(c, []rune{0x0A, 0x0A, 0x0D, 0x0D, 0x2028, 0x2029}) {
		s.runEnd, s.stops16 = runLine, newUnitSet(asInts())
		return
	}
	n := 0
	for k := 0; k < len(c); k += 2 {
		lo, hi := c[k], c[k+1]
		if hi >= 0x80 || hi-lo >= 3 || n+int(hi-lo+1) > 3 {
			s.runEnd = runEach
			return
		}
		for r := lo; r <= hi; r++ {
			s.stops[n] = byte(r)
			n++
		}
	}
	for k := n; k < 3; k++ {
		s.stops[k] = s.stops[n-1]
	}
	s.runEnd, s.nStops, s.stops16 = runStops, n, newUnitSet(asInts())
}

// planSearch works out the search of a tree, after simplify. backrefs says the pattern has a backreference, which
// rules out what depends on a start reaching a subset of another's places; first is the first set of the program.
func planSearch(root *node, backrefs bool, first *charSet, units bool) *search {
	s := &search{}
	if noSearch.Load() {
		return s
	}
	if first != nil {
		s.firstBytes = newFirstBytes(first, units)
		s.firstUnits = newFirstUnits(first)
	}
	ts := terms(root)
	// A leading \b before characters that are all ASCII word characters.
	boundary := len(ts) > 0 && ts[0].kind == nodeWordBoundary && !ts[0].fold
	if boundary && asciiWordSet(first) {
		s.leadingWordBoundary = true
	}
	if found := anchors(root, 0, 0); found != nil {
		var lits [][]rune
		var cuts []int
		lo, hi := int64(1<<62), int64(0)
		counts, cutShort := literalCounts(found)
		for i := range found {
			a := &found[i]
			lo, hi = min(lo, a.lo), max(hi, a.hi)
			expanded := expand(a, counts[i])
			c := utf8Cuts(cutShort, expanded)
			for e, l := range expanded {
				if !slices.ContainsFunc(lits, func(x []rune) bool { return slices.Equal(x, l) }) {
					lits = append(lits, l)
					cuts = append(cuts, c[e])
				}
			}
		}
		nonEmpty := true
		for _, l := range lits {
			nonEmpty = nonEmpty && len(l) > 0
		}
		if len(lits) > 0 && len(lits) <= maxLiterals && nonEmpty {
			s.shapeOfLiterals(root, found, counts, cutShort)
			s.literals = newLiterals(lits, cuts)
			// Where a window has more than one start, the terms before the anchors are matched backwards from each
			// occurrence, so that only the starts they allow are tried (the reverse search of the regex crate).
			if hi > 0 {
				prefixes := make([][]*node, len(found))
				longer := false
				for i, a := range found {
					prefixes[i] = a.prefix
					longer = longer || len(a.rest) > len(a.chars)
				}
				s.reverse = buildReverseDfa(prefixes)
				// Where every anchor is the end of its alternative, the occurrence is all the rest there is to check.
				if longer {
					alts := make([]*node, len(found))
					for i, a := range found {
						alts[i] = withSubs(nodeCat, slices.Clone(a.rest)...)
					}
					if dfa := buildAdfa(withSubs(nodeAlt, alts...)); dfa != nil {
						s.rest = &restCheck{dfa: dfa}
					}
				}
			}
			s.plan, s.lo, s.hi = planWindows, int(lo), int(hi)
			return s
		}
	}
	s.shapeOfOne(root)
	if backrefs {
		return s
	}
	// Loops with no upper bound, then a literal.
	k := 0
	if boundary {
		k = 1
	}
	var sets []*charSet
	var mins []int
	for ; k < len(ts); k++ {
		set := openLoop(ts[k])
		if set == nil {
			break
		}
		sets = append(sets, set)
		mins = append(mins, int(ts[k].min))
	}
	if len(sets) == 0 {
		return s
	}
	// \b is allowed when every loop's set is of word characters (a later start of the runs can be in any loop's run,
	// where \b holds after a word character before a member that is not one: \b\d*\s*a over " a"), and the sets must
	// share no character, any two of them.
	words, disjoint, small := true, true, true
	for i, set := range sets {
		words = words && asciiWordSet(set)
		for j := i + 1; j < len(sets); j++ {
			disjoint = disjoint && set.intersect(sets[j]).isEmpty()
		}
		small = small && mins[i] <= 1
	}
	var chars [][]rune
	literalAt := k
	for ; k < len(ts); k++ {
		f := forms(ts[k])
		if f == nil {
			break
		}
		chars = append(chars, f)
	}
	if (!boundary || words) && disjoint && small && len(chars) > 0 {
		a := anchor{chars: chars}
		count, cutShort := literalCounts([]anchor{a})
		lits := expand(&a, count[0])
		rest := slices.Clone(ts[literalAt:])
		s.plan, s.literals, s.sets, s.mins = planAfterLoops, newLiterals(lits, utf8Cuts(cutShort, lits)), sets, mins
		if len(rest) > len(a.chars) {
			if dfa := buildAdfa(withSubs(nodeCat, rest...)); dfa != nil {
				s.rest = &restCheck{dfa: dfa}
			}
		}
	}
	// A failed start may skip the rest of its leading loop's run (the loop is the first term).
	if !boundary {
		s.skipRun = sets[0]
	}
	return s
}
