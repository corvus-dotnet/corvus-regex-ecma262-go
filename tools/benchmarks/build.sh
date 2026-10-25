#!/usr/bin/env bash
# Builds the harness and the data-set runner into target/harness: the harness (this directory's module, which takes
# goja) and the runner of the library's own module. run.sh calls it, and the image (Dockerfile) calls it when it is
# built.
set -euo pipefail
cd "$(dirname "$0")/../.."
out=target/harness
rm -rf "$out" && mkdir -p "$out"
export GOFLAGS="${GOFLAGS:--p=4}"
go build -o "$out/dataset" ./tools/dataset
(cd tools/benchmarks && go build -o "../../$out/industry" ./industry && go build -o "../../$out/goja-dataset" ./goja-dataset)
