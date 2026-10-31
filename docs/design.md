# corvus-regex-ecma262-go: design

An outline of the library as it is: what it offers, how the engine is built and why, and how it is tested and
measured. The [getting-started page](getting-started.md) shows how to use it.

## 1. What it is

A Go regular expression engine with ECMA-262's grammar and answers (ECMAScript 2025), and method names that follow the
standard library's `regexp`. It is a port of the corvus-regex-ecma262 Rust crate to pure Go, with no dependencies and
no assembly, over UTF-8 (`string`, `[]byte`) and UTF-16 (`[]uint16`).

**Why ECMA-262.** JSON Schema, OpenAPI and many wire formats specify their patterns as ECMAScript regular
expressions. RE2's grammar, which `regexp` reads, has no backreferences or lookaround, and differs in its classes and
its line terminators, so a translation to it can never be exact. An engine that reads ECMA-262 directly, held to V8's
answers over a large data set (section 6), needs no translation.

## 2. Public surface

**Names.** `Regexp`, `Compile`, `MustCompile`, `MatchString`, `FindStringIndex`, `FindAllStringSubmatchIndex`,
`ReplaceString` and the rest are `regexp`'s names, so a Go programmer reads the API as one they know. Positions are
byte offsets, as `regexp`'s are, and a group that did not take part is -1, -1.

**Flags.** The flags are ECMA-262's, as a string (`"gu"`) or the `Flags` struct, and nothing else. No flag is a
`regexp` option the engine would have to refuse.

**Edition and Unicode version.** By default a pattern is read with today's grammar (`EditionCurrent`, ECMAScript 2025)
and V8's version of Unicode (`UnicodeDefault`, 17 today). `Options` overrides either, independently of the other and
of the flags: `EditionES2020` is the grammar JSON Schema 2020-12 names, and `UnicodeVersion` is 13, 17, 18 or latest. Both are resolved when the pattern is compiled, so a match costs the same whatever they are.

**Validity.** `IsValid` and `IsValidOptions` read a pattern as compiling it would, so a pattern over a limit of size
is not valid there. `IsValidPattern` and `IsValidPatternOptions` ask the grammar alone, as the .NET engine's
`IsValidPattern` and the Rust crate's `is_valid_pattern` do: a pattern too large to compile is valid there when its
grammar is. They read the pattern in place (`validate.go`, the Rust crate's `validate.rs` in Go, which makes the
parser's decision at each step), with the groups and the nested classes of the v flag on stacks of their own, so no
pattern can overflow the stack, and they allocate nothing unless the groups nest more than 64 deep, there are more than
63 groups, or there are more than 12 named groups. A named group is kept as the disjunction and the alternative it lies
in, so the memory is in proportion to the pattern however deeply named groups nest. The data-set runner asks the reader about every record in every grammar and checks its answer
against V8's. This is what a JSON Schema `format: regex` check needs: the grammar, whatever an engine's limits.

**Errors.** `Compile` returns an `*Error` whose `Kind` is `ErrInvalid`, `ErrFlags` or `ErrTooLarge` (with a
`*TooLargeError` naming the limit); `MustCompile` panics with it. A match that reaches its limit, and text that is not
UTF-8, are errors too: the `Try` methods return them (`ErrTime`, `ErrMemory`, `ErrEncoding`), and the methods with
`regexp`'s names, which return no error, panic with them.

**ECMA-262 algorithms.** `ReplaceString` substitutes as `GetSubstitution` does (`$$`, `$&`, `` $` ``, `$'`, `$n`,
`$nn`, `$<name>`), replacing every match only with `g`; `ReplaceAllString` always does. `Split` follows
`RegExp.prototype[Symbol.split]` and returns `Piece`s, since a group that did not take part is undefined.

**Input.** Every matching method has forms for `string`, `[]byte`, `Text` (UTF-8 checked once by `NewText`) and
`[]uint16`. UTF-8 is checked before it is read and refused when it is not UTF-8, never read as U+FFFD. Nothing is
transcoded.

**Allocation.** A match takes its working memory from a `sync.Pool`, so a warm `MatchString` allocates nothing; a
`Captures` and a `Matches` are reused from match to match and text to text. The `Find` and `Replace` methods allocate
what they return.

**Limits.** No pattern and no text can hang a program or overflow the stack.

- A match timeout per pattern, or a process-wide default (`SetDefaultMatchTimeout`), bounds every match, and caps its
  backtracking memory with it.
- Size limits on the pattern (`MaxPatternLength`, `MaxNesting`, its size as it is read) are deterministic.
- The matcher keeps its choices on a stack of its own. The parser and the compiler recurse on the goroutine's stack,
  which grows up to 1 GB, so the deepest pattern the limits allow is read on any goroutine.

## 3. Engine

**One matcher, two encodings.** The matcher and the readers of the automata are written once, in `vm_template.go`,
`regex_template.go` and `automata_template.go`, and expanded by `internal/expand` into a copy for each encoding
(`vm8_gen.go`, `vm16_gen.go` and so on), so no call through an interface is left in the loop. Matching by UTF-16 code
unit over UTF-8 places the position between the halves of a four-byte character at its third byte.

**The matcher.** A backtracking matcher over a flat program, with V8's quick check of the next character at each
choice. A memo of the places that failed, taken into use once a match has made sixty-four steps a position, makes a
pattern without backreferences linear in the text. The clock is read every 4,096 steps.

**Where a match can start.** The search layer is the Rust crate's, with Go's tools in place of `memchr` and
`aho-corasick`: one byte by `bytes.IndexByte` (vector code in the runtime), two to four bytes and ranges by SWAR over
eight bytes a `uint64`, UTF-16 units by SWAR over four units a word. A literal is found by its rarest byte or by two of
its rarest positions compared over eight starts at once; several literals by their bytes at one position, by that pair
search, or by a table. A literal at a fixed or bounded distance opens windows of starts. Teddy is not ported, since it
rests on a byte shuffle Go reaches only in assembly; the pair search takes its place.

**The automata.** For a pattern that needs no backtracking: an automaton over ASCII for a test from the start, an
anchored leftmost-first automaton for where a match from a start ends, a reverse automaton for the terms before a
window's anchors, and a forward automaton for a pattern with no literal. Each is built on first use, with its states
bounded.

**Simple shapes.** A literal alone, one class, one unbounded loop, an alternation of literals and a case-insensitive
ASCII literal are answered before a working memory is taken.

## 4. Generated code

The expanded templates are Go source checked in beside the templates, and CI checks they are current. The Unicode
tables of each version (`internal/ucd`), the properties of strings and the property names of ECMAScript 2020 are
written from the Rust crate's tables by `tools/unicode`, and CI checks them against the crate, so the engines carry the
same tables.

## 5. Targets and layout

Go 1.25 and later, on any platform Go builds for; CI tests on Linux (x64 and arm64) and Windows. The module is its
root package. `internal/expand` writes the per-encoding copies, `internal/ucd` holds the Unicode tables,
`tools/dataset` runs the test data, `tools/stackprobe` probes the stack, `tools/unicode` writes the tables and
`tools/benchmarks` is the benchmark harness.

## 6. Testing

**The ECMA-262 test data.** [`ecma262-regex-test-data`](https://github.com/corvus-dotnet/ecma262-regex-test-data)
holds V8's answers: validity under each grammar, `test` answers, the positions of `exec`, the global search, `y`,
`replace` and `split`, and every `exec` call of test262. `tools/dataset` runs all of it over UTF-8 and UTF-16, with
none wrong, refused or at a limit; CI runs it on every change, again with the memo from the first step, and as the
data's own scripts write it for Unicode 13 and 18 and for ECMAScript 2020.

**Other tests.** Unit tests over the methods, the searches and the automata; a test that a warm match allocates
nothing, over every search; the race detector; the getting-started page's programs; and the stack probe, which asks
964 deep, wide and long patterns through every method, each in a process of its own with the largest stack at 1 GB and
at 2 MB, none ending the program.

## 7. Benchmarks

The harness image, `ghcr.io/corvus-dotnet/corvus-regex-ecma262-go`, runs the Industry workloads of
`dotnet/performance` over this library (UTF-8 and UTF-16), `regexp`, goja and V8. The published figures are on
[the benchmark page](https://corvus-oss.org/corvus-regex-ecma262-benchmarks/), which runs every engine's image each
night; this repository publishes none of its own.

Three rows are not like for like with an engine that is not ECMA-262's, as the price of its rules under u: `.*` (`.`
excludes CR, so the CRLF text has more matches), `Sher[a-z]+|Hol[a-z]+` under i (the search must also find the long s
and the Kelvin sign), and `[a-q][^u-z]{13}x` (a supplementary character is two units, so the distance to the `x` is
not fixed).

## 8. Differences a Go user will meet

- The grammar is ECMA-262's: backreferences and lookaround, `(?i:...)` groups but no `(?i)` prefix, no `\A`, `\z` or
  `[[:alpha:]]`. `\d` is `[0-9]`, `\w` the 63 ASCII word characters, and `.` excludes LF, CR, U+2028 and U+2029.
- A pattern with a backreference is not linear in the text, as every pattern in `regexp` is, so a pattern from outside
  is given a timeout.
- The methods without `Try` panic on a match that reaches its timeout and on text that is not UTF-8.

The [getting-started page](getting-started.md) has the full list.
