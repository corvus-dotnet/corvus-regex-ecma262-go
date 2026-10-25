#!/usr/bin/env bash
# The benchmark harness, which the central benchmark site runs each night (as the image, Dockerfile) and anyone can run:
# it builds the library and the harness, works out the compliance of each ECMA-262 engine over the test data, runs
# every engine over the Industry workloads, and writes the run's one data file in the results page's shape.
#
#   tools/benchmarks/run.sh <workloads.json> <texts directory> <out.json> [test data directory]
#
# It needs Go 1.26 or later (the harness takes goja, which needs it; the library builds with 1.25) and Python 3, and
# Node 24 on PATH for the V8 rows.
# - workloads.json: the Industry workloads ({name, pattern, flags, file, id}), as tools/benchmarks/v8/workloads.py
#   writes them from the .NET reference engine's Industry.cs (tools/benchmarks/workloads.json is a copy).
# - texts directory: the .txt.gz files the workloads name (those of the .NET benchmark); a file not there is fetched
#   from dotnet/performance into it.
# - out.json: the data file (meta, vocab, measures, impls, schemas, results; see tools/benchmarks/industry-results.py).
# - test data directory: the data/ directory of github.com/corvus-dotnet/ecma262-regex-test-data; without it the
#   repository is cloned at depth 1.
#
# Optional settings: ROUNDS (3), SECONDS_EACH (0.7) and PASSES (7) of the interleaved protocol and SPREAD (0.05), the
# spread over which a figure is listed, or JOB=short for a quick look (3 rounds of 0.1 s, one pass); CORES, a taskset
# core list to run the timed parts on; SETTLE, seconds to wait between the builds and the timed parts (0; 180 for a
# measured run on a quiet machine); SKIP_BUILD=1 when the programs are built (the image); V8=0 for no V8 rows (the
# central run, for every image but one), whose counts the workloads carry (v8Count); GOJA_COMPLIANCE=1 to work out
# goja's compliance again (tools/benchmarks/goja-dataset, some minutes) rather than take
# tools/benchmarks/compliance-goja.json; RUN_URL, the address of the run.
set -euo pipefail
here="$(cd "$(dirname "$0")/../.." && pwd)"
abs() { mkdir -p "$(dirname "$1")"; echo "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }
workloads="$(abs "${1:?workloads.json}")"
texts="$(mkdir -p "${2:?texts directory}" && cd "$2" && pwd)"
out_file="$(abs "${3:?out.json}")"
data="${4:-}"
. "$here/tools/benchmarks/protocol.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
timed() { if [ -n "${CORES:-}" ]; then taskset -c "$CORES" "$@"; else "$@"; fi; }
cd "$here"
if [ -z "$data" ]; then
  git clone --quiet --depth 1 https://github.com/corvus-dotnet/ecma262-regex-test-data "$work/test-data"
  data="$work/test-data/data"
fi
bin=target/harness
# The image (Dockerfile) has them built already.
if [ "${SKIP_BUILD:-0}" != 1 ]; then tools/benchmarks/build.sh; fi
# COUNT_GATE=1 (the harness-image job): every workload counted by this library's engines beside V8, one round of the
# least timing, each count checked against V8's by count-gate.py; nothing else runs.
if [ "${COUNT_GATE:-0}" = 1 ]; then
  export CORVUS_REGEX_TEXTS="$texts" CORVUS_REGEX_PLAIN="$work/plain"
  "$bin/industry" "$workloads" --engines corvus-utf8,corvus-utf16,v8 --v8 tools/benchmarks/v8/industry.mjs --rounds 1 \
    --seconds 0.01 --passes 1 --out "$work/counts.json" 2> "$work/counts.log" || true
  python3 tools/benchmarks/count-gate.py "$workloads" "$work/counts.json" corvus-utf8 corvus-utf16
  exit $?
fi
# Each runner writes its engine's compliance file (--compliance): the counts, the runner and the calls it makes, and
# what it leaves out, which the results script gives the page as it is.
"$bin/dataset" "$data" "$work/wrong-corvus.tsv" --compliance "$work/compliance-corvus.json" \
  > "$work/dataset-corvus.txt" 2>&1 || true
v8=(--v8 tools/benchmarks/v8/industry.mjs)
compliance_v8=(--compliance-v8 "$work/compliance-v8.json")
if [ "${V8:-1}" = 0 ]; then
  compliance_v8=()
  engines=(--engines corvus-utf8,corvus-utf16,regexp,goja)
else
  node tools/benchmarks/v8/dataset.mjs "$data" --compliance "$work/compliance-v8.json" > "$work/dataset-v8.txt" 2>&1 || true
  engines=()
fi
if [ "${GOJA_COMPLIANCE:-0}" = 1 ]; then
  "$bin/goja-dataset" "$data" --compliance "$work/compliance-goja.json" > "$work/dataset-goja.txt" || true
  # The commit of the data, when it is a checkout, goes in the file as dataCommit, as the checked-in file's does.
  if commit=$(git -C "$data/.." rev-parse HEAD 2>/dev/null) && [ -f "$work/compliance-goja.json" ]; then
    python3 -c 'import json, sys; c = json.load(open(sys.argv[1])); c["dataCommit"] = sys.argv[2]; json.dump(c, open(sys.argv[1], "w"))' \
      "$work/compliance-goja.json" "$commit"
  fi
else
  cp tools/benchmarks/compliance-goja.json "$work/compliance-goja.json"
fi
tail -n 1 "$work/dataset-corvus.txt" $([ -f "$work/dataset-v8.txt" ] && echo "$work/dataset-v8.txt") \
  $([ -f "$work/dataset-goja.txt" ] && echo "$work/dataset-goja.txt") || true
if [ "${SETTLE:-0}" -gt 0 ]; then
  echo "settling for $SETTLE s"
  sleep "$SETTLE"
fi
export CORVUS_REGEX_TEXTS="$texts" CORVUS_REGEX_PLAIN="$work/plain"
timed "$bin/industry" "$workloads" "${v8[@]}" "${engines[@]}" --rounds "$rounds" --seconds "$seconds" \
  --passes "$passes" --spread "$spread" --out "$work/run.json" 2> "$work/run.log"
grep -E '^(SPREAD|COUNT) ' "$work/run.log" || true
python3 tools/benchmarks/industry-results.py --workloads "$workloads" --run "$work/run.json" --out "$out_file" \
  --compliance-corvus "$work/compliance-corvus.json" --compliance-goja "$work/compliance-goja.json" \
  "${compliance_v8[@]}" --run-url "${RUN_URL:-}"
if [ -n "${KEEP:-}" ]; then mkdir -p "$KEEP" && cp "$work"/*.json "$work"/*.txt "$work"/*.log "$KEEP"/; fi
