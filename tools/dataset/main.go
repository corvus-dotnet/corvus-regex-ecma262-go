// Command dataset runs the engine over the ECMA-262 regular expression test data
// (github.com/corvus-dotnet/ecma262-regex-test-data), as the runner of the Rust crate does (tools/src/bin/dataset.rs
// there), with the same judging rules.
//
//	go run ./tools/dataset <data directory> <wrong-cases output file> [file filter] [utf16|utf8|both] [--compliance <file>]
//
// Each record is asked about under each grammar V8 found it valid in: the record's flags with u added (the unicode
// answers), the record's flags alone (the legacy answers) and, in the file that has them, the flags with v added (the
// unicode_sets answers). A text is matched as UTF-16 and as UTF-8; a text with a lone surrogate has no UTF-8 form and
// is matched as UTF-16 alone. Records marked v8_deviation are left out. A pattern is UTF-8, so a lone surrogate in one
// is read as U+FFFD, as the Rust runner reads it. captures.jsonl is judged on exec, the global search, the sticky flag,
// replace and split. ill-formed-utf8.jsonl holds bytes that are not UTF-8, each one answer over UTF-8 under each
// grammar the pattern is valid in, whose right answer is a refusal: the error of kind ErrEncoding.
//
// exec/test262.jsonl, the exec calls of test262 (in a directory of its own), is judged last: each call one exec with
// the record's flags from its lastIndex, right when the positions of the match and of every group, the named groups
// and, with g or y, the lastIndex after are V8's; one answer over UTF-16 and, when the text holds no lone surrogate and
// lastIndex is not between the halves of a pair, one over UTF-8 with positions in bytes.
//
// With --compliance <file>, the engine's compliance object is written to the file, as the benchmark page reads it
// (docs/implementing-a-source.md in corvus-regex-ecma262-benchmarks): the counts, this runner and the calls it makes
// in each encoding, and what it leaves out, which for this library is nothing.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

const limit = 2 * time.Second

var replacement = "<$&|$1|$2|$10|$<a>|$$|$`|$'>"

// exec is the file of the exec calls of test262, in a directory of its own.
const exec = "exec/test262.jsonl"

type counts struct {
	right, wrong, refused, accepted, limited, errors atomic.Int64
}

func (c *counts) add(o *counts) {
	c.right.Add(o.right.Load())
	c.wrong.Add(o.wrong.Load())
	c.refused.Add(o.refused.Load())
	c.accepted.Add(o.accepted.Load())
	c.limited.Add(o.limited.Load())
	c.errors.Add(o.errors.Load())
}

var asUTF16, asUTF8 bool

func main() {
	args := os.Args[1:]
	compliance := ""
	if at := slices.Index(args, "--compliance"); at >= 0 {
		compliance = args[at+1]
		args = slices.Delete(args, at, at+2)
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dataset <data directory> <wrong-cases output file> [file filter] [utf16|utf8|both] [--compliance <file>]")
		os.Exit(2)
	}
	directory, wrongPath := args[0], args[1]
	filter, encodings := "*.jsonl", "both"
	if len(args) > 2 {
		filter = args[2]
	}
	if len(args) > 3 {
		encodings = args[3]
	}
	asUTF16 = encodings == "both" || encodings == "utf16"
	asUTF8 = encodings == "both" || encodings == "utf8"
	entries, err := os.ReadDir(directory)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var files []string
	for _, e := range entries {
		if ok, _ := filepath.Match(filter, e.Name()); ok && strings.HasSuffix(e.Name(), ".jsonl") && !e.IsDir() {
			files = append(files, e.Name())
		}
	}
	slices.Sort(files)
	// The exec calls of test262: with the default filter, or one that names them.
	if filter == "*.jsonl" || strings.HasPrefix(filter, "exec") {
		if st, err := os.Stat(filepath.Join(directory, exec)); err == nil && !st.IsDir() {
			files = append(files, exec)
		}
	}
	var total counts
	var wrongCases []string
	var mu sync.Mutex
	var records, skipped atomic.Int64
	clock := time.Now()
	threads := 4
	if t, err := strconv.Atoi(os.Getenv("THREADS")); err == nil && t > 0 {
		threads = t
	}
	for _, name := range files {
		var c counts
		lines, err := readLines(filepath.Join(directory, name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		var next atomic.Int64
		var wg sync.WaitGroup
		for range threads {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var wrong []string
				for {
					k := int(next.Add(1) - 1)
					if k >= len(lines) {
						break
					}
					r, err := parseJSON(lines[k])
					if err != nil {
						panic(fmt.Sprintf("%s:%d: %v", name, k+1, err))
					}
					if get(r, "v8_deviation") == true {
						skipped.Add(1)
						continue
					}
					records.Add(1)
					judgeRecord(name, r, &c, &wrong)
				}
				mu.Lock()
				wrongCases = append(wrongCases, wrong...)
				mu.Unlock()
			}()
		}
		wg.Wait()
		fmt.Printf("%-24s right %10d  wrong %6d  refused-valid %4d  accepted-invalid %4d  limit %4d  %6.1fs\n", name,
			c.right.Load(), c.wrong.Load(), c.refused.Load(), c.accepted.Load(), c.limited.Load(),
			time.Since(clock).Seconds())
		total.add(&c)
	}
	slices.Sort(wrongCases)
	out := strings.Join(wrongCases, "\n")
	if len(wrongCases) > 0 {
		out += "\n"
	}
	if err := os.WriteFile(wrongPath, []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if compliance != "" {
		writeCompliance(compliance, &total)
	}
	fmt.Println()
	fmt.Printf("records %d (skipped %d deviations)  right %d  wrong %d  refused-valid %d  accepted-invalid %d  limit %d  errors %d  in %.1fs\n",
		records.Load(), skipped.Load(), total.right.Load(), total.wrong.Load(), total.refused.Load(),
		total.accepted.Load(), total.limited.Load(), total.errors.Load(), time.Since(clock).Seconds())
	if total.wrong.Load()+total.refused.Load()+total.accepted.Load()+total.errors.Load()+total.limited.Load() != 0 {
		os.Exit(1)
	}
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<30)
	for s.Scan() {
		if line := s.Text(); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, s.Err()
}

// writeCompliance writes the engine's compliance as the benchmark page reads it: the counts, this runner and the
// calls it makes in each encoding it asked in, and what it leaves out.
func writeCompliance(path string, t *counts) {
	var calls []string
	if asUTF16 {
		calls = append(calls, `"utf16":["Regexp.TryMatchUTF16","Regexp.TryFindUTF16SubmatchIndex","Regexp.TryFindAllUTF16SubmatchIndex",`+
			`"Regexp.TryFindAllUTF16Index","Regexp.TryReplaceUTF16","Regexp.TrySplitUTF16"]`)
	}
	if asUTF8 {
		calls = append(calls, `"utf8":["Regexp.TryMatchString","Regexp.TryFindStringSubmatchIndexAt","Regexp.TryFindAllStringIndex",`+
			`"Regexp.TryReplaceString"]`)
	}
	json := fmt.Sprintf(`{"right":%d,"wrong":%d,"refused":%d,"acceptedInvalid":%d,"limited":%d,`+
		`"judge":{"runner":"https://github.com/corvus-dotnet/corvus-regex-ecma262-go/blob/main/tools/dataset/main.go",`+
		`"calls":{%s}},"excludes":[]}`+"\n", t.right.Load(), t.wrong.Load(), t.refused.Load(), t.accepted.Load(),
		t.limited.Load()+t.errors.Load(), strings.Join(calls, ","))
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// Units of text.

func hasLoneSurrogate(s []uint16) bool {
	for k := 0; k < len(s); k++ {
		u := s[k]
		if 0xD800 <= u && u <= 0xDBFF {
			if k+1 < len(s) && 0xDC00 <= s[k+1] && s[k+1] <= 0xDFFF {
				k++
				continue
			}
			return true
		}
		if 0xDC00 <= u && u <= 0xDFFF {
			return true
		}
	}
	return false
}

// lossy returns the UTF-8 of UTF-16 units, each lone surrogate written as U+FFFD.
func lossy(s []uint16) string { return string(utf16.Decode(s)) }

func escape(units []uint16) string {
	var b strings.Builder
	for _, u := range units {
		if u >= 0x20 && u < 0x7f && u != '\\' {
			b.WriteByte(byte(u))
		} else {
			fmt.Fprintf(&b, "\\u%04x", u)
		}
	}
	return b.String()
}

func str(v any) []uint16 {
	s, _ := v.([]uint16)
	return s
}

func get(o any, name string) any {
	if m, ok := o.(map[string]any); ok {
		return m[name]
	}
	return nil
}

func array(o any) []any {
	a, _ := o.([]any)
	return a
}

type mode struct {
	add, answers string
	valid        any
}

// modes returns the modes a record is asked about: the flags to add, the name of the answers, and whether V8 found it
// valid.
func modes(r any) []mode {
	valid := get(r, "valid")
	ms := []mode{{"u", "unicode", get(valid, "unicode")}, {"", "legacy", get(valid, "legacy")}}
	if v := get(valid, "unicode_sets"); v != nil {
		ms = append(ms, mode{"v", "unicode_sets", v})
	}
	return ms
}

func firstLine(s string) string {
	if k := strings.IndexByte(s, '\n'); k >= 0 {
		return s[:k]
	}
	return s
}

func compile(pattern, flags string) (*ecmaregex.Regexp, error) {
	f, err := ecmaregex.ParseFlags(flags)
	if err != nil {
		return nil, err
	}
	return ecmaregex.CompileFlags(pattern, f, limit)
}

// limitKind says which limit a match reached, or "" when the error is not a limit.
func limitKind(err error) string {
	switch {
	case errors.Is(err, ecmaregex.ErrTime):
		return "time"
	case errors.Is(err, ecmaregex.ErrMemory):
		return "memory"
	}
	return ""
}

func judgeRecord(name string, r any, c *counts, wrong *[]string) {
	pattern := lossy(str(get(r, "pattern")))
	switch name {
	case exec:
		judgeExec(r, pattern, c, wrong)
	case "captures.jsonl":
		if asUTF16 {
			judgeCaptures(r, pattern, c, wrong)
		}
	case "ill-formed-utf8.jsonl":
		if asUTF8 {
			judgeIllFormed(r, pattern, c, wrong)
		}
	default:
		judgeTests(name, r, pattern, c, wrong)
	}
}

func judgeIllFormed(r any, pattern string, c *counts, wrong *[]string) {
	name := "ill-formed-utf8.jsonl"
	flags := lossy(str(get(r, "flags")))
	shown := escape(utf16.Encode([]rune(pattern)))
	for _, m := range modes(r) {
		modeFlags := flags + m.add
		expectedValid := m.valid == true
		head := name + "\t/" + shown + "/" + modeFlags
		re, err := compile(pattern, modeFlags)
		if err != nil {
			if expectedValid {
				c.refused.Add(1)
				*wrong = append(*wrong, "REFUSED\t"+head+"\t"+firstLine(err.Error()))
			}
			continue
		}
		if !expectedValid {
			c.accepted.Add(1)
			*wrong = append(*wrong, "ACCEPTED-INVALID\t"+head)
			continue
		}
		for _, t := range array(get(r, "tests")) {
			hex := lossy(str(get(t, "bytes")))
			bytes := make([]byte, len(hex)/2)
			for k := range bytes {
				v, _ := strconv.ParseUint(hex[2*k:2*k+2], 16, 8)
				bytes[k] = byte(v)
			}
			got, err := re.TryMatchString(string(bytes))
			var e *ecmaregex.Error
			switch {
			case err == nil:
				c.wrong.Add(1)
				*wrong = append(*wrong, fmt.Sprintf("WRONG\t%s\t%s\tutf8\tread as UTF-8 (%v), not refused", head, hex, got))
			case limitKind(err) != "":
				c.limited.Add(1)
				*wrong = append(*wrong, "LIMIT\t"+head+"\t"+hex+"\tutf8")
			case errors.As(err, &e) && e.Kind == ecmaregex.ErrEncoding:
				c.right.Add(1)
			default:
				c.errors.Add(1)
				*wrong = append(*wrong, "ERROR\t"+head+"\t"+hex+"\tutf8\t"+err.Error())
			}
		}
	}
}

func judgeTests(name string, r any, pattern string, c *counts, wrong *[]string) {
	flags := lossy(str(get(r, "flags")))
	shown := escape(utf16.Encode([]rune(pattern)))
	for _, m := range modes(r) {
		modeFlags := flags + m.add
		expectedValid := m.valid == true
		re, err := compile(pattern, modeFlags)
		if err != nil {
			var e *ecmaregex.Error
			message := "ERROR: " + err.Error()
			if errors.As(err, &e) {
				switch e.Kind {
				case ecmaregex.ErrInvalid:
					message = "PARSE: " + err.Error()
				case ecmaregex.ErrTooLarge:
					message = "TOO-LARGE: " + err.Error()
				default:
					c.errors.Add(1)
				}
			}
			if expectedValid {
				c.refused.Add(1)
				*wrong = append(*wrong, "REFUSED\t"+name+"\t/"+shown+"/"+modeFlags+"\t"+firstLine(message))
			}
			continue
		}
		if !expectedValid {
			c.accepted.Add(1)
			*wrong = append(*wrong, "ACCEPTED-INVALID\t"+name+"\t/"+shown+"/"+modeFlags)
			continue
		}
		for _, t := range array(get(r, "tests")) {
			expected, ok := get(t, m.answers).(bool)
			if !ok {
				continue
			}
			text := str(get(t, "text"))
			if asUTF16 {
				got, err := re.TryMatchUTF16(text)
				check(name, shown, modeFlags, text, "utf16", expected, got, err, c, wrong)
			}
			if asUTF8 && !hasLoneSurrogate(text) {
				got, err := re.TryMatchString(lossy(text))
				check(name, shown, modeFlags, text, "utf8", expected, got, err, c, wrong)
			}
		}
	}
}

func check(name, shown, modeFlags string, text []uint16, encoding string, expected, got bool, err error, c *counts,
	wrong *[]string) {
	where := name + "\t/" + shown + "/" + modeFlags + "\t" + escape(text) + "\t" + encoding
	switch {
	case err == nil && got == expected:
		c.right.Add(1)
	case err == nil:
		c.wrong.Add(1)
		*wrong = append(*wrong, fmt.Sprintf("WRONG\t%s\texpected %v", where, expected))
	case limitKind(err) != "":
		c.limited.Add(1)
		*wrong = append(*wrong, "LIMIT\t"+where+"\t"+limitKind(err))
	default:
		c.errors.Add(1)
		*wrong = append(*wrong, "ERROR\t"+where+"\t"+err.Error())
	}
}

// exec/test262.jsonl

// lastIndexOf returns a call's lastIndex. Past the end of the text exec gives null; test262 sets it as far as 2^53 - 1
// and Number.MAX_VALUE, so anything past the end reads as the text's length plus one.
func lastIndexOf(t any, length int) int {
	v := get(t, "lastIndex").(float64)
	if v > float64(length) {
		return length + 1
	}
	return int(v)
}

// insidePair reports whether a position is between the halves of a surrogate pair, where UTF-8 has no position.
func insidePair(s []uint16, at int) bool {
	return at > 0 && at < len(s) && 0xDC00 <= s[at] && s[at] <= 0xDFFF && 0xD800 <= s[at-1] && s[at-1] <= 0xDBFF
}

// sameSpan compares a span of the answer (null, or [start, end]) with group g of the engine's slots (-1 for no
// group).
func sameSpan(e any, slots []int, g int, at func(int) int) bool {
	b := array(e)
	took := g >= 0 && slots[2*g] >= 0
	if b == nil {
		return !took
	}
	return took && at(int(b[0].(float64))) == slots[2*g] && at(int(b[1].(float64))) == slots[2*g+1]
}

// named returns the group with the name that took part in the match, or -1.
func named(re *ecmaregex.Regexp, slots []int, name string) int {
	for _, g := range re.SubexpIndex(name) {
		if slots[2*g] >= 0 {
			return g
		}
	}
	return -1
}

// sameExec reports whether an engine's match (nil for none) is the call's answer: both null, or the same groups, the
// same named groups (present exactly when the pattern has names) and, with g or y, a lastIndex after at the match's
// end.
func sameExec(answer any, re *ecmaregex.Regexp, slots []int, names []string, carries bool, at func(int) int) bool {
	if answer == nil || slots == nil {
		return answer == nil && slots == nil
	}
	captures := array(get(answer, "captures"))
	if captures == nil || len(captures) != len(slots)/2 {
		return false
	}
	for g := range captures {
		if !sameSpan(captures[g], slots, g, at) {
			return false
		}
	}
	if groups, ok := get(answer, "groups").(map[string]any); !ok {
		if len(names) != 0 {
			return false
		}
	} else {
		if len(groups) != len(names) {
			return false
		}
		for name, v := range groups {
			if !slices.Contains(names, name) || !sameSpan(v, slots, named(re, slots, name), at) {
				return false
			}
		}
	}
	return !carries || at(int(get(answer, "lastIndex").(float64))) == slots[1]
}

func countExec(c *counts, wrong *[]string, where, encoding string, ok bool, err error) {
	switch {
	case err != nil:
		c.limited.Add(1)
		*wrong = append(*wrong, "LIMIT\t"+exec+"\t"+where+"\t"+encoding)
	case ok:
		c.right.Add(1)
	default:
		c.wrong.Add(1)
		*wrong = append(*wrong, "WRONG\t"+exec+"\t"+where+"\t"+encoding)
	}
}

// groupNames returns the distinct names of the pattern's groups.
func groupNames(re *ecmaregex.Regexp) []string {
	var names []string
	for _, n := range re.SubexpNames() {
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// judgeExec judges a record of exec/test262.jsonl: the pattern with its own flags (g, y and d among them), from each
// lastIndex.
func judgeExec(r any, pattern string, c *counts, wrong *[]string) {
	flags := lossy(str(get(r, "flags")))
	shown := escape(utf16.Encode([]rune(pattern)))
	re, err := compile(pattern, flags)
	if err != nil {
		c.refused.Add(1)
		*wrong = append(*wrong, "REFUSED\t"+exec+"\t/"+shown+"/"+flags+"\t"+firstLine(err.Error()))
		return
	}
	carries := strings.ContainsAny(flags, "gy")
	names := groupNames(re)
	for _, t := range array(get(r, "tests")) {
		if get(t, "v8_deviation") == true {
			continue
		}
		text := str(get(t, "text"))
		start := 0
		if carries {
			start = lastIndexOf(t, len(text))
		}
		answer := get(t, "answer")
		where := fmt.Sprintf("/%s/%s\t%s\t%v", shown, flags, escape(text), get(t, "lastIndex"))
		if asUTF16 {
			var slots []int
			var err error
			if start <= len(text) {
				slots, err = re.TryFindUTF16SubmatchIndex(text, start)
			}
			countExec(c, wrong, where, "utf16", err == nil && sameExec(answer, re, slots, names, carries, identity), err)
		}
		if asUTF8 && !hasLoneSurrogate(text) && !insidePair(text, start) {
			byteAt := byteOffsets(text)
			var slots []int
			var err error
			if start <= len(text) {
				slots, err = re.TryFindStringSubmatchIndexAt(lossy(text), byteAt[start])
			}
			toByte := func(k int) int { return byteAt[k] }
			countExec(c, wrong, where, "utf8", err == nil && sameExec(answer, re, slots, names, carries, toByte), err)
		}
	}
}

// captures.jsonl

func identity(k int) int { return k }

// execOf returns the groups of an exec answer: nil for no match, else the bounds of each group, nil for one not taking
// part.
func execOf(e any) [][]int {
	groups := array(e)
	if groups == nil {
		return nil
	}
	out := make([][]int, len(groups))
	for k, g := range groups {
		if b := array(g); b != nil {
			out[k] = []int{int(b[0].(float64)), int(b[1].(float64))}
		}
	}
	return out
}

// byteOffsets returns, for each UTF-16 index of a text (and the index past its end), the byte offset in UTF-8.
func byteOffsets(s []uint16) []int {
	at := make([]int, len(s)+1)
	b := 0
	for k := 0; k < len(s); {
		at[k] = b
		c := s[k]
		if 0xD800 <= c && c <= 0xDBFF && k+1 < len(s) && 0xDC00 <= s[k+1] && s[k+1] <= 0xDFFF {
			at[k+1] = b + 2
			b += 4
			k += 2
			continue
		}
		switch {
		case c < 0x80:
			b++
		case c < 0x800:
			b += 2
		default:
			b += 3
		}
		k++
	}
	at[len(s)] = b
	return at
}

// groupsOf returns the bounds of each group of slots, nil for one not taking part, or nil for no match.
func groupsOf(slots []int) [][]int {
	if slots == nil {
		return nil
	}
	out := make([][]int, len(slots)/2)
	for g := range out {
		if slots[2*g] >= 0 {
			out[g] = []int{slots[2*g], slots[2*g+1]}
		}
	}
	return out
}

func sameSlots(expected, got [][]int, at func(int) int) bool {
	if expected == nil || got == nil {
		return expected == nil && got == nil
	}
	if len(expected) != len(got) {
		return false
	}
	for k := range expected {
		a, b := expected[k], got[k]
		if a == nil || b == nil {
			if (a == nil) != (b == nil) {
				return false
			}
		} else if at(a[0]) != b[0] || at(a[1]) != b[1] {
			return false
		}
	}
	return true
}

// sameAll compares a global search with the data's, which keeps the first twenty matches only: found has at most 21,
// and one more than the data has is right only when the data kept twenty.
func sameAll(all [][][]int, found [][]int, at func(int) int) bool {
	for n, slots := range found {
		if n >= len(all) {
			return len(all) == 20
		}
		if !sameSlots(all[n], groupsOf(slots), at) {
			return false
		}
	}
	return !((len(all) < 20 && len(found) != len(all)) || (len(all) == 20 && len(found) < 20))
}

func sameSplit16(expected any, got []ecmaregex.Piece16) bool {
	e := array(expected)
	if len(e) != len(got) {
		return false
	}
	for k := range e {
		if e[k] == nil {
			if got[k].Defined {
				return false
			}
		} else if !got[k].Defined || !slices.Equal(str(e[k]), got[k].Text) {
			return false
		}
	}
	return true
}

type regexps struct {
	pattern string
	cache   map[string]*ecmaregex.Regexp
}

func (r *regexps) make(flags string) *ecmaregex.Regexp {
	if re, ok := r.cache[flags]; ok {
		return re
	}
	re, _ := compile(r.pattern, flags)
	r.cache[flags] = re
	return re
}

func judgeCaptures(r any, pattern string, c *counts, wrong *[]string) {
	flags := lossy(str(get(r, "flags")))
	valid := get(r, "valid")
	shown := escape(utf16.Encode([]rune(pattern)))
	tests := array(get(r, "tests"))
	cache := &regexps{pattern, map[string]*ecmaregex.Regexp{}}
	for _, t := range tests {
		if get(t, "v8_deviation") == true {
			continue
		}
		text := str(get(t, "text"))
		for _, m := range []string{"unicode", "legacy"} {
			if get(valid, m) != true {
				continue
			}
			a := get(t, m)
			if a == nil {
				continue
			}
			modeFlags := flags
			if m == "unicode" {
				modeFlags += "u"
			}
			where := "/" + shown + "/" + modeFlags + "\t" + escape(text)
			re, g, y := cache.make(modeFlags), cache.make(modeFlags+"g"), cache.make(modeFlags+"y")
			if re == nil || g == nil || y == nil {
				c.refused.Add(1)
				*wrong = append(*wrong, "REFUSED\tcaptures.jsonl\t"+where)
				continue
			}
			lone := hasLoneSurrogate(text)
			var u8 string
			var byteAt []int
			if !lone {
				u8 = lossy(text)
				byteAt = byteOffsets(text)
			}
			toByte := func(k int) int { return byteAt[k] }
			v := func(what string, ok bool, err error) {
				switch {
				case err != nil:
					c.limited.Add(1)
					*wrong = append(*wrong, "LIMIT\tcaptures.jsonl\t"+where+"\t"+what)
				case ok:
					c.right.Add(1)
				default:
					c.wrong.Add(1)
					*wrong = append(*wrong, "WRONG\tcaptures.jsonl\t"+where+"\t"+what)
				}
			}

			// exec
			exec := execOf(get(a, "exec"))
			got, err := re.TryFindUTF16SubmatchIndex(text, 0)
			if err != nil {
				c.limited.Add(1)
				*wrong = append(*wrong, "LIMIT\tcaptures.jsonl\t"+where)
				continue
			}
			v("exec", sameSlots(exec, groupsOf(got), identity), nil)
			if !lone {
				got, err := re.TryFindStringSubmatchIndex(u8)
				v("exec-utf8", sameSlots(exec, groupsOf(got), toByte), err)
			}

			// all
			var all [][][]int
			for _, e := range array(get(a, "all")) {
				all = append(all, execOf(e))
			}
			found, err := g.TryFindAllUTF16SubmatchIndex(text, 21)
			v("all", sameAll(all, found, identity), err)
			if !lone {
				found, err := g.TryFindAllStringIndex(u8, 21)
				v("all-utf8", sameAllBounds(all, found, toByte), err)
			}

			// sticky at 1
			if len(text) > 0 {
				sticky := execOf(get(a, "sticky"))
				got, err := y.TryFindUTF16SubmatchIndex(text, 1)
				v("sticky", sameSlots(sticky, groupsOf(got), identity), err)
				if !lone && !(0xD800 <= text[0] && text[0] <= 0xDBFF) {
					got, err := y.TryFindStringSubmatchIndexAt(u8, byteAt[1])
					v("sticky-utf8", sameSlots(sticky, groupsOf(got), toByte), err)
				}
			}

			// replace
			rep16 := utf16.Encode([]rune(replacement))
			replace, replaceAll := str(get(a, "replace")), str(get(a, "replaceAll"))
			out, err := re.TryReplaceUTF16(text, rep16)
			v("replace", slices.Equal(out, replace), err)
			out, err = g.TryReplaceUTF16(text, rep16)
			v("replaceAll", slices.Equal(out, replaceAll), err)
			if !lone {
				// Over UTF-8 a half character is written as U+FFFD, which is what a lossy conversion of V8's answer
				// writes for it.
				s, err := re.TryReplaceString(u8, replacement)
				v("replace-utf8", s == lossy(replace), err)
				s, err = g.TryReplaceString(u8, replacement)
				v("replaceAll-utf8", s == lossy(replaceAll), err)
			}

			// split
			pieces, err := re.TrySplitUTF16(text, -1)
			v("split", sameSplit16(get(a, "split"), pieces), err)
			pieces, err = re.TrySplitUTF16(text, 3)
			v("split3", sameSplit16(get(a, "split3"), pieces), err)
		}
	}
}

// sameAllBounds is sameAll on the bounds of the whole match alone.
func sameAllBounds(all [][][]int, found [][]int, at func(int) int) bool {
	for n, b := range found {
		if n >= len(all) {
			return len(all) == 20
		}
		e := all[n]
		if e == nil || at(e[0][0]) != b[0] || at(e[0][1]) != b[1] {
			return false
		}
	}
	return !((len(all) < 20 && len(found) != len(all)) || (len(all) == 20 && len(found) < 20))
}

// A small JSON reader whose strings are UTF-16 units ([]uint16) and keep lone surrogates.

type reader struct {
	s string
	i int
}

func parseJSON(s string) (v any, err error) {
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("%v", x)
		}
	}()
	r := &reader{s: s}
	v = r.value()
	r.space()
	if r.i != len(s) {
		return nil, fmt.Errorf("unexpected text at %d", r.i)
	}
	return v, nil
}

func (r *reader) space() {
	for r.i < len(r.s) && strings.IndexByte(" \t\n\r", r.s[r.i]) >= 0 {
		r.i++
	}
}

func (r *reader) expect(c byte) {
	if r.s[r.i] != c {
		panic(fmt.Sprintf("expected %c at %d", c, r.i))
	}
	r.i++
}

func (r *reader) value() any {
	r.space()
	switch r.s[r.i] {
	case '{':
		r.i++
		m := map[string]any{}
		r.space()
		if r.s[r.i] == '}' {
			r.i++
			return m
		}
		for {
			r.space()
			key := lossy(r.string())
			r.space()
			r.expect(':')
			m[key] = r.value()
			r.space()
			if r.s[r.i] == ',' {
				r.i++
				continue
			}
			r.expect('}')
			return m
		}
	case '[':
		r.i++
		a := []any{}
		r.space()
		if r.s[r.i] == ']' {
			r.i++
			return a
		}
		for {
			a = append(a, r.value())
			r.space()
			if r.s[r.i] == ',' {
				r.i++
				continue
			}
			r.expect(']')
			return a
		}
	case '"':
		return r.string()
	case 't':
		r.i += 4
		return true
	case 'f':
		r.i += 5
		return false
	case 'n':
		r.i += 4
		return nil
	}
	start := r.i
	for r.i < len(r.s) && strings.IndexByte("+-0123456789.eE", r.s[r.i]) >= 0 {
		r.i++
	}
	f, err := strconv.ParseFloat(r.s[start:r.i], 64)
	if err != nil {
		panic(err)
	}
	return f
}

func (r *reader) string() []uint16 {
	r.expect('"')
	out := []uint16{}
	for {
		c := r.s[r.i]
		if c == '"' {
			r.i++
			return out
		}
		if c != '\\' {
			start := r.i
			for r.i < len(r.s) && r.s[r.i] != '"' && r.s[r.i] != '\\' {
				r.i++
			}
			for _, ch := range r.s[start:r.i] {
				out = utf16.AppendRune(out, ch)
			}
			continue
		}
		e := r.s[r.i+1]
		r.i += 2
		switch e {
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case 'r':
			out = append(out, '\r')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'u':
			v, err := strconv.ParseUint(r.s[r.i:r.i+4], 16, 16)
			if err != nil {
				panic(err)
			}
			out = append(out, uint16(v))
			r.i += 4
		default:
			out = append(out, uint16(e))
		}
	}
}
