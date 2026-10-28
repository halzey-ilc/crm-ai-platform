#!/usr/bin/env bash
# Download the local model outside the repository and verify its sha256 against
# the LFS oid published by the Hugging Face API. Safe to re-run: an intact file
# is kept, a partial one is resumed. The remainder is fetched in parallel byte
# ranges because per-connection throughput from the CDN can be very low.
#
# Model: Qwen3.5-4B (Apache-2.0, https://huggingface.co/Qwen/Qwen3.5-4B),
# GGUF Q4_K_M quantisation by bartowski (third party, built from the official weights).
set -euo pipefail
REPO=${AI_MODEL_REPO:-bartowski/Qwen_Qwen3.5-4B-GGUF}
FILE=${AI_MODEL_FILE:-Qwen_Qwen3.5-4B-Q4_K_M.gguf}
DIR=${AI_MODELS_DIR:-$HOME/.local/share/crm-ai-platform/models}
PARTS=${AI_MODEL_PARALLEL:-8}
URL="https://huggingface.co/$REPO/resolve/main/$FILE?download=true"
mkdir -p "$DIR"

read -r SIZE SHA < <(curl -fsSL "https://huggingface.co/api/models/$REPO/tree/main" | python3 -I -c "
import json, sys
f = next(x for x in json.load(sys.stdin) if x['path'] == '$FILE')
print(f['lfs']['size'], f['lfs']['oid'])")

if [ -f "$DIR/$FILE" ] && echo "$SHA  $DIR/$FILE" | sha256sum -c --status -; then
  echo "model present and verified: $DIR/$FILE"
  exit 0
fi

PART="$DIR/$FILE.part"
touch "$PART"
HAVE=$(stat -c %s "$PART")
REMAIN=$((SIZE - HAVE))
echo "downloading $FILE: have $HAVE of $SIZE bytes, fetching the rest in $PARTS parallel ranges"
if [ "$REMAIN" -gt 0 ]; then
  CHUNK=$(( (REMAIN + PARTS - 1) / PARTS ))
  pids=()
  for i in $(seq 0 $((PARTS - 1))); do
    START=$((HAVE + i * CHUNK))
    END=$((START + CHUNK - 1)); [ "$END" -ge "$SIZE" ] && END=$((SIZE - 1))
    [ "$START" -gt "$END" ] && continue
    SEG="$PART.seg$i"
    # Resume a segment from where an earlier run stopped.
    GOT=0; [ -f "$SEG" ] && GOT=$(stat -c %s "$SEG")
    if [ $((START + GOT)) -le "$END" ]; then
      curl -fsSL --retry 5 --retry-delay 3 -r "$((START + GOT))-$END" "$URL" >> "$SEG" &
      pids+=($!)
    fi
  done
  for pid in "${pids[@]}"; do wait "$pid"; done
  for i in $(seq 0 $((PARTS - 1))); do
    [ -f "$PART.seg$i" ] && cat "$PART.seg$i" >> "$PART" && rm "$PART.seg$i"
  done
fi
[ "$(stat -c %s "$PART")" -eq "$SIZE" ] || { echo "size mismatch; re-run to resume"; exit 1; }
echo "$SHA  $PART" | sha256sum -c -
mv "$PART" "$DIR/$FILE"
printf '%s  %s\n' "$SHA" "$FILE" > "$DIR/$FILE.sha256"
echo "model verified: $DIR/$FILE"
