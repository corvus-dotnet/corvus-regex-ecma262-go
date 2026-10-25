package ecmaregex

// The compiler: a syntax tree to a program of the backtracking matcher, with what the matcher needs to give up early
// (compile.rs).

import (
	"slices"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

type op uint8

const (
	// opChar and opSet consume one code point going forward. The Back forms consume the one before the position,
	// which is how the body of a lookbehind runs.
	opChar op = iota
	opCharBack
	opSet
	opSetBack
	// opLit consumes a run of literal bytes going forward.
	opLit
	opFail
	opBol
	opEol
	opWordBoundary
	opNotWordBoundary
	// opLineStart and opLineEnd are ^ and $ with the m modifier. The Fold forms of the word boundaries and of the
	// backreferences belong to a case-insensitive group.
	opLineStart
	opLineEnd
	opWordBoundaryFold
	opNotWordBoundaryFold
	opJmp
	// opSplit continues at x and leaves y as the alternative.
	opSplit
	// opSaveTmp notes where a group was entered. opCapture records the group once it has closed.
	opSaveTmp
	opCapture
	// opClear resets the groups a to b, as ECMA-262 does at the start of each iteration of a quantified body.
	opClear
	opBackref
	opBackrefBack
	opBackrefFold
	opBackrefFoldBack
	// opBackrefUnits and opBackrefUnitsBack compare a group a code unit at a time, for a pattern matched by code unit.
	opBackrefUnits
	opBackrefUnitsBack
	// opLook opens a lookaround. opLookOk ends the body of a positive one and opLookFail the body of a negative one.
	opLook
	opLookOk
	opLookFail
	// opRep and opRepLazy repeat one character or class without entering the general loop.
	opRep
	opRepLazy
	// opLoopInit, opLoop and opLoopIter run a counted or possibly empty quantified body.
	opLoopInit
	opLoop
	opLoopIter
	opMatch
)

// inst is one instruction of the backtracking matcher.
type inst struct {
	op op
	// flag marks a lazy opLoop, a backward opCapture, a negative opLook, and an opBackrefFold that folds case as the u
	// flag grammar does.
	flag bool
	r    rune
	set  *charSet
	// lit8 and lit16 are the literal run of an opLit, in UTF-8 and in UTF-16.
	lit8  []byte
	lit16 []uint16
	// back is, for an opRep, the ASCII bytes (up to three) what follows it can start with, when those are all it can
	// start with: its give-back looks for them with a search back (see setBack).
	back []byte
	// x and y are jump targets.
	x, y int32
	// a and b are a group, a loop or a range of groups, depending on the instruction.
	a, b int32
	// min and max bound a repetition. A max of -1 is no bound.
	min, max int32
	// need8 is the least number of bytes of text that every way from this instruction to a match consumes going
	// forward (see setNeeds). It is set for an opLoop, and counts none of the iterations the loop has still to make.
	// need16 is the same in UTF-16 code units.
	need8, need16 int32
	// iter8 is the least number of bytes one iteration of the body of an opLoop consumes going forward, and iter16 the
	// least number of UTF-16 code units.
	iter8, iter16 int32
	// m is the number of the instruction's memo point, counted from 1, or 0 when it has none (see memoPoint). It is
	// set for an opSplit, an opLoop, an opRep and an opRepLazy.
	m int32
}

// maxMemoLoops is the number of loops whose state a memo point can tell apart.
const maxMemoLoops = 2

// memoPoint is a place where the matcher makes a choice and can remember that the choice has failed: a split, a loop
// that may be entered or left, and what follows a single character loop that gives characters back or takes more.
// Whether a way from such a place leads to a match depends on the place and the position alone when nothing else the
// matcher keeps can change what follows. So a pattern with a backreference has no memo points (what a group holds
// changes what follows), and neither has a place inside a loop that counts its iterations up to a bound. Inside a loop
// with no upper bound and a least count of zero or one, one thing more decides what follows: whether the loop would
// refuse the iteration as empty if it ended here. That is one bit for each such loop the place is in (loops,
// innermost last), and a place in more than maxMemoLoops of them has no memo point.
type memoPoint struct {
	loops [maxMemoLoops]int32
	mins  [maxMemoLoops]int32
}

// loopContext is one counted loop that the instruction being written is inside of: its number, its least count, and
// whether its state is one bit to a memo point (no upper bound, and a least count of zero or one).
type loopContext struct {
	loop, min int32
	simple    bool
}

// program is a pattern compiled for the backtracking matcher.
type program struct {
	insts []inst
	// groups is the number of groups the program records (every capturing group, or those some backreference names),
	// and loops the number of counted loops.
	groups, loops int
	// units says the pattern was read without the u flag, so the text is matched by UTF-16 code unit (see units.go).
	units bool
	// anchored says every match starts at the start of the text.
	anchored bool
	// need8 is the least number of bytes a match consumes (see setNeeds), and need16 of UTF-16 code units.
	need8, need16 int
	// points are the memo points of the program (see memoPoint).
	points []memoPoint
	// quick says, for each instruction, which characters can come next on a way from it to the end of its match (see
	// setQuick).
	quick []quick
	// first is every code point a match can start with, or nil when a match can be empty or when the set is not
	// known.
	first *charSet
	// search is how the search finds where a match can start (see search.go).
	search *search
	// anchoredDfa is the anchored automaton that finds where a match from a start ends, for a program that records no
	// group and needs no backtracking (see adfa.go).
	anchoredDfa *anchoredDfa
	// forwardRoot is the tree of a pattern whose search tries every start its first set allows, from which the forward
	// automaton (see buildForwardDfa) is built on the first search that wants it.
	forwardRoot *node
	forwardOnce sync.Once
	forwardDfa  *anchoredDfa
	// skip is, for the same patterns, the window of positions read one unit at a time before the first set is
	// searched for (see skipWin).
	skip *skipWin
	// afterLiteral is, for a pattern every match of which starts with one literal (a window at distance 0 of one
	// literal over UTF-8), the anchored automaton's states after it (see anchoredDfa.after), and afterLength its
	// length: an occurrence goes on from there, its units not read again.
	afterLiteral *[4]uint32
	afterLength  int
}

// forward returns the forward automaton, built on the first search that wants it, or nil when there is none.
func (p *program) forward() *anchoredDfa {
	if p.forwardRoot == nil {
		return nil
	}
	p.forwardOnce.Do(func() { p.forwardDfa = buildForwardDfa(p.forwardRoot) })
	return p.forwardDfa
}

type compiler struct {
	insts []inst
	// tracked maps a capturing group's number to its slot, for the groups that are recorded.
	tracked map[int32]int32
	loops   int
	// unicode says the pattern was read with the u flag grammar, which decides how a backreference folds case.
	unicode bool
	// memo says the pattern has no backreference, so its choices can have memo points. open lists the counted loops
	// around the instruction being written, outermost first, inside the innermost lookaround: what a lookaround's body
	// does depends on nothing outside it. points collects the memo points.
	memo   bool
	open   []loopContext
	points []memoPoint
}

// compileProgram compiles a tree, changing it in place. With groups at 0 or more the program records every one of
// that many capturing groups, group g in slot g - 1, so that a match can report what each captured; with groups below
// 0, only the groups some backreference names.
func compileProgram(root *node, unicode bool, groups int) *program {
	var open []int32
	root = simplify(root, &open)
	c := &compiler{tracked: map[int32]int32{}, unicode: unicode}
	var referenced []int32
	collectBackrefs(root, &referenced)
	// A choice can be remembered to have failed only where what a group holds cannot change what follows.
	c.memo = len(referenced) == 0
	if groups >= 0 {
		for g := int32(1); g <= int32(groups); g++ {
			c.tracked[g] = g - 1
		}
	} else {
		// Slots follow group order, so the groups inside any one subtree take a contiguous range of slots.
		slices.Sort(referenced)
		referenced = slices.Compact(referenced)
		for slot, g := range referenced {
			c.tracked[g] = int32(slot)
		}
	}
	c.emit(root, false)
	c.add(inst{op: opMatch})
	insts := c.insts
	need, need16 := setNeeds(insts)
	q := setQuick(insts)
	setBack(insts, q)
	p := &program{
		insts:    insts,
		groups:   len(c.tracked),
		loops:    c.loops,
		units:    !unicode,
		anchored: startsAtBol(root),
		need8:    need,
		need16:   need16,
		points:   c.points,
		quick:    q,
	}
	backrefs := len(referenced) > 0
	if p.groups == 0 && !backrefs {
		p.anchoredDfa = buildAdfa(root)
	}
	p.search = &search{}
	if !p.anchored {
		var pairs []rune
		if nullable, known := firstChars(root, &pairs); known && !nullable {
			p.first = newCharSet(pairs, false)
		}
		p.search = planSearch(root, backrefs, p.first, !unicode)
		if p.search.plan == planWindows && p.search.hi == 0 && p.anchoredDfa != nil && p.search.literals.single8 != nil {
			literal := p.search.literals.single8
			if rows, ok := p.anchoredDfa.after(literal); ok {
				p.afterLiteral, p.afterLength = &rows, len(literal)
			}
		}
		if !noSearch.Load() && !backrefs && shallowerThan(root, 64) && p.search.plan == planScan &&
			p.search.shape == shapeNone {
			p.forwardRoot = root
			p.skip = skipWindow(root)
		}
	}
	return p
}

// shallowerThan reports whether a tree nests fewer than limit levels.
func shallowerThan(n *node, limit int) bool {
	if limit <= 0 {
		return false
	}
	for _, s := range n.subs {
		if !shallowerThan(s, limit-1) {
			return false
		}
	}
	return true
}

// simplify returns the tree with two rewrites, both of which V8 makes. Neither changes what the pattern matches or
// what any group holds. Without them a pattern can take time exponential in its counts for no reason that ECMA-262
// gives.
//
// A backreference to a group it is inside of is the empty string. The group is not set until it closes, and the
// repetition around a group clears it at the start of each iteration, so inside the group it is never set.
//
// A repetition whose body cannot consume a character, such as (?:\b|(?=a)|){4,5}, is the body once when its minimum
// is one or more, and nothing when its minimum is zero. Every iteration of such a body is empty. ECMA-262 refuses an
// empty iteration after the minimum, so the iterations are exactly the minimum. Each of them starts with the groups of
// the body cleared and at the same position, so what the last one can do does not depend on the ones before it, and
// the ways through the last one come in the same order whatever the others chose. Run as written, the matcher would
// try every combination of the ways through every iteration.
func simplify(n *node, open *[]int32) *node {
	if n.kind == nodeBackref {
		all := true
		for _, g := range n.groups {
			if !slices.Contains(*open, g) {
				all = false
				break
			}
		}
		if all {
			return newNode(nodeEmpty)
		}
		return n
	}
	if len(n.subs) == 0 {
		return n
	}
	opened := n.kind == nodeGroup && n.group > 0
	if opened {
		*open = append(*open, n.group)
	}
	for k, sub := range n.subs {
		n.subs[k] = simplify(sub, open)
	}
	if opened {
		*open = (*open)[:len(*open)-1]
	}
	if n.kind == nodeCat && slices.ContainsFunc(n.subs, func(s *node) bool { return s.kind == nodeCat }) {
		// A sequence inside a sequence (a written-out repetition) is the same sequence: flat, so that what reads the
		// terms (the searches, the first characters) sees them all.
		flat := make([]*node, 0, len(n.subs))
		for _, sub := range n.subs {
			if sub.kind == nodeCat {
				flat = append(flat, sub.subs...)
			} else {
				flat = append(flat, sub)
			}
		}
		n.subs = flat
	}
	if n.kind == nodeRepeat && n.max != 0 && !consumes(n.subs[0]) {
		if n.min == 0 {
			return newNode(nodeEmpty)
		}
		return n.subs[0]
	}
	if unrolling && n.kind == nodeRepeat && n.min == n.max && 2 <= n.max && n.max <= 32 &&
		(n.subs[0].kind == nodeSet || n.subs[0].kind == nodeChar) {
		// An exact count of one character or class is that many of it in a row: a straight run of tests without a
		// loop's counting, which pays where a search tries the run at every occurrence of a literal after it
		// ([a-q][^u-z]{13}x). (The .NET reference engine's Simplify.)
		return unroll(n)
	}
	if unrolling && n.kind == nodeRepeat && 1 <= n.max && n.max <= 4 && n.min >= 1 && !nullable(n.subs[0]) &&
		!hasCapture(n.subs[0]) && treeSize(n.subs[0]) <= 32 &&
		(n.subs[0].kind == nodeGroup || n.subs[0].kind == nodeAlt || n.subs[0].kind == nodeCat) {
		// A small counted repetition of a body that holds no group and cannot be empty is the body written out, with
		// the copies beyond the least count optional one inside the other: the same matches in the same order, without
		// a loop's counting. A body of one character stays a counted loop, which the matcher runs without choices.
		return unroll(n)
	}
	return n
}

// unrolling says whether small counted repetitions are written out. The tests of the loops' needs and memo points
// turn it off, to keep every counted repetition a loop.
var unrolling = true

// unroll writes out a counted repetition (see simplify): X{2,4} is X X (X (X)?)?, the optional copies one inside the
// other, so that a copy is tried only when the one before it matched.
func unroll(n *node) *node {
	body := n.subs[0]
	terms := make([]*node, 0, n.min+1)
	for k := int32(0); k < n.min; k++ {
		terms = append(terms, cloneNode(body))
	}
	if n.max > n.min {
		var optional *node
		for k := n.min; k < n.max; k++ {
			inner := cloneNode(body)
			if optional != nil {
				inner = withSubs(nodeCat, inner, optional)
			}
			optional = &node{kind: nodeRepeat, min: 0, max: 1, lazy: n.lazy, subs: []*node{inner}}
		}
		terms = append(terms, optional)
	}
	if len(terms) == 1 {
		return terms[0]
	}
	flat := make([]*node, 0, len(terms))
	for _, t := range terms {
		if t.kind == nodeCat {
			flat = append(flat, t.subs...)
		} else {
			flat = append(flat, t)
		}
	}
	return withSubs(nodeCat, flat...)
}

// cloneNode returns a deep copy of a tree. A class is immutable, so the copy shares it.
func cloneNode(n *node) *node {
	c := *n
	if n.subs != nil {
		c.subs = make([]*node, len(n.subs))
		for k, sub := range n.subs {
			c.subs[k] = cloneNode(sub)
		}
	}
	c.groups = slices.Clone(n.groups)
	c.name = slices.Clone(n.name)
	return &c
}

// hasCapture reports whether a node holds a capturing group, a backreference or a lookaround.
func hasCapture(n *node) bool {
	return n.kind == nodeGroup && n.group > 0 || n.kind == nodeBackref || n.kind == nodeLook ||
		slices.ContainsFunc(n.subs, hasCapture)
}

// treeSize returns the number of nodes of a tree.
func treeSize(n *node) int {
	s := 1
	for _, sub := range n.subs {
		s += treeSize(sub)
	}
	return s
}

// consumes reports whether n can match one character or more. It errs towards true.
func consumes(n *node) bool {
	switch n.kind {
	case nodeChar, nodeSet, nodeBackref:
		return true
	case nodeCat, nodeAlt, nodeGroup:
		return slices.ContainsFunc(n.subs, consumes)
	case nodeRepeat:
		return n.max != 0 && consumes(n.subs[0])
	}
	return false
}

func collectBackrefs(n *node, out *[]int32) {
	if n.kind == nodeBackref {
		*out = append(*out, n.groups...)
	}
	for _, sub := range n.subs {
		collectBackrefs(sub, out)
	}
}

func (c *compiler) add(i inst) int32 {
	c.insts = append(c.insts, i)
	return int32(len(c.insts) - 1)
}

func (c *compiler) pc() int32 { return int32(len(c.insts)) }

// point returns the number of a new memo point for an instruction written here, or 0 when a choice here cannot be
// remembered.
func (c *compiler) point() int32 {
	if !c.memo || len(c.open) > maxMemoLoops {
		return 0
	}
	p := memoPoint{loops: [maxMemoLoops]int32{-1, -1}}
	for k, l := range c.open {
		if !l.simple {
			return 0
		}
		p.loops[k], p.mins[k] = l.loop, l.min
	}
	c.points = append(c.points, p)
	return int32(len(c.points))
}

// groupRange returns the first and last slot of the tracked groups inside n, and false when it has none.
func (c *compiler) groupRange(n *node) (int32, int32, bool) {
	lo, hi, found := int32(0), int32(0), false
	var walk func(n *node)
	walk = func(n *node) {
		if n.kind == nodeGroup && n.group > 0 {
			if t, ok := c.tracked[n.group]; ok {
				if !found {
					lo, hi, found = t, t, true
				} else {
					lo, hi = min(lo, t), max(hi, t)
				}
			}
		}
		for _, sub := range n.subs {
			walk(sub)
		}
	}
	walk(n)
	return lo, hi, found
}

// emit writes the instructions for n. With back set the instructions match right to left, as the body of a
// lookbehind must.
func (c *compiler) emit(n *node, back bool) {
	switch n.kind {
	case nodeEmpty:
	case nodeChar:
		o := opChar
		if back {
			o = opCharBack
		}
		c.add(inst{op: o, r: n.r})
	case nodeSet:
		if n.set.isEmpty() {
			c.add(inst{op: opFail})
		} else {
			o := opSet
			if back {
				o = opSetBack
			}
			c.add(inst{op: o, set: n.set})
		}
	case nodeCat:
		if back {
			for k := len(n.subs) - 1; k >= 0; k-- {
				c.emit(n.subs[k], true)
			}
			return
		}
		for i := 0; i < len(n.subs); {
			// A run of literal characters becomes one comparison of bytes.
			j := i
			var lit []byte
			for j < len(n.subs) && n.subs[j].kind == nodeChar && literalRune(n.subs[j].r) {
				lit = utf8.AppendRune(lit, n.subs[j].r)
				j++
			}
			if j-i >= 2 {
				c.add(inst{op: opLit, lit8: lit, lit16: utf16.Encode([]rune(string(lit)))})
				i = j
				continue
			}
			c.emit(n.subs[i], false)
			i++
		}
	case nodeAlt:
		var jumps []int32
		for i, sub := range n.subs {
			if i == len(n.subs)-1 {
				c.emit(sub, back)
				break
			}
			m := c.point()
			split := c.add(inst{op: opSplit, m: m})
			c.insts[split].x = c.pc()
			c.emit(sub, back)
			jumps = append(jumps, c.add(inst{op: opJmp}))
			c.insts[split].y = c.pc()
		}
		for _, j := range jumps {
			c.insts[j].x = c.pc()
		}
	case nodeGroup:
		t, ok := int32(0), false
		if n.group != 0 {
			t, ok = c.tracked[n.group]
		}
		if !ok {
			c.emit(n.subs[0], back)
			return
		}
		c.add(inst{op: opSaveTmp, a: t})
		c.emit(n.subs[0], back)
		c.add(inst{op: opCapture, a: t, flag: back})
	case nodeBol:
		c.add(inst{op: opBol})
	case nodeEol:
		c.add(inst{op: opEol})
	case nodeLineStart:
		c.add(inst{op: opLineStart})
	case nodeLineEnd:
		c.add(inst{op: opLineEnd})
	case nodeWordBoundary:
		if n.fold {
			c.add(inst{op: opWordBoundaryFold})
		} else {
			c.add(inst{op: opWordBoundary})
		}
	case nodeNotWordBoundary:
		if n.fold {
			c.add(inst{op: opNotWordBoundaryFold})
		} else {
			c.add(inst{op: opNotWordBoundary})
		}
	case nodeBackref:
		var o op
		switch {
		case n.fold && back:
			o = opBackrefFoldBack
		case n.fold:
			o = opBackrefFold
		case back && !c.unicode:
			o = opBackrefUnitsBack
		case back:
			o = opBackrefBack
		case !c.unicode:
			o = opBackrefUnits
		default:
			o = opBackref
		}
		// When several groups share the name, at most one has taken part, and the others match the empty string. So
		// matching each in turn matches the one that took part.
		for _, g := range n.groups {
			c.add(inst{op: o, a: c.tracked[g], flag: c.unicode})
		}
	case nodeLook:
		look := c.add(inst{op: opLook, flag: n.negative})
		// Whether the body matches depends on nothing outside it.
		outer := c.open
		c.open = nil
		c.emit(n.subs[0], n.behind)
		c.open = outer
		if n.negative {
			c.add(inst{op: opLookFail})
		} else {
			c.add(inst{op: opLookOk})
		}
		c.insts[look].x = c.pc()
	case nodeRepeat:
		c.emitRepeat(n, back)
	}
}

func (c *compiler) emitBody(body *node, back bool, lo, hi int32, clear bool) {
	if clear {
		c.add(inst{op: opClear, a: lo, b: hi})
	}
	c.emit(body, back)
}

// split writes a choice between entering the body and leaving the loop, to be completed by setSplit.
func (c *compiler) split() int32 {
	return c.add(inst{op: opSplit, m: c.point()})
}

// setSplit completes a split in the order the quantifier prefers.
func (c *compiler) setSplit(lazy bool, at, enter, exit int32) {
	i := &c.insts[at]
	if lazy {
		i.x, i.y = exit, enter
	} else {
		i.x, i.y = enter, exit
	}
}

func (c *compiler) emitRepeat(n *node, back bool) {
	body := n.subs[0]
	if n.max == 0 {
		return
	}
	minC, maxC := n.min, n.max
	if !back && (body.kind == nodeChar || body.kind == nodeSet && !body.set.isEmpty()) {
		o := opRep
		if n.lazy {
			o = opRepLazy
		}
		c.add(inst{op: o, r: body.r, set: body.set, min: minC, max: maxC, m: c.point()})
		return
	}
	lo, hi, clear := c.groupRange(body)
	// A body that always consumes something needs no check for empty iterations, so the unbounded forms and the
	// optional form are plain choices.
	if !nullable(body) {
		switch {
		case n.min == 0 && n.max == unbounded:
			at := c.split()
			enter := c.pc()
			c.emitBody(body, back, lo, hi, clear)
			c.add(inst{op: opJmp, x: at})
			c.setSplit(n.lazy, at, enter, c.pc())
			return
		case n.min == 1 && n.max == unbounded:
			enter := c.pc()
			c.emitBody(body, back, lo, hi, clear)
			at := c.split()
			c.setSplit(n.lazy, at, enter, c.pc())
			return
		case n.min == 0 && n.max == 1:
			at := c.split()
			enter := c.pc()
			c.emitBody(body, back, lo, hi, clear)
			c.setSplit(n.lazy, at, enter, c.pc())
			return
		}
	}
	k := int32(c.loops)
	c.loops++
	c.add(inst{op: opLoopInit, a: k})
	var iter, iter16 int64
	if !back {
		iter, iter16 = minLength(body, true), minLength(body, false)
	}
	simple := maxC == unbounded && minC <= 1
	// The loop is left or entered from here, and what follows either depends on the loops around this one.
	var point int32
	if simple {
		point = c.point()
	}
	loop := c.add(inst{op: opLoop, a: k, min: minC, max: maxC, flag: n.lazy, iter8: int32(iter), iter16: int32(iter16), m: point})
	c.add(inst{op: opLoopIter, a: k})
	c.open = append(c.open, loopContext{loop: k, min: minC, simple: simple})
	c.emitBody(body, back, lo, hi, clear)
	c.open = c.open[:len(c.open)-1]
	c.add(inst{op: opJmp, x: loop})
	c.insts[loop].x = c.pc()
}

// noMatch is the need of an instruction from which no way leads to a match. Every need is capped at it.
const noMatch int64 = 1 << 30

// runeLen returns the number of bytes of UTF-8 a code point takes.
func runeLen(r rune) int64 {
	switch {
	case r <= 0x7F:
		return 1
	case r <= 0x7FF:
		return 2
	case r <= 0xFFFF:
		return 3
	}
	return 4
}

// minLength returns the least number of units a match of n consumes, capped at noMatch: bytes of UTF-8 with utf8set,
// and UTF-16 code units without it. Like the needs, it counts a character as its units when the matcher compares units
// for it and as one unit otherwise, so it can only be too small.
func minLength(n *node, utf8set bool) int64 {
	switch n.kind {
	case nodeChar:
		switch {
		case !literalRune(n.r):
			return 1
		case utf8set:
			return runeLen(n.r)
		case n.r > 0xFFFF:
			return 2
		}
		return 1
	case nodeSet:
		if n.set.isEmpty() {
			return noMatch
		}
		return 1
	case nodeCat:
		var total int64
		for _, sub := range n.subs {
			total += minLength(sub, utf8set)
			if total >= noMatch {
				return noMatch
			}
		}
		return total
	case nodeAlt:
		least := noMatch
		for _, sub := range n.subs {
			least = min(least, minLength(sub, utf8set))
		}
		return least
	case nodeGroup:
		return minLength(n.subs[0], utf8set)
	case nodeRepeat:
		return timesNeed(int64(n.min), minLength(n.subs[0], utf8set))
	}
	return 0
}

// timesNeed returns count times need, capped at noMatch.
func timesNeed(count, need int64) int64 {
	if count == 0 || need == 0 {
		return 0
	}
	if need >= noMatch || count >= noMatch/need {
		return noMatch
	}
	return count * need
}

// setNeeds works out, for every instruction, the least number of bytes of text that any way from it to a match
// consumes going forward, and writes it into the counted loops. It returns the need of the whole program. The matcher
// gives up at a counted loop when fewer bytes than that are left, as V8 does at every choice. A loop whose iterations
// can be empty would otherwise try every combination of its ways at a position from which the rest of the pattern
// cannot match at all.
//
// A counted loop that is entered needs its least count of iterations, each of them the least its body consumes
// (inst.iter), and then what follows it. The loop instruction itself is also reached after each iteration, so its own
// need counts only what follows, and the matcher adds the iterations it has still to make.
//
// A character is counted as one byte, a literal run as its bytes and a single character loop as its minimum, so the
// figure can only be too small. The body of a lookaround ends at its own last instruction and the instruction that
// opens one goes straight to what follows it, so a body is counted by itself. An instruction that matches right to
// left, which is the body of a lookbehind, counts nothing.
//
// The needs are worked out twice, in bytes of UTF-8 and in UTF-16 code units, and the two needs of the program are
// returned.
func setNeeds(insts []inst) (int, int) {
	need16, _ := needsIn(insts, false)
	need, _ := needsIn(insts, true)
	return need, need16
}

func needsIn(insts []inst, utf8set bool) (int, int) {
	need := make([]int32, len(insts))
	for k := range need {
		need[k] = int32(noMatch)
	}
	passes := 0
	// The needs only fall, and nearly every way goes forward, so going through the instructions from the last to the
	// first settles all but the loops in one pass.
	for changed := true; changed; {
		changed = false
		passes++
		for pc := len(insts) - 1; pc >= 0; pc-- {
			i := &insts[pc]
			n := noMatch
			// Both figures can be noMatch, so the sum is made in 64 bits: in 32 it would wrap to a negative need.
			next := func(to int32, weight int64) {
				if v := int64(need[to]) + weight; v < n {
					n = v
				}
			}
			after := int32(pc + 1)
			switch i.op {
			case opMatch, opLookOk, opLookFail:
				n = 0
			case opFail:
			case opChar, opSet:
				next(after, 1)
			case opLit:
				if utf8set {
					next(after, int64(len(i.lit8)))
				} else {
					next(after, int64(len(i.lit16)))
				}
			case opRep, opRepLazy:
				next(after, int64(i.min))
			case opJmp:
				// The one way that goes back is the jump at the end of the body of a loop, to the instruction at its
				// head, which this pass has not reached. The need of that instruction is the need of what follows the
				// loop, since its other way, through the body, comes back to it and cannot need less. So the jump takes
				// what follows the loop, which this pass has settled. Taking the need of the head would settle one loop
				// of a nest for each pass: a thousand passes for loops a thousand deep.
				head := &insts[i.x]
				switch {
				case int(i.x) < pc && head.op == opLoop:
					next(head.x, 0)
				case int(i.x) < pc && head.op == opSplit:
					next(head.x, 0)
					next(head.y, 0)
				default:
					next(i.x, 0)
				}
			case opLook:
				next(i.x, 0)
			case opSplit:
				next(i.x, 0)
				next(i.y, 0)
			case opLoop:
				next(after, 0)
				next(i.x, 0)
			case opLoopInit:
				loop := &insts[pc+1]
				iter := loop.iter16
				if utf8set {
					iter = loop.iter8
				}
				next(after, timesNeed(int64(loop.min), int64(iter)))
			default:
				next(after, 0)
			}
			if v := int32(min(n, noMatch)); v < need[pc] {
				need[pc] = v
				changed = true
			}
		}
	}
	for pc := range insts {
		if insts[pc].op == opLoop {
			if utf8set {
				insts[pc].need8 = need[pc]
			} else {
				insts[pc].need16 = need[pc]
			}
		}
	}
	return int(need[0]), passes
}

// quick says what can come next on a way from an instruction to the end of its match, which is the end of the pattern
// or of the lookaround the instruction is in. The matcher looks at it before it takes a way: when the next character
// of the text is none that the way can start with, and the way cannot end without one, the way fails, so it is not
// taken, and where it was one of two no choice is kept for it. V8 makes the same check ("quick check") at its choices.
// It is what keeps a choice between ways that need different characters from being a choice.
type quick struct {
	// ascii is the characters below 128 a way can consume next. other says it can consume a character that is not
	// ASCII.
	ascii [2]uint64
	other bool
	// mid says a way reaches the end of its match consuming nothing more while text is left, and end that one does at
	// the end of the text.
	mid, end bool
}

// allows8 and allows16 report whether a way can go on from the position.
func (q *quick) allows8(input []byte, pos int) bool {
	if pos >= len(input) {
		return q.end
	}
	if q.mid {
		return true
	}
	if c := input[pos]; c < 0x80 {
		return (q.ascii[c>>6]>>(c&63))&1 != 0
	}
	return q.other
}

func (q *quick) allows16(input []uint16, pos int) bool {
	if pos >= len(input) {
		return q.end
	}
	if q.mid {
		return true
	}
	if c := input[pos]; c < 0x80 {
		return (q.ascii[c>>6]>>(c&63))&1 != 0
	}
	return q.other
}

func (q *quick) addRune(r rune) {
	if 0 <= r && r < 0x80 {
		q.ascii[r>>6] |= 1 << (r & 63)
	} else {
		q.other = true
	}
}

func (q *quick) addSet(s *charSet) {
	q.ascii[0] |= s.ascii[0]
	q.ascii[1] |= s.ascii[1]
	if n := len(s.ranges); n > 0 && s.ranges[n-1] >= 0x80 {
		q.other = true
	}
}

func (q *quick) union(o quick) {
	q.ascii[0] |= o.ascii[0]
	q.ascii[1] |= o.ascii[1]
	q.other = q.other || o.other
	q.mid = q.mid || o.mid
	q.end = q.end || o.end
}

// setQuick works out the quick of every instruction. Every figure is a set that can only be too large: an assertion is
// taken to hold, a backreference and an instruction that matches right to left (the body of a lookbehind) to allow
// anything. A positive lookahead needs both its body and what follows it to go on from the same position, so it allows
// what both allow.
func setQuick(insts []inst) []quick {
	q, _ := quicks(insts)
	return q
}

// quicks is setQuick, and also the number of passes over the instructions it made, the last of which changed nothing.
func quicks(insts []inst) ([]quick, int) {
	q := make([]quick, len(insts))
	all := quick{ascii: [2]uint64{^uint64(0), ^uint64(0)}, other: true, mid: true, end: true}
	passes := 0
	// The sets only grow, and nearly every way goes forward, so going through the instructions from the last to the
	// first settles all but the loops in one pass.
	for changed := true; changed; {
		changed = false
		passes++
		for pc := len(insts) - 1; pc >= 0; pc-- {
			i := &insts[pc]
			var n quick
			switch i.op {
			case opMatch, opLookOk, opLookFail:
				n = all
			case opFail:
			case opChar:
				n.addRune(i.r)
			case opSet:
				n.addSet(i.set)
			case opLit:
				n.addRune(rune(i.lit8[0]))
			case opRep, opRepLazy:
				if i.set != nil {
					n.addSet(i.set)
				} else {
					n.addRune(i.r)
				}
				if i.min == 0 {
					n.union(q[pc+1])
				}
			case opJmp:
				// The jump at the end of the body of a loop goes back to the head of the loop, which this pass has not
				// reached. The head allows what follows the loop, which this pass has settled, and what the body starts
				// with. So the jump takes what follows the loop too, and the first pass settles the head of every loop,
				// the second every instruction, and the third finds nothing to change, however deep the loops nest.
				n = q[i.x]
				head := &insts[i.x]
				if int(i.x) < pc && head.op == opLoop {
					n.union(q[head.x])
				} else if int(i.x) < pc && head.op == opSplit {
					n.union(q[head.x])
					n.union(q[head.y])
				}
			case opSplit:
				n = q[i.x]
				n.union(q[i.y])
			case opLoop:
				n = q[pc+1]
				n.union(q[i.x])
			case opEol:
				n.end = q[pc+1].end
			case opLook:
				n = q[i.x]
				if !i.flag {
					b := q[pc+1]
					n.ascii[0] &= b.ascii[0]
					n.ascii[1] &= b.ascii[1]
					n.other, n.mid, n.end = n.other && b.other, n.mid && b.mid, n.end && b.end
				}
			case opCharBack, opSetBack, opBackref, opBackrefBack, opBackrefFold, opBackrefFoldBack, opBackrefUnits,
				opBackrefUnitsBack:
				n = all
			default:
				n = q[pc+1]
			}
			if n != q[pc] {
				q[pc] = n
				changed = true
			}
		}
	}
	return q, passes
}

// setBack gives each greedy single-character loop the ASCII bytes what follows it can start with, when there are one
// to three and nothing else can follow: the vectorised give-back of the .NET reference engine.
func setBack(insts []inst, q []quick) {
	for pc := range insts {
		if insts[pc].op != opRep || pc+1 >= len(q) {
			continue
		}
		next := q[pc+1]
		if next.other || next.mid {
			continue
		}
		var bytes []byte
		for c := byte(0); c < 0x80; c++ {
			if (next.ascii[c>>6]>>(c&63))&1 != 0 {
				bytes = append(bytes, c)
			}
		}
		if 1 <= len(bytes) && len(bytes) <= 3 {
			insts[pc].back = bytes
		}
	}
}

// literalRune reports whether a code point can be matched by comparing its UTF-8 bytes. A surrogate has no encoding,
// and U+FFFD is also what a malformed byte of the text decodes to.
func literalRune(r rune) bool {
	return 0 <= r && r <= 0x10FFFF && !(0xD800 <= r && r <= 0xDFFF) && r != runeError
}

// nullable reports whether n can match without consuming anything. It errs towards true.
func nullable(n *node) bool {
	switch n.kind {
	case nodeChar, nodeSet:
		return false
	case nodeCat:
		for _, sub := range n.subs {
			if !nullable(sub) {
				return false
			}
		}
		return true
	case nodeAlt:
		return slices.ContainsFunc(n.subs, nullable)
	case nodeGroup:
		return nullable(n.subs[0])
	case nodeRepeat:
		return n.min == 0 || nullable(n.subs[0])
	}
	return true
}

// startsAtBol reports whether every way through n begins with ^.
func startsAtBol(n *node) bool {
	switch n.kind {
	case nodeBol:
		return true
	case nodeCat, nodeGroup:
		return startsAtBol(n.subs[0])
	case nodeAlt:
		for _, sub := range n.subs {
			if !startsAtBol(sub) {
				return false
			}
		}
		return true
	case nodeRepeat:
		return n.min >= 1 && startsAtBol(n.subs[0])
	}
	return false
}

// firstChars appends, as inclusive pairs, the code points a match of n can start with. It returns whether n can match
// the empty string, and whether the set is known at all. Assertions are treated as matching the empty string, which
// can only make the set larger than it need be.
func firstChars(n *node, pairs *[]rune) (empty, known bool) {
	switch n.kind {
	case nodeChar:
		*pairs = append(*pairs, n.r, n.r)
		return false, true
	case nodeSet:
		*pairs = append(*pairs, n.set.ranges...)
		return false, true
	case nodeCat:
		for _, sub := range n.subs {
			e, ok := firstChars(sub, pairs)
			if !ok {
				return false, false
			}
			if !e {
				return false, true
			}
		}
		return true, true
	case nodeAlt:
		any := false
		for _, sub := range n.subs {
			e, ok := firstChars(sub, pairs)
			if !ok {
				return false, false
			}
			any = any || e
		}
		return any, true
	case nodeGroup:
		return firstChars(n.subs[0], pairs)
	case nodeRepeat:
		if n.max == 0 {
			return true, true
		}
		e, ok := firstChars(n.subs[0], pairs)
		if !ok {
			return false, false
		}
		return e || n.min == 0, true
	case nodeBackref:
		return false, false
	}
	return true, true
}
