package ecmaregex

import (
	"strings"
	"testing"
	"time"
)

// TestAWarmMatchAllocatesNothing: a match allocates nothing once the working memory has grown to what the pattern and
// the text need: not a test, not a global search through a Matches, and not a read of the groups into a Captures
// (allocations.rs of the Rust crate, AllocationTest of the Java engine). Each case reaches one of the searches for
// where a match starts.
func TestAWarmMatchAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector's sync.Pool drops what is put back, so a match allocates its state again")
	}
	text := strings.Repeat("The year 2026, the month 10; Ünïcödé text, 😀 and more letters after it. ", 20)
	text8, _ := NewTextString(text)
	text16 := utf16Of(text)
	cases := [][2]string{
		{`\d+`, ""},
		{`(?<y>\d{4})\D+(\d+)`, "u"},
		{`text|month`, "i"},
		{`(?<=the )\w+`, "u"},
		{`(t|e)\1`, ""},
		{`\p{L}+`, "u"},
		{`[\p{L}--[a-z]]+`, "v"},
		{`^.*$`, "m"},
		{`\bt\w*\b`, "gi"},
		// The searches for where a match starts: a literal, several (packed over UTF-16), the case forms, the windows
		// with the reverse automaton, the search after loops, the forward automaton with the skip window, and the
		// shapes answered without the matcher.
		{`letters`, ""},
		{`year|month|text|letters`, ""},
		{`the`, "i"},
		{`year.{0,25}month|month.{0,25}year`, ""},
		{`\s[a-z]{0,12}ers\s`, ""},
		{`\w+\s+month`, ""},
		{`\d\d:?\d\d`, ""},
		{`.*`, "s"},
		{`[^\n]*`, ""},
		{`\p{L}`, "u"},
	}
	for _, c := range cases {
		// With no timeout, and with one, whose clock is read as the match goes.
		for _, timeout := range []time.Duration{0, time.Minute} {
			f, _ := ParseFlags(c[1])
			re, err := CompileFlags(c[0], f, timeout)
			if err != nil {
				t.Fatal(err)
			}
			m, caps := re.NewMatches(), re.NewCaptures()
			allocs := testing.AllocsPerRun(50, func() {
				if !re.MatchText(text8) || !re.MatchUTF16(text16) || !re.MatchString(text) {
					t.Fatal("no match")
				}
				if m.ResetText(text8).Count() == 0 || m.ResetUTF16(text16).Count() == 0 {
					t.Fatal("no matches")
				}
			})
			if allocs != 0 {
				t.Errorf("/%s/%s with a timeout of %v: %v allocations a run", c[0], c[1], timeout, allocs)
			}
			allocs = testing.AllocsPerRun(50, func() {
				for m.ResetText(text8); m.Next(caps); {
				}
				for m.ResetUTF16(text16); m.Next(caps); {
				}
				if !re.CapturesText(caps, text8, 0) || !re.CapturesString(caps, text, 0) {
					t.Fatal("no match")
				}
				if ok, _ := re.TryCapturesUTF16(caps, text16, 0); !ok {
					t.Fatal("no match")
				}
			})
			if allocs != 0 {
				t.Errorf("/%s/%s with a timeout of %v: %v allocations a run reading the groups", c[0], c[1], timeout, allocs)
			}
		}
	}
}
