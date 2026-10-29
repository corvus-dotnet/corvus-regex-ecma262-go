package ecmaregex

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestGettingStarted runs each program of the getting-started page the documentation site publishes
// (docs/getting-started.md) against this module, so that the page shows only code that works.
func TestGettingStarted(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program for each block")
	}
	page, err := os.ReadFile(filepath.Join("docs", "getting-started.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?ms)^```go\n(.*?)^```$").FindAllSubmatch(page, -1)
	if len(blocks) < 5 {
		t.Fatalf("the page has %d programs", len(blocks))
	}
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for n, block := range blocks {
		// A block that is not a whole program, such as the import of Setup, is not run.
		if !strings.HasPrefix(string(block[1]), "package main\n") {
			continue
		}
		t.Run("block-"+strconv.Itoa(n+1), func(t *testing.T) {
			dir := t.TempDir()
			mod := "module example\n\ngo 1.25\n\nrequire github.com/corvus-dotnet/corvus-regex-ecma262-go v0.0.0\n\n" +
				"replace github.com/corvus-dotnet/corvus-regex-ecma262-go => " + root + "\n"
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "main.go"), block[1], 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "run", ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}
