package ecmaregex

import "testing"

// TestDfaAgreesWithTheMatcher compares the automaton's answer with the matcher's over ASCII texts, for patterns the
// automaton takes, and checks that it decides every ASCII text.
func TestDfaAgreesWithTheMatcher(t *testing.T) {
	patterns := []struct{ pattern, flags string }{
		{`^[a-z]+\d*$`, ""}, {`\bfoo\b`, ""}, {`\Bo+\B`, "u"}, {`(a|ab)(c|bcd)(d*)`, ""}, {`colou?r`, "i"},
		{`^$`, ""}, {`x{2,4}y`, ""}, {`[^\s]+@[^\s]+`, "u"}, {`a|`, ""}, {`(?:ab)*c`, ""},
	}
	texts := []string{"", "a", "abc123", "foo", "a foo b", "food", "xxy", "xxxxxy", "COLOR", "colour", "a@b",
		"abcd", "ababc", "éfoo", " oo "}
	for _, p := range patterns {
		re := MustCompile(p.pattern, p.flags)
		if !re.regular {
			t.Fatalf("/%s/%s is not regular", p.pattern, p.flags)
		}
		d := re.automaton()
		if d == nil {
			t.Fatalf("/%s/%s has no automaton", p.pattern, p.flags)
		}
		for _, s := range texts {
			_, _, want, _ := re.exec8([]byte(s), 0, false, nil)
			got, decided := d.isMatch8([]byte(s))
			ascii := true
			for _, b := range []byte(s) {
				ascii = ascii && b < 0x80
			}
			// A text beyond ASCII may still be decided before its first such unit (a|, which accepts at once).
			if ascii && !decided {
				t.Errorf("/%s/%s over %q: not decided", p.pattern, p.flags, s)
			}
			if decided && got != want {
				t.Errorf("/%s/%s over %q: the automaton says %v, the matcher %v", p.pattern, p.flags, s, got, want)
			}
		}
	}
	if MustCompile(`(a)\1`, "").regular || MustCompile(`a(?=b)`, "").regular {
		t.Error("a backreference or a lookahead is regular")
	}
}
