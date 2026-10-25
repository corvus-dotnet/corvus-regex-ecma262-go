package ecmaregex

// A deterministic automaton over ASCII text, for the patterns that need no backtracking (dfa.rs).
//
// The strings a schema tests are short and nearly always ASCII, and most patterns in schemas are small. For such a
// pattern every state set the text can lead to is worked out once, and a match is then one table read per byte.
//
// The automaton reads units below 0x80 only. A text with any other unit is handed back to the backtracking matcher
// from the start, so nothing here needs to know UTF-8 or the Unicode classes: a class is its ASCII members. It takes \b
// and \B (not those of a case-insensitive pattern with the u flag, which fold): a state carries whether the unit before
// it was a word character and whether it is at the start of the text, and a boundary still to pass is passed, or not,
// when the next unit is known (section 4.4 of the design of the .NET reference engine). A pattern whose automaton has
// more states than a byte can number has no automaton. The automaton is built on the first match, not when the
// pattern is compiled, so a program pays nothing for the patterns it never matches.

import (
	"slices"
	"strings"
)

// maxRe2Cost is the largest program a linear matcher is asked to compile, in the instructions estimated for it before
// it is written (see re2Cost). A counted repetition is written out, so a{1000} is a thousand instructions.
const maxRe2Cost = 10_000

// re2Cost estimates the number of instructions of the program a linear matcher compiles for a tree (re2.rs). A counted
// repetition is written out: x{n,m} is m copies of x, and x{n,} is n copies and a loop. The estimate stops counting
// above maxRe2Cost.
func re2Cost(n *node) int64 {
	switch n.kind {
	case nodeEmpty:
		return 0
	case nodeCat, nodeAlt, nodeGroup:
		cost := int64(len(n.subs) - 1)
		for _, sub := range n.subs {
			cost += re2Cost(sub)
			if cost > maxRe2Cost {
				return maxRe2Cost + 1
			}
		}
		return cost
	case nodeRepeat:
		copies := int64(n.max)
		if n.max == unbounded {
			copies = int64(n.min)
		}
		copies = max(copies, 1)
		// Each copy has the body and one instruction for the choice to enter it.
		body := re2Cost(n.subs[0]) + 1
		if copies > maxRe2Cost || body*copies > maxRe2Cost {
			return maxRe2Cost + 1
		}
		return body * copies
	}
	return 1
}

// The states every automaton has. dfaDead: no match can follow. dfaAccept: a match was found. dfaBail: the text is not
// ASCII.
const (
	dfaDead   = 0
	dfaAccept = 1
	dfaBail   = 2
	dfaFirst  = 3
)

// An automaton has at most this many states (they are numbered in a byte), built from at most this many states of the
// pattern, in at most this many steps (a step is one state of the pattern looked at while a set is made). The steps
// bound what a pattern too large for an automaton costs before that is known, to about a millisecond.
const (
	maxDfaStates = 256
	maxNfaStates = 512
	maxDfaSteps  = 400_000
)

// The kinds of the pattern's states. nSet reads one byte of its set and goes to out. nSplit goes to out and to alt,
// reading nothing. nBol and nEol go to out at the start and at the end of the text. nWord and nNotWord go to out where
// a word boundary holds and where it does not.
const (
	nSet uint8 = iota
	nSplit
	nBol
	nEol
	nWord
	nNotWord
	nMatch
)

type dfa struct {
	// classOf is the class of each byte: bytes the pattern does not tell apart share one.
	classOf [256]byte
	// trans[state<<shift|class] is the state after a byte.
	trans []byte
	shift uint
	start int
	// atEnd says, for each state, that the pattern matches if the text ends there.
	atEnd []bool
	// empty says the pattern matches the empty text.
	empty bool
}

// isMatch8 reports whether the pattern matches the text, and false for decided when the text is not ASCII.
func (d *dfa) isMatch8(text []byte) (matched, decided bool) {
	if len(text) == 0 {
		return d.empty, true
	}
	s := d.start
	if s == dfaAccept {
		return true, true
	}
	trans, shift := d.trans, d.shift
	for _, b := range text {
		// A byte beyond ASCII is in class 0.
		s = int(trans[s<<shift|int(d.classOf[b])])
		if s < dfaFirst {
			return s == dfaAccept, s != dfaBail
		}
	}
	return d.atEnd[s], true
}

// isMatch16 is isMatch8 over UTF-16 text.
func (d *dfa) isMatch16(text []uint16) (matched, decided bool) {
	if len(text) == 0 {
		return d.empty, true
	}
	s := d.start
	if s == dfaAccept {
		return true, true
	}
	trans, shift := d.trans, d.shift
	for _, u := range text {
		// A unit beyond ASCII is in class 0, as every byte beyond ASCII is.
		s = int(trans[s<<shift|int(d.classOf[min(u, 0x80)])])
		if s < dfaFirst {
			return s == dfaAccept, s != dfaBail
		}
	}
	return d.atEnd[s], true
}

// nfa is the states of the pattern, as parallel slices.
type nfa struct {
	kind       []uint8
	set0, set1 []uint64
	out, alt   []int
	// failed says the pattern has something an automaton over bytes cannot decide, or is too large.
	failed bool
}

func (a *nfa) add(k uint8, s0, s1 uint64, o, alt int) int {
	if len(a.kind) >= maxNfaStates {
		a.failed = true
		return 0
	}
	a.kind = append(a.kind, k)
	a.set0 = append(a.set0, s0)
	a.set1 = append(a.set1, s1)
	a.out = append(a.out, o)
	a.alt = append(a.alt, alt)
	return len(a.kind) - 1
}

func (a *nfa) has(i, c int) bool {
	if c < 64 {
		return a.set0[i]>>c&1 != 0
	}
	return a.set1[i]>>(c-64)&1 != 0
}

// compile adds the states of a node that go on to next, and returns the state they start at.
func (a *nfa) compile(n *node, next int) int {
	if a.failed {
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
		return a.add(nSet, s0, s1, next, 0)
	case nodeSet:
		return a.add(nSet, n.set.ascii[0], n.set.ascii[1], next, 0)
	case nodeCat:
		for k := len(n.subs) - 1; k >= 0; k-- {
			next = a.compile(n.subs[k], next)
		}
		return next
	case nodeAlt:
		last := len(n.subs) - 1
		start := a.compile(n.subs[last], next)
		for k := last - 1; k >= 0; k-- {
			out := a.compile(n.subs[k], next)
			start = a.add(nSplit, 0, 0, out, start)
		}
		return start
	case nodeGroup:
		return a.compile(n.subs[0], next)
	case nodeRepeat:
		sub := n.subs[0]
		if n.max == unbounded {
			// The loop, then the copies that must match before it.
			loop := a.add(nSplit, 0, 0, 0, next)
			if a.failed {
				return 0
			}
			body := a.compile(sub, loop)
			a.out[loop] = body
			next = loop
		} else {
			// The copies that may match, innermost last.
			for i := n.min; i < n.max && !a.failed; i++ {
				out := a.compile(sub, next)
				next = a.add(nSplit, 0, 0, out, next)
			}
		}
		for i := int32(0); i < n.min && !a.failed; i++ {
			next = a.compile(sub, next)
		}
		return next
	case nodeBol:
		return a.add(nBol, 0, 0, next, 0)
	case nodeEol:
		return a.add(nEol, 0, 0, next, 0)
	case nodeWordBoundary:
		if !n.fold {
			return a.add(nWord, 0, 0, next, 0)
		}
	case nodeNotWordBoundary:
		if !n.fold {
			return a.add(nNotWord, 0, 0, next, 0)
		}
	}
	// A word boundary that folds, or anything that needs the backtracking matcher.
	a.failed = true
	return 0
}

// dfaBuilder makes the automaton's states from sets of the pattern's states.
type dfaBuilder struct {
	a *nfa
	// mark and generation are the states visited by closure, and stack its stack.
	mark       []int
	generation int
	stack      []int
	// steps is the steps left (see maxDfaSteps).
	steps int
	// ids, sets and bits are the states of the automaton being built.
	ids  map[string]int
	sets [][]int
	bits [][2]bool
}

// closure adds to set every state that reading nothing reaches from the states in from, keeping the states that read
// a byte, the anchors and boundaries still to pass, and the match. A start anchor is passed only atStart, and an end
// anchor only atEnd. A word boundary is kept to pass later when words is false, and otherwise (whether the units before
// and after the position are word characters) is passed or dropped.
func (b *dfaBuilder) closure(set, from []int, atStart, atEnd, words, before, after bool) []int {
	b.generation++
	b.stack = append(b.stack[:0], from...)
	for len(b.stack) > 0 {
		i := b.stack[len(b.stack)-1]
		b.stack = b.stack[:len(b.stack)-1]
		b.steps--
		if b.mark[i] == b.generation {
			continue
		}
		b.mark[i] = b.generation
		switch b.a.kind[i] {
		case nSplit:
			b.stack = append(b.stack, b.a.out[i], b.a.alt[i])
		case nBol:
			if atStart {
				b.stack = append(b.stack, b.a.out[i])
			}
		case nEol:
			if atEnd {
				b.stack = append(b.stack, b.a.out[i])
			} else {
				set = append(set, i)
			}
		case nWord, nNotWord:
			if !words {
				set = append(set, i)
			} else if (before != after) == (b.a.kind[i] == nWord) {
				b.stack = append(b.stack, b.a.out[i])
			}
		default:
			set = append(set, i)
		}
	}
	return set
}

func (b *dfaBuilder) hasMatch(set []int) bool {
	for _, i := range set {
		if b.a.kind[i] == nMatch {
			return true
		}
	}
	return false
}

// dfaKey returns a state of the automaton as a map key: its states in increasing order, then whether the unit before
// it was a word character and whether it is at the start of the text.
func dfaKey(set []int, word, atStart bool) string {
	s := slices.Clone(set)
	slices.Sort(s)
	var k strings.Builder
	for _, i := range s {
		k.WriteByte(byte(i))
		k.WriteByte(byte(i >> 8))
	}
	flags := byte(0)
	if word {
		flags |= 2
	}
	if atStart {
		flags |= 1
	}
	k.WriteByte(flags)
	return k.String()
}

// buildDfa builds the automaton of a pattern for an unanchored search, or returns nil when it has none.
func buildDfa(root *node) *dfa {
	a := &nfa{}
	match := a.add(nMatch, 0, 0, 0, 0)
	startState := a.compile(root, match)
	if a.failed {
		return nil
	}
	b := &dfaBuilder{a: a, mark: make([]int, len(a.kind)), steps: maxDfaSteps, ids: map[string]int{}}

	// The classes of bytes: two bytes are in one class when every set of the pattern has both or neither, and both or
	// neither is a word character. Class 0 is the bytes that are not ASCII.
	d := &dfa{}
	classes := 1
	signatures := map[string]int{}
	signature := make([]byte, 0, len(a.kind)+1)
	for c := 0; c < 128; c++ {
		signature = signature[:0]
		signature = append(signature, boolByte(isWordByte(byte(c))))
		for s := range a.kind {
			if a.kind[s] == nSet {
				signature = append(signature, boolByte(a.has(s, c)))
			}
		}
		cls, ok := signatures[string(signature)]
		if !ok {
			cls = classes
			classes++
			signatures[string(signature)] = cls
		}
		d.classOf[c] = byte(cls)
	}
	for 1<<d.shift < classes {
		d.shift++
	}
	// One byte of each class, to move a set by.
	var sample [129]int
	for c := 127; c >= 0; c-- {
		sample[d.classOf[c]] = c
	}

	// The search is unanchored: a match may start at any position, so after every byte the states the pattern starts
	// in (not at the start of the text) join the set.
	startList := []int{startState}
	restart := b.closure(nil, startList, false, false, false, false, false)

	for range dfaFirst {
		b.sets = append(b.sets, nil)
		b.bits = append(b.bits, [2]bool{})
	}
	stride := 1 << d.shift
	d.trans = make([]byte, dfaFirst*stride)
	d.atEnd = make([]bool, dfaFirst)
	for cls := 0; cls < stride; cls++ {
		d.trans[dfaAccept*stride+cls] = dfaAccept
		d.trans[dfaBail*stride+cls] = dfaBail
	}
	first := b.closure(nil, startList, true, false, false, false, false)
	s0 := b.intern(d, stride, first, false, true)
	if s0 < 0 {
		return nil
	}
	d.start = s0
	d.empty = d.start == dfaAccept || d.atEnd[d.start]
	var resolved, moved, next []int
	for id := dfaFirst; id < len(b.sets); id++ {
		set := b.sets[id]
		word, atStart := b.bits[id][0], b.bits[id][1]
		for cls := 0; cls < stride; cls++ {
			if cls == 0 || cls >= classes {
				// Not ASCII (or no such class).
				d.trans[id*stride+cls] = dfaBail
				continue
			}
			c := sample[cls]
			// The boundaries at this position are passed now that the unit after it is known.
			resolved = b.closure(resolved[:0], set, atStart, false, true, word, isWordByte(byte(c)))
			if b.hasMatch(resolved) {
				d.trans[id*stride+cls] = dfaAccept
				continue
			}
			moved = moved[:0]
			for _, i := range resolved {
				if a.kind[i] == nSet && a.has(i, c) {
					moved = append(moved, a.out[i])
				}
			}
			moved = append(moved, restart...)
			next = b.closure(next[:0], moved, false, false, false, false, false)
			to := b.intern(d, stride, next, isWordByte(byte(c)), false)
			if to < 0 || b.steps < 0 {
				return nil
			}
			d.trans[id*stride+cls] = byte(to)
		}
	}
	return d
}

func boolByte(b bool) byte {
	if b {
		return '1'
	}
	return '0'
}

// intern gives a set its state, or returns -1 when there are too many.
func (b *dfaBuilder) intern(d *dfa, stride int, set []int, word, atStart bool) int {
	if b.hasMatch(set) {
		return dfaAccept
	}
	if len(set) == 0 {
		return dfaDead
	}
	key := dfaKey(set, word, atStart)
	if known, ok := b.ids[key]; ok {
		return known
	}
	if len(b.sets) >= maxDfaStates {
		return -1
	}
	id := len(b.sets)
	b.ids[key] = id
	b.sets = append(b.sets, slices.Clone(set))
	b.bits = append(b.bits, [2]bool{word, atStart})
	d.trans = append(d.trans, make([]byte, stride)...)
	// At the end of the text the end anchors are passed, and a boundary is between a unit and none.
	closed := b.closure(nil, set, atStart, true, true, word, false)
	d.atEnd = append(d.atEnd, b.hasMatch(closed))
	return id
}
