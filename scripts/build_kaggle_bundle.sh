#!/usr/bin/env bash
# Build the bundle that notebooks/kaggle_gpu_eval.ipynb runs on Kaggle:
#   dist/kaggle-eval-bundle.zip = evalrun (static linux/amd64) + synthetic datasets + manifest.
# Upload the zip as a private Kaggle Dataset and attach it to the notebook.
# The repository stays private: only the compiled tool and synthetic cases leave this machine.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=dist/kaggle-eval
rm -rf "$OUT" dist/kaggle-eval-bundle.zip
mkdir -p "$OUT/datasets"

docker run --rm -v "$PWD":/src -w /src -v crmai-gomod:/go/pkg/mod -v crmai-gocache:/root/.cache/go-build \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache \
  golang:1.27.3-bookworm go build -buildvcs=false -trimpath -ldflags=-s -o "$OUT/evalrun" ./tools/evalrun

cp evaluation/datasets/gpu_eval.synthetic.v1.jsonl evaluation/datasets/call_summary.synthetic.v1.jsonl "$OUT/datasets/"
{
  echo "commit=$(git rev-parse HEAD)"
  echo "dirty=$([ -n "$(git status --porcelain)" ] && echo yes || echo no)"
  echo "built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  (cd "$OUT" && sha256sum evalrun datasets/*.jsonl)
} > "$OUT/MANIFEST.txt"
(cd dist && python3 -I -c "
import pathlib, zipfile
root = pathlib.Path('kaggle-eval')
with zipfile.ZipFile('kaggle-eval-bundle.zip', 'w', zipfile.ZIP_DEFLATED) as z:
    for p in sorted(root.rglob('*')):
        if p.is_file():
            z.write(p, p.relative_to(root))
")
cat "$OUT/MANIFEST.txt"
ls -la dist/kaggle-eval-bundle.zip
