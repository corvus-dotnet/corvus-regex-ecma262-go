//go:build ignore

// The template of the searches of a Regexp (regex.rs and replace.rs), which internal/expand writes out for each
// encoding: regex8_gen.go over UTF-8 bytes and regex16_gen.go over UTF-16 code units (see vm_template.go).

package ecmaregex

// exec_T finds the first match at or after start (at start alone with sticky), with the pattern's timeout. When slots
// is given it receives the start and end of the match and of each group, -1 for a group that did not take part.
func (re *Regexp) exec_T(input []unit_T, start int, sticky bool, slots []int) (from, to int, found bool, err error) {
	start = normalize_T(input, start, re.unicode())
	if slots == nil {
		return re.prog.exec_T(input, start, sticky, re.timeout, nil)
	}
	from, to, found, err = re.capsProgram().exec_T(input, start, sticky, re.timeout, slots[2:])
	if found {
		slots[0], slots[1] = from, to
	}
	return from, to, found, err
}

// nextStart_T returns the position a global search goes on from after a match from..to.
func (re *Regexp) nextStart_T(input []unit_T, from, to int) int {
	if to == from {
		return advance_T(input, to, re.unicode())
	}
	return to
}

// findAll_T returns each match of a global search, at most n (all when n is below 0), with its groups when submatch.
func (re *Regexp) findAll_T(input []unit_T, n int, submatch bool) ([][]int, error) {
	var out [][]int
	for at := 0; n < 0 || len(out) < n; {
		var slots []int
		if submatch {
			slots = make([]int, 2*(re.info.groups+1))
		}
		from, to, found, err := re.exec_T(input, at, re.flags.Sticky, slots)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		if slots == nil {
			slots = []int{from, to}
		}
		out = append(out, slots)
		at = re.nextStart_T(input, from, to)
		if at > len(input) {
			break
		}
	}
	return out, nil
}

// replace_T replaces the first match, or every match with all, by the template.
func (re *Regexp) replace_T(input, template []unit_T, all bool) ([]unit_T, error) {
	slots := make([]int, 2*(re.info.groups+1))
	var out []unit_T
	last, at, any := 0, 0, false
	for at <= len(input) {
		from, to, found, err := re.exec_T(input, at, re.flags.Sticky, slots)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		any = true
		out = pushLossy_T(out, input, last, from)
		out = re.substitute_T(out, input, slots, template)
		last = to
		if !all {
			break
		}
		at = re.nextStart_T(input, from, to)
	}
	if !any {
		return input, nil
	}
	return pushLossy_T(out, input, last, len(input)), nil
}

// substitute_T appends the template with each $ form replaced (GetSubstitution).
func (re *Regexp) substitute_T(out, input []unit_T, slots []int, template []unit_T) []unit_T {
	digit := func(k int) int {
		if k < len(template) && '0' <= template[k] && template[k] <= '9' {
			return int(template[k] - '0')
		}
		return -1
	}
	groups := len(slots)/2 - 1
	group := func(out []unit_T, g int) []unit_T {
		if slots[2*g] != none {
			out = pushLossy_T(out, input, slots[2*g], slots[2*g+1])
		}
		return out
	}
	for k := 0; k < len(template); {
		if template[k] != '$' || k+1 >= len(template) {
			out = append(out, template[k])
			k++
			continue
		}
		switch next := template[k+1]; {
		case next == '$':
			out = append(out, template[k])
			k += 2
		case next == '&':
			out = pushLossy_T(out, input, slots[0], slots[1])
			k += 2
		case next == '`':
			out = pushLossy_T(out, input, 0, slots[0])
			k += 2
		case next == '\'':
			out = pushLossy_T(out, input, slots[1], len(input))
			k += 2
		case '0' <= next && next <= '9':
			d1, d2 := digit(k+1), digit(k+2)
			if d2 >= 0 && 1 <= d1*10+d2 && d1*10+d2 <= groups {
				out = group(out, d1*10+d2)
				k += 3
			} else if 1 <= d1 && d1 <= groups {
				out = group(out, d1)
				k += 2
			} else {
				out = append(out, template[k])
				k++
			}
		case next == '<':
			// Without a named group $< is itself. With one, the name runs to the next >; a name that is no group's is
			// the empty string, and a < with no > after it is itself.
			close := -1
			for j := k + 2; j < len(template); j++ {
				if template[j] == '>' {
					close = j
					break
				}
			}
			if close < 0 || len(re.info.names) == 0 {
				out = append(out, template[k])
				k++
				continue
			}
			for _, g := range re.info.named_T(template[k+2 : close]) {
				if slots[2*g] != none {
					out = group(out, g)
					break
				}
			}
			k = close + 1
		default:
			out = append(out, template[k])
			k++
		}
	}
	return out
}

// split_T splits the text around the matches (RegExp.prototype[Symbol.split]), with at most limit items (all when
// limit is below 0). The flags g and y do not apply: split searches with a copy of the pattern that is sticky, from
// each position in turn, which is what a search from the position finds first.
func (re *Regexp) split_T(input []unit_T, limit int) ([]span, error) {
	size := len(input)
	var out []span
	if limit == 0 {
		return out, nil
	}
	slots := make([]int, 2*(re.info.groups+1))
	if size == 0 {
		_, _, found, err := re.exec_T(input, 0, true, slots)
		if err != nil {
			return nil, err
		}
		if !found {
			out = append(out, span{0, 0})
		}
		return out, nil
	}
	p, q := 0, 0
	for q < size {
		from, to, found, err := re.exec_T(input, q, false, slots)
		if err != nil {
			return nil, err
		}
		if !found || from >= size {
			break
		}
		e := min(to, size)
		if e == p {
			q = advance_T(input, from, re.unicode())
			continue
		}
		out = append(out, span{p, from})
		if len(out) == limit {
			return out, nil
		}
		p = e
		for g := 1; g < len(slots)/2; g++ {
			if slots[2*g] == none {
				out = append(out, span{none, none})
			} else {
				out = append(out, span{slots[2*g], slots[2*g+1]})
			}
			if len(out) == limit {
				return out, nil
			}
		}
		q = p
	}
	return append(out, span{p, size}), nil
}
