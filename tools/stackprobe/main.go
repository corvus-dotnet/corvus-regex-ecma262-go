// Command stackprobe asks the engine about patterns built to overflow the stack: deep (groups, lookarounds, modifier
// groups, quantified groups and the classes of the v flag, each nested to the limit, one past it and far past it), wide
// (many groups, classes, alternatives, backreferences, set operations and string alternatives side by side) and long
// (the longest pattern allowed, and one past it), in every grammar, through every member over UTF-8 and UTF-16, as the
// Rust crate's stack-probe does.
//
//	go run ./tools/stackprobe [-stacks 1G,2M] [-jobs 4] [-filter text]
//
// A goroutine's stack grows as it needs to, up to the largest a program allows (debug.SetMaxStack, 1 GB on 64-bit
// machines by default), and a goroutine that needs more ends the program: no caller can recover. So each probe runs in
// a process of its own with each of the stacks as that largest. A probe is answered (compiled and matched), refused
// (not valid, or too large, which a caller can catch), or a fault: the process died or made no answer within two
// minutes. Any fault makes the exit status 1.
//
// The stacks are 1 GB, the largest a Go program allows unless it lowers it, and 2 MB, the least at which the deepest
// pattern the engine reads (MaxNesting groups) is answered or refused. The Rust crate and the Java engine read a deep
// pattern on a thread of their own with a large stack, so that a caller's small one is enough; a goroutine's stack
// grows, so the engine needs no thread of its own, and a program that lowers the largest below 2 MB with
// debug.SetMaxStack can have a deep pattern end it.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

// probe is a pattern and the text it is matched against, built only by the process that asks about it.
type probe struct {
	name  string
	flags string
	build func() (string, string)
}

func nest(open, body, close string, depth int) string {
	return strings.Repeat(open, depth) + body + strings.Repeat(close, depth)
}

func text(n int) string { return strings.Repeat("a", min(max(n, 64), 1<<20)) + "b" }

func probes() []probe {
	var all []probe
	add := func(name, flags string, build func() (string, string)) {
		all = append(all, probe{name, flags, build})
	}
	depths := []int{32, 33, 200, 1000, 1001, 5000, 200_000}
	grammars := []string{"", "u", "v", "i", "iv"}

	// Deep: each kind of group nested.
	groups := [][3]string{
		{"group", "(", ")"},
		{"non-capturing", "(?:", ")"},
		{"lookahead", "(?=", ")"},
		{"negative lookahead", "(?!", ")"},
		{"lookbehind", "(?<=", ")"},
		{"negative lookbehind", "(?<!", ")"},
		{"modifier i", "(?i:", ")"},
		{"modifier -i", "(?-i:", ")"},
		{"star", "(?:", ")*"},
		{"counted", "(?:", "){2}"},
		{"lazy plus", "(?:", ")+?"},
		{"alternative", "(?:b|", ")"},
	}
	for _, g := range groups {
		for _, depth := range depths {
			for _, flags := range grammars {
				add(fmt.Sprintf("deep %s x%d /%s", g[0], depth, flags), flags,
					func() (string, string) { return nest(g[1], "a", g[2], depth), text(depth) })
			}
		}
	}
	for _, depth := range depths {
		for _, flags := range grammars {
			add(fmt.Sprintf("deep named x%d /%s", depth, flags), flags, func() (string, string) {
				var named strings.Builder
				for i := range depth {
					fmt.Fprintf(&named, "(?<g%d>", i)
				}
				return named.String() + "a" + strings.Repeat(")", depth), text(depth)
			})
			add(fmt.Sprintf("deep backreference x%d /%s", depth, flags), flags,
				func() (string, string) { return nest("(", "a", `)\1`, depth), text(depth) })
		}
	}

	// Deep: the classes of the v flag nested, asked of every grammar (the others refuse them as not valid).
	classes := [][4]string{
		{"class", "[", "a", "]"},
		{"negated class", "[^", "a", "]"},
		{"difference", "[a--", "[b]", "]"},
		{"intersection", "[a&&", "[a]", "]"},
		{"string alternatives", `[\q{ab|c}`, "a", "]"},
		{"class in a group", "(?:[", "a", "])"},
	}
	for _, c := range classes {
		for _, depth := range depths {
			for _, flags := range grammars {
				add(fmt.Sprintf("deep %s x%d /%s", c[0], depth, flags), flags,
					func() (string, string) { return nest(c[1], c[2], c[3], depth), text(depth) })
			}
		}
	}
	for _, depth := range depths {
		add(fmt.Sprintf("deep [ unclosed x%d /v", depth), "v",
			func() (string, string) { return strings.Repeat("[", depth), text(64) })
		add(fmt.Sprintf("deep ( unclosed x%d /v", depth), "v",
			func() (string, string) { return strings.Repeat("(", depth), text(64) })
	}

	// Wide: many of one thing side by side.
	named := func(w int) string {
		var b strings.Builder
		for i := range w {
			fmt.Fprintf(&b, "(?<g%d>a)", i)
		}
		return b.String()
	}
	wide := []struct {
		kind    string
		pattern func(int) string
	}{
		{"alternatives", func(w int) string { return strings.Repeat("a|", w) + "b" }},
		{"empty groups", func(w int) string { return strings.Repeat("()", w) }},
		{"groups", func(w int) string { return strings.Repeat("(a)", w) }},
		{"named groups", named},
		{"classes", func(w int) string { return strings.Repeat("[a]", w) }},
		{"lookaheads", func(w int) string { return strings.Repeat("(?=a)", w) + "a" }},
		{"backreferences", func(w int) string { return "(a)" + strings.Repeat(`\1`, w) }},
		{"optional", func(w int) string { return strings.Repeat("a?", w) }},
		{"optional groups", func(w int) string { return strings.Repeat("(?:a?)", w) }},
		{"ranges in a class", func(w int) string { return "[" + strings.Repeat("a-b", w) + "]" }},
		{"properties", func(w int) string { return strings.Repeat(`\p{L}`, w) }},
		{"stars", func(w int) string { return strings.Repeat("a*", w) }},
		{"alternatives in a star", func(w int) string { return "(?:" + strings.Repeat("a|", w) + "b)*" }},
		{"differences", func(w int) string { return "[a" + strings.Repeat("--b", w) + "]" }},
		{"intersections", func(w int) string { return "[a" + strings.Repeat("&&a", w) + "]" }},
		{"string alternatives", func(w int) string { return `[\q{` + strings.Repeat("a|", w) + "b}]" }},
	}
	for _, width := range []int{1000, 100_000, 300_000} {
		for _, w := range wide {
			for _, flags := range grammars {
				add(fmt.Sprintf("wide %s x%d /%s", w.kind, width, flags), flags,
					func() (string, string) { return w.pattern(width), text(width) })
			}
		}
	}

	// Long: the longest pattern allowed, and one past it.
	for _, length := range []int{ecmaregex.MaxPatternLength, ecmaregex.MaxPatternLength + 1} {
		for _, flags := range grammars {
			add(fmt.Sprintf("long literal x%d /%s", length, flags), flags,
				func() (string, string) { return strings.Repeat("a", length), text(length) })
		}
	}
	return all
}

// ask asks the engine everything about one probe; the answer is a line for the parent.
func ask(p probe) string {
	pattern, s := p.build()
	f, err := ecmaregex.ParseFlags(p.flags)
	if err != nil {
		return "FAULT the flags " + p.flags
	}
	re, err := ecmaregex.CompileFlags(pattern, f, 5*time.Second)
	var e *ecmaregex.Error
	valid := ecmaregex.IsValid(pattern, p.flags)
	switch {
	case err != nil && errors.As(err, &e) && e.Kind == ecmaregex.ErrTooLarge:
		if valid {
			return "FAULT IsValid accepted a pattern too large to compile"
		}
		return "refused too large"
	case err != nil && valid:
		return "FAULT IsValid accepted a pattern Compile refused: " + err.Error()
	case err != nil:
		return "refused not valid"
	case !valid:
		return "FAULT IsValid refused a pattern Compile accepted"
	}
	u := utf16.Encode([]rune(s))
	var answers []string
	add := func(v any, err error) {
		if err != nil {
			answers = append(answers, err.Error())
		} else {
			answers = append(answers, fmt.Sprint(v))
		}
	}
	add(re.TryMatchString(s))
	add(re.TryMatchUTF16(u))
	add(re.TryFindStringIndex(s))
	add(re.TryFindStringSubmatchIndex(s))
	add(re.TryFindUTF16SubmatchIndex(u, 0))
	add(re.TryFindAllStringIndex(s, 1000))
	add(re.TryFindAllUTF16Index(u, 1000))
	add(re.TryFindAllStringSubmatchIndex(s, 100))
	c := re.NewCaptures()
	add(re.TryCapturesString(c, s, 0))
	out, err := re.TryReplaceAllString(s, "$1x")
	add(len(out), err)
	out16, err := re.TryReplaceAllUTF16(u, []uint16{'x'})
	add(len(out16), err)
	pieces, err := re.TrySplit(s, -1)
	add(len(pieces), err)
	pieces16, err := re.TrySplitUTF16(u, -1)
	add(len(pieces16), err)
	return "answered " + strings.Join(answers, " ")
}

func size(s string) int {
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		panic("a stack size such as 256K or 8M")
	}
	switch s[len(s)-1] {
	case 'K':
		return n << 10
	case 'M':
		return n << 20
	case 'G':
		return n << 30
	}
	panic("a stack size such as 256K or 8M")
}

func main() {
	if len(os.Args) == 4 && os.Args[1] == "-child" {
		index, _ := strconv.Atoi(os.Args[2])
		stack, _ := strconv.Atoi(os.Args[3])
		debug.SetMaxStack(stack)
		fmt.Println(ask(probes()[index]))
		return
	}
	stacks := flag.String("stacks", "1G,2M", "the largest stacks to run each probe with")
	jobs := flag.Int("jobs", 4, "the probes run at once")
	filter := flag.String("filter", "", "only the probes whose names hold this")
	flag.Parse()
	names := strings.Split(*stacks, ",")
	all := probes()
	type item struct{ i, s int }
	var work []item
	for i, p := range all {
		if strings.Contains(p.name, *filter) {
			for s := range names {
				work = append(work, item{i, s})
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	var next atomic.Int64
	results := make([]string, len(work))
	started := time.Now()
	var wg sync.WaitGroup
	for range *jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n := int(next.Add(1) - 1)
				if n >= len(work) {
					return
				}
				w := work[n]
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				cmd := exec.CommandContext(ctx, exe, "-child", strconv.Itoa(w.i), strconv.Itoa(size(names[w.s])))
				var out bytes.Buffer
				cmd.Stdout = &out
				err := cmd.Run()
				switch {
				case ctx.Err() != nil:
					results[n] = "FAULT no answer in two minutes"
				case err != nil:
					results[n] = "FAULT died: " + err.Error()
				default:
					results[n] = strings.TrimSpace(out.String())
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	answered, refused, faults := 0, 0, 0
	var lines []string
	for n, r := range results {
		switch {
		case strings.HasPrefix(r, "answered"):
			answered++
		case strings.HasPrefix(r, "refused"):
			refused++
		default:
			faults++
			lines = append(lines, fmt.Sprintf("%s on %s: %s", all[work[n].i].name, names[work[n].s], r))
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
	fmt.Printf("%d probes on %d stacks: %d answered, %d refused, %d faults, in %.0f s\n", len(work)/len(names),
		len(names), answered, refused, faults, time.Since(started).Seconds())
	if faults > 0 {
		os.Exit(1)
	}
}
