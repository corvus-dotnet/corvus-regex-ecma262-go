// Command industry is the Industry benchmark: each workload counted by each engine, as the V8 harness of the .NET
// reference engine (tools/benchmarks/v8/industry.mjs) counts it, in nanoseconds per count. A port of the Rust crate's
// tools/src/bin/industry.rs (by way of the Java engine's Industry.java), with the same protocol.
//
//	industry <workloads.json> [--engines corvus-utf8,corvus-utf16,regexp,goja,v8] [--v8 <industry.mjs>]
//	         [--rounds 3] [--seconds 0.7] [--passes 7] [--spread 0.05] [--filter <substring of the id>]
//	         [--ids <file>] [--out <results.json>]
//
// The protocol is interleaved, so that what changes on the host over a run (its clock, its heat, its other work) falls
// on every engine alike: the workloads are the outer loop, each workload's rounds next and the engines innermost, their
// order turned by one each round. Each figure is a child process of its own (this program, or Node for V8), so that one
// engine's caches, heap and collector do not reach another's figures; it warms for two fifths of its seconds, then
// times passes passes of as many counts as fit the rest and reports their median. A workload's figure for an engine is
// the median of its rounds, with the fastest and the slowest round as its spread. Where the spread, (slowest -
// fastest) / median, is over --spread, the figure is listed in the run's facts and on stderr. A figure that does not
// finish in 30 s is stopped and recorded as giving no result within 30 s, and is not run in the workload's later
// rounds. A count is the global search of ECMA-262: after an empty match the search goes on one character later.
//
// The engines: this library over UTF-8 (a Text, checked once before the timing) and over UTF-16 ([]uint16), with the
// workload's flags and u, counted by a Matches (corvus-utf8, corvus-utf16); regexp, the standard library's engine,
// which is given the pattern as it is and the flags i, m and s, counted with FindAllIndex, and whose figure is an
// error where the pattern does not compile; goja, the ECMA-262 engine of the JavaScript interpreter written in Go,
// counted by the V8 harness's loop with gu added; and V8 (--v8, the harness run by node), with gu added.
//
// The texts are those of the reference run, in CORVUS_REGEX_TEXTS (or ~/.local/share/corvus-regex-benchmarks) and
// fetched from dotnet/performance when missing. The run unzips each once into a directory of its own
// (CORVUS_REGEX_PLAIN), which the children read.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/dop251/goja"

	ecmaregex "github.com/corvus-dotnet/corvus-regex-ecma262-go"
)

const source = "https://raw.githubusercontent.com/dotnet/performance/main/src/benchmarks/micro/libraries/" +
	"System.Text.RegularExpressions/TestData/"

var defaultEngines = []string{"corvus-utf8", "corvus-utf16", "regexp", "goja"}

// sink is what the counts are summed into, so that no count is taken away as dead code.
var sink int

type workload struct {
	ID      string   `json:"id"`
	Pattern string   `json:"pattern"`
	Flags   string   `json:"flags"`
	File    string   `json:"file"`
	V8Count *float64 `json:"v8Count"`
}

func workloads(path string) []workload {
	b, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	var w []workload
	if err := json.Unmarshal(b, &w); err != nil {
		fail(err)
	}
	return w
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "industry:", err)
	os.Exit(2)
}

func textDir() string {
	if d := os.Getenv("CORVUS_REGEX_TEXTS"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "corvus-regex-benchmarks")
}

// text returns the bytes of a text: gunzipped, without a byte order mark (as unzipped in CORVUS_REGEX_PLAIN).
func text(file string) []byte {
	if plain := os.Getenv("CORVUS_REGEX_PLAIN"); plain != "" {
		if b, err := os.ReadFile(filepath.Join(plain, strings.TrimSuffix(file, ".gz"))); err == nil {
			return b
		}
	}
	path := filepath.Join(textDir(), file)
	if _, err := os.Stat(path); err != nil {
		if err := os.MkdirAll(textDir(), 0o755); err != nil {
			fail(err)
		}
		curl := exec.Command("curl", "-sSfL", "-o", path, source+file)
		curl.Stdout, curl.Stderr = os.Stderr, os.Stderr
		if err := curl.Run(); err != nil {
			fail(fmt.Errorf("could not fetch %s: %w", file, err))
		}
	}
	f, err := os.Open(path)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		fail(err)
	}
	b, err := io.ReadAll(z)
	if err != nil {
		fail(err)
	}
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

// flagsU returns the workload's flags with u added, unless it has u or v.
func flagsU(flags string) string {
	if strings.ContainsAny(flags, "uv") {
		return flags
	}
	return flags + "u"
}

// flagsGU returns the workload's flags with g and u added, each once, as the V8 harness gives them.
func flagsGU(flags string) string {
	out := ""
	for _, c := range flags + "gu" {
		if !strings.ContainsRune(out, c) {
			out += string(c)
		}
	}
	return out
}

// figures are one figure's times, in nanoseconds per count, its count and the counts timed.
type figures struct {
	median, min, max float64
	count, reps      int
}

// measure times a count: it warms, then times the passes.
func measure(seconds float64, passes int, count func() int) figures {
	expected := count()
	warmEnd := time.Now().Add(time.Duration(seconds * 0.4e9))
	reps := 0
	for time.Now().Before(warmEnd) {
		if count() != expected {
			panic("the count changed")
		}
		reps++
	}
	perPass := max(1, int(math.Round(float64(reps)*0.6/float64(passes))))
	times := make([]float64, passes)
	for p := range passes {
		t0 := time.Now()
		s := 0
		for range perPass {
			s += count()
		}
		times[p] = float64(time.Since(t0).Nanoseconds()) / float64(perPass)
		sink += s
	}
	sort.Float64s(times)
	return figures{times[passes/2], times[0], times[passes-1], expected, perPass * passes}
}

// gojaCount is the V8 harness's count, run by goja.
const gojaCount = `(function (re, s) {
  re.lastIndex = 0;
  let n = 0;
  for (;;) {
    const from = re.lastIndex;
    if (!re.test(s)) return n;
    n++;
    if (re.lastIndex === from) {
      if (from >= s.length) return n;
      const c = s.charCodeAt(from);
      re.lastIndex = from + (c >= 0xd800 && c <= 0xdbff && from + 1 < s.length ? 2 : 1);
    }
  }
})`

// moduleVersion returns the version of a module the program was built with.
func moduleVersion(path string) string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			if d.Path == path {
				return d.Version
			}
		}
	}
	return ""
}

// one runs one workload on one engine in this process and prints its figures as one line of JSON.
func one(path string, index int, engine string, seconds float64, passes int) {
	w := workloads(path)[index]
	b := text(w.File)
	var f figures
	name := ""
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		switch engine {
		case "corvus-utf8":
			t, err := ecmaregex.NewText(b)
			if err != nil {
				return err
			}
			re, err := ecmaregex.Compile(w.Pattern, flagsU(w.Flags))
			if err != nil {
				return err
			}
			m := re.NewMatches()
			f = measure(seconds, passes, func() int { return m.ResetText(t).Count() })
		case "corvus-utf16":
			u := utf16.Encode([]rune(string(b)))
			re, err := ecmaregex.Compile(w.Pattern, flagsU(w.Flags))
			if err != nil {
				return err
			}
			m := re.NewMatches()
			f = measure(seconds, passes, func() int { return m.ResetUTF16(u).Count() })
		case "regexp":
			prefix := ""
			for _, c := range "ims" {
				if strings.ContainsRune(w.Flags, c) {
					prefix += string(c)
				}
			}
			pattern := w.Pattern
			if prefix != "" {
				pattern = "(?" + prefix + ")" + pattern
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return err
			}
			f = measure(seconds, passes, func() int { return len(re.FindAllIndex(b, -1)) })
		case "goja":
			vm := goja.New()
			fn, err := vm.RunString(gojaCount)
			if err != nil {
				return err
			}
			count, _ := goja.AssertFunction(fn)
			ctor, err := vm.RunString("RegExp")
			if err != nil {
				return err
			}
			re, err := vm.New(ctor, vm.ToValue(w.Pattern), vm.ToValue(flagsGU(w.Flags)))
			if err != nil {
				return err
			}
			s := vm.ToValue(string(b))
			f = measure(seconds, passes, func() int {
				v, err := count(goja.Undefined(), re, s)
				if err != nil {
					panic(err)
				}
				return int(v.ToInteger())
			})
			name = "goja " + moduleVersion("github.com/dop251/goja")
		default:
			return fmt.Errorf("unknown engine %s", engine)
		}
		return nil
	}()
	if err != nil {
		line, _ := json.Marshal(map[string]string{"error": strings.SplitN(err.Error(), "\n", 2)[0]})
		fmt.Println(string(line))
		return
	}
	out := fmt.Sprintf(`{"median":%.1f,"min":%.1f,"max":%.1f,"count":%d,"reps":%d`, f.median, f.min, f.max, f.count, f.reps)
	if name != "" {
		q, _ := json.Marshal(name)
		out += `,"engine":` + string(q)
	}
	fmt.Println(out + "}")
}

// stopped is the line of a figure stopped at the limit, which the page shows as giving no result within 30 s.
const stopped = `{"error":"did not produce a result within 30 s","limit":30}`

// figure runs engine over workload index in a child process, and returns its line of JSON, or one with the error.
func figure(v8, path string, index int, engine, seconds, passes string) string {
	var cmd []string
	if engine == "v8" {
		cmd = []string{"node", v8, "--one", path, strconv.Itoa(index), seconds, passes}
	} else {
		self, err := os.Executable()
		if err != nil {
			return errorLine(err)
		}
		cmd = []string{self, "--one", path, strconv.Itoa(index), engine, seconds, passes}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	child.Stderr = os.Stderr
	out, err := child.StdoutPipe()
	if err != nil {
		return errorLine(err)
	}
	if err := child.Start(); err != nil {
		return errorLine(err)
	}
	line, readErr := bufio.NewReader(out).ReadString('\n')
	_ = child.Process.Kill()
	_ = child.Wait()
	switch {
	case ctx.Err() != nil:
		return stopped
	case readErr != nil && line == "":
		return `{"error":"the process ended without figures"}`
	}
	return strings.TrimSpace(line)
}

func errorLine(err error) string {
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	return string(b)
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func firstLine(command ...string) string {
	out, _ := exec.Command(command[0], command[1:]...).Output()
	line, _, _ := strings.Cut(string(out), "\n")
	return line
}

func field(file, prefix string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, prefix) {
			_, v, _ := strings.Cut(l, ":")
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// facts returns what the run is: the host, the runtime, the affinity, the load and the commit, read from the run.
func facts(seconds, passes string, extra [][2]string) string {
	load := ""
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		load, _, _ = strings.Cut(string(b), " ")
	}
	commit := os.Getenv("CORVUS_COMMIT")
	if commit == "" {
		commit = firstLine("git", "rev-parse", "HEAD")
	}
	f := [][2]string{
		{"cpu", field("/proc/cpuinfo", "model name")},
		{"cores", strconv.Itoa(runtime.NumCPU())},
		{"affinity", field("/proc/self/status", "Cpus_allowed_list")},
		{"os", firstLine("uname", "-sr")},
		{"load", load},
		{"go", runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH},
		{"commit", commit},
		{"date", time.Now().UTC().Format("2006-01-02T15:04:05Z")},
		{"seconds", seconds},
		{"passes", passes},
		// On a GitHub-hosted runner, what the runner says it is.
		{"runner", os.Getenv("RUNNER_NAME")},
		{"runner_os", os.Getenv("RUNNER_OS")},
		{"image", strings.TrimSpace(os.Getenv("ImageOS") + " " + os.Getenv("ImageVersion"))},
		{"corvus-regex-ecma262", ecmaregex.Version},
		{"regexp", "regexp (" + runtime.Version() + ")"},
		{"goja", moduleVersion("github.com/dop251/goja")},
	}
	f = append(f, extra...)
	var b strings.Builder
	b.WriteString("{")
	for k, kv := range f {
		if k > 0 {
			b.WriteString(",")
		}
		key, _ := json.Marshal(kv[0])
		value, _ := json.Marshal(kv[1])
		b.Write(key)
		b.WriteString(":")
		b.Write(value)
	}
	return b.String() + "}"
}

func countOf(line string) (int, bool) {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return 0, false
	}
	c, ok := m["count"].(float64)
	return int(c), ok
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--one" {
		index, _ := strconv.Atoi(os.Args[3])
		seconds, _ := strconv.ParseFloat(os.Args[5], 64)
		passes, _ := strconv.Atoi(os.Args[6])
		one(os.Args[2], index, os.Args[4], seconds, passes)
		return
	}
	if len(os.Args) < 2 {
		fail(fmt.Errorf("usage: industry <workloads.json> [--engines ...] [--v8 <industry.mjs>] [--out <file>]"))
	}
	path := os.Args[1]
	opt := map[string]string{}
	for k := 2; k+1 < len(os.Args); k += 2 {
		opt[os.Args[k]] = os.Args[k+1]
	}
	get := func(name, def string) string {
		if v, ok := opt[name]; ok {
			return v
		}
		return def
	}
	v8 := opt["--v8"]
	engines := slices.Clone(defaultEngines)
	if e, ok := opt["--engines"]; ok {
		engines = strings.Split(e, ",")
	} else if v8 != "" {
		engines = append(engines, "v8")
	}
	if slices.Contains(engines, "v8") && v8 == "" {
		fail(fmt.Errorf("V8 is run by the V8 harness: give --v8 <industry.mjs>"))
	}
	rounds, _ := strconv.Atoi(get("--rounds", "3"))
	seconds, passes := get("--seconds", "0.7"), get("--passes", "7")
	limit, _ := strconv.ParseFloat(get("--spread", "0.05"), 64)
	filter := opt["--filter"]
	var ids []string
	if f, ok := opt["--ids"]; ok {
		b, err := os.ReadFile(f)
		if err != nil {
			fail(err)
		}
		ids = strings.Fields(string(b))
	}
	all := workloads(path)
	var chosen []int
	for k, w := range all {
		if strings.Contains(w.ID, filter) && (ids == nil || slices.Contains(ids, w.ID)) {
			chosen = append(chosen, k)
		}
	}
	plain := os.Getenv("CORVUS_REGEX_PLAIN")
	if plain == "" {
		fail(fmt.Errorf("run.sh sets CORVUS_REGEX_PLAIN, the directory of the unzipped texts"))
	}
	// The texts unzipped once, for every child.
	if err := os.MkdirAll(plain, 0o755); err != nil {
		fail(err)
	}
	for _, w := range all {
		p := filepath.Join(plain, strings.TrimSuffix(w.File, ".gz"))
		if _, err := os.Stat(p); err != nil {
			if err := os.WriteFile(p, text(w.File), 0o644); err != nil {
				fail(err)
			}
		}
	}
	ne := len(engines)
	times := make([][][]float64, ne)
	lines := make([][]string, ne)
	for e := range ne {
		times[e] = make([][]float64, len(all))
		lines[e] = make([]string, len(all))
	}
	names := map[string]string{}
	for done, k := range chosen {
		for round := range rounds {
			for turn := range ne {
				e := (turn + round) % ne
				// A figure stopped at the limit in an earlier round is not run again.
				if lines[e][k] == stopped {
					continue
				}
				line := figure(v8, path, k, engines[e], seconds, passes)
				var parsed map[string]any
				_ = json.Unmarshal([]byte(line), &parsed)
				if m, ok := parsed["median"].(float64); ok {
					times[e][k] = append(times[e][k], m)
				} else {
					fmt.Fprintf(os.Stderr, "%-13s %-62s %s\n", engines[e], all[k].ID, line)
				}
				if n, ok := parsed["engine"].(string); ok {
					names[engines[e]] = n
				}
				lines[e][k] = line
			}
		}
		fmt.Fprintf(os.Stderr, "workload %d of %d done\n", done+1, len(chosen))
	}
	var over, differ []string
	// V8 is the oracle of the counts: an engine that counts otherwise has given a wrong answer, whatever its dialect.
	v8e := slices.Index(engines, "v8")
	oracle := make([]*int, len(all))
	for k := range all {
		if v8e >= 0 {
			if c, ok := countOf(lines[v8e][k]); ok {
				oracle[k] = &c
			}
		} else if all[k].V8Count != nil {
			c := int(*all[k].V8Count)
			oracle[k] = &c
		}
	}
	var results []string
	for e, engine := range engines {
		var rows []string
		for _, k := range chosen {
			line, ts := lines[e][k], times[e][k]
			id := all[k].ID
			var entry string
			if len(ts) < rounds {
				// A round with no figure (an error, a limit) makes the figure an error.
				entry = line
				if !strings.Contains(line, `"error"`) {
					entry = `{"error":"a round gave no figure"}`
				}
			} else {
				inOrder := make([]string, len(ts))
				for i, t := range ts {
					inOrder[i] = fmt.Sprintf("%.1f", t)
				}
				sorted := slices.Sorted(slices.Values(ts))
				m, lo, hi := median(sorted), sorted[0], sorted[len(sorted)-1]
				spread := 0.0
				if m > 0 {
					spread = (hi - lo) / m
				}
				if spread > limit {
					over = append(over, fmt.Sprintf("%s %s %.1f%%", engine, id, spread*100))
					fmt.Fprintf(os.Stderr, "SPREAD %-13s %-62s %.1f%% over %.1f%%\n", engine, id, spread*100, limit*100)
				}
				count, _ := countOf(line)
				fig := fmt.Sprintf(`"median":%.1f,"min":%.1f,"max":%.1f,"count":%d,"rounds":%d,"spread":%.4f,"each":[%s]`,
					m, lo, hi, count, len(ts), spread, strings.Join(inOrder, ","))
				if o := oracle[k]; o != nil && *o != count && engine != "v8" {
					differ = append(differ, fmt.Sprintf("%s %s %d vs V8 %d", engine, id, count, *o))
					fmt.Fprintf(os.Stderr, "COUNT  %-13s %-62s %d against V8's %d\n", engine, id, count, *o)
					entry = errorLine(fmt.Errorf("counted %d where V8 counts %d", count, *o))
				} else {
					entry = "{" + fig + "}"
				}
			}
			q, _ := json.Marshal(id)
			rows = append(rows, string(q)+":"+entry)
		}
		q, _ := json.Marshal(engine)
		results = append(results, string(q)+":{"+strings.Join(rows, ",")+"}")
	}
	extra := [][2]string{
		{"rounds", strconv.Itoa(rounds)},
		{"protocol", "interleaved"},
		{"spread_limit", strconv.FormatFloat(limit, 'f', -1, 64)},
		{"spread_over", strings.Join(over, "; ")},
		{"counts_differ", strings.Join(differ, "; ")},
		{"v8", names["v8"]},
	}
	if n := names["goja"]; n != "" {
		extra = append(extra, [2]string{"goja_engine", n})
	}
	end, _ := json.Marshal(time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	out := `{"facts":` + facts(seconds, passes, extra) + `,"end":` + string(end) + `,"results":{` +
		strings.Join(results, ",") + "}}\n"
	if f, ok := opt["--out"]; ok {
		if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
			fail(err)
		}
	} else {
		fmt.Print(out)
	}
}
