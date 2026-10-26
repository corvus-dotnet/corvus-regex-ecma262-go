package ecmaregex

// The parts of the searches of the matcher that differ between the encodings beyond the unit, written once for each:
// the template of the matcher calls them by their names ending in _T.

// firstFind8 returns the first position at or after at whose byte a member of the first set can start with, or -1;
// at itself where the search has no first bytes.
func (p *program) firstFind8(input []byte, at int) int {
	if fb := p.search.firstBytes; fb != nil {
		return fb.find(input, at)
	}
	return at
}

// firstFind16 returns the first position at or after at whose unit a member of the first set can start with, or -1;
// at itself where the search has no first units. With the u or v flag the trail half of a pair is not a character a
// match can start at.
func (p *program) firstFind16(input []uint16, at int) int {
	fu := p.search.firstUnits
	if fu == nil {
		return at
	}
	for {
		at = fu.find(input, at)
		if at < 0 || p.units || !midPair16(input, at) {
			return at
		}
		at++
	}
}

// firstStart8 is firstFind8 for the scan, which passes over the trail half of a pair itself.
func (p *program) firstStart8(input []byte, at int) int { return p.firstFind8(input, at) }

// firstStart16 returns the first position at or after at whose unit a member of the first set can start with, or -1;
// at itself where the search has no first units. It may be the trail half of a pair, which the scan passes over
// itself, so that the skip is run from the place after it (firstFind16 passes over it).
func (p *program) firstStart16(input []uint16, at int) int {
	if fu := p.search.firstUnits; fu != nil {
		return fu.find(input, at)
	}
	return at
}

func midPair8(input []byte, at int) bool { return false }

// midPair16 reports whether the position is between the halves of a surrogate pair.
func midPair16(input []uint16, at int) bool {
	return at > 0 && at < len(input) && isLow(input[at]) && isHigh(input[at-1])
}

// runEnd8 returns the end of the run of the loop's members that starts at at, or -1 to find it a member at a time.
func (s *search) runEnd8(input []byte, at int) int {
	n := len(input)
	switch s.runEnd {
	case runAll:
		return n
	case runStops:
		var f int
		switch s.nStops {
		case 1:
			f = indexByte(input, at, n, s.stops[0])
		case 2:
			f = indexByte2(input, at, n, s.stops[0], s.stops[1])
		default:
			f = indexByte4(input, at, n, s.stops[0], s.stops[1], s.stops[2], s.stops[2])
		}
		if f < 0 {
			return n
		}
		return f
	case runLine:
		for k := at; ; {
			j := indexByte4(input, k, n, '\n', '\r', 0xE2, 0xE2)
			if j < 0 {
				return n
			}
			if input[j] != 0xE2 || j+2 < n && input[j+1] == 0x80 && input[j+2]|1 == 0xA9 {
				return j
			}
			k = j + 1
		}
	}
	return -1
}

// runEnd16 is runEnd8 over UTF-16 text.
func (s *search) runEnd16(input []uint16, at int) int {
	switch s.runEnd {
	case runAll:
		return len(input)
	case runStops, runLine:
		if f := s.stops16.find(input, at); f >= 0 {
			return f
		}
		return len(input)
	}
	return -1
}

// afterFind8 returns where the match that starts at q ends, for a pattern every match of which starts with the one
// literal found at q: the anchored automaton goes on after it. Or adfaNone, or adfaBail where the matcher decides.
func (p *program) afterFind8(input []byte, q int) int {
	if p.afterLiteral == nil {
		return adfaBail
	}
	return p.anchoredDfa.findAfter8(input, q, q+p.afterLength, p.afterLiteral)
}

// afterFind16 is afterFind8 over UTF-16 text, where the literal's states are not known: the matcher decides.
func (p *program) afterFind16(input []uint16, q int) int { return adfaBail }
