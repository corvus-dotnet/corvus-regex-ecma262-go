// The judging of one file of the ECMA-262 test data by goja's RegExp: tools/benchmarks/v8/dataset.mjs's judgeFile,
// run inside goja by goja-dataset (main.go), which gives it the file's lines, takes its progress and times each
// answer. The answers are V8's, asked as dataset.mjs asks them of V8 and TRegex, with two differences: the grammar of
// the v flag is not asked, since goja has no such mode (the compliance declares it), and an answer goja throws on (the
// d flag, which goja does not have, among them) is a wrong answer.
//
// The progress lines, written by host.emit, are dataset.mjs's: "R <file> <index>" before each record, "S" for one left
// out, "C <done> <right> <wrong> <refused> <accepted> <unanswered>" for what was counted since the last ("C 1" ends an
// answer), and "E" after the record. from ([file, index, answer, spent]) starts at that answer of that record.
(function (name, lines, from, host) {
  const REPLACEMENT = "<$&|$1|$2|$10|$<a>|$$|$`|$'>";
  const EXEC = "exec/test262.jsonl";
  const CAPTURES = ["exec", "all", "sticky", "replace", "replaceAll", "split", "split3"];
  function captures(m) {
    if (m === null) return null;
    const out = [[m.index, m.index + m[0].length]];
    if (m.indices) {
      for (let g = 1; g < m.length; g++) out.push(m.indices[g] === undefined ? null : [m.indices[g][0], m.indices[g][1]]);
    }
    return out;
  }
  function captureAnswer(what, pattern, flags, text) {
    switch (what) {
      case "exec": return captures(new RegExp(pattern, flags + "d").exec(text));
      case "all": {
        const all = [];
        for (const m of text.matchAll(new RegExp(pattern, flags + "dg"))) {
          all.push(captures(m));
          if (all.length >= 20) break;
        }
        return all;
      }
      case "sticky": {
        const y = new RegExp(pattern, flags + "dy");
        y.lastIndex = 1;
        return captures(y.exec(text));
      }
      case "replace": return text.replace(new RegExp(pattern, flags), REPLACEMENT);
      case "replaceAll": return text.replace(new RegExp(pattern, flags + "g"), REPLACEMENT);
      case "split": return text.split(new RegExp(pattern, flags)).map((s) => (s === undefined ? null : s));
      default: return text.split(new RegExp(pattern, flags), 3).map((s) => (s === undefined ? null : s));
    }
  }
  function execAnswer(re, flags, text, lastIndex) {
    const carries = flags.includes("g") || flags.includes("y");
    re.lastIndex = lastIndex;
    const m = re.exec(text);
    if (m === null) return null;
    const span = (x) => (x === undefined ? null : [x[0], x[1]]);
    const a = { captures: Array.from(m.indices, span) };
    if (m.indices.groups) a.groups = Object.fromEntries(Object.keys(m.indices.groups).map((k) => [k, span(m.indices.groups[k])]));
    if (carries) a.lastIndex = re.lastIndex;
    return a;
  }
  let fr = 0, fw = 0, ff = 0, fa = 0, fl = 0;
  let index = -1, next = 0, first = 0, spent = false;
  let written = [0, 0, 0, 0, 0];
  const write = (done) => {
    const now = [fr, fw, ff, fa, fl];
    host.emit(`C\t${done ? 1 : 0}\t${now.map((v, k) => v - written[k]).join("\t")}`);
    written = now;
  };
  // One answer: whether judged finds it right. An answer goja throws on is wrong.
  const ask = (judged) => {
    if (next++ < first) {
      written = [fr, fw, ff, fa, fl];
      return;
    }
    write(false);
    if (spent) {
      fl++;
      write(true);
      return;
    }
    host.began();
    let ok = false;
    try { ok = judged(); } catch (e) { ok = false; }
    host.ended();
    if (ok) fr++; else fw++;
    write(true);
  };
  for (const line of lines) {
    if (!line) continue;
    index++;
    if (from && name === from[0] && index < Number(from[1])) continue;
    const r = JSON.parse(line);
    host.emit(`R\t${name}\t${index}`);
    if (r.v8_deviation) {
      host.emit("S");
      continue;
    }
    next = 0;
    const here = from && name === from[0] && index === Number(from[1]);
    first = here ? Number(from[2]) : 0;
    spent = here && from[3] === "1";
    written = [fr, fw, ff, fa, fl];
    if (name === EXEC) {
      let re = null;
      try { re = new RegExp(r.pattern, r.flags.includes("d") ? r.flags : r.flags + "d"); } catch (e) { re = null; }
      if (re === null) {
        ff++;
      } else {
        for (const t of r.tests || []) {
          if (t.v8_deviation) continue;
          ask(() => JSON.stringify(execAnswer(re, r.flags, t.text, t.lastIndex)) === JSON.stringify(t.answer));
        }
      }
    } else {
      // The grammar of the v flag is left out: goja has no such mode.
      const modes = [["u", "unicode", r.valid.unicode], ["", "legacy", r.valid.legacy]];
      for (const [suffix, answer, valid] of modes) {
        let re = null;
        try { re = new RegExp(r.pattern, r.flags + suffix); } catch (e) { re = null; }
        if (re === null) { if (valid) ff++; continue; }
        if (!valid) { fa++; continue; }
        for (const t of r.tests || []) {
          if (t.v8_deviation || !(answer in t)) continue;
          if (name === "captures.jsonl") {
            for (const what of CAPTURES) {
              if (what === "sticky" && t.text.length === 0) continue;
              ask(() => JSON.stringify(captureAnswer(what, r.pattern, r.flags + suffix, t.text)) === JSON.stringify(t[answer][what]));
            }
            continue;
          }
          ask(() => {
            re.lastIndex = 0;
            return re.test(t.text) === t[answer];
          });
        }
      }
    }
    write(false);
    host.emit("E");
  }
})
