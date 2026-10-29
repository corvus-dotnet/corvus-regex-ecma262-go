# Getting started with Go

`github.com/corvus-dotnet/corvus-regex-ecma262-go` is a pure Go engine whose patterns have the grammar and the answers
of JavaScript's `RegExp`, held to V8's answers on the ECMA-262 test data, over UTF-8 (`string`, `[]byte`) and UTF-16
(`[]uint16`). Its methods are those of the standard library's `regexp`, with ECMA-262's answers.

## Install

```
go get github.com/corvus-dotnet/corvus-regex-ecma262-go
```

It needs Go 1.25 or later, and has no dependencies.

## Compile a pattern

`Compile` takes a pattern and its flags as `new RegExp(pattern, flags)` takes them: `d`, `g`, `i`, `m`, `s`, `u`, `v`
and `y`. Without `u` or `v` a pattern is read by the Annex B grammar and matched by UTF-16 code unit, as ECMA-262 reads
it; with them, by code point. A pattern that is not valid returns an `*Error` whose `Kind` says why; `MustCompile`
panics instead, for a pattern written into the program.

```go
package main

import (
	"errors"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func main() {
	date := ecmaregex.MustCompile(`(?<year>\d{4})-(?<month>\d{2})`, "gu")
	if date.NumSubexp() != 2 {
		panic("two groups")
	}

	_, err := ecmaregex.Compile("a{2,1}", "u")
	var e *ecmaregex.Error
	if !errors.As(err, &e) || e.Kind != ecmaregex.ErrInvalid {
		panic("a pattern that is not valid is ErrInvalid")
	}
	if ecmaregex.IsValid("a", "uv") {
		panic("u with v is not valid")
	}
}
```

## Match

Positions are byte offsets into the UTF-8 text, as in `regexp`. A group that did not take part is -1, -1.

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

	// The groups, by number and by name, into a Captures that is reused from match to match.
	c := date.NewCaptures()
	if !date.CapturesString(c, text, 0) || text[c.Start(c.Named("year")):c.End(c.Named("year"))] != "2026" {
		panic("year")
	}

	// Every match of a global search, as matchAll makes it.
	if all := date.FindAllStringIndex(text, -1); len(all) != 2 {
		panic(all)
	}

	// ReplaceString replaces every match with the g flag, and the first without it, with every $ form of ECMA-262.
	if got := date.ReplaceString(text, "$<month>/$1"); got != "due 10/2026, then 01/2027" {
		panic(got)
	}

	// Split splices in what the groups captured, as RegExp.prototype[Symbol.split] does.
	var pieces []string
	for _, p := range ecmaregex.MustCompile(`\s*(;)\s*`, "").Split("a ; b;c", -1) {
		pieces = append(pieces, p.Text)
	}
	if !slices.Equal(pieces, []string{"a", ";", "b", ";", "c"}) {
		panic(pieces)
	}
}
```

## UTF-8 and UTF-16

Every method checks that its text is UTF-8, and refuses text that is not: it is never matched or read as U+FFFD.
`NewText` checks a text once, for a text matched many times. The `UTF16` methods take `[]uint16`, lone surrogates and
all, with positions in code units.

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

	if _, err := dotB.TryMatchString("\xff"); err == nil {
		panic("text that is not UTF-8 is refused")
	}
	if text, err := ecmaregex.NewText([]byte("a😀b")); err != nil || !dotB.MatchText(text) {
		panic("a text checked once")
	}
}
```

## Patterns and texts from outside

A match has no limit by default. A program that matches a pattern or a text it did not write gives the pattern a match
timeout, as a .NET `Regex` has. The `Try` methods return `ErrTime` when a match runs past it, and `ErrMemory` when its
backtracking memory reaches its cap; the others panic then. `SetDefaultMatchTimeout` sets the timeout of every pattern
compiled after it.

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
	if err != nil {
		panic(err)
	}
	if _, err := slow.TryMatchString(strings.Repeat("a", 60) + "b"); !errors.Is(err, ecmaregex.ErrTime) {
		panic("the match stops at its timeout")
	}
}
```

## The edition and the version of Unicode

JSON Schema 2020-12 names the grammar of ECMAScript 2020. `EditionES2020` refuses what later editions added: the `d`
and `v` flags, modifier groups, a group name used twice, and property names outside the 2020 tables. The version of
Unicode is chosen apart from it: `Unicode13`, `Unicode17`, `Unicode18`, `UnicodeLatest` (18) or `UnicodeDefault`
(V8's, 17). Both are read when the pattern is compiled, so a match costs the same whatever they are.

```go
package main

import (
	"time"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

func main() {
	greek, err := ecmaregex.CompileOptions(`^\p{Script=Greek}+$`, ecmaregex.Options{
		Flags:          ecmaregex.Flags{Unicode: true},
		MatchTimeout:   time.Second,
		Edition:        ecmaregex.EditionES2020,
		UnicodeVersion: ecmaregex.Unicode13,
	})
	if err != nil || !greek.MatchString("αβγ") {
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

## Differences from Go's regexp

- The grammar is ECMA-262's, not RE2's. Backreferences, lookahead and lookbehind are there. The flags are a string as
  `RegExp` takes them, and `(?i:...)` groups set them inside a pattern; there is no `(?i)` prefix, no `\A` or `\z`,
  and no `[[:alpha:]]` classes.
- `\d` is `[0-9]`, `\w` the 63 ASCII word characters, `\s` ECMAScript's white space and line terminators, and `.`
  excludes LF, CR, U+2028 and U+2029.
- A replacement template writes a named group as `$<name>`, not `${name}`, and `$&`, `` $` `` and `$'` are the match
  and the text before and after it.
- `Split` puts what the groups captured between the pieces, and returns `Piece`s, since a group that did not take part
  is undefined, not an empty string.
- `regexp` guarantees time linear in the text for every pattern. Here a pattern without a backreference is linear, but
  one with a backreference can take time exponential in the text, so give a pattern from outside a timeout.

## Where next

- [The API reference](https://pkg.go.dev/github.com/corvus-dotnet/corvus-regex-ecma262-go) on pkg.go.dev.
- [The README](../README.md).
- [The benchmark page](https://corvus-oss.org/corvus-regex-ecma262-benchmarks/), with every engine measured nightly.
