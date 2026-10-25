module github.com/corvus-dotnet/corvus-regex-ecma262-go

// Go 1.25 or later, the same floor as the Corvus JSON Schema module for Go, which takes this one in place of its own
// engine. The module has no dependencies: the Unicode data it reads is its own (ucd/tables_gen.go), so it answers the
// same whatever toolchain builds it.
go 1.25
