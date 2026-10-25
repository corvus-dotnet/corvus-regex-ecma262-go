package ecmaregex

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// The tests of the searches for where a match starts, and of the automata they use, ported from the Rust crate's
// tests.rs (by way of the Java engine's SearchTest): each search finds what trying every position finds.

// searchedAndPlain returns a pattern compiled as every other is, and with the search that tries every position its
// first set allows, or nil when the pattern is not valid.
func searchedAndPlain(t *testing.T, pattern, flags string) (*Regexp, *Regexp) {
	t.Helper()
	searched, err := Compile(pattern, flags)
	if err != nil {
		return nil, nil
	}
	noSearch.Store(true)
	defer noSearch.Store(false)
	plain := MustCompile(pattern, flags)
	// The program that records the groups is built on the first use, so it is built here.
	plain.capsProgram()
	searched.capsProgram()
	return searched, plain
}

func utf16Of(s string) []uint16 { return utf16.Encode([]rune(s)) }

func TestTheSearchesFindWhatEveryPositionFinds(t *testing.T) {
	patterns := [][2]string{
		{`Sherlock`, ""}, {`Sherlock|Street`, ""}, {`Sherlock|Holmes|Watson|Irene|Adler|John|Baker`, "i"},
		{`Sher[a-z]+|Hol[a-z]+`, "i"}, {`[a-q][^u-z]{13}x`, ""}, {`[a-zA-Z]+ing`, ""},
		{`\s[a-zA-Z]{0,12}ing\s`, ""}, {`\w+\s+Holmes`, ""}, {`\w+\s+Holmes\s+\w+`, ""}, {`\b\w+n\b`, ""},
		{`\w+n\b`, ""}, {`.*$`, ""}, {`(?:.)??$`, ""}, {`.*\s`, ""}, {`abcd|bc`, ""}, {`(?:x|y)*z|q`, ""},
		{`\b(?:the|he)\b`, "i"}, {`Holmes.{0,25}Watson|Watson.{0,25}Holmes`, ""}, {`Tom.{1,5}er|er.{1,5}Tom`, ""},
		{`\w+n(?=\s)`, ""}, {`[a-z]+n\B`, "i"}, {`\w+\s+Holmes$`, "m"}, {`[a-z]+ing\b|x`, ""},
		{`.{0,2}(Tom|Sawyer|Huckleberry|Finn)`, ""}, {`.{2,4}(Tom|Sawyer|Huckleberry|Finn)`, ""},
		{`Tom.{10,25}river|river.{10,25}Tom`, ""}, {`([A-Za-z]awyer|[A-Za-z]inn)\s`, ""},
		{`[\w\.+-]+@[\w\.-]+\.[\w\.-]+`, ""}, {`\p{Sm}`, ""},
		{`(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9])`, ""},
		{"\u0428\u0435\u0440\u043b\u043e\u043a \u0425\u043e\u043b\u043c\u0441|\u0414\u0436\u043e\u043d" +
			" \u0423\u043e\u0442\u0441\u043e\u043d", "i"},
		{"\u590f\u6d1b\u514b|\u798f\u5c14\u6469\u65af", ""}, {`sk`, "i"}, {`ks`, "i"},
		{`\u{1F600}a|b\u{10400}`, ""}, {`a\u{1F600}`, ""}, {`\ud83d`, ""}, {`x*y`, ""}, {`[a-c]*b+c`, ""},
		{`(?:ab)?c`, ""}, {"\\b\u00e9\\w*", ""}, {`(?<=a)b|c`, ""}, {`[\p{L}--[a-z]]+t`, "v"},
		{`[\q{abc|de}x]y`, "v"}, {`\w+`, ""}, {`.*`, ""}, {`.+`, "s"}, {`[^\n]*`, ""}, {`[^\n.]+`, ""},
		{`\p{L}`, ""}, {`[\ud800-\udfff]`, ""}, {`Tom|river|Tomas`, ""}, {`ab|a`, ""},
		{`sherlock|holmes|k`, "i"}, {"\\u{1F600}|\u0428", ""},
		// A start that one occurrence's terms do not allow, and a later occurrence's do, is tried first.
		{`(|([^a]|[^a][^a]){2,4}){2}(\3){3,5}?x`, ""}, {`[^a]{0,3}(?:xx|x)`, ""}, {`\s[a-zA-Z]{0,12}ing\s`, ""},
		// A window of positions read one unit at a time (skipWin).
		{`\d{3}-\d{4}`, ""}, {`[0-9]+\.[0-9]+`, ""}, {`(?:\d+\.){3}\d+`, ""}, {`\b\d\d:\d\d\b`, ""},
		{`[A-Z]{2}\d{2}`, ""}, {`(?:1|22|333)\.5\.\d`, ""}, {"\\d{2}[\u00e95]\\d", "u"},
		{"\\d{3}[\u00e9\u00e8\u00ea\u00eb5]", "u"},
		// An occurrence of the one literal every match starts with, which the automaton goes on after.
		{`^abc`, "m"}, {`x{0}^abc`, ""}, {`\babc\b`, ""}, {`abc\s+\w+`, ""},
	}
	alphabet := []string{"a", "b", "c", "e", "i", "k", "n", "s", "t", "x", "y", "g", "S", "K", " ", "\n", ".", "@",
		"1", "2", "5", "+", "=", "\u017f", "\u212a", "\u00e9", "\u0428", "\u0435", "\u0440", "\u043b", "\u043e",
		"\u043a", "\u0425", "\u590f", "\u6d1b", "\u514b", "\U0001F600", "\U00010400", "ing", "Tom", "river",
		"Holmes", "Sherlock", "sherLOCK", "awyer", "inn", "Watson",
		"\u0428\u0435\u0440\u043b\u043e\u043a \u0425\u043e\u043b\u043c\u0441", "\u590f\u6d1b\u514b", "abc", "de",
		"0", "25", "255.", "192.168.", "333.5.", "12:34", "555-1234", "AB12", "123\u00e9"}
	seed := uint64(0x2545_F491_4F6C_DD1D)
	next := func() uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}
	var texts []string
	for range 60 {
		n := 1 + int(next()%300)
		var b strings.Builder
		for range n {
			b.WriteString(alphabet[next()%uint64(len(alphabet))])
		}
		texts = append(texts, b.String())
	}
	for _, pf := range patterns {
		for _, mode := range []string{"", "u"} {
			flags := pf[1] + mode
			if pf[1] == "v" {
				flags = "v"
			}
			searched, plain := searchedAndPlain(t, pf[0], flags)
			if searched == nil {
				continue
			}
			if plain.prog.search.plan != planScan {
				t.Fatalf("/%s/%s: the plain pattern has a plan", pf[0], flags)
			}
			for _, text := range texts {
				where := fmt.Sprintf("/%s/%s on %q", pf[0], flags, text)
				if a, b := plain.FindAllStringSubmatchIndex(text, -1), searched.FindAllStringSubmatchIndex(text, -1); !equalSpans(a, b) {
					t.Fatalf("%s over UTF-8: %v, the plain search %v", where, b, a)
				}
				if a, b := plain.FindAllStringIndex(text, -1), searched.FindAllStringIndex(text, -1); !equalSpans(a, b) {
					t.Fatalf("%s over UTF-8, without groups: %v, the plain search %v", where, b, a)
				}
				u := utf16Of(text)
				a, _ := plain.TryFindAllUTF16SubmatchIndex(u, -1)
				b, _ := searched.TryFindAllUTF16SubmatchIndex(u, -1)
				if !equalSpans(a, b) {
					t.Fatalf("%s over UTF-16: %v, the plain search %v", where, b, a)
				}
				a, _ = plain.TryFindAllUTF16Index(u, -1)
				b, _ = searched.TryFindAllUTF16Index(u, -1)
				if !equalSpans(a, b) {
					t.Fatalf("%s over UTF-16, without groups: %v, the plain search %v", where, b, a)
				}
			}
		}
	}
}

func equalSpans(a, b [][]int) bool { return slices.EqualFunc(a, b, slices.Equal) }

// TestTheSearchesFindTheStartsTheFuzzerFound holds the faults the start-searches family of the test data and its
// fuzzer (fuzz.js there) found, each pattern over the text that showed it.
func TestTheSearchesFindTheStartsTheFuzzerFound(t *testing.T) {
	cases := [][3]string{
		{`\b\w+n\b`, "", "a nation"}, {`\b\w+n\b`, "", "an nation nn nan noon"},
		{`\w+\s+Holmes`, "", "Holmes Holmes"}, {`\b\d*\s*a\b`, "", " a"},
		{`\b\d*\W*x\b`, "s", " \u00e9\u00e9x"}, {`\b\d*\W*x`, "", "a1 x"},
		{`\b\d*\W+\w+ab`, "", "ab0 aab"},
		{`\b\w*[\u{1F600}-\u{1F64F}]*S\B`, "u", "\U0001F600S_n\U0001F600S"},
		{`\W+[a-z]*\W*a`, "m", " a"}, {`[a-z\u00e9\u00e8]+\d*[a-z]+on`, "i", " ONaON"},
		{`\w+?[^\w\s]*\d+?n`, "im", "000n"}, {`\w*\W+-$`, "", " a\u00a0-a--"},
		{`\w*[^\w\s]+k$`, "", "---_-ka.k"}, {`\b[0-9a-f]*\u00e9t`, "", "\n\u00e9t0a\u00e9t"},
		{`.{1,3}(?:on|\.).`, "", "\U0001F600\U0001F600\U0001F600ona on"},
		{`\b[^\n]{1,3}(?:k|\.)\b`, "im", "k \U0001F600nk"},
		{`[^\n]{1,3}(?:\u00e9|a)`, "", "\u4e2d\u4e2d\u4e2da"}, {`\B[^\w\s]{1,3}(?:a|n)`, "", "a\U0001F600a"},
	}
	for _, c := range cases {
		for _, mode := range []string{"", "u"} {
			searched, plain := searchedAndPlain(t, c[0], c[1]+mode)
			if searched == nil {
				continue
			}
			where := fmt.Sprintf("/%s/%s on %q", c[0], c[1]+mode, c[2])
			u := utf16Of(c[2])
			a, _ := plain.TryFindAllUTF16Index(u, -1)
			b, _ := searched.TryFindAllUTF16Index(u, -1)
			if !equalSpans(a, b) {
				t.Errorf("%s over UTF-16: %v, the plain search %v", where, b, a)
			}
			if a, b := plain.FindAllStringIndex(c[2], -1), searched.FindAllStringIndex(c[2], -1); !equalSpans(a, b) {
				t.Errorf("%s over UTF-8: %v, the plain search %v", where, b, a)
			}
		}
	}
}

// TestALiteralWithARareByteIsFoundAsAnyOther: a literal with a byte rare in text is found by the search for that byte,
// wherever in the literal it is.
func TestALiteralWithARareByteIsFoundAsAnyOther(t *testing.T) {
	seed := uint32(0x2f6b1c3d)
	below := func(n int) int {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		return int(seed % uint32(n))
	}
	for _, literal := range []string{"zqj", "aqj", "qq", "Xa", "abZ", "jj j"} {
		re := MustCompile(literal, "g")
		for range 200 {
			var b strings.Builder
			for k := below(200); k > 0; k-- {
				b.WriteByte("abqjzZX "[below(8)])
			}
			text := b.String()
			var want []int
			for i := strings.Index(text, literal); i >= 0; {
				want = append(want, i)
				j := strings.Index(text[i+len(literal):], literal)
				if j < 0 {
					break
				}
				i += len(literal) + j
			}
			var got, got16 []int
			for _, m := range re.FindAllStringIndex(text, -1) {
				got = append(got, m[0])
			}
			all16, _ := re.TryFindAllUTF16Index(utf16Of(text), -1)
			for _, m := range all16 {
				got16 = append(got16, m[0])
			}
			if !slices.Equal(want, got) || !slices.Equal(want, got16) {
				t.Fatalf("/%s/ in %q: %v and %v over UTF-16, want %v", literal, text, got, got16, want)
			}
		}
	}
}

// TestARunOfTheDotWithTheSFlagIsTheRestOfTheText: .* with the s flag is answered without reading the text, from where
// the search starts (a code point's start with the u or v flag, so a start between the halves of a pair moves back to
// the pair's start; any code unit without them) to the end. Each answer is compared with the matcher's, which a group
// in the pattern makes the search use, and with that rule written out, over UTF-16 text with lone halves anywhere.
func TestARunOfTheDotWithTheSFlagIsTheRestOfTheText(t *testing.T) {
	texts := [][]uint16{{}, {'a', 0xD800}, {'a', 0xDC00}, {0xDC00, 'a', 0xD83D, 0xDE00}, {0xD83D, 0xDE00},
		{0xDC00, 'b', 0xD83D, 0xDE00, 0xD800, 'c', 0xDBFF}}
	span := func(m []int) []int {
		if m == nil {
			return nil
		}
		return m[:2]
	}
	for _, flags := range []string{"s", "su", "sv", "sy", "suy"} {
		unicode := strings.ContainsAny(flags, "uv")
		shortcut, matcher := MustCompile(`.*`, flags), MustCompile(`(.*)`, flags)
		plus, plusMatcher := MustCompile(`.+`, flags), MustCompile(`(.+)`, flags)
		if shortcut.prog.search.shape != shapeLoop {
			t.Fatalf("/.*/%s is not a loop shape", flags)
		}
		for _, text := range texts {
			for start := 0; start <= len(text); start++ {
				from := start
				if unicode && midPair16(text, start) {
					from = start - 1
				}
				where := fmt.Sprintf("/.*/%s over %x from %d", flags, text, start)
				got, _ := shortcut.TryFindUTF16Index(text, start)
				want, _ := matcher.TryFindUTF16SubmatchIndex(text, start)
				if !slices.Equal(span(want), got) || !slices.Equal(got, []int{from, len(text)}) {
					t.Errorf("%s: %v, the matcher %v", where, got, span(want))
				}
				got, _ = plus.TryFindUTF16Index(text, start)
				want, _ = plusMatcher.TryFindUTF16SubmatchIndex(text, start)
				var rule []int
				if from < len(text) {
					rule = []int{from, len(text)}
				}
				if !slices.Equal(span(want), got) || !slices.Equal(got, rule) {
					t.Errorf("/.+/%s over %x from %d: %v, the matcher %v", flags, text, start, got, span(want))
				}
			}
		}
	}
}

// TestTheForwardAutomatonIsKeptForTheScan: the forward automaton and the window of the skip are taken where the plan is
// the scan, and not without it.
func TestTheForwardAutomatonIsKeptForTheScan(t *testing.T) {
	re := MustCompile(`(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9])`, "")
	if re.prog.forward() == nil || re.prog.skip == nil {
		t.Error("the forward automaton or the window was not taken")
	}
	noSearch.Store(true)
	defer noSearch.Store(false)
	if MustCompile(`\d{3}-\d{4}`, "").prog.forward() != nil {
		t.Error("a pattern without the search has a forward automaton")
	}
}

func TestTheUtf16SearchForARareUnitFindsWhatEveryPositionFinds(t *testing.T) {
	// A literal found by the search for its rarest unit, wherever that is in it, with the starts that unit lets
	// through and the literal does not; and up to four literals that share a rare first unit.
	cases := [][3]string{
		{"Twain", "u", "Twi Twa Twain Twain Twai"},
		{"aqj", "u", "q aq aqj qj aqjaqj"},
		{"zqj", "", "zq zqj"},
		{"\u0428\u0435\u0440\u043b\u043e\u043a", "u", "\u0428 \u0428\u0435\u0440\u043b\u043e\u043a \u0448\u0435\u0440\u043b\u043e\u043a"},
		{"\u590f\u6d1b\u514b", "u", "\u590f\u6d1b \u590f\u6d1b\u514b\u590f\u6d1b\u514b"},
		{"a\U0001F600b", "u", "a\U0001F600 a\U0001F600b"},
		{"Sherlock|Street", "u", "Sher Street Sherlock St"},
		{"Sherlock|Street", "y", "Street"},
	}
	for _, c := range cases {
		searched, plain := searchedAndPlain(t, c[0], c[1]+"g")
		text := utf16Of(c[2])
		a, _ := plain.TryFindAllUTF16Index(text, -1)
		b, _ := searched.TryFindAllUTF16Index(text, -1)
		if !equalSpans(a, b) {
			t.Errorf("/%s/%s over %q: %v, the plain search %v", c[0], c[1], c[2], b, a)
		}
		for from := 0; from <= len(text); from++ {
			x, _ := searched.TryFindUTF16Index(text, from)
			y, _ := plain.TryFindUTF16Index(text, from)
			if !slices.Equal(x, y) {
				t.Errorf("/%s/%s from %d: %v, the plain search %v", c[0], c[1], from, x, y)
			}
		}
	}
}

// TestThePlansAreTaken: each kind of pattern takes the plan, the shape and the automata it is meant to, so that the
// comparisons above compare the searches and not the scan with itself.
func TestThePlansAreTaken(t *testing.T) {
	cases := []struct {
		pattern, flags string
		plan           searchPlan
		shape          searchShape
		reverse        bool
	}{
		{`Sherlock`, "", planWindows, shapeLiterals, false},
		{`Sherlock|Holmes|Watson`, "i", planWindows, shapeForms, false},
		{`[a-zA-Z]+ing`, "", planAfterLoops, shapeNone, false},
		{`.{0,2}(Tom|Sawyer|Huckleberry|Finn)`, "", planWindows, shapeNone, true},
		{`\w+`, "", planScan, shapeLoop, false},
		{`\p{Sm}`, "u", planScan, shapeSet, false},
	}
	for _, c := range cases {
		re := MustCompile(c.pattern, c.flags)
		s := re.prog.search
		if s.plan != c.plan || s.shape != c.shape || (s.reverse != nil) != c.reverse {
			t.Errorf("/%s/%s: plan %d shape %d reverse %v", c.pattern, c.flags, s.plan, s.shape, s.reverse != nil)
		}
	}
	if re := MustCompile(`Sherlock\s+\w+`, ""); re.prog.afterLiteral == nil || re.prog.anchoredDfa == nil {
		t.Error("/Sherlock\\s+\\w+/ does not go on after its literal")
	}
	if re := MustCompile(`\b\w+n\b`, ""); !re.prog.search.leadingWordBoundary {
		t.Error("/\\b\\w+n\\b/ does not pass over the rest of a word")
	}
}
