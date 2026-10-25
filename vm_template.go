//go:build ignore

// The template of the backtracking matcher (vm.rs), which internal/expand writes out for each encoding: vm8_gen.go
// over UTF-8 bytes and vm16_gen.go over UTF-16 code units. In it unit_T is the unit of the text (byte or uint16), a
// name ending in _T is the one of that encoding (decode_T is decode8 or decode16, lit_T is lit8 or lit16), and isUTF8_T
// is true or false, so that each expansion is the matcher of one encoding with no branch on the encoding inside it.

package ecmaregex

import "time"

// exec_T finds the first match at or after start (at start alone with sticky), and returns where it starts and ends,
// with found false for no match. When it finds one and caps is given, caps receives the start and end of each group
// the program records, or -1 for a group that did not take part. With a limit the match is abandoned when it has run
// for longer than that, or when its stack has reached MaxStack, and the error says which.
func (p *program) exec_T(input []unit_T, start int, sticky bool, limit time.Duration, caps []int) (from, to int, found bool, err error) {
	st := states.Get().(*state)
	st.reset(p, len(input), limit)
	from, found = p.search_T(st, input, start, sticky)
	to = st.end
	stop := st.stop
	if found && caps != nil {
		copy(caps, st.caps)
	}
	st.release()
	states.Put(st)
	if err := stopError(stop); err != nil {
		return 0, 0, false, err
	}
	return from, to, found, nil
}

func (p *program) search_T(st *state, input []unit_T, start int, sticky bool) (int, bool) {
	length := len(input)
	need := p.need_T
	if start > length || length-start < need {
		return 0, false
	}
	if sticky || p.anchored {
		if p.anchored && start > 0 {
			return 0, false
		}
		return start, p.run_T(st, input, start)
	}
	return p.scan_T(st, input, start, need)
}

// scan_T tries every position from start that the first set allows, until one matches.
func (p *program) scan_T(st *state, input []unit_T, start, need int) (int, bool) {
	length := len(input)
	units := p.units
	for at := start; ; {
		if length-at < need {
			return 0, false
		}
		if at == length {
			return at, p.first == nil && p.run_T(st, input, at)
		}
		r, w := rune(input[at]), 1
		if input[at] >= 0x80 {
			r, w = read_T(input, at, units)
		}
		if (p.first == nil || p.first.contains(r)) && p.run_T(st, input, at) {
			return at, true
		}
		if st.stop != stopNone {
			return 0, false
		}
		at += w
	}
}

// run_T reports whether the program matches at the position start. It keeps its choices on an explicit stack, so the
// depth of the search is bounded by memory and not by the stack of the goroutine. When it fails, every group and
// counter is back to the value it had on entry. It counts its steps, and when a check says the match has reached a
// limit it returns false with the reason in st.stop.
func (p *program) run_T(st *state, input []unit_T, start int) bool {
	stack := st.stack[:0]
	matched := p.runOn_T(st, &stack, input, start)
	st.stack = stack
	return matched
}

func (p *program) runOn_T(st *state, stackp *[]entry, input []unit_T, start int) bool {
	insts := p.insts
	quick := p.quick
	stack := *stackp
	// The text is matched by UTF-16 code unit (see units.go).
	units := p.units
	length := len(input)
	pc, pos := 0, start
	steps := st.steps
	// memo says the match has taken the memory of failed places into use.
	memo := st.memoOn
	for {
		steps--
		if steps < 0 {
			if !st.check(len(stack)) {
				st.steps = 0
				*stackp = stack
				return false
			}
			steps = checkEvery
			memo = st.memoOn
		}
		i := &insts[pc]
		switch i.op {
		case opChar:
			if pos < length {
				if c := input[pos]; c < 0x80 {
					if rune(c) == i.r {
						pos++
						pc++
						continue
					}
				} else if r, w := read_T(input, pos, units); r == i.r {
					pos += w
					pc++
					continue
				}
			}
		case opSet:
			if pos < length {
				if c := input[pos]; c < 0x80 {
					if i.set.hasASCII(byte(c)) {
						pos++
						pc++
						continue
					}
				} else if r, w := read_T(input, pos, units); i.set.contains(r) {
					pos += w
					pc++
					continue
				}
			}
		case opLit:
			lit := i.lit_T
			n := len(lit)
			if length-pos >= n && equal_T(input[pos:pos+n], lit) {
				pos += n
				pc++
				continue
			}
		case opCharBack, opSetBack:
			if pos > 0 {
				r, w := rune(input[pos-1]), 1
				if input[pos-1] >= 0x80 {
					r, w = readLast_T(input, pos, units)
				}
				if i.op == opCharBack && r == i.r || i.op == opSetBack && i.set.contains(r) {
					pos -= w
					pc++
					continue
				}
			}
		case opFail:
		case opBol:
			if pos == 0 {
				pc++
				continue
			}
		case opEol:
			if pos == length {
				pc++
				continue
			}
		case opWordBoundary, opNotWordBoundary:
			before := pos > 0 && isWordUnit(uint32(input[pos-1]))
			after := pos < length && isWordUnit(uint32(input[pos]))
			if (before != after) == (i.op == opWordBoundary) {
				pc++
				continue
			}
		case opLineStart:
			if pos == 0 || lineTerminatorBefore_T(input, pos) {
				pc++
				continue
			}
		case opLineEnd:
			if pos == length || lineTerminatorAt_T(input, pos) {
				pc++
				continue
			}
		case opWordBoundaryFold, opNotWordBoundaryFold:
			before := false
			if pos > 0 {
				r, _ := decodeLast_T(input, pos)
				before = isFoldWordRune(r)
			}
			after := false
			if pos < length {
				r, _ := decode_T(input, pos)
				after = isFoldWordRune(r)
			}
			if (before != after) == (i.op == opWordBoundaryFold) {
				pc++
				continue
			}
		case opJmp:
			pc = int(i.x)
			continue
		case opSplit:
			// A place from which every way has failed before fails at once. The mark is under whatever comes of this
			// place, a choice or not, so that the place is remembered when it has all failed.
			if memo && i.m != 0 {
				idx := p.memoIndex(st, i.m, pos)
				if (st.memo[idx>>6]>>(idx&63))&1 != 0 {
					goto backtrack
				}
				if len(stack) < maxMarkDepth {
					stack = append(stack, entry{kind: eMark, pos: idx})
				}
			}
			// A way the next character rules out is not taken, and no choice is kept for it.
			if !quick[i.y].allows_T(input, pos) {
				if !quick[i.x].allows_T(input, pos) {
					goto backtrack
				}
				pc = int(i.x)
				continue
			}
			if !quick[i.x].allows_T(input, pos) {
				pc = int(i.y)
				continue
			}
			stack = append(stack, entry{kind: eChoice, pc: i.y, pos: pos})
			pc = int(i.x)
			continue
		case opSaveTmp:
			a := int(i.a)
			stack = append(stack, entry{kind: eTmp, pc: i.a, pos: st.tmp[a]})
			st.tmp[a] = pos
			pc++
			continue
		case opCapture:
			a := int(i.a)
			stack = append(stack, entry{kind: eCap, pc: i.a, pos: st.caps[2*a], a: st.caps[2*a+1]})
			if i.flag {
				st.caps[2*a], st.caps[2*a+1] = pos, st.tmp[a]
			} else {
				st.caps[2*a], st.caps[2*a+1] = st.tmp[a], pos
			}
			pc++
			continue
		case opClear:
			steps -= int64(i.b - i.a)
			for g := i.a; g <= i.b; g++ {
				k := int(g)
				if st.caps[2*k] != none {
					stack = append(stack, entry{kind: eCap, pc: g, pos: st.caps[2*k], a: st.caps[2*k+1]})
					st.caps[2*k], st.caps[2*k+1] = none, none
				}
			}
			pc++
			continue
		case opBackref:
			// A group that has not taken part matches the empty string.
			a := int(i.a)
			from, to := st.caps[2*a], st.caps[2*a+1]
			if from == none {
				pc++
				continue
			}
			n := to - from
			if length-pos >= n && equal_T(input[pos:pos+n], input[from:to]) && whole_T(input, pos, pos+n) {
				steps -= int64(n >> 3)
				pos += n
				pc++
				continue
			}
		case opBackrefBack:
			a := int(i.a)
			from, to := st.caps[2*a], st.caps[2*a+1]
			if from == none {
				pc++
				continue
			}
			n := to - from
			if pos >= n && equal_T(input[pos-n:pos], input[from:to]) && whole_T(input, pos-n, pos) {
				steps -= int64(n >> 3)
				pos -= n
				pc++
				continue
			}
		case opBackrefFold, opBackrefUnits:
			// A character at a time: to fold case, or because a group matched by code unit can begin or end between
			// the halves of a character, where its bytes do not say what it holds.
			a := int(i.a)
			from, to := st.caps[2*a], st.caps[2*a+1]
			if from == none {
				pc++
				continue
			}
			steps -= int64((to - from) >> 3)
			at, ok := pos, true
			for from < to && ok {
				want, w := read_T(input, from, units)
				from += w
				if at >= length {
					ok = false
					break
				}
				got, w2 := read_T(input, at, units)
				at += w2
				ok = got == want || i.op == opBackrefFold && canonicalize(got, i.flag) == canonicalize(want, i.flag)
			}
			if ok {
				pos = at
				pc++
				continue
			}
		case opBackrefFoldBack, opBackrefUnitsBack:
			a := int(i.a)
			from, to := st.caps[2*a], st.caps[2*a+1]
			if from == none {
				pc++
				continue
			}
			steps -= int64((to - from) >> 3)
			at, ok := pos, true
			for from < to && ok {
				want, w := readLast_T(input, to, units)
				to -= w
				if at == 0 {
					ok = false
					break
				}
				got, w2 := readLast_T(input, at, units)
				at -= w2
				ok = got == want || i.op == opBackrefFoldBack && canonicalize(got, i.flag) == canonicalize(want, i.flag)
			}
			if ok {
				pos = at
				pc++
				continue
			}
		case opLook:
			// What follows a lookaround starts where the lookaround does, and so does the body of a lookahead.
			if quick[pc].allows_T(input, pos) {
				kind := eLook
				if i.flag {
					kind = eLookNeg
				}
				stack = append(stack, entry{kind: kind, pc: i.x, pos: pos})
				pc++
				continue
			}
		case opLookOk:
			{
				// The body matched. A lookaround is atomic, so the choices made inside it are dropped. The undo entries
				// stay, because the groups it set remain set until something below this point is retried.
				f := len(stack) - 1
				for stack[f].kind != eLook {
					f--
				}
				pos = stack[f].pos
				keep := f
				for k := f + 1; k < len(stack); k++ {
					if stack[k].kind >= eTmp {
						stack[keep] = stack[k]
						keep++
					}
				}
				stack = stack[:keep]
				pc++
				continue
			}
		case opLookFail:
			// The body of a negative lookaround matched, so the lookaround fails and everything it did is undone.
			for len(stack) > 0 {
				e := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if e.kind == eLookNeg {
					break
				}
				st.undo(e)
			}
		case opRep:
			{
				at, n, floor := pos, int32(0), pos
				if set := i.set; set != nil {
					for n != i.max && at < length {
						if c := input[at]; c < 0x80 {
							if !set.hasASCII(byte(c)) {
								break
							}
							at++
						} else {
							r, w := read_T(input, at, units)
							if !set.contains(r) {
								break
							}
							at += w
						}
						n++
						if n == i.min {
							floor = at
						}
					}
				} else {
					for n != i.max && at < length {
						r, w := rune(input[at]), 1
						if input[at] >= 0x80 {
							r, w = read_T(input, at, units)
						}
						if r != i.r {
							break
						}
						at += w
						n++
						if n == i.min {
							floor = at
						}
					}
				}
				steps -= int64((at - pos) >> 3)
				if n >= i.min {
					if memo && i.m != 0 {
						idx := p.memoIndex(st, i.m, at)
						if (st.memo[idx>>6]>>(idx&63))&1 != 0 {
							// What follows the loop has failed from here before. The loop gives a character back.
							if at > floor {
								stack = append(stack, entry{kind: eRep, pc: int32(pc + 1), pos: at, a: floor})
							}
							goto backtrack
						}
						if at == floor && len(stack) < maxMarkDepth {
							stack = append(stack, entry{kind: eMark, pos: idx})
						}
					}
					if at > floor {
						stack = append(stack, entry{kind: eRep, pc: int32(pc + 1), pos: at, a: floor})
					}
					pos = at
					pc++
					continue
				}
			}
		case opRepLazy:
			{
				at, n := pos, int32(0)
				for n < i.min {
					w := p.one_T(i, input, at, units)
					if w == 0 {
						break
					}
					at += w
					n++
				}
				steps -= int64(n >> 3)
				if n == i.min {
					if memo && i.m != 0 {
						idx := p.memoIndex(st, i.m, at)
						if (st.memo[idx>>6]>>(idx&63))&1 != 0 {
							// What follows the loop has failed from here before. The loop takes one more character.
							if n != i.max {
								stack = append(stack, entry{kind: eRepLazy, pc: int32(pc), pos: at, a: int(n)})
							}
							goto backtrack
						}
						if n == i.max && len(stack) < maxMarkDepth {
							stack = append(stack, entry{kind: eMark, pos: idx})
						}
					}
					if n != i.max {
						stack = append(stack, entry{kind: eRepLazy, pc: int32(pc), pos: at, a: int(n)})
					}
					pos = at
					pc++
					continue
				}
			}
		case opLoopInit:
			a := int(i.a)
			stack = append(stack, entry{kind: eCounter, pc: i.a, pos: st.counts[2*a], a: st.counts[2*a+1]})
			st.counts[2*a], st.counts[2*a+1] = 0, none
			pc++
			continue
		case opLoop:
			{
				a := int(i.a)
				n := int64(st.counts[2*a])
				minC, need, left, iter := int64(i.min), int64(i.need_T), int64(length-pos), int64(i.iter_T)
				// Fewer units are left than any way from here to a match consumes: what follows the loop, and the
				// iterations it has still to make.
				if left < need || n < minC && iter != 0 && left-need < (minC-n)*iter {
					goto backtrack
				}
				// Once the minimum is met, ECMA-262 rejects an iteration that consumed nothing. That is what stops a loop
				// over a body that can be empty.
				if n > minC && pos == st.counts[2*a+1] {
					goto backtrack
				}
				if n < minC {
					pc++
					continue
				}
				if i.max >= 0 && n >= int64(i.max) {
					pc = int(i.x)
					continue
				}
				// The loop may be left or entered from here. A place from which both have failed before fails at once.
				if memo && i.m != 0 {
					idx := p.memoIndex(st, i.m, pos)
					if (st.memo[idx>>6]>>(idx&63))&1 != 0 {
						goto backtrack
					}
					if len(stack) < maxMarkDepth {
						stack = append(stack, entry{kind: eMark, pos: idx})
					}
				}
				switch {
				case !quick[i.x].allows_T(input, pos):
					// The next character rules out leaving the loop, so there is no choice.
					pc++
				case !quick[pc+1].allows_T(input, pos):
					pc = int(i.x)
				case i.flag:
					stack = append(stack, entry{kind: eChoice, pc: int32(pc + 1), pos: pos})
					pc = int(i.x)
				default:
					stack = append(stack, entry{kind: eChoice, pc: i.x, pos: pos})
					pc++
				}
				continue
			}
		case opLoopIter:
			a := int(i.a)
			stack = append(stack, entry{kind: eCounter, pc: i.a, pos: st.counts[2*a], a: st.counts[2*a+1]})
			st.counts[2*a]++
			st.counts[2*a+1] = pos
			pc++
			continue
		case opMatch:
			st.steps = steps
			st.end = pos
			*stackp = stack
			return true
		}
	backtrack:
		// The instruction failed. Take up the most recent choice, undoing what was recorded since.
	unwind:
		for {
			if len(stack) == 0 {
				st.steps = steps
				*stackp = stack
				return false
			}
			top := len(stack) - 1
			e := stack[top]
			switch e.kind {
			case eChoice:
				pc, pos = int(e.pc), e.pos
				stack = stack[:top]
				break unwind
			case eRep:
				// What followed the loop from the position it stood at has failed.
				rep := &insts[e.pc-1]
				marked := memo && rep.m != 0
				if marked {
					idx := p.memoIndex(st, rep.m, e.pos)
					st.memo[idx>>6] |= 1 << (idx & 63)
				}
				// Give back the last character taken. The characters between a and pos were decoded going forward, and
				// decoding the last of them going backward finds the same boundary.
				at := e.pos - giveBack_T(input, e.a, e.pos, units)
				if isUTF8_T && len(rep.back) > 0 && at > e.a && !quick[e.pc].allows_T(input, at) {
					// What follows the loop can only start with one of a few ASCII bytes: the last of them before here
					// is found by a search back, and the positions after it are passed over at once.
					at = lastOfBack_T(input, e.a, at, rep.back)
				}
				stack[top].pos = at
				pc, pos = int(e.pc), at
				last := at <= e.a
				// A position from which what follows the loop cannot go on is passed over, and so is one from which it
				// has failed before.
				pass := !quick[pc].allows_T(input, pos)
				if marked && !pass {
					idx := p.memoIndex(st, rep.m, pos)
					if (st.memo[idx>>6]>>(idx&63))&1 != 0 {
						pass = true
					} else if last {
						// The loop has nothing more to give back. The entry stays to note this last way.
						stack[top] = entry{kind: eMark, pos: idx}
						break unwind
					}
				}
				if last {
					stack = stack[:top]
				}
				if pass {
					steps--
					continue
				}
				break unwind
			case eRepLazy:
				// What followed the loop from the position it stood at has failed.
				rep := &insts[e.pc]
				marked := memo && rep.m != 0
				if marked {
					idx := p.memoIndex(st, rep.m, e.pos)
					st.memo[idx>>6] |= 1 << (idx & 63)
				}
				w := p.one_T(rep, input, e.pos, units)
				if w == 0 {
					stack = stack[:top]
					continue
				}
				at, taken := e.pos+w, e.a+1
				stack[top].pos, stack[top].a = at, taken
				pc, pos = int(e.pc)+1, at
				last := int64(taken) == int64(rep.max)
				if marked {
					idx := p.memoIndex(st, rep.m, pos)
					if (st.memo[idx>>6]>>(idx&63))&1 != 0 {
						if last {
							stack = stack[:top]
						}
						steps--
						continue
					}
					if last {
						stack[top] = entry{kind: eMark, pos: idx}
						break unwind
					}
				}
				if last {
					stack = stack[:top]
				}
				break unwind
			case eMark:
				// Every way from a memo point has failed.
				st.memo[e.pos>>6] |= 1 << (e.pos & 63)
				stack = stack[:top]
			case eLook:
				// The body of a positive lookaround has no way left to match.
				stack = stack[:top]
			case eLookNeg:
				// The body of a negative lookaround has no way to match, so the lookaround holds.
				pc, pos = int(e.pc), e.pos
				stack = stack[:top]
				break unwind
			default:
				st.undo(e)
				stack = stack[:top]
			}
		}
	}
}

// one_T returns the width of the character at the position at when the instruction's character or class matches it,
// and 0 otherwise. The character is a code unit with units set.
func (p *program) one_T(i *inst, input []unit_T, at int, units bool) int {
	if at >= len(input) {
		return 0
	}
	r, w := rune(input[at]), 1
	if input[at] >= 0x80 {
		r, w = read_T(input, at, units)
	}
	return oneOf(i, r, w)
}
