package ecmaregex

// The backtracking matcher (vm.rs): its working memory, its limits and the memory of failed places. The matcher
// itself is the template vm_template.go, expanded for each encoding into vm8_gen.go and vm16_gen.go.

import (
	"errors"
	"sync"
	"time"
)

// ErrTime and ErrMemory say why a match was abandoned. Neither is an answer.
var (
	// ErrTime says the match ran for longer than its limit.
	ErrTime = errors.New("ecmaregex: the match ran for longer than its limit")
	// ErrMemory says the match needed more backtracking memory than its limit: its stack reached MaxStack entries.
	ErrMemory = errors.New("ecmaregex: the match needed more backtracking memory than its limit")
)

// checkEvery is the number of steps the matcher takes between two looks at the clock and at the depth of its stack. A
// step is one instruction, and eight characters of a single character loop or of a backreference. A step takes a few
// nanoseconds, so the clock is read every ten to twenty microseconds, a match that ends before its first check never
// reads it, and a match goes past its limit by that much at most.
const checkEvery int64 = 1 << 12

// MaxStack is the number of entries the backtracking stack of a match with a limit may hold. A match that needs more
// is abandoned. A pattern that keeps a choice for every character of its text needs one entry or a few for each, so
// this is room for a text of some hundred thousand characters, and a pattern that pushes without consuming, such as
// (?:a?){2147483647}, reaches it in a few milliseconds. It is looked at with the clock, so the stack can go past it by
// checkEvery entries.
const MaxStack = 1 << 20

// Why a match was abandoned.
const (
	stopNone uint8 = iota
	stopTime
	stopMemory
)

// The kinds of entry on the backtracking stack. A choice entry says where to resume. An undo entry restores a value
// that was overwritten after the entries below it were pushed.
const (
	// eChoice resumes at pc with the position pos.
	eChoice uint8 = iota
	// eRep belongs to a greedy single character loop that stopped at pos. Resuming gives back one character, down to
	// the position a.
	eRep
	// eRepLazy belongs to a lazy single character loop that has taken a characters and stands at pos. Resuming takes
	// one more.
	eRepLazy
	// eLook marks the start of a positive lookaround and eLookNeg of a negative one. Both hold the position to
	// restore, and eLookNeg the instruction to continue at when its body has failed.
	eLook
	eLookNeg
	// eMark lies under the choices of a memo point. When it is reached, every way from the point has failed, and the
	// bit pos of the memory is set (see memo in state).
	eMark
	// eTmp restores the entry position of group pc to pos.
	eTmp
	// eCap restores the start and end of group pc to pos and a.
	eCap
	// eCounter restores the count and the iteration start of loop pc to pos and a.
	eCounter
)

type entry struct {
	kind uint8
	pc   int32
	pos  int
	a    int
}

// none is the value of a group that has not taken part, and of the start of an iteration that has not started.
const none = -1

// state is the working memory of one match. A match takes one from a pool and gives it back, so a match allocates
// only while the stack is still growing to the depth the pattern and the text need.
type state struct {
	stack []entry
	// caps holds the start and end of each tracked group, or none, and tmp the position at which each was entered.
	caps []int
	tmp  []int
	// counts holds, for each loop, the number of iterations started and the position at which the last one started.
	counts []int
	// end is where the match that was found ends.
	end int
	// limit is the time the match may take, or 0 for no limit. The clock starts at the first check, so deadline is set
	// then.
	limit    time.Duration
	deadline time.Time
	// steps is the number of steps left before the next check, and stop why the match was abandoned.
	steps int64
	stop  uint8
	// memo is the memory of the places where a way has failed: one bit for each memo point of the program, each
	// position of the text and each state of the loops around the point (see memoPoint). A bit is set when every way
	// from the place has failed, and a way that comes to a place whose bit is set fails at once. So no place is gone
	// through twice, and a pattern of repetitions inside repetitions takes time proportional to its size times the
	// text and not time that doubles with each character.
	//
	// The memory costs its size to clear, and a mark on the stack for each place gone through, so a match starts
	// without it. It takes it at the check by which it has made as many steps as the memory has words and
	// memoStepsPerPosition more for each position of the text, which a match that goes through its text once or a few
	// times never makes. A place whose way failed before then is not remembered, which loses time and nothing else.
	// memoOn says the memory is in use, memoOff that it never will be (the program has no memo points, or the memory
	// would be larger than MaxMemoWords), and width is the number of positions of the text.
	memo    []uint64
	memoOn  bool
	memoOff bool
	width   int
	checks  int
	points  int
}

// MaxMemoWords is the largest memory of failed places, in words of 64 bits: 8 MB. A match that would need more goes
// without it.
const MaxMemoWords = 1 << 20

const (
	// memoStepsPerPosition is the number of steps for each position of the text that a match makes before it takes
	// the memory into use.
	memoStepsPerPosition = 64
	// maxRetainedMemo is the largest memory kept between matches.
	maxRetainedMemo = 1 << 16
	// maxMarkDepth is the depth of the backtracking stack beyond which a memo point is not marked. A mark is an entry
	// of the stack, and a pattern that keeps no choice for a character still marks a place for each, so on a long text
	// the marks alone would reach MaxStack. A place that is not marked is not remembered, which loses time and nothing
	// else, and the places below this depth are remembered when the stack unwinds, so a later start has the room to
	// mark the next ones.
	maxMarkDepth = MaxStack / 2
	// maxRetainedStack is the largest stack kept between matches.
	maxRetainedStack = 1 << 16
)

// memoFromTheStart makes every match take its memory of failed places before its first step. The tests set it, to run
// every pattern they have with the memory in use, and so does the test data runner when asked.
var memoFromTheStart = false

var states = sync.Pool{New: func() any { return new(state) }}

// remember takes the memory of failed places into use when the match has made enough steps to pay for it.
func (st *state) remember() {
	words := (((st.points * st.width) << maxMemoLoops) + 63) >> 6
	if words > MaxMemoWords {
		st.memoOff = true
		return
	}
	if st.checks*int(checkEvery) < words+memoStepsPerPosition*st.width && !memoFromTheStart {
		return
	}
	if cap(st.memo) < words {
		st.memo = make([]uint64, words)
	} else {
		st.memo = st.memo[:words]
		clear(st.memo)
	}
	st.memoOn = true
}

// check is called every checkEvery steps. It reports whether the match may go on.
//
//go:noinline
func (st *state) check(depth int) bool {
	if !st.memoOn && !st.memoOff {
		st.checks++
		st.remember()
	}
	if st.limit <= 0 {
		return true
	}
	if depth > MaxStack {
		st.stop = stopMemory
		return false
	}
	now := time.Now()
	if st.deadline.IsZero() {
		st.deadline = now.Add(st.limit)
	} else if now.After(st.deadline) {
		st.stop = stopTime
		return false
	}
	return true
}

// undo applies one undo entry.
func (st *state) undo(e entry) {
	k := int(e.pc)
	switch e.kind {
	case eTmp:
		st.tmp[k] = e.pos
	case eCap:
		st.caps[2*k], st.caps[2*k+1] = e.pos, e.a
	case eCounter:
		st.counts[2*k], st.counts[2*k+1] = e.pos, e.a
	}
}

// reset readies the state for a match of the program over a text of length units.
func (st *state) reset(p *program, length int, limit time.Duration) {
	st.caps = resized(st.caps, 2*p.groups, none)
	st.tmp = resized(st.tmp, p.groups, 0)
	st.counts = resized(st.counts, 2*p.loops, 0)
	st.limit, st.deadline, st.steps, st.stop = max(limit, 0), time.Time{}, checkEvery, stopNone
	st.memoOn, st.memoOff, st.checks = false, len(p.points) == 0, 0
	st.width, st.points = length+1, len(p.points)
	if !st.memoOff && memoFromTheStart {
		st.remember()
	}
}

// release gives up a stack or a memory too large to keep between matches.
func (st *state) release() {
	if cap(st.stack) > maxRetainedStack {
		st.stack = nil
	}
	if cap(st.memo) > maxRetainedMemo {
		st.memo = nil
	}
}

// resized returns s with n elements, each v.
func resized(s []int, n, v int) []int {
	if cap(s) < n {
		s = make([]int, n)
	} else {
		s = s[:n]
	}
	for k := range s {
		s[k] = v
	}
	return s
}

// memoIndex returns the bit of the memory for the memo point m at a position, with the state of the loops around the
// point: for each, whether it would refuse an iteration that ended here as empty.
func (p *program) memoIndex(st *state, m int32, pos int) int {
	pt := &p.points[m-1]
	idx := (int(m-1)*st.width + pos) << maxMemoLoops
	for k := 0; k < maxMemoLoops; k++ {
		if l := pt.loops[k]; l >= 0 && st.counts[2*l] > int(pt.mins[k]) && st.counts[2*l+1] == pos {
			idx |= 1 << k
		}
	}
	return idx
}

// stopError returns the error of a match abandoned for the reason, or nil.
func stopError(stop uint8) error {
	switch stop {
	case stopTime:
		return ErrTime
	case stopMemory:
		return ErrMemory
	}
	return nil
}

// oneOf returns the width of the character at the position when the instruction's character or class matches it, and
// 0 otherwise; r and w are the character and its width.
func oneOf(i *inst, r rune, w int) int {
	if i.set != nil {
		if !i.set.contains(r) {
			return 0
		}
	} else if r != i.r {
		return 0
	}
	return w
}
