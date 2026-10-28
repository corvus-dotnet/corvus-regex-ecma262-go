package ecmaregex

// The edition of the grammar and the version of Unicode (tests/editions.rs of the Rust crate): ECMAScript 2020 refuses
// what later editions added and nothing else, and each version of Unicode has its own scripts and case folding, in the
// matcher as in the parser.

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// U+10570 and U+10597 are a capital and a small letter of Vithkuqi, a script of Unicode 14. U+A7C0 and U+A7C1 are the
// capital and the small Old Polish O, also of Unicode 14.
const (
	vithkuqiPair   = "\U00010570\U00010597"
	oldPolishPair  = "Ꟁꟁ"
	vithkuqiLetter = "\U00010597"
)

func compileWith(pattern, flags string, edition Edition, version UnicodeVersion) (*Regexp, error) {
	f, err := ParseFlags(flags)
	if err != nil {
		return nil, err
	}
	return CompileOptions(pattern, Options{Flags: f, Edition: edition, UnicodeVersion: version})
}

func under(t *testing.T, pattern, flags string, version UnicodeVersion) *Regexp {
	t.Helper()
	re, err := compileWith(pattern, flags, EditionCurrent, version)
	if err != nil {
		t.Fatalf("%s /%s under %v: %v", pattern, flags, version, err)
	}
	return re
}

func kindOf(err error) (ErrorKind, bool) {
	var e *Error
	if !errors.As(err, &e) {
		return 0, false
	}
	return e.Kind, true
}

func TestTheDefaultsAreTodaysGrammarAndV8sUnicode(t *testing.T) {
	re := MustCompile("a", "")
	if re.Edition() != EditionCurrent || re.UnicodeVersion() != Unicode17 {
		t.Errorf("Compile gives %v and %v", re.Edition(), re.UnicodeVersion())
	}
	re, err := CompileOptions("a", Options{})
	if err != nil || re.UnicodeVersion() != Unicode17 || re.MatchTimeout() != 0 {
		t.Errorf("CompileOptions with no settings gives %v, %v, %v", re, err, re.MatchTimeout())
	}
}

func TestAVersionIsResolvedWhenThePatternIsCompiled(t *testing.T) {
	for _, c := range []struct{ asked, resolved UnicodeVersion }{
		{UnicodeDefault, Unicode17},
		{Unicode13, Unicode13},
		{Unicode17, Unicode17},
		{Unicode18, Unicode18},
		{UnicodeLatest, Unicode18},
	} {
		if got := under(t, "a", "", c.asked).UnicodeVersion(); got != c.resolved {
			t.Errorf("%v resolved to %v", c.asked, got)
		}
		if c.asked.Resolve() != c.resolved {
			t.Errorf("%v.Resolve() = %v", c.asked, c.asked.Resolve())
		}
	}
	if UnicodeLatest.Major() != 18 || UnicodeDefault.Major() != 17 || Unicode13.Major() != 13 {
		t.Error("Major")
	}
	if UnicodeLatest.String() != "latest" || Unicode13.String() != "13" || EditionES2020.String() != "es2020" {
		t.Error("String")
	}
}

func TestEachVersionHasItsOwnScripts(t *testing.T) {
	for _, c := range []struct {
		pattern string
		version UnicodeVersion
		valid   bool
	}{
		{`\p{Script=Vithkuqi}`, Unicode13, false},
		{`\p{Script=Vithkuqi}`, UnicodeDefault, true},
		{`\p{Script=Yezidi}`, Unicode13, true},
		{`\p{Script=Jurchen}`, UnicodeDefault, false},
		{`\p{scx=Jurc}`, Unicode18, true},
		{`\p{Script=Jurchen}`, UnicodeLatest, true},
	} {
		opts := Options{Flags: Flags{Unicode: true}, UnicodeVersion: c.version}
		if IsValidOptions(c.pattern, opts) != c.valid {
			t.Errorf("IsValidOptions(%s) under %v = %v", c.pattern, c.version, !c.valid)
		}
		if _, err := CompileOptions(c.pattern, opts); (err == nil) != c.valid {
			t.Errorf("CompileOptions(%s) under %v: %v", c.pattern, c.version, err)
		}
	}
}

func TestAPropertySetIsItsVersionsHoweverTheCacheWasFilled(t *testing.T) {
	// The same expression under two versions, in both orders: the cache of property sets is one for each version.
	for range 2 {
		if !under(t, `^\p{Ll}$`, "u", Unicode17).MatchString(vithkuqiLetter) {
			t.Error(`\p{Ll} under 17`)
		}
		if under(t, `^\p{Ll}$`, "u", Unicode13).MatchString(vithkuqiLetter) {
			t.Error(`\p{Ll} under 13`)
		}
	}
}

func TestALiteralInACaseInsensitivePatternFoldsAsItsVersionFolds(t *testing.T) {
	if !under(t, `\u{10570}`, "iu", Unicode17).MatchString(vithkuqiLetter) {
		t.Error("Vithkuqi under 17")
	}
	if under(t, `\u{10570}`, "iu", Unicode13).MatchString(vithkuqiLetter) {
		t.Error("Vithkuqi under 13")
	}
	if !under(t, "Ꟁ", "i", Unicode17).MatchString("ꟁ") {
		t.Error("Old Polish O under 17")
	}
	if under(t, "Ꟁ", "i", Unicode13).MatchString("ꟁ") {
		t.Error("Old Polish O under 13")
	}
}

func TestABackreferenceInACaseInsensitivePatternFoldsAsItsVersionFolds(t *testing.T) {
	for _, c := range []struct{ flags, text string }{{"iu", vithkuqiPair}, {"i", oldPolishPair}} {
		first := string([]rune(c.text)[:1])
		twice := strings.Repeat(first, 2)
		t16 := utf16.Encode([]rune(c.text))
		for _, pattern := range []string{`(.)\1`, `(?<=\1(.))`, `(?:x|(.))+\1$`} {
			for _, version := range []UnicodeVersion{Unicode17, Unicode18} {
				re := under(t, pattern, c.flags, version)
				if !re.MatchString(c.text) {
					t.Errorf("%s /%s under %v", pattern, c.flags, version)
				}
				if !re.MatchUTF16(t16) {
					t.Errorf("%s /%s over UTF-16 under %v", pattern, c.flags, version)
				}
			}
			re := under(t, pattern, c.flags, Unicode13)
			if re.MatchString(c.text) {
				t.Errorf("%s /%s under 13", pattern, c.flags)
			}
			if re.MatchUTF16(t16) {
				t.Errorf("%s /%s over UTF-16 under 13", pattern, c.flags)
			}
			if !re.MatchString(twice) {
				t.Errorf("%s /%s under 13, the same letter twice", pattern, c.flags)
			}
			// The program that records every group folds as the one that finds the match.
			if re.FindStringSubmatchIndex(twice) == nil {
				t.Errorf("%s /%s captures under 13", pattern, c.flags)
			}
			if re.FindStringSubmatchIndex(c.text) != nil {
				t.Errorf("%s /%s captures under 13", pattern, c.flags)
			}
		}
	}
}

func TestTheGrammarOf2020RefusesWhatLaterEditionsAdded(t *testing.T) {
	for _, c := range []struct{ pattern, flags string }{
		{"(?i:a)", ""},
		{"(?-i:a)", "i"},
		{"(?s:.)", "u"},
		{"(?<a>x)|(?<a>y)", ""},
		{"(?<a>x)|(?<a>y)", "u"},
		{`\p{Script=Yezidi}`, "u"},
		{`\p{sc=Vith}`, "u"},
		{`\p{Script_Extensions=Toto}`, "u"},
	} {
		_, err := compileWith(c.pattern, c.flags, EditionES2020, UnicodeLatest)
		if kind, ok := kindOf(err); !ok || kind != ErrInvalid {
			t.Errorf("%s /%s in 2020: %v", c.pattern, c.flags, err)
		}
		if _, err := compileWith(c.pattern, c.flags, EditionCurrent, UnicodeLatest); err != nil {
			t.Errorf("%s /%s today: %v", c.pattern, c.flags, err)
		}
	}
}

func TestTheGrammarOf2020AcceptsWhatItHad(t *testing.T) {
	for _, c := range []struct{ pattern, flags string }{
		{`\p{Script=Greek}`, "u"},
		{`\p{L}\p{gc=Lu}\p{Lowercase}`, "u"},
		{`\P{scx=Latn}`, "iu"},
		{`\p{Script=Yezidi}`, ""},
		{`(?<a>x)(?<b>y)\k<a>`, "u"},
		{`(?<=a)(?<!b)`, "s"},
		{`(?:i)`, ""},
	} {
		if _, err := compileWith(c.pattern, c.flags, EditionES2020, Unicode13); err != nil {
			t.Errorf("%s /%s: %v", c.pattern, c.flags, err)
		}
	}
}

func TestTheGrammarOf2020HasNoDOrVFlag(t *testing.T) {
	for _, flags := range []string{"d", "v"} {
		_, err := compileWith("a", flags, EditionES2020, UnicodeDefault)
		if kind, ok := kindOf(err); !ok || kind != ErrFlags {
			t.Errorf("%s in 2020: %v", flags, err)
		}
		f, _ := ParseFlags(flags)
		if IsValidOptions("a", Options{Flags: f, Edition: EditionES2020}) {
			t.Errorf("IsValidOptions with %s in 2020", flags)
		}
		if _, err := compileWith("a", flags, EditionCurrent, UnicodeDefault); err != nil {
			t.Errorf("%s today: %v", flags, err)
		}
	}
}

func TestCompileOptionsTakesAMatchTimeout(t *testing.T) {
	re, err := CompileOptions("a+", Options{Flags: Flags{Unicode: true}, MatchTimeout: 50 * time.Millisecond})
	if err != nil || re.MatchTimeout() != 50*time.Millisecond || re.Edition() != EditionCurrent {
		t.Errorf("CompileOptions with a timeout: %v %v", re, err)
	}
	if _, err := CompileOptions("a{2,1}", Options{Flags: Flags{Unicode: true}}); err == nil {
		t.Error("a{2,1} /u compiled")
	} else if kind, _ := kindOf(err); kind != ErrInvalid {
		t.Errorf("a{2,1} /u: %v", err)
	}
	if _, err := CompileOptions("\xff", Options{}); err == nil {
		t.Error("a pattern that is not UTF-8 compiled")
	}
}
