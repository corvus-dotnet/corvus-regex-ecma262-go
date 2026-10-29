# Contributing

This guide is for someone who wants to build the library, run its tests and gates, and open a pull request. It covers
the tools, the build step by step, the gates every change must pass, and how a pull request is released. The
[design document](design.md) describes what is underneath.

## 1. Tools

| Tool | Needed for | Version |
|---|---|---|
| Go | Everything | 1.25 or later for the library; 1.26 or later for the benchmark harness |
| Git | The repository and the test data | Any recent version |
| Python 3 | The check of the Unicode tables against the Rust crate's, and the benchmark harness | 3.10 or later |
| Node.js | The test data for other versions of Unicode and for ECMAScript 2020, and the V8 rows of the harness | 24 (the V8 it carries is the oracle) |
| PowerShell 7 | The release scripts (`tools/release`) | 7.4 or later |

A contributor who only builds the library and runs its tests needs Go and Git.

- **Windows:** `winget install GoLang.Go Git.Git Python.Python.3.13 OpenJS.NodeJS.LTS Microsoft.PowerShell`.
- **Linux:** Go from [go.dev/dl](https://go.dev/dl) (unpack under your home directory and put its `bin` on `PATH`);
  Git and Python 3 from the distribution; Node.js 24 from nodejs.org or the NodeSource feed; PowerShell from
  Microsoft's feed or `snap install powershell --classic`.
- **macOS:** `brew install go git python node@24 powershell`.

`go version` must show 1.25 or later.

## 2. Getting the sources

```
git clone https://github.com/corvus-dotnet/corvus-regex-ecma262-go.git
git clone https://github.com/corvus-dotnet/ecma262-regex-test-data.git   # beside it: the test data
git clone https://github.com/corvus-dotnet/corvus-regex-ecma262-rs.git   # beside it: for the table check
cd corvus-regex-ecma262-go
```

The test data is the oracle for the engine (design, section 6). It is not vendored; the commands below assume it at
`../ecma262-regex-test-data`.

## 3. Building and testing, step by step

Every command runs from the repository root.

1. **Formatted and vetted.** `gofmt -l .` must print nothing, and `go vet ./...` must pass.
2. **The expanded templates.** After a change to `vm_template.go`, `regex_template.go` or `automata_template.go`, run
   `go generate ./...`; `go run ./internal/expand -check` must pass.
3. **The tests.** `go test ./...` runs the unit tests, the allocation test and the getting-started page's programs;
   `go test -race ./...` runs them under the race detector; and `CORVUS_REGEX_MEMO_FROM_THE_START=1 go test .` runs them
   with the memo of failed places taken into use from the first step.
4. **The stack probe.** `go run ./tools/stackprobe` asks the deep, wide and long patterns, each in a process of its
   own; none may end the program.
5. **The test data.** `go run ./tools/dataset ../ecma262-regex-test-data/data wrong-cases.tsv` must report 0 wrong, 0
   refused and 0 at a limit, and again with `CORVUS_REGEX_MEMO_FROM_THE_START=1`. For the other versions of Unicode and
   for ECMAScript 2020, write the data with its own scripts and run it with the engine set to match:

   ```
   node ../ecma262-regex-test-data/unicode.js apply 13.0 /tmp/u13 ../ecma262-regex-test-data/data
   go run ./tools/dataset /tmp/u13 wrong-u13.tsv --unicode 13
   node ../ecma262-regex-test-data/edition.js apply /tmp/es2020 "" ../ecma262-regex-test-data/data
   go run ./tools/dataset /tmp/es2020 wrong-es2020.tsv --edition es2020
   ```

## 4. The Unicode tables

The tables of each version of Unicode (`internal/ucd/tables<major>_gen.go`), the properties of strings
(`strings<major>_gen.go`) and the property names of ECMAScript 2020 (`es2020_gen.go`) are written from the Rust
crate's, so that every engine carries the same tables. After the crate's tables change:

```
src=../corvus-regex-ecma262-rs/corvus-regex-ecma262/src
for major in 13 17 18; do
  python3 tools/unicode/gen_tables.py $src/tables$major.rs
  python3 tools/unicode/gen_strings.py $src/strings$major.rs
done
python3 tools/unicode/gen_es2020.py $src/es2020.rs
```

With `--check`, each script fails when what is checked in differs, as CI runs them.

## 5. Benchmarks

`tools/benchmarks/run.sh <workloads.json> <texts directory> <out.json>` builds the harness (its own module, which takes
goja and so needs Go 1.26) and runs this library, `regexp`, goja and V8 over the Industry workloads; `JOB=short` gives a
quick look. The script's header lists its settings. The published figures are on
[the benchmark page](https://corvus-oss.org/corvus-regex-ecma262-benchmarks/), which runs this repository's harness
image each night beside every other engine; a run on another machine is comparable with itself, never with the page.

goja's compliance is worked out once (`GOJA_COMPLIANCE=1`) and checked in as
`tools/benchmarks/compliance-goja.json`, with the commit of the test data it was made at; CI fails when that commit is
not the one it checks out.

## 6. What a pull request needs

- **The gates of section 3** passing on the change as pushed: formatting, the templates, the tests with and without
  the race detector, the stack probe, and the test data at 0 wrong in every setting.
- **The case in the test data first** for a change to the engine's semantics: add the pattern to the data repository's
  family it belongs to, regenerate with its `generate.js`, check with `verify.js`, and pin the new data commit in
  `ci.yml`. V8 is the oracle; a fault V8 has is marked in the data, never copied into the engine.
- **A failing test first** for a bug: the test that reproduces it, then the fix.
- **A measured run** for a change made for speed, with the rows it moved.
- **The design document kept current**, describing the library as it is.
- **Doc comments** on every exported name, since the API reference on pkg.go.dev is generated from them.
- **The getting-started page** kept true: its programs are run by `go test`.

### Conventions

- Plain Go, formatted by `gofmt`, with no dependencies and no assembly.
- Nothing on the match path allocates once warm: the working memory comes from the pool, and the allocation test holds
  that line.
- A change to the matcher or the automata is made in the templates, never in the expanded copies.

## 7. Releases

Every pull request merged into main releases the module with the next patch version, unless it is labelled `no_release`:
`auto-release.yml` runs `tools/release` on the merge, which tags `v<version>` on a commit with the version written into
`Version`, as the Go module proxy serves it. A pull request that needs a minor or major version sets `nextVersion` in
`.github/release.json`. A change to the benchmark harness alone is a harness release, which publishes the harness image.
The documentation site takes the published documents (the getting-started page, the API reference, the design document
and this guide) from `main`, and they describe the latest release: a change to them alone is merged with `no_release`,
and a change to the API carries its documents, which its release brings into step.

`main` is protected by a ruleset: changes arrive by pull request, and the `CI gate` and `Harness image` checks must be
green on the branch as it will merge.
