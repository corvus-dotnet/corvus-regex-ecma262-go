package ecmaregex

// The []byte forms of the UTF-8 methods, Text (UTF-8 checked once) and Captures (the groups of a match, kept from one
// match to the next so that matching allocates nothing).

import "unicode/utf8"

// Text is UTF-8 text that has been checked once, so that the methods that take it check nothing. A program that
// matches one text many times makes a Text of it and pays for the check once.
type Text struct {
	s string
}

// NewText checks that the bytes are UTF-8 and returns them as a Text, which holds them without a copy: they must not
// be changed while it is in use. Bytes that are not UTF-8 are an error of kind ErrEncoding.
func NewText(b []byte) (Text, error) {
	if !utf8.Valid(b) {
		return Text{}, errNotUTF8
	}
	return Text{stringOf(b)}, nil
}

// NewTextString checks that the string is UTF-8 and returns it as a Text.
func NewTextString(s string) (Text, error) {
	if !utf8.ValidString(s) {
		return Text{}, errNotUTF8
	}
	return Text{s}, nil
}

// String returns the text.
func (t Text) String() string { return t.s }

// Len returns the length of the text in bytes.
func (t Text) Len() int { return len(t.s) }

// Captures holds the start and end of a match and of each group of a pattern. A match into one allocates nothing, so a
// program that matches many times makes one with NewCaptures and passes it to each match. A Captures is not safe for
// concurrent use.
type Captures struct {
	re    *Regexp
	slots []int
	found bool
}

// NewCaptures returns a Captures for the pattern's matches.
func (re *Regexp) NewCaptures() *Captures {
	return &Captures{re: re, slots: make([]int, 2*(re.info.groups+1))}
}

func (c *Captures) of(re *Regexp) []int {
	if c.re != re {
		panic("ecmaregex: a Captures is used with a pattern other than the one that made it")
	}
	return c.slots
}

// Found reports whether the last match into the Captures found one.
func (c *Captures) Found() bool { return c.found }

// Len returns the number of groups, the whole match counted as group 0.
func (c *Captures) Len() int { return len(c.slots) / 2 }

// Matched reports whether group g took part in the match.
func (c *Captures) Matched(g int) bool { return c.found && c.slots[2*g] >= 0 }

// Start returns where group g starts, or -1 when it did not take part.
func (c *Captures) Start(g int) int {
	if !c.found {
		return -1
	}
	return c.slots[2*g]
}

// End returns where group g ends, or -1 when it did not take part.
func (c *Captures) End(g int) int {
	if !c.found {
		return -1
	}
	return c.slots[2*g+1]
}

// Named returns the group with the name that took part in the match, the first of them where groups in separate
// alternatives share it, or -1.
func (c *Captures) Named(name string) int {
	if !c.found {
		return -1
	}
	for _, n := range c.re.info.names {
		if n.name == name {
			for _, g := range n.groups {
				if c.slots[2*g] >= 0 {
					return g
				}
			}
		}
	}
	return -1
}

// Index returns the start and end of each group, -1 for a group that did not take part, as FindStringSubmatchIndex
// does, or nil when the last match found none. It is the Captures' own memory, which the next match overwrites.
func (c *Captures) Index() []int {
	if !c.found {
		return nil
	}
	return c.slots
}

// captures8 matches into c from the byte offset start.
func (re *Regexp) captures8(c *Captures, input []byte, start int) (bool, error) {
	_, _, found, err := re.exec8(input, start, re.flags.Sticky, c.of(re))
	c.found = found && err == nil
	return c.found, err
}

// TryCapturesString matches the text from the byte offset start (at it alone with the y flag) into c, and reports
// whether it found a match.
func (re *Regexp) TryCapturesString(c *Captures, s string, start int) (bool, error) {
	if !utf8.ValidString(s) {
		c.found = false
		return false, errNotUTF8
	}
	return re.captures8(c, bytesOf(s), start)
}

// CapturesString is TryCapturesString, which panics when a match reaches a limit or the text is not UTF-8.
func (re *Regexp) CapturesString(c *Captures, s string, start int) bool {
	return must(re.TryCapturesString(c, s, start))
}

// TryCapturesUTF16 matches the text from the code unit start (at it alone with the y flag) into c, and reports whether
// it found a match.
func (re *Regexp) TryCapturesUTF16(c *Captures, u []uint16, start int) (bool, error) {
	_, _, found, err := re.exec16(u, start, re.flags.Sticky, c.of(re))
	c.found = found && err == nil
	return c.found, err
}

// The methods over a Text, which check nothing.

// TryMatchText reports whether the pattern matches the text.
func (re *Regexp) TryMatchText(t Text) (bool, error) {
	_, _, found, err := re.exec8(bytesOf(t.s), 0, re.flags.Sticky, nil)
	return found, err
}

// MatchText is TryMatchText, which panics when a match reaches a limit.
func (re *Regexp) MatchText(t Text) bool { return must(re.TryMatchText(t)) }

// TryFindTextIndexAt returns the start and end of the first match at or after the byte offset start, or nil.
func (re *Regexp) TryFindTextIndexAt(t Text, start int) ([]int, error) {
	from, to, found, err := re.exec8(bytesOf(t.s), start, re.flags.Sticky, nil)
	if !found || err != nil {
		return nil, err
	}
	return []int{from, to}, nil
}

// TryCapturesText matches the text from the byte offset start into c, and reports whether it found a match.
func (re *Regexp) TryCapturesText(c *Captures, t Text, start int) (bool, error) {
	return re.captures8(c, bytesOf(t.s), start)
}

// CapturesText is TryCapturesText, which panics when a match reaches a limit.
func (re *Regexp) CapturesText(c *Captures, t Text, start int) bool {
	return must(re.TryCapturesText(c, t, start))
}

// TryFindAllTextIndex returns the start and end of each match of a global search, at most n (all when n is below 0).
func (re *Regexp) TryFindAllTextIndex(t Text, n int) ([][]int, error) {
	return re.findAll8(bytesOf(t.s), n, false)
}

// TryReplaceText replaces the first match, or every match with the g flag, as String.prototype.replace does.
func (re *Regexp) TryReplaceText(src Text, template string) (string, error) {
	if !utf8.ValidString(template) {
		return "", errNotUTF8
	}
	out, err := re.replace8(bytesOf(src.s), bytesOf(template), re.flags.Global)
	return stringOf(out), err
}

// TryReplaceAllText replaces every match, as String.prototype.replaceAll does.
func (re *Regexp) TryReplaceAllText(src Text, template string) (string, error) {
	if !utf8.ValidString(template) {
		return "", errNotUTF8
	}
	out, err := re.replace8(bytesOf(src.s), bytesOf(template), true)
	return stringOf(out), err
}

// TrySplitText splits the text around the matches, as String.prototype.split does.
func (re *Regexp) TrySplitText(t Text, limit int) ([]Piece, error) { return re.TrySplit(t.s, limit) }

// The []byte forms, which check that the bytes are UTF-8 on each call.

// TryMatch reports whether the pattern matches the bytes.
func (re *Regexp) TryMatch(b []byte) (bool, error) {
	if !utf8.Valid(b) {
		return false, errNotUTF8
	}
	_, _, found, err := re.exec8(b, 0, re.flags.Sticky, nil)
	return found, err
}

// Match is TryMatch, which panics when a match reaches a limit or the bytes are not UTF-8.
func (re *Regexp) Match(b []byte) bool { return must(re.TryMatch(b)) }

// TryFindIndex returns the start and end of the first match, or nil.
func (re *Regexp) TryFindIndex(b []byte) ([]int, error) {
	if !utf8.Valid(b) {
		return nil, errNotUTF8
	}
	return re.TryFindTextIndexAt(Text{stringOf(b)}, 0)
}

// FindIndex is TryFindIndex, which panics when a match reaches a limit or the bytes are not UTF-8.
func (re *Regexp) FindIndex(b []byte) []int { return must(re.TryFindIndex(b)) }

// TryFindSubmatchIndex returns the start and end of the first match and of each group, -1 for a group that did not
// take part, or nil.
func (re *Regexp) TryFindSubmatchIndex(b []byte) ([]int, error) {
	if !utf8.Valid(b) {
		return nil, errNotUTF8
	}
	return re.TryFindStringSubmatchIndexAt(stringOf(b), 0)
}

// FindSubmatchIndex is TryFindSubmatchIndex, which panics when a match reaches a limit or the bytes are not UTF-8.
func (re *Regexp) FindSubmatchIndex(b []byte) []int { return must(re.TryFindSubmatchIndex(b)) }

// TryFindAllIndex returns the start and end of each match of a global search, at most n (all when n is below 0).
func (re *Regexp) TryFindAllIndex(b []byte, n int) ([][]int, error) {
	if !utf8.Valid(b) {
		return nil, errNotUTF8
	}
	return re.findAll8(b, n, false)
}

// FindAllIndex is TryFindAllIndex, which panics when a match reaches a limit or the bytes are not UTF-8.
func (re *Regexp) FindAllIndex(b []byte, n int) [][]int { return must(re.TryFindAllIndex(b, n)) }

// TryFindAllSubmatchIndex is TryFindAllIndex with the start and end of each group of each match.
func (re *Regexp) TryFindAllSubmatchIndex(b []byte, n int) ([][]int, error) {
	if !utf8.Valid(b) {
		return nil, errNotUTF8
	}
	return re.findAll8(b, n, true)
}

// FindAllSubmatchIndex is TryFindAllSubmatchIndex, which panics when a match reaches a limit or the bytes are not
// UTF-8.
func (re *Regexp) FindAllSubmatchIndex(b []byte, n int) [][]int {
	return must(re.TryFindAllSubmatchIndex(b, n))
}

// TryReplace replaces the first match, or every match with the g flag, as String.prototype.replace does. The result is
// a new slice, or src itself when nothing matched.
func (re *Regexp) TryReplace(src, template []byte) ([]byte, error) {
	if !utf8.Valid(src) || !utf8.Valid(template) {
		return nil, errNotUTF8
	}
	return re.replace8(src, template, re.flags.Global)
}

// Replace is TryReplace, which panics when a match reaches a limit or the bytes are not UTF-8.
func (re *Regexp) Replace(src, template []byte) []byte { return must(re.TryReplace(src, template)) }

// TryReplaceAll replaces every match, as String.prototype.replaceAll does.
func (re *Regexp) TryReplaceAll(src, template []byte) ([]byte, error) {
	if !utf8.Valid(src) || !utf8.Valid(template) {
		return nil, errNotUTF8
	}
	return re.replace8(src, template, true)
}

// ReplaceAll is TryReplaceAll, which panics when a match reaches a limit or the bytes are not UTF-8.
func (re *Regexp) ReplaceAll(src, template []byte) []byte {
	return must(re.TryReplaceAll(src, template))
}
