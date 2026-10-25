// Command goja-dataset works out goja's compliance over the ECMA-262 test data: judge.js (tools/benchmarks/v8/
// dataset.mjs's judging, run inside goja) with the limit of 2 seconds on each answer that every runner gives, as the
// Java engine's tregex-compliance.py works out TRegex's.
//
//	goja-dataset <data directory> [--compliance <file>]
//
// goja cannot be stopped inside a match, so each file is judged in a child process (this program with --child) that
// writes each answer as it gives it, and a watchdog goroutine in the child times each answer from just before goja is
// called and ends the process when one passes 2 seconds. That answer is counted unanswered ("limit", as the judging
// rule of the data counts an answer an engine never gave), and a new child goes on from the next answer, so a record
// loses only the answer that did not finish. Once three answers of a record have reached the limit, its other answers
// are unanswered without being asked, so that a pattern goja cannot answer on any text costs seconds, not one limit
// for each of its answers. The limit is the wall clock's, so the run is made with nothing else running on the machine.
// The output is a runner's: a line for each file and the last line of counts, with each answer that reached the limit
// named.
//
// With --compliance, goja's compliance object is written to the file, as the benchmark page reads it
// (docs/implementing-a-source.md in corvus-regex-ecma262-benchmarks): the counts, this runner and the calls judge.js
// makes, and what it leaves out: UTF-8 (a JavaScript string is UTF-16) and the grammar of the v flag (goja has no such
// mode). A run takes some minutes, so run.sh takes tools/benchmarks/compliance-goja.json, this program's compliance
// file, unless GOJA_COMPLIANCE=1.
package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

//go:embed judge.js
var judgeJS string

const (
	exec262 = "exec/test262.jsonl"
	// limit is the time an answer may take.
	limit = 2 * time.Second
	// silence is how long a child may go without a word before it is taken for lost (its watchdog ends it within 2 s
	// of an answer that does not finish; this catches one stuck anywhere else).
	silence = 60 * time.Second
	// limitsPerRecord is the answers of a record that may reach the limit before its other answers are unanswered
	// without being asked.
	limitsPerRecord = 3
)

// child judges one file from the answer from, writing its progress to stderr.
func child(dir, name string, from []string) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	out := bufio.NewWriter(os.Stderr)
	var started atomic.Int64
	go func() {
		for range time.Tick(10 * time.Millisecond) {
			if s := started.Load(); s != 0 && time.Since(time.Unix(0, s)) > limit {
				// Nothing that takes a lock: the engine is still in its match.
				os.Stderr.Write([]byte("\nL\n"))
				os.Exit(9)
			}
		}
	}()
	vm := goja.New()
	fn, err := vm.RunString(judgeJS)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	judge, _ := goja.AssertFunction(fn)
	host := map[string]any{
		"emit": func(s string) {
			out.WriteString(s)
			out.WriteByte('\n')
			// Flushed before each answer is begun, so that the supervisor knows which answer reached the limit.
			if strings.HasPrefix(s, "C\t0") {
				out.Flush()
			}
		},
		"began": func() { started.Store(time.Now().UnixNano()) },
		"ended": func() { started.Store(0) },
	}
	var fromValue goja.Value = goja.Null()
	if from != nil {
		fromValue = vm.ToValue(from)
	}
	var lines []any
	for _, l := range strings.Split(string(b), "\n") {
		lines = append(lines, l)
	}
	if _, err := judge(goja.Undefined(), vm.ToValue(name), vm.NewArray(lines...), fromValue, vm.ToValue(host)); err != nil {
		out.Flush()
		fmt.Fprintln(os.Stderr, "judge.js:", err)
		os.Exit(2)
	}
	out.Flush()
}

type counts struct{ right, wrong, refused, accepted, limit, records, skipped int }

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--child" {
		var from []string
		if len(os.Args) > 4 {
			from = strings.Split(os.Args[4], "\t")
		}
		child(os.Args[2], os.Args[3], from)
		return
	}
	args := os.Args[1:]
	compliance := ""
	if at := slices.Index(args, "--compliance"); at >= 0 {
		compliance = args[at+1]
		args = slices.Delete(args, at, at+2)
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: goja-dataset <data directory> [--compliance <file>]")
		os.Exit(2)
	}
	dir := args[0]
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, e.Name())
		}
	}
	slices.Sort(files)
	if _, err := os.Stat(filepath.Join(dir, exec262)); err == nil {
		files = append(files, exec262)
	}
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	start := time.Now()
	perFile := map[string]*counts{}
	var limited []string
	for _, name := range files {
		c := &counts{}
		perFile[name] = c
		var at []string // the record and answer to start from: nil for the beginning
		atLimit := 0    // the answers of the record at at that reached the limit
		for {
			cmdArgs := []string{"--child", dir, name}
			if at != nil {
				spent := "0"
				if atLimit >= limitsPerRecord {
					spent = "1"
				}
				cmdArgs = append(cmdArgs, strings.Join(append(slices.Clone(at), spent), "\t"))
			}
			cmd := exec.Command(self, cmdArgs...)
			stderr, err := cmd.StderrPipe()
			if err != nil {
				panic(err)
			}
			if err := cmd.Start(); err != nil {
				panic(err)
			}
			lines := make(chan string)
			go func() {
				s := bufio.NewScanner(stderr)
				s.Buffer(make([]byte, 1<<20), 1<<26)
				for s.Scan() {
					lines <- s.Text()
				}
				close(lines)
			}()
			// The record being judged and the number of its answers given so far.
			current, given := -1, 0
			if at != nil {
				current, _ = strconv.Atoi(at[1])
				given, _ = strconv.Atoi(at[2])
			}
			why := ""
		read:
			for {
				select {
				case line, ok := <-lines:
					if !ok {
						break read
					}
					f := strings.Split(line, "\t")
					switch f[0] {
					case "R":
						index, _ := strconv.Atoi(f[2])
						if at == nil || index != current {
							given = 0
						}
						current = index
					case "S":
						c.skipped++
					case "C":
						for k, p := range []*int{&c.right, &c.wrong, &c.refused, &c.accepted, &c.limit} {
							v, _ := strconv.Atoi(f[2+k])
							*p += v
						}
						done, _ := strconv.Atoi(f[1])
						given += done
					case "E":
						c.records++
					case "L":
						why = "no answer in 2 s"
					default:
						if line != "" {
							fmt.Fprintln(os.Stderr, name+":", line)
						}
					}
				case <-time.After(silence):
					why = fmt.Sprintf("nothing written in %v", silence)
					break read
				}
			}
			_ = cmd.Process.Kill()
			err = cmd.Wait()
			if why == "" && err == nil {
				break
			}
			if why == "" {
				why = "the process ended: " + err.Error()
			}
			if current < 0 {
				fmt.Fprintf(os.Stderr, "%s: the child ended before its first record: %s\n", name, why)
				os.Exit(2)
			}
			// The answer being given is unanswered; the next child goes on from the one after it.
			c.limit++
			limited = append(limited, fmt.Sprintf("%s record %d answer %d: %s", name, current+1, given+1, why))
			fmt.Fprintf(os.Stderr, "LIMIT %s record %d answer %d: %s\n", name, current+1, given+1, why)
			if at == nil || at[1] != strconv.Itoa(current) {
				atLimit = 0
			}
			atLimit++
			if atLimit == limitsPerRecord {
				limited = append(limited, fmt.Sprintf("%s record %d answer %d: its answers after this not asked: %d at the limit",
					name, current+1, given+1, limitsPerRecord))
			}
			at = []string{name, strconv.Itoa(current), strconv.Itoa(given + 1)}
			// The record whose answer reached the limit was not ended, so it is counted when the next child ends it;
			// its skipped and records counts are whole, as each child counts from the record it starts at.
		}
		fmt.Printf("%-24s right %10d  wrong %6d  refused-valid %4d  accepted-invalid %4d  limit %4d  %6.1fs\n", name,
			c.right, c.wrong, c.refused, c.accepted, c.limit, time.Since(start).Seconds())
	}
	var t counts
	for _, c := range perFile {
		t.right += c.right
		t.wrong += c.wrong
		t.refused += c.refused
		t.accepted += c.accepted
		t.limit += c.limit
		t.records += c.records
		t.skipped += c.skipped
	}
	for _, l := range limited {
		fmt.Println("limit: " + l)
	}
	if compliance != "" {
		c := map[string]any{
			"right": t.right, "wrong": t.wrong, "refused": t.refused, "acceptedInvalid": t.accepted, "limited": t.limit,
			"judge": map[string]any{
				"runner": "https://github.com/corvus-dotnet/corvus-regex-ecma262-go/blob/main/tools/benchmarks/goja-dataset/judge.js",
				"calls": map[string]any{"utf16": []string{"RegExp.prototype.test", "RegExp.prototype.exec",
					"String.prototype.matchAll", "String.prototype.replace", "String.prototype.split"}},
			},
			"excludes": []any{
				map[string]any{"class": "encoding", "leaves": "utf8"},
				map[string]any{"class": "grammar", "leaves": []string{"unicode_sets"}},
			},
		}
		b, _ := json.Marshal(c)
		if err := os.WriteFile(compliance, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	fmt.Println()
	fmt.Printf("records %d (skipped %d deviations)  right %d  wrong %d  refused-valid %d  accepted-invalid %d  limit %d  errors 0  in %.1fs\n",
		t.records, t.skipped, t.right, t.wrong, t.refused, t.accepted, t.limit, time.Since(start).Seconds())
}
