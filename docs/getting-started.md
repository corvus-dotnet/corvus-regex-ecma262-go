# Getting started with Go

## Introduction

`github.com/corvus-dotnet/corvus-regex-ecma262-go` is a Go engine whose patterns answer as ECMAScript does. Where the
standard library's `regexp` has RE2's grammar and its meaning for `\d`, `\w`, `\s`, `.` and case folding, this engine
has ECMA-262's, so a pattern written for JavaScript, or taken from a JSON Schema, matches the same texts here as it
does there. It is a port of the corvus-regex-ecma262 Rust crate, held to V8's answers on the ECMA-262 test data, in
pure Go with no dependencies. Its methods are named as `regexp`'s are.

This guide covers:

- **Setup**: adding the module.
- **Patterns and flags**: the flags are ECMA-262's letters, and what each one does.
- **The edition and the version of Unicode**: the grammar of ECMAScript 2020, and the Unicode tables a pattern reads.
- **Matching**: `MatchString`, `FindStringIndex`, the global search, `ReplaceString` and `Split`, and `Captures` and
  `Matches`, which are reused from match to match.
- **Timeouts**: the limit of time and of memory a match may take, and the `Try` methods that return it as an error.
- **UTF-8 and UTF-16**: strings and `[]byte` as UTF-8, checked once with `NewText`, and `[]uint16` as UTF-16.
- **Differences from regexp**: what to expect when the two meet.

## Setup

```
go get github.com/corvus-dotnet/corvus-regex-ecma262-go
```

It needs Go 1.25 or later, on any platform Go builds for, and has no dependencies.

```go
import ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
```

## Patterns and flags

A pattern is written as it would be in JavaScript, and the flags are the letters `RegExp` takes, as a string or as the
`Flags` struct:

| Flag | `Flags` | Meaning |
|---|---|---|
| `d` | `HasIndices` | Accepted for completeness; indices are always kept. |
| `g` | `Global` | `ReplaceString` replaces every match; without it, the first, as `String.prototype.replace` does. |
| `i` | `IgnoreCase` | Canonicalize of the mode in force: simple case folding with `u` or `v`, `toUpperCase` without. Never the locale. |
| `m` | `Multiline` | `^` and `$` at line terminators. |
| `s` | `DotAll` | The dot matches line terminators. |
| `u` | `Unicode` | The `u` flag grammar; matched by code point. |
| `v` | `UnicodeSets` | Set notation and properties of strings; matched by code point. `u` and `v` together is an error, as in ECMA-262. |
| `y` | `Sticky` | A match only at the start position. |

```go
package main

import (
	"slices"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func main() {
	a := ecmaregex.MustCompile(`\p{Script=Greek}+`, "u")
	b := ecmaregex.MustCompile(`[[a-z]--[aeiou]]+`, "v")
	c := ecmaregex.MustCompile(`^(?<year>\d{4})-(?<month>\d{2})$`, "u")

	if !a.MatchString("αβγ") || !b.MatchString("rhythm") {
		panic("Greek, and a word with no vowels")
	}
	if names := c.SubexpNames(); !slices.Equal(names, []string{"", "year", "month"}) {
		panic(names)
	}
}
```

With no flag the pattern is read with the Annex B grammar and matched by UTF-16 code unit, which is what ECMA-262 does
with no flag. JSON Schema's reading, the `u` flag first and Annex B when that refuses, is two calls of
`CompileOptions`, which returns an error rather than panicking and takes the match timeout, so no default timeout is
needed:

```go
package main

import (
	"time"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func schemaPattern(pattern string) (*ecmaregex.Regexp, error) {
	re, err := ecmaregex.CompileOptions(pattern, ecmaregex.Options{Flags: ecmaregex.Flags{Unicode: true}, MatchTimeout: time.Second})
	if err != nil {
		re, err = ecmaregex.CompileOptions(pattern, ecmaregex.Options{MatchTimeout: time.Second})
	}
	return re, err
}

func main() {
	if re, err := schemaPattern(`^\p{L}+$`); err != nil || !re.Flags().Unicode {
		panic("read with u")
	}
	// An identity escape, which only Annex B allows.
	if re, err := schemaPattern(`\-`); err != nil || re.Flags().Unicode {
		panic("read with Annex B")
	}
}
```

A pattern that is not valid gives an `*Error` whose `Kind` is `ErrInvalid` and whose message says why; invalid flags
are `ErrFlags`. Every pattern has deterministic limits of size, and `ErrTooLarge` says when one is passed, with a
`*TooLargeError` naming the limit. `MustCompile` panics with the error, for a pattern written into the program.

```go
package main

import (
	"errors"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func main() {
	_, err := ecmaregex.Compile("a{2,1}", "u")
	var e *ecmaregex.Error
	if !errors.As(err, &e) || e.Kind != ecmaregex.ErrInvalid {
		panic("a pattern that is not valid is ErrInvalid")
	}
	if _, err := ecmaregex.Compile("a", "uv"); !errors.As(err, &e) || e.Kind != ecmaregex.ErrFlags {
		panic("u with v is ErrFlags")
	}
}
```

## The edition and the version of Unicode

By default a pattern is read with today's grammar, ECMAScript 2025 (`EditionCurrent`), and with the tables of the
version of Unicode that V8 carries, 17 today (`UnicodeDefault`). Either can be overridden when the pattern is compiled,
each independently of the other.

JSON Schema 2020-12 names the grammar of ECMAScript 2020, which `EditionES2020` selects in place of the default. It
refuses what later editions added, and nothing else: the `d` and `v` flags, modifier groups such as `(?i:...)`, a group
name used by two groups, and a property name or value that is not in the 2020 tables (such as `\p{Script=Yezidi}`). A
pattern it accepts has the same answers as in today's grammar.

The version of Unicode is chosen apart from the edition: `Unicode13`, `Unicode17`, `Unicode18`, `UnicodeLatest` (18
today) or `UnicodeDefault`, the version of the V8 the answers are held to (17 today). It decides the members of each
`\p{...}`, the characters of a group name and the case folding of the `i` flag. `UnicodeVersion()` reports the version
a pattern was compiled with. Both are set in the `Options` and read when the pattern is compiled, so a match costs the
same whichever is chosen.

```go
package main

import (
	"time"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func main() {
	// ECMAScript 2020 as its edition was published, with the Unicode of its year.
	greek, err := ecmaregex.CompileOptions(`^\p{Script=Greek}+$`, ecmaregex.Options{
		Flags:          ecmaregex.Flags{Unicode: true},
		MatchTimeout:   time.Second,
		Edition:        ecmaregex.EditionES2020,
		UnicodeVersion: ecmaregex.Unicode13,
	})
	if err != nil || !greek.MatchString("αβγ") || greek.UnicodeVersion() != ecmaregex.Unicode13 {
		panic("Greek in ECMAScript 2020 with Unicode 13")
	}

	// Vithkuqi is in Unicode 14 and later, so not in Unicode 13.
	if ecmaregex.IsValidOptions(`\p{Script=Vithkuqi}`, ecmaregex.Options{
		Flags: ecmaregex.Flags{Unicode: true}, UnicodeVersion: ecmaregex.Unicode13,
	}) {
		panic("no Vithkuqi in Unicode 13")
	}
}
```

## Matching

The methods are named as `regexp`'s are, with the position as an argument (the `At` forms) where JavaScript keeps
`lastIndex`. A match is a pair of byte offsets, and a group that did not take part is -1, -1.

```go
package main

import (
	"slices"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func main() {
	date := ecmaregex.MustCompile(`(?<year>\d{4})-(?<month>\d{2})`, "gu")
	text := "due 2026-10, then 2027-01"

	if !date.MatchString(text) {
		panic("a match")
	}
	if got := date.FindStringIndex(text); !slices.Equal(got, []int{4, 11}) {
		panic(got)
	}
	m := date.FindStringSubmatchIndex(text)
	year := date.SubexpIndex("year")[0]
	if text[m[2*year]:m[2*year+1]] != "2026" || text[m[4]:m[5]] != "10" {
		panic("the groups")
	}

	// Every match of a global search, as matchAll makes it.
	if all := date.FindAllStringIndex(text, -1); len(all) != 2 {
		panic(all)
	}

	// ReplaceString replaces every match with the g flag, and the first without it, with every $ form of ECMA-262.
	if got := date.ReplaceString(text, "$<month>/$1"); got != "due 10/2026, then 01/2027" {
		panic(got)
	}

	// Split puts what the groups captured between the pieces; a group that did not take part is a Piece that is not OK.
	var pieces []string
	for _, p := range ecmaregex.MustCompile(`\s*(;)\s*`, "").Split("a ; b;c", -1) {
		pieces = append(pieces, p.Text)
	}
	if !slices.Equal(pieces, []string{"a", ";", "b", ";", "c"}) {
		panic(pieces)
	}
}
```

`FindStringIndexAt(s, start)` and the other `At` forms are `exec` with `lastIndex` set to `start`, and `y` anchors
them there. The `Find` and `Replace` methods allocate what they return. `MatchString`, a `Captures` from
`NewCaptures`, which holds a match's groups and is reused from match to match, and a `Matches` from `NewMatches`, a
global search reused from text to text, allocate nothing once they have grown:

```go
package main

import ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"

func main() {
	date := ecmaregex.MustCompile(`(?<year>\d{4})-(?<month>\d{2})`, "u")
	c := date.NewCaptures()
	text := "due 2026-10"
	if !date.CapturesString(c, text, 0) || text[c.Start(1):c.End(1)] != "2026" {
		panic("year")
	}
}
```

A `*Regexp` holds no mutable state, so it is safe for concurrent use by any number of goroutines.

## Timeouts

A match has no limit by default. A pattern compiled with a match timeout abandons a match that reaches it, or that
reaches the cap on backtracking memory the timeout brings, whatever the pattern. The `Try` methods return that as an
error of kind `ErrTime` or `ErrMemory` (matching `ErrTime` and `ErrMemory` with `errors.Is`); the others panic with it,
as they do on text that is not UTF-8.

```go
package main

import (
	"errors"
	"strings"
	"time"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func main() {
	slow, err := ecmaregex.CompileFlags(`^(?:(a+)\1*)+$`, ecmaregex.Flags{}, 100*time.Millisecond)
	if err != nil || slow.MatchTimeout() != 100*time.Millisecond {
		panic("the timeout")
	}
	if _, err := slow.TryMatchString(strings.Repeat("a", 60) + "b"); !errors.Is(err, ecmaregex.ErrTime) {
		panic("the match stops at its timeout")
	}
}
```

A pattern compiled without a timeout (with `Compile`) takes `DefaultMatchTimeout()`, which is none unless
`SetDefaultMatchTimeout` sets it. A program that matches patterns or texts from outside should set one, for example
with `ecmaregex.SetDefaultMatchTimeout(time.Second)` when it starts, and use the `Try` methods.

A pattern without a backreference is matched in time linear in the text, and no pattern can overflow the stack, so the
timeout is what bounds the others.

## UTF-8 and UTF-16

A `string` or a `[]byte` is UTF-8, matched as its bytes with no transcoding, and positions are byte offsets. It is
checked before it is read: text that is not UTF-8 is refused with an error of kind `ErrEncoding` (the `Try` methods)
or a panic, and is never matched or read as U+FFFD. `NewText` checks a text once, for a text matched many times, and
the `Text` methods take it. The `UTF16` methods take a `[]uint16`, lone surrogates and all, with positions in code
units.

```go
package main

import (
	"slices"
	"unicode/utf16"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func main() {
	dotB := ecmaregex.MustCompile(".b", "u")
	if got := dotB.FindStringIndex("a😀b"); !slices.Equal(got, []int{1, 6}) {
		panic(got) // bytes of UTF-8
	}

	units := utf16.Encode([]rune("a😀b"))
	if got, _ := dotB.TryFindUTF16Index(units, 0); !slices.Equal(got, []int{1, 4}) {
		panic(got) // UTF-16 code units
	}

	if text, err := ecmaregex.NewText([]byte("a😀b")); err != nil || !dotB.MatchText(text) {
		panic("a text checked once")
	}
	if _, err := dotB.TryMatchString("\xff"); err == nil {
		panic("text that is not UTF-8 is refused")
	}
}
```

Under the `u` and `v` flags a character is a code point in both encodings, so a class, a dot or a quantifier takes a
four-byte sequence as one character. With no flag ECMA-262 matches by UTF-16 code unit; over UTF-8 the engine keeps
that meaning, so a match can start or end between the halves of a character beyond the Basic Multilingual Plane, at
the character's third byte.

## Differences from regexp

The method names follow `regexp`; the meaning follows ECMA-262. The differences a user coming from `regexp` will meet:

- **The grammar.** Backreferences, lookahead and lookbehind are there, which RE2 does not have. No `(?i)` inline option
  (ECMA-262 has `(?i:...)` groups), no `\A` or `\z`, and no `[[:alpha:]]` classes. The flags are a separate string.
- **The character classes.** `\d` is `[0-9]`, `\w` is the 63 ASCII word characters, `\s` is ECMAScript's white space
  and line terminators, and `.` excludes the four line terminators (LF, CR, U+2028, U+2029).
- **Replacement.** A template writes a group as `$1` or `$<name>`, not `${name}`, and `$&`, `` $` `` and `$'` are the
  match and the text before and after it; `ReplaceString` replaces every match only with the `g` flag, and
  `ReplaceAllString` always does.
- **Split.** `Split` is JavaScript's, which puts what the groups captured between the pieces, and returns `Piece`s,
  since a group that did not take part is undefined, not an empty string.
- **Timeouts.** `regexp` is linear in the text for every pattern, having no backreferences. Here a pattern without a
  backreference is linear too, and a timeout bounds the others.
