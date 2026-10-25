package ecmaregex

// An anchored, leftmost-first automaton over ASCII text, which finds where the match that starts at a position ends
// for a pattern that needs no backtracking (adfa.rs): what the regex crate's DFAs do for the match, taken into this
// engine where ECMA-262's semantics allow it.
//
// A state is an ordered list of the pattern's states (its threads, the most preferred first, as the backtracking
// matcher would try them), with whether the unit before it was a word character and whether it is at the start of the
// text. When the match state is among the threads, a match ends here, and the threads after it, which the backtracking
// matcher would only try had this way failed, are dropped; the threads before it may still find a longer match, which
// it would prefer. So the last match found before the threads run out is the backtracking matcher's match, for a
// pattern with no lookaround, no backreference and no repetition whose body can match the empty string (where
// ECMA-262's rule for an empty iteration would differ from a thread's). A unit beyond ASCII hands the start back to the
// backtracking matcher.
//
// Every state is worked out when the pattern is compiled, within bounds of states and steps, so that a pattern too
// large for an automaton costs little before that is known. This file builds the automata; the loops that read them
// over a text are in automata_template.go.

import (
	"slices"
	"strings"
)

const (
	adfaMaxStates = 2048
	adfaMaxNfa    = 1024
	adfaMaxSteps  = 2_000_000
	// adfaDead is the state with no thread left: no match can end further on.
	adfaDead = 0
	// adfaMatched flags a transition across which a match ends before the unit, and fastMatched the same in the
	// premultiplied table.
	adfaMatched = 0x8000
	fastMatched = 0x8000_0000
)

// What find8 and find16 answers where no match starts, and where a unit beyond ASCII was reached first.
const (
	adfaNone = -1
	adfaBail = -2
)

// The kinds of what scan8 and scan16 found.
const (
	scanMatch = iota
	scanIdle
	scanBail
	scanNone
)

// anchoredDfa is the anchored automaton of a pattern.
type anchoredDfa struct {
	classOf [128]byte
	shift   uint
	// fast[state*stride+class] is the next state, premultiplied by the stride, with fastMatched when a match ends
	// before the unit.
	fast []uint32
	// atEnd says, for each state, that a match ends at the end of the text.
	atEnd []bool
	// starts is the start state for each of: the unit before is a word character (bit 1), the start of the text (bit
	// 0).
	starts [4]int
	// context says the start states differ (the pattern looks back at the unit before or at the start of the text).
	context bool
	// startLike says, for the forward automaton (see buildForwardDfa), whether each state is a start state: no thread
	// of an earlier start is alive in it.
	startLike []bool
}

// after returns the state after prefix from each start state, premultiplied by the stride (0 where no match can start
// with it), or false when a match can end inside it or it holds a byte beyond ASCII: what findAfter goes on from where
// a match is known to start with prefix.
func (a *anchoredDfa) after(prefix []byte) ([4]uint32, bool) {
	var rows [4]uint32
	for k := range 4 {
		at := uint32(a.starts[k]) << a.shift
		for _, b := range prefix {
			if at == 0 {
				// No match from this start state begins with the prefix.
				break
			}
			if b >= 0x80 {
				return rows, false
			}
			t := a.fast[at+uint32(a.classOf[b])]
			if t&fastMatched != 0 {
				return rows, false
			}
			at = t
		}
		rows[k] = at
	}
	return rows, true
}

// skipWin is the positions of a pattern read before a match can end: V8's Boyer-Moore lookahead (BoyerMooreLookahead
// in its regexp compiler). For a window of positions lo..=hi of every match, the units each can be (table for ASCII,
// wide for any other) are joined; a unit at hi from a place that is none of them rules out the starts from that place
// to hi - lo units on, so the search reads one unit in hi - lo + 1 where the window's units are rare.
type skipWin struct {
	// hi is the window's last position, the one read, and len the starts one unit rules out, hi - lo + 1.
	hi, len int
	table   [128]bool
	wide    bool
}

// hit reports whether the unit can be at some position of the window.
func (s *skipWin) hit(c int) bool {
	if c < 128 {
		return s.table[c]
	}
	return s.wide
}

// reverseDfa is an automaton of the terms before the anchors of a search, read backwards (accepting where any of them
// can start): from an occurrence of an anchor it finds, over ASCII text, every start from which some alternative's
// terms reach it. Only terms with no assertion are taken.
type reverseDfa struct {
	classOf [128]byte
	shift   uint
	// fast holds the transitions, each the next state's row (premultiplied by the stride, so 0 is the dead state) with
	// fastMatched where that state accepts.
	fast []uint32
	// acceptFirst says the first state accepts (an empty run of terms).
	acceptFirst bool
}

// The kinds of the pattern's states.
const (
	aSet uint8 = iota
	aSplit
	aBol
	aEol
	aWord
	aNotWord
	aMatch
)

// adfaNfa is the states of a pattern, as parallel slices that grow.
type adfaNfa struct {
	kind       []uint8
	set0, set1 []uint64
	// wide says the set holds a character beyond ASCII, which the automaton gives up at.
	wide     []bool
	out, alt []int
	failed   bool
}

func (b *adfaNfa) add(k uint8, s0, s1 uint64, w bool, o, a int) int {
	if len(b.kind) >= adfaMaxNfa {
		b.failed = true
		return 0
	}
	b.kind = append(b.kind, k)
	b.set0 = append(b.set0, s0)
	b.set1 = append(b.set1, s1)
	b.wide = append(b.wide, w)
	b.out = append(b.out, o)
	b.alt = append(b.alt, a)
	return len(b.kind) - 1
}

func (b *adfaNfa) addTo(k uint8, o int) int { return b.add(k, 0, 0, false, o, 0) }

// split adds a split whose preferred way is first.
func (b *adfaNfa) split(first, second int) int { return b.add(aSplit, 0, 0, false, first, second) }

func (b *adfaNfa) has(i, c int) bool {
	if c < 64 {
		return b.set0[i]>>c&1 != 0
	}
	return b.set1[i]>>(c-64)&1 != 0
}

// compile adds the states of a node that go on to next, and returns the state they start at.
func (b *adfaNfa) compile(n *node, next int) int {
	if b.failed {
		return 0
	}
	switch n.kind {
	case nodeEmpty:
		return next
	case nodeChar:
		var s0, s1 uint64
		if n.r >= 0 && n.r < 64 {
			s0 = 1 << n.r
		} else if n.r >= 64 && n.r < 128 {
			s1 = 1 << (n.r - 64)
		}
		return b.add(aSet, s0, s1, n.r >= 128, next, 0)
	case nodeSet:
		r := n.set.ranges
		return b.add(aSet, n.set.ascii[0], n.set.ascii[1], len(r) > 0 && r[len(r)-1] >= 128, next, 0)
	case nodeCat:
		for k := len(n.subs) - 1; k >= 0; k-- {
			next = b.compile(n.subs[k], next)
		}
		return next
	case nodeAlt:
		last := len(n.subs) - 1
		start := b.compile(n.subs[last], next)
		for k := last - 1; k >= 0; k-- {
			o := b.compile(n.subs[k], next)
			start = b.split(o, start)
		}
		return start
	case nodeGroup:
		return b.compile(n.subs[0], next)
	case nodeRepeat:
		sub := n.subs[0]
		if n.max != 0 && n.max != 1 && nullable(sub) {
			// ECMA-262 refuses an empty iteration after the least count, which a thread does not know.
			b.failed = true
			return 0
		}
		if n.max == unbounded {
			loop := b.split(0, 0)
			if b.failed {
				return 0
			}
			body := b.compile(sub, loop)
			if n.lazy {
				b.out[loop], b.alt[loop] = next, body
			} else {
				b.out[loop], b.alt[loop] = body, next
			}
			next = loop
		} else {
			for i := n.min; i < n.max && !b.failed; i++ {
				body := b.compile(sub, next)
				if n.lazy {
					next = b.split(next, body)
				} else {
					next = b.split(body, next)
				}
			}
		}
		for i := int32(0); i < n.min && !b.failed; i++ {
			next = b.compile(sub, next)
		}
		return next
	case nodeBol:
		return b.addTo(aBol, next)
	case nodeEol:
		return b.addTo(aEol, next)
	case nodeWordBoundary:
		if !n.fold {
			return b.addTo(aWord, next)
		}
	case nodeNotWordBoundary:
		if !n.fold {
			return b.addTo(aNotWord, next)
		}
	}
	b.failed = true
	return 0
}

// compileBack adds the states of a node read backwards that go on to next.
func (b *adfaNfa) compileBack(n *node, next int) int {
	if b.failed {
		return 0
	}
	switch n.kind {
	case nodeCat:
		for _, sub := range n.subs {
			next = b.compileBack(sub, next)
		}
		return next
	case nodeAlt:
		last := len(n.subs) - 1
		start := b.compileBack(n.subs[last], next)
		for k := last - 1; k >= 0; k-- {
			o := b.compileBack(n.subs[k], next)
			start = b.split(o, start)
		}
		return start
	case nodeGroup:
		return b.compileBack(n.subs[0], next)
	case nodeRepeat:
		sub := n.subs[0]
		if n.max == unbounded {
			loop := b.split(0, 0)
			if b.failed {
				return 0
			}
			body := b.compileBack(sub, loop)
			b.out[loop], b.alt[loop] = body, next
			next = loop
		} else {
			for i := n.min; i < n.max && !b.failed; i++ {
				body := b.compileBack(sub, next)
				next = b.split(body, next)
			}
		}
		for i := int32(0); i < n.min && !b.failed; i++ {
			next = b.compileBack(sub, next)
		}
		return next
	case nodeEmpty, nodeChar, nodeSet:
		return b.compile(n, next)
	}
	b.failed = true
	return 0
}

// adfaDet makes the automaton's states from lists of the pattern's states.
type adfaDet struct {
	nfa        *adfaNfa
	mark       []int
	generation int
	steps      int
	stack      []int
}

func newAdfaDet(nfa *adfaNfa) *adfaDet {
	return &adfaDet{nfa: nfa, mark: make([]int, len(nfa.kind)), steps: adfaMaxSteps}
}

// closure appends to out, in order of preference, the threads that reading nothing reaches from from, keeping the
// states that read a unit, the anchors and boundaries still to pass, and the match, after which nothing more is kept.
// Boundaries are passed or dropped with words, and kept to pass later without it.
func (d *adfaDet) closure(out, from []int, atStart, atEnd, words, before, after bool) []int {
	d.generation++
	return d.closureOn(out, from, atStart, atEnd, words, before, after)
}

// closureOn is closure after the closure just made, leaving out the states it reached.
func (d *adfaDet) closureOn(out, from []int, atStart, atEnd, words, before, after bool) []int {
	d.stack = d.stack[:0]
	for k := len(from) - 1; k >= 0; k-- {
		d.stack = append(d.stack, from[k])
	}
	nfa := d.nfa
	for len(d.stack) > 0 {
		i := d.stack[len(d.stack)-1]
		d.stack = d.stack[:len(d.stack)-1]
		d.steps--
		if d.mark[i] == d.generation {
			continue
		}
		d.mark[i] = d.generation
		switch nfa.kind[i] {
		case aSplit:
			d.stack = append(d.stack, nfa.alt[i], nfa.out[i])
		case aBol:
			if atStart {
				d.stack = append(d.stack, nfa.out[i])
			}
		case aEol:
			if atEnd {
				d.stack = append(d.stack, nfa.out[i])
			} else {
				out = append(out, i)
			}
		case aWord, aNotWord:
			if !words {
				out = append(out, i)
			} else if (before != after) == (nfa.kind[i] == aWord) {
				d.stack = append(d.stack, nfa.out[i])
			}
		case aMatch:
			return append(out, i)
		default:
			out = append(out, i)
		}
	}
	return out
}

func (d *adfaDet) has(set []int, k uint8) bool {
	for _, i := range set {
		if d.nfa.kind[i] == k {
			return true
		}
	}
	return false
}

// skipPositions is the most positions looked at for a window.
const skipPositions = 16

// unitFrequency is how often a unit is found in text, roughly: what a window's worth is reckoned by.
func unitFrequency(c int) float64 {
	switch {
	case c == ' ':
		return 0.15
	case 'a' <= c && c <= 'z':
		return 0.06
	case c == '\n' || c == '\r' || c == '.' || c == ',':
		return 0.015
	case 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return 0.004
	}
	return 0.003
}

// skipClosure returns the states each depth's threads are at for skipWindow, assertions taken as passed (they only
// rule threads out), or false where a match can end, so that no later position is known.
func skipClosure(nfa *adfaNfa, from []int) ([]int, bool) {
	seen := make([]bool, len(nfa.kind))
	stack := slices.Clone(from)
	var sets []int
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[i] {
			continue
		}
		seen[i] = true
		switch nfa.kind[i] {
		case aMatch:
			return nil, false
		case aSet:
			sets = append(sets, i)
		case aSplit:
			stack = append(stack, nfa.out[i], nfa.alt[i])
		default:
			stack = append(stack, nfa.out[i])
		}
	}
	return sets, true
}

// skipWindow returns the window of a tree (after simplify) worth reading one unit of instead of every unit, or nil:
// the units each position before a match can end can be, from the pattern's automaton without its determinisation,
// and the window whose units are rare enough that one unit read passes over three starts or more, reckoned by
// unitFrequency.
func skipWindow(root *node) *skipWin {
	b := &adfaNfa{}
	match := b.addTo(aMatch, 0)
	start := b.compile(root, match)
	if b.failed {
		return nil
	}
	var tables [][128]bool
	var wides []bool
	threads := []int{start}
	for len(tables) < skipPositions {
		sets, ok := skipClosure(b, threads)
		if !ok {
			break
		}
		var table [128]bool
		wide := false
		for _, i := range sets {
			for c := range 128 {
				table[c] = table[c] || b.has(i, c)
			}
			wide = wide || b.wide[i]
		}
		tables = append(tables, table)
		wides = append(wides, wide)
		// A character beyond ASCII takes more than one unit of UTF-8: the positions after it are not known.
		if wide {
			break
		}
		next := make([]int, 0, len(sets))
		for _, i := range sets {
			next = append(next, b.out[i])
		}
		threads = next
	}
	bestWorth, bestLo, bestHi := 0.0, -1, -1
	for lo := range tables {
		var table [128]bool
		wide := false
		for hi := lo; hi < len(tables); hi++ {
			for c := range 128 {
				table[c] = table[c] || tables[hi][c]
			}
			wide = wide || wides[hi]
			p := 0.0
			if wide {
				p = 0.1
			}
			for c := range 128 {
				if table[c] {
					p += unitFrequency(c)
				}
			}
			worth := float64(hi-lo+1) * (1.0 - min(p, 1.0))
			if worth >= 3.0 && (bestLo < 0 || worth > bestWorth) {
				bestWorth, bestLo, bestHi = worth, lo, hi
			}
		}
	}
	if bestLo < 0 {
		return nil
	}
	s := &skipWin{hi: bestHi, len: bestHi - bestLo + 1}
	for k := bestLo; k <= bestHi; k++ {
		for c := range 128 {
			s.table[c] = s.table[c] || tables[k][c]
		}
		s.wide = s.wide || wides[k]
	}
	return s
}

// buildAdfa builds the anchored automaton of a tree (after simplify), or returns nil when the pattern has none.
func buildAdfa(root *node) *anchoredDfa { return buildAdfaWith(root, false) }

// buildForwardDfa builds the forward automaton of a tree: the anchored automaton with a new start joined at every
// unit, behind every thread already alive (so last in preference, as the search tries later starts after earlier
// ones). Read over the text from a start, it says where the first match ends, and its start states say where no thread
// of an earlier start is alive: the leftmost match starts at or after the last of them, the forward search of the
// regex crate.
func buildForwardDfa(root *node) *anchoredDfa { return buildAdfaWith(root, true) }

// unitClasses fills classOf with the classes of units, those every set has both or neither of, and returns the number
// of classes.
func unitClasses(nfa *adfaNfa, classOf *[128]byte, words bool) int {
	signatures := map[string]int{}
	sig := make([]byte, 0, len(nfa.kind)+1)
	for c := range 128 {
		sig = sig[:0]
		if words {
			sig = append(sig, boolByte(isWordByte(byte(c))))
		}
		for s := range nfa.kind {
			if nfa.kind[s] == aSet {
				sig = append(sig, boolByte(nfa.has(s, c)))
			}
		}
		cls, ok := signatures[string(sig)]
		if !ok {
			cls = len(signatures)
			signatures[string(sig)] = cls
		}
		classOf[c] = byte(cls)
	}
	return len(signatures)
}

// adfaTable is the states of an automaton being built, by their key.
type adfaTable struct {
	ids    map[string]int
	sets   [][]int
	bits   [][2]bool
	trans  []int
	atEnd  []bool
	stride int
}

func adfaKey(set []int, word, atStart bool) string {
	var k strings.Builder
	for _, i := range set {
		k.WriteByte(byte(i))
		k.WriteByte(byte(i >> 8))
	}
	k.WriteByte(boolByte(word))
	k.WriteByte(boolByte(atStart))
	return k.String()
}

// decode returns the threads of a forward state with those of the current start numbered as the pattern numbers them.
func decode(set []int, n int) []int {
	out := make([]int, len(set))
	for k, i := range set {
		if i >= n {
			i -= n
		}
		out[k] = i
	}
	return out
}

func buildAdfaWith(root *node, forward bool) *anchoredDfa {
	nfa := &adfaNfa{}
	match := nfa.addTo(aMatch, 0)
	start := nfa.compile(root, match)
	if nfa.failed {
		return nil
	}
	d := newAdfaDet(nfa)
	// In the forward automaton a thread of the start joined at the current unit is kept apart from the threads of
	// earlier starts, as its state's number plus the number of states, so that a state whose threads are all of the
	// current start (a start state) is known: no earlier start can still match.
	n := len(nfa.kind)

	a := &anchoredDfa{}
	classes := unitClasses(nfa, &a.classOf, true)
	shift := uint(0)
	for 1<<shift < classes {
		shift++
	}
	stride := 1 << shift
	sample := make([]int, classes)
	for c := 127; c >= 0; c-- {
		sample[a.classOf[c]] = c
	}

	// The word bit matters only to a pattern with a boundary and the start bit only to one with a start anchor (each
	// may be reached later, behind an end anchor or a boundary still to pass, so the whole pattern is asked).
	boundaries, anchors := false, false
	for s := range n {
		boundaries = boundaries || nfa.kind[s] == aWord || nfa.kind[s] == aNotWord
		anchors = anchors || nfa.kind[s] == aBol
	}
	t := &adfaTable{ids: map[string]int{}, sets: [][]int{nil}, bits: [][2]bool{{}}, trans: make([]int, stride),
		atEnd: []bool{false}, stride: stride}
	startList := []int{start}
	for k := range 4 {
		word, atStart := k&2 != 0, k&1 != 0
		first := d.closure(nil, startList, atStart, false, false, false, false)
		if forward {
			for j := range first {
				first[j] += n
			}
		}
		s := t.intern(d, first, word && boundaries, atStart && anchors, n)
		if s < 0 {
			return nil
		}
		a.starts[k] = s
	}
	var resolved, moved []int
	for id := 1; id < len(t.sets); id++ {
		set := decode(t.sets[id], n)
		word, atStart := t.bits[id][0], t.bits[id][1]
		for cl := range classes {
			c := sample[cl]
			resolved = d.closure(resolved[:0], set, atStart, false, true, word, isWordByte(byte(c)))
			flag := 0
			for j, i := range resolved {
				if nfa.kind[i] == aMatch {
					// A match ends before this unit; only the threads it would have been tried after go on.
					flag = adfaMatched
					resolved = resolved[:j]
					break
				}
			}
			moved = moved[:0]
			for _, i := range resolved {
				if nfa.kind[i] == aSet && nfa.has(i, c) {
					moved = append(moved, nfa.out[i])
				}
			}
			next := d.closure(nil, moved, false, false, false, false, false)
			if forward && flag == 0 {
				for _, i := range d.closureOn(nil, startList, false, false, false, false, false) {
					next = append(next, i+n)
				}
			}
			to := t.intern(d, next, isWordByte(byte(c)) && boundaries, false, n)
			if to < 0 || d.steps < 0 {
				return nil
			}
			t.trans[id*stride+cl] = to | flag
		}
	}
	a.shift = shift
	a.fast = make([]uint32, len(t.trans))
	for k, v := range t.trans {
		f := uint32(v&^adfaMatched) << shift
		if v&adfaMatched != 0 {
			f |= fastMatched
		}
		a.fast[k] = f
	}
	a.atEnd = t.atEnd[:len(t.sets)]
	a.context = a.starts[1] != a.starts[0] || a.starts[2] != a.starts[0] || a.starts[3] != a.starts[0]
	if forward {
		a.startLike = make([]bool, len(t.sets))
		for id, set := range t.sets {
			all := len(set) > 0
			for _, i := range set {
				all = all && i >= n
			}
			a.startLike[id] = all
		}
	}
	return a
}

// intern gives a list of threads its state, keyed with the bits that matter, or returns -1 when there are too many.
func (t *adfaTable) intern(d *adfaDet, set []int, word, atStart bool, n int) int {
	if len(set) == 0 {
		return adfaDead
	}
	key := adfaKey(set, word, atStart)
	if known, ok := t.ids[key]; ok {
		return known
	}
	if len(t.sets) >= adfaMaxStates {
		return -1
	}
	id := len(t.sets)
	closed := d.closure(nil, decode(set, n), atStart, true, true, word, false)
	t.atEnd = append(t.atEnd, d.has(closed, aMatch))
	t.trans = append(t.trans, make([]int, t.stride)...)
	t.ids[key] = id
	t.sets = append(t.sets, slices.Clone(set))
	t.bits = append(t.bits, [2]bool{word, atStart})
	return id
}

// buildReverseDfa returns the reverse automaton of the prefixes of a search's anchors, or nil when one holds an
// assertion or the automaton is too large.
func buildReverseDfa(prefixes [][]*node) *reverseDfa {
	b := &adfaNfa{}
	match := b.addTo(aMatch, 0)
	var starts []int
	for _, prefix := range prefixes {
		next := match
		for _, t := range prefix {
			next = b.compileBack(t, next)
		}
		starts = append(starts, next)
	}
	if b.failed || len(starts) == 0 {
		return nil
	}
	start := starts[len(starts)-1]
	for k := len(starts) - 2; k >= 0; k-- {
		start = b.split(starts[k], start)
	}
	if b.failed {
		return nil
	}
	d := newAdfaDet(b)
	r := &reverseDfa{}
	classes := unitClasses(b, &r.classOf, false)
	shift := uint(0)
	for 1<<shift < classes {
		shift++
	}
	stride := 1 << shift
	sample := make([]int, classes)
	for c := 127; c >= 0; c-- {
		sample[r.classOf[c]] = c
	}
	ids := map[string]int{}
	sets := [][]int{nil}
	trans := make([]int, 2*stride)
	accept := []bool{false, false}
	first := reverseClosure(d, []int{start})
	ids[adfaKey(first, false, false)] = 1
	accept[1] = d.has(first, aMatch)
	sets = append(sets, first)
	for id := 1; id < len(sets); id++ {
		for cl := range classes {
			c := sample[cl]
			var moved []int
			for _, i := range sets[id] {
				if b.kind[i] == aSet && b.has(i, c) {
					moved = append(moved, b.out[i])
				}
			}
			next := reverseClosure(d, moved)
			to := adfaDead
			if len(next) > 0 {
				k := adfaKey(next, false, false)
				if known, ok := ids[k]; ok {
					to = known
				} else {
					if len(sets) >= adfaMaxStates {
						return nil
					}
					to = len(sets)
					accept = append(accept, d.has(next, aMatch))
					ids[k] = to
					sets = append(sets, next)
					trans = append(trans, make([]int, stride)...)
				}
			}
			if d.steps < 0 {
				return nil
			}
			trans[id*stride+cl] = to
		}
	}
	r.shift = shift
	r.fast = make([]uint32, len(trans))
	for k, v := range trans {
		f := uint32(v) << shift
		if v != adfaDead && accept[v] {
			f |= fastMatched
		}
		r.fast[k] = f
	}
	r.acceptFirst = accept[1]
	return r
}

// reverseClosure returns the closure of the reverse automaton, sorted: every thread is kept (any of them reaching the
// match is a start).
func reverseClosure(d *adfaDet, from []int) []int {
	var out []int
	d.generation++
	stack := slices.Clone(from)
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		d.steps--
		if d.mark[i] == d.generation {
			continue
		}
		d.mark[i] = d.generation
		if d.nfa.kind[i] == aSplit {
			stack = append(stack, d.nfa.alt[i], d.nfa.out[i])
		} else {
			out = append(out, i)
		}
	}
	slices.Sort(out)
	return out
}
