# corvus-regex-ecma262 for Go

Regular expressions with the grammar and the answers of ECMA-262, the `RegExp` of JavaScript, for Go 1.25 and later.

- Every flag: `d`, `g`, `i`, `m`, `s`, `u`, `v` and `y`, with the grammar of each. Without `u` or `v` a pattern is
  read by the Annex B grammar and matched by UTF-16 code unit, as ECMA-262 matches it; with them, by code point. The
  `v` flag has nested classes, the difference and the intersection of classes, strings in a class and the seven
  properties of strings.
- ECMAScript 2025: lookbehind, named groups (and a name shared between alternatives), modifier groups such as
  `(?i:...)`, and every Unicode property ECMA-262 lists, from tables of Unicode 17 the module carries, so a pattern
  answers the same whatever Go builds it.
- Text as UTF-8 (a `string`, a `[]byte`, or a `Text`, which is checked once) or as UTF-16 (`[]uint16`, lone
  surrogates and all). Nothing is transcoded. Text that is not UTF-8 is refused, never read as U+FFFD. A match into a
  `Captures` allocates nothing once the working memory has grown.
- The method set of the standard library's `regexp` (`MatchString`, `FindStringIndex`, `FindAllStringIndex`,
  `FindStringSubmatchIndex`, `ReplaceAllString`, `Split` and their `[]byte` forms), with ECMA-262's answers: the global
  search moves on as `matchAll` does, and replace has every `$` form of `GetSubstitution`.
- The search layer of the crate: literals found by their rarest bytes, the windows of an alternation, the search after
  loops, the shapes answered without the matcher, and automata over ASCII for where a match starts and ends
  (`docs/port-checklist.md`). A global search through `Matches` into a `Captures` allocates nothing.
- A pattern without a backreference is matched in time proportional to its size times the text. A pattern can be
  given a match timeout, as a `Regex` of .NET can, which bounds the time and the backtracking memory of every match;
  the `Try` methods return the error. With a timeout and the module's limits of pattern size, neither a pattern nor a
  text from outside can make a program hang. `SetDefaultMatchTimeout` gives every pattern one.

**Compliance.** Over the [ECMA-262 test data](https://github.com/corvus-dotnet/ecma262-regex-test-data) (what V8 says
about its patterns and texts, test262's patterns and `exec` calls, the global search, `replace` and `split` among them,
and texts of bytes that are not UTF-8, which it refuses), the module gives every answer right and none wrong, refuses
no valid pattern, accepts no invalid one and reaches no limit, over UTF-8 and over UTF-16. CI runs the whole data on
every change.

It is a port of the Rust crate [corvus-regex-ecma262](https://github.com/corvus-dotnet/corvus-regex-ecma262-rs), and it
follows the design of the .NET engine [Corvus.Regex.Ecma262](https://github.com/corvus-dotnet/Corvus.Regex.Ecma262).

```go
import ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"

re := ecmaregex.MustCompile(`(?<year>\d{4})-(?<month>\d{2})`, "u")
re.MatchString("on 2026-10-25")                           // true
re.ReplaceAllString("2026-10-25", "$<month>/$<year>")     // "10/2026-25"

c := re.NewCaptures()
if re.CapturesString(c, "on 2026-10-25", 0) {
    month := c.Named("month")                             // 2
    _ = c.Start(month)                                    // 8
}
```

## Status

Not yet released. The parser, the compiler, the matcher and the crate's search layer (where a match can start, and the
automata, `docs/port-checklist.md`) are in, and the whole test data passes. The harness for the central benchmark site
and the first release, 0.1.0, come next.

## Installing

Once 0.1.0 is out:

```
go get github.com/corvus-dotnet/corvus-regex-ecma262-go
```

The module has no dependency.

## Benchmarks

The [central benchmark site](https://corvus-oss.org/corvus-regex-ecma262-benchmarks/) runs this repository's harness
image, `ghcr.io/corvus-dotnet/corvus-regex-ecma262-go`, each night beside the engines of every other source: this
library over UTF-8 and UTF-16, Go's `regexp`, goja (a JavaScript engine written in Go) and V8, over the Industry
workloads of dotnet/performance, with each ECMA-262 engine's compliance over the test data. This repository publishes
no figures of its own. `tools/benchmarks/run.sh <workloads.json> <texts> <out.json>` runs the same harness anywhere
(see the script for what it needs).

goja's compliance takes some minutes to work out, so it is worked out once (`GOJA_COMPLIANCE=1`, which runs
`tools/benchmarks/goja-dataset`) and checked in as `tools/benchmarks/compliance-goja.json`, with the commit of the data
it was made at. goja cannot be stopped in a match, so each file is judged in a child process whose watchdog ends it at
an answer that passes 2 seconds; that answer is unanswered, and a new child goes on from the next. goja has no `d`
flag, so the answers that need the positions of groups are wrong, and no `v` flag, so that grammar is left out, as its
compliance declares.

## Building and testing

```
go generate ./...          # after changing vm_template.go or regex_template.go
go test ./...
go run ./tools/dataset <ecma262-regex-test-data>/data wrong-cases.tsv
```

`vm8_gen.go`, `vm16_gen.go`, `regex8_gen.go` and `regex16_gen.go` are written from the templates by `internal/expand`,
one for each encoding, and CI checks they are current. The Unicode tables (`internal/ucd/tables_gen.go`,
`strings_gen.go`) are written from the Rust crate's by `tools/unicode`.

## Releasing

Every pull request merged into main without the `no_release` label releases the module with the next patch version
(`.github/workflows/auto-release.yml`, `tools/release`): a `v<version>` tag on a commit of the merge with the version
written into `Version`, which is what the Go module proxy serves. A pull request that needs a minor or major version
sets `nextVersion` in `.github/release.json`.

## License

Apache 2.0.
