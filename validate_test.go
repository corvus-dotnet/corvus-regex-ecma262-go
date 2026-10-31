package ecmaregex

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestTheGrammarOnlyReaderAppliesNoLimitOfSize: a pattern over a limit of size is valid when its grammar is, and not
// valid when it is not, however large.
func TestTheGrammarOnlyReaderAppliesNoLimitOfSize(t *testing.T) {
	deep := func(n int, open, close string) string {
		return strings.Repeat(open, n) + "a" + strings.Repeat(close, n)
	}
	var names, shared, twice strings.Builder
	for n := 0; n < 5000; n++ {
		fmt.Fprintf(&names, "(?<n%d>a)", n)
		if n > 0 {
			shared.WriteString("|")
		}
		shared.WriteString("(?<n>a)")
		twice.WriteString("(?<n>a)")
	}
	cases := []struct {
		pattern, flags string
		valid          bool
	}{
		{strings.Repeat("a", MaxPatternLength+1), "u", true},
		{strings.Repeat("a", MaxPatternLength+1) + "(", "u", false},
		{deep(MaxNesting+1, "(?:", ")"), "u", true},
		{deep(200000, "(?:", ")"), "", true},
		{deep(200000, "(?:", ")") + ")", "u", false},
		{deep(200000, "(?=", ")"), "", true},
		{deep(200000, "[", "]"), "v", true},
		{deep(200000, "[^", "]"), "v", true},
		{deep(200000, "[", "]") + "]", "v", false},
		{names.String() + `\k<n4999>`, "u", true},
		{names.String() + `\k<m>`, "u", false},
		{shared.String(), "u", true},
		{twice.String(), "u", false},
		// A string that is not UTF-8 is not a pattern.
		{"a\xff", "", false},
	}
	for _, c := range cases {
		shown := c.pattern
		if len(shown) > 40 {
			shown = shown[:40]
		}
		if got := IsValidPattern(c.pattern, c.flags); got != c.valid {
			t.Errorf("IsValidPattern(%q, %q) = %v", shown, c.flags, got)
		}
		if !c.valid && IsValid(c.pattern, c.flags) {
			t.Errorf("IsValid(%q, %q) = true", shown, c.flags)
		}
	}
}

// TestTheGrammarOnlyReaderAgreesWithTheParser: wherever the parser reaches no limit, the reader decides as it does,
// in each grammar, edition and version of Unicode.
func TestTheGrammarOnlyReaderAgreesWithTheParser(t *testing.T) {
	patterns := []string{
		`(?i:a)`, `(?<a>x)|(?<a>y)`, `(?<a>x)(?<a>y)`, `((?<a>x)|(?<a>y))(?<a>z)`, `(?<a>x)|((?<a>y)|(?<a>z))`,
		`\k<a>(?<a>x)`, `\k<a>`, `\k`, `(?<a>x)\k`, `\1(x)`, `\2(x)`, `\8`, `\08`, `[\8]`, `\c`, `[\c_]`,
		`\u{10FFFF}`, `\u{110000}`, `😀`, `(?<\u{1d4d0}>x)`, `(?<𝓐>x)`, `(?<𝓐>x)`,
		`\p{Script=Greek}`, `\p{sc=Yezi}`, `\p{Script=Sidetic}`, `\P{RGI_Emoji}`, `\p{RGI_Emoji}`, `[^\p{RGI_Emoji}]`,
		`\p{L}`, `\p{Lu}`, `\p{Letter}`, `\p{ASCII}`, `\p{Any}`, `\p{Assigned}`, `\p{gc=LC}`, `\p{scx=Hrkt}`,
		`\p{é}`, `\p{`, `\p{}`, `a{2,1}`, `a{99999999999999999999,1}`, `a{1,99999999999999999999}`, `a{,1}`,
		`x{`, `{1}`, `]`, `}`, `[a-]`, `[\d-a]`, `[a-\d]`, `[z-a]`, `[\b-\n]`, `(?=a)*`, `(?<=a)*`, `(?!a){2}`,
		`[[a]&&[b]]`, `[a&&b&&c]`, `[a--b--c]`, `[a&&b--c]`, `[a-z&&b]`, `[\q{abc}]`, `[^\q{abc}]`, `[^\q{a|b}]`,
		`[^[\q{ab}]]`, `[^[^\q{ab}]]`, `[[\q{ab}]&&a]`, `[^[\q{ab}]&&a]`, `[^a--\q{ab}]`, `[\q{ab}--a]`,
		`[^\q{ab}--a]`, `[a&&]`, `[&&a]`, `[a!!b]`, `[\!]`, `[(]`, `[a-[b]]`, `[\q{a`, `(?i-i:a)`, `(?-:a)`,
		`(?ii:a)`, `(?x:a)`, `(?i`, `(?<`, `(?<a`, `(?<a>`, `(?<=`, `a|*`, `\`, `[\`, `(`, `)`, `a)`, `\b+`,
		`(?:(?<a>x))|(?<a>y)`, `(?:(?<a>x)|b)(?<a>y)`, `((?<a>x)|(?:(?<a>y)))`, `(?:(?<a>x)|(?:c))(?:d|(?<a>y))`,
		`(?:(?:(?<a>x)))|(?:(?:(?<a>y)))|(?<a>z)`, `(?:(?:(?<a>x))|(?<b>y))(?<a>z)`, `(?:(?<a>x)|(?:(?<a>y)|(?<a>z)))`,
		`(?:(?:(?<a>x)|y)|(?<a>z))`, `(?:(?:(?<a>x)|y)(?<a>z))`, `(?<a>x)|(?:(?:(?<b>y))(?<a>z))`,
		`(?:(?:(?<a>x))(?:(?<b>y)))|(?<a>z)(?<b>w)`, `(?:(?<a>x))(?:(?<b>y)|(?<a>z))`,
		`^*`, `$+`, `\B{2}`, `\xg`, `\x4`, `\-`, `[\-]`, `\/`, `\a`, `\_`, "😀{2}", "[😀-😁]", "[😁-😀]",
	}
	for _, p := range patterns {
		for _, flags := range []string{"", "u", "v", "i", "iu"} {
			f, err := ParseFlags(flags)
			if err != nil {
				t.Fatal(err)
			}
			for _, edition := range []Edition{EditionCurrent, EditionES2020} {
				for _, version := range []UnicodeVersion{UnicodeDefault, Unicode13, Unicode18} {
					opts := Options{Flags: f, Edition: edition, UnicodeVersion: version}
					if got, want := IsValidPatternOptions(p, opts), IsValidOptions(p, opts); got != want {
						t.Errorf("/%s/%s %v %v: the reader says %v, the parser %v", p, flags, edition, version, got, want)
					}
				}
			}
		}
	}
}

// TestTheGrammarOnlyReaderAllocatesNothing: for a pattern of ordinary size, valid or not, in any grammar, the reader
// keeps its stacks on arrays of its own and a property name as its place in the pattern.
func TestTheGrammarOnlyReaderAllocatesNothing(t *testing.T) {
	var names strings.Builder
	for n := 0; n < 12; n++ {
		fmt.Fprintf(&names, "(?<n%d>a)", n)
	}
	names.WriteString(`\k<n11>`)
	nested := strings.Repeat("(?:", 60) + "a" + strings.Repeat(")", 60)
	cases := [][2]string{
		{`^[a-z][a-z0-9_]{0,29}$`, "u"},
		{`^(?<year>\d{4})-(?<month>\d{2})\k<year>$`, "u"},
		{`(?<a>x)|(?<a>y)`, "u"},
		{`^\p{Script=Greek}+\P{L}$`, "u"},
		{`[\p{L}--[a-z]&&\q{abc|d}]`, "v"},
		{`[^\p{RGI_Emoji}]`, "v"},
		{`(?i:a)(?<=b)(?!c)`, ""},
		{`\8[\c_]\k{2}`, ""},
		{`[z-a]`, "u"},
		{`(?<a>x)(?<a>y)`, "u"},
		{names.String(), "u"},
		{nested, "u"},
		{strings.Repeat("(a)", 60), "u"},
	}
	opts := Options{Flags: Flags{Unicode: true}}
	for _, c := range cases {
		f, err := ParseFlags(c[1])
		if err != nil {
			t.Fatal(err)
		}
		opts.Flags = f
		IsValidPatternOptions(c[0], opts)
		if allocs := testing.AllocsPerRun(50, func() { IsValidPatternOptions(c[0], opts) }); allocs != 0 {
			t.Errorf("/%s/%s: %v allocations", c[0], c[1], allocs)
		}
	}
}

// TestNamedGroupsAreReadInMemoryAndTimeInProportionToThePattern: each named group keeps the disjunction and the
// alternative it lies in, not a copy of the path to it, so nested named groups take memory in proportion to their
// number, not to its square. A name kept deep inside one alternative and used again in the next walks out of the closed
// groups to the disjunction around both, and the walk is shortened as it goes, so each name does not walk the whole
// depth again.
func TestNamedGroupsAreReadInMemoryAndTimeInProportionToThePattern(t *testing.T) {
	var nested, deep, again strings.Builder
	for k := 0; k < 100000; k++ {
		fmt.Fprintf(&nested, "(?<g%d>", k)
	}
	nested.WriteString("a" + strings.Repeat(")", 100000))
	for k := 0; k < 20000; k++ {
		fmt.Fprintf(&deep, "(?<n%d>a)", k)
		fmt.Fprintf(&again, "(?<n%d>b)", k)
	}
	reused := strings.Repeat("(?:", 50000) + deep.String() + strings.Repeat(")", 50000) + "|" + again.String()
	for _, c := range []struct {
		pattern string
		valid   bool
	}{{nested.String(), true}, {reused, true}, {reused + "(?<n0>c)", false}} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		if got := IsValidPattern(c.pattern, "u"); got != c.valid {
			t.Errorf("a pattern of %d bytes: %v", len(c.pattern), got)
		}
		took := time.Since(start)
		runtime.ReadMemStats(&after)
		if bytes := after.TotalAlloc - before.TotalAlloc; bytes > uint64(64*len(c.pattern)) {
			t.Errorf("%d bytes for a pattern of %d", bytes, len(c.pattern))
		}
		if took > 10*time.Second {
			t.Errorf("%v for a pattern of %d", took, len(c.pattern))
		}
	}
}
