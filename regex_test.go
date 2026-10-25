package ecmaregex

import (
	"errors"
	"slices"
	"testing"
	"time"
	"unicode/utf16"
)

func TestParseFlags(t *testing.T) {
	f, err := ParseFlags("ysvmigd")
	if err != nil {
		t.Fatal(err)
	}
	if f.String() != "dgimsvy" {
		t.Errorf("String() = %q", f.String())
	}
	for _, bad := range []string{"x", "gg", "uv"} {
		var e *Error
		if _, err := ParseFlags(bad); !errors.As(err, &e) || e.Kind != ErrFlags {
			t.Errorf("ParseFlags(%q) = %v, want an error of kind ErrFlags", bad, err)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	var e *Error
	if _, err := Compile("a{2,1}", ""); !errors.As(err, &e) || e.Kind != ErrInvalid {
		t.Errorf("a{2,1}: %v, want ErrInvalid", err)
	}
	if _, err := Compile("\\p{Lu}", "q"); !errors.As(err, &e) || e.Kind != ErrFlags {
		t.Errorf("flag q: %v, want ErrFlags", err)
	}
	if _, err := Compile("a\xffb", ""); !errors.As(err, &e) || e.Kind != ErrEncoding {
		t.Errorf("ill-formed pattern: %v, want ErrEncoding", err)
	}
	if !IsValid("\\p{Lu}", "u") || IsValid("\\p{Lu", "u") || !IsValid("\\p{Lu", "") {
		t.Error("IsValid")
	}
}

func TestMatchAndFind(t *testing.T) {
	re := MustCompile(`(?<year>\d{4})-(?<month>\d{2})`, "u")
	if !re.MatchString("on 2026-10-25") || re.MatchString("on 26-10") {
		t.Error("MatchString")
	}
	if got := re.FindStringIndex("on 2026-10-25"); !slices.Equal(got, []int{3, 10}) {
		t.Errorf("FindStringIndex = %v", got)
	}
	if got := re.FindStringSubmatchIndex("on 2026-10-25"); !slices.Equal(got, []int{3, 10, 3, 7, 8, 10}) {
		t.Errorf("FindStringSubmatchIndex = %v", got)
	}
	if got := re.SubexpNames(); !slices.Equal(got, []string{"", "year", "month"}) {
		t.Errorf("SubexpNames = %v", got)
	}
	if got := re.SubexpIndex("month"); !slices.Equal(got, []int{2}) {
		t.Errorf("SubexpIndex = %v", got)
	}
	if got := re.String(); got != `/(?<year>\d{4})-(?<month>\d{2})/u` {
		t.Errorf("String = %q", got)
	}
}

func TestGroupThatDidNotTakePart(t *testing.T) {
	re := MustCompile(`(a)|(b)`, "")
	if got := re.FindStringSubmatchIndex("b"); !slices.Equal(got, []int{0, 1, -1, -1, 0, 1}) {
		t.Errorf("FindStringSubmatchIndex = %v", got)
	}
}

func TestFindAllAdvancesPastEmptyMatches(t *testing.T) {
	re := MustCompile(`a*`, "u")
	got := re.FindAllStringIndex("baa😀", -1)
	want := [][]int{{0, 0}, {1, 3}, {3, 3}, {7, 7}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("FindAllStringIndex = %v, want %v", got, want)
	}
	if got := re.FindAllStringIndex("baa", 2); len(got) != 2 {
		t.Errorf("n = 2 gave %d matches", len(got))
	}
}

func TestReplace(t *testing.T) {
	re := MustCompile(`(?<year>\d{4})-(?<month>\d{2})`, "u")
	if got := re.ReplaceString("2026-10 and 2027-11", "$<month>/$<year>"); got != "10/2026 and 2027-11" {
		t.Errorf("ReplaceString = %q", got)
	}
	if got := re.ReplaceAllString("2026-10 and 2027-11", "[$&|$1|$$|$`|$']"); got !=
		"[2026-10|2026|$|| and 2027-11] and [2027-11|2027|$|2026-10 and |]" {
		t.Errorf("ReplaceAllString = %q", got)
	}
	g := MustCompile(`o`, "g")
	if got := g.ReplaceString("foo", "0"); got != "f00" {
		t.Errorf("g flag ReplaceString = %q", got)
	}
	// Without a named group $< is itself.
	if got := MustCompile(`o`, "").ReplaceString("o", "$<x>"); got != "$<x>" {
		t.Errorf("$< without names = %q", got)
	}
}

func TestSplit(t *testing.T) {
	re := MustCompile(`(-)|(\+)`, "")
	got := re.Split("a-b+c", -1)
	want := []Piece{{"a", true}, {"-", true}, {"", false}, {"b", true}, {"", false}, {"+", true}, {"c", true}}
	if !slices.Equal(got, want) {
		t.Errorf("Split = %v", got)
	}
	if got := re.Split("a-b+c", 3); len(got) != 3 {
		t.Errorf("Split limit 3 = %v", got)
	}
	if got := MustCompile(`x*`, "").Split("", -1); !slices.Equal(got, []Piece{}) {
		t.Errorf("Split of the empty text by a pattern that matches it = %v", got)
	}
	if got := MustCompile(`x`, "").Split("", -1); !slices.Equal(got, []Piece{{"", true}}) {
		t.Errorf("Split of the empty text = %v", got)
	}
}

func TestIllFormedTextIsRefused(t *testing.T) {
	re := MustCompile(`a`, "u")
	var e *Error
	if _, err := re.TryMatchString("a\xff"); !errors.As(err, &e) || e.Kind != ErrEncoding {
		t.Errorf("TryMatchString = %v, want ErrEncoding", err)
	}
	defer func() {
		if recover() == nil {
			t.Error("MatchString did not panic on text that is not UTF-8")
		}
	}()
	re.MatchString("a\xff")
}

func TestUTF16(t *testing.T) {
	u := utf16.Encode([]rune("x😀y"))
	if got, _ := MustCompile(`^.y`, "").TryMatchUTF16(u[2:]); !got {
		t.Error("a lone low surrogate is a character without the u flag")
	}
	if got, _ := MustCompile(`x.y`, "u").TryMatchUTF16(u); !got {
		t.Error("the dot takes a pair with the u flag")
	}
	if got, _ := MustCompile(`x.y`, "").TryMatchUTF16(u); got {
		t.Error("the dot takes one unit without the u flag")
	}
	pieces, _ := MustCompile(`😀`, "u").TrySplitUTF16(u, -1)
	if len(pieces) != 2 || string(utf16.Decode(pieces[0].Text)) != "x" || string(utf16.Decode(pieces[1].Text)) != "y" {
		t.Errorf("TrySplitUTF16 = %v", pieces)
	}
}

func TestHalfCharacterIsWrittenAsReplacement(t *testing.T) {
	// Without the u flag \udc00 is a code unit, the second half of 😀 (F0 9F 98 80 is D83D DE00).
	re := MustCompile(`\ude00`, "")
	if got := re.ReplaceString("a😀b", "-"); got != "a�-b" {
		t.Errorf("ReplaceString = %q", got)
	}
}

func TestTimeout(t *testing.T) {
	re, err := CompileFlags(`(?:a?){1000}a{1000}`, Flags{}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	text := string(make([]byte, 0)) + func() string {
		b := make([]byte, 999)
		for k := range b {
			b[k] = 'a'
		}
		return string(b)
	}()
	if _, err := re.TryMatchString(text); err != nil && !errors.Is(err, ErrTime) && !errors.Is(err, ErrMemory) {
		t.Errorf("TryMatchString = %v", err)
	}
	if MustCompile(`a`, "").MatchTimeout() != 0 {
		t.Error("a pattern without a timeout has one")
	}
}

func TestCapturesAllocateNothing(t *testing.T) {
	re := MustCompile(`(?<k>\w+)=(?<v>\d+)`, "u")
	c := re.NewCaptures()
	text, err := NewTextString("x a=1 b=22")
	if err != nil {
		t.Fatal(err)
	}
	if !re.CapturesText(c, text, 0) || c.Start(0) != 2 || c.End(2) != 5 || c.Named("v") != 2 {
		t.Fatalf("CapturesText = %v", c.Index())
	}
	if raceEnabled {
		t.Skip("the race detector's sync.Pool drops what is put back, so a match allocates its state again")
	}
	allocs := testing.AllocsPerRun(100, func() {
		for at := 0; re.CapturesText(c, text, at); at = c.End(0) {
		}
	})
	if allocs != 0 {
		t.Errorf("%v allocations for each search", allocs)
	}
	if re.CapturesText(c, text, 11) || c.Found() || c.Index() != nil {
		t.Error("a failed match leaves the last one")
	}
}

func TestBytes(t *testing.T) {
	re := MustCompile(`b+`, "g")
	if !re.Match([]byte("abbc")) || !slices.Equal(re.FindIndex([]byte("abbc")), []int{1, 3}) {
		t.Error("Match or FindIndex")
	}
	if got := string(re.Replace([]byte("abcb"), []byte("[$&]"))); got != "a[b]c[b]" {
		t.Errorf("Replace = %q", got)
	}
	if _, err := re.TryMatch([]byte{0xC0, 0x80}); err == nil {
		t.Error("an overlong form is read as UTF-8")
	}
	if _, err := NewText([]byte{0xED, 0xA0, 0x80}); err == nil {
		t.Error("a surrogate in UTF-8 is read as UTF-8")
	}
}

// TestANegativeStartIsTheStart: a start below 0 is read as 0, as RegExpBuiltinExec reads a lastIndex below 0 (ToLength),
// in every encoding and whatever search the pattern takes.
func TestANegativeStartIsTheStart(t *testing.T) {
	for _, c := range [][2]string{{`abc`, ""}, {`\w+`, ""}, {`[0-9]`, ""}, {"\\b\u0428\\b", "iu"}, {`x*`, ""}} {
		re := MustCompile(c[0], c[1])
		for _, s := range []string{"", "abc 1 \u0428", "zz"} {
			want, _ := re.TryFindStringIndexAt(s, 0)
			got, err := re.TryFindStringIndexAt(s, -3)
			if err != nil || !slices.Equal(got, want) {
				t.Errorf("/%s/%s over %q from -3: %v %v, want %v", c[0], c[1], s, got, err, want)
			}
			u := utf16.Encode([]rune(s))
			want16, _ := re.TryFindUTF16Index(u, 0)
			got16, err := re.TryFindUTF16SubmatchIndex(u, -3)
			if err != nil || (got16 == nil) != (want16 == nil) || got16 != nil && !slices.Equal(got16[:2], want16) {
				t.Errorf("/%s/%s over UTF-16 %q from -3: %v %v, want %v", c[0], c[1], s, got16, err, want16)
			}
		}
	}
}
