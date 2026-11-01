#!/usr/bin/env bash
# Speech recognition against the running stack (make up-speech), no CRM needed:
# upload a recording, transcribe it, then summarise the transcript. Use only
# your own or synthetic recordings, never customer calls.
#
#   scripts/smoke_speech.sh path/to/call.mp3 [ru|en] [mono|manager_left|manager_right]
set -euo pipefail
cd "$(dirname "$0")/.."
FILE=${1:?usage: smoke_speech.sh recording [language] [channels]}
LANG_=${2:-ru}
CHANNELS=${3:-manager_left}
GW="${GW:-http://127.0.0.1:8090}"
TENANT="smoke-speech-$(date +%s)"
set -a; source .env; set +a   # the dev signing key; never printed
MINTED=0
# Tokens live at most 10 minutes and a CPU summary can take longer: re-mint every 8.
refresh() {
  if [ $(( $(date +%s) - MINTED )) -gt 480 ]; then
    TOK=$(docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false -e AI_AUTH_HS256_KEY \
      -v crmai-gomod:/go/pkg/mod -v crmai-gocache:/root/.cache/go-build golang:1.27.3-bookworm \
      go run ./tools/devtoken -tenant "$TENANT" -sub user:smoke -scopes "ai.audio.write ai.jobs.create ai.jobs.read")
    MINTED=$(date +%s)
    auth=(-H "Authorization: Bearer $TOK")
  fi
}
CT=audio/wav
case "$FILE" in *.mp3) CT=audio/mpeg ;; *.ogg) CT=audio/ogg ;; *.m4a) CT=audio/mp4 ;; esac

refresh
curl -fsS "${auth[@]}" -X PUT -H "Content-Type: $CT" --data-binary "@$FILE" "$GW/v1/audio/call-1"; echo

run_job() { # type, input json -> prints the finished job
  local id body status
  refresh
  id=$(curl -fsS "${auth[@]}" -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-$RANDOM$RANDOM" \
    -X POST "$GW/v1/jobs" -d "{\"type\":\"$1\",\"input\":$2}" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["id"])')
  for _ in $(seq 1 600); do
    refresh
    body=$(curl -fsS "${auth[@]}" "$GW/v1/jobs/$id")
    status=$(echo "$body" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["status"])')
    case "$status" in succeeded|failed|cancelled) break ;; esac
    sleep 3
  done
  echo "$body"
}

started=$(date +%s)
T=$(run_job call.transcribe.v1 "{\"audio_ref\":\"call-1\",\"language\":\"$LANG_\",\"channels\":\"$CHANNELS\"}")
echo "transcribed in $(( $(date +%s) - started )) s"
echo "$T" | python3 -I -c '
import json, sys
d = json.load(sys.stdin)
if d["status"] != "succeeded":
    print("FAILED:", d.get("error")); sys.exit(1)
o = d["result"]["output"]
print("model:", d["result"]["model"], "| audio:", round(o["duration_s"], 1), "s | segments:", len(o["segments"]))
for s in o["segments"]:
    print("  [%5.1f-%5.1f] %-8s %s" % (s["start_s"], s["end_s"], s["speaker"], s["text"]))'

TEXT=$(echo "$T" | python3 -I -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["result"]["output"]["text"], ensure_ascii=False))')
started=$(date +%s)
S=$(run_job call.summary.v2 "{\"transcript\":$TEXT,\"language\":\"$LANG_\"}")
echo "summarised in $(( $(date +%s) - started )) s"
echo "$S" | python3 -I -c '
import json, sys
d = json.load(sys.stdin)
if d["status"] != "succeeded":
    print("FAILED:", d.get("error")); sys.exit(1)
o = d["result"]["output"]
print("summary:", o["summary"])
for k in o["key_points"]:
    print("  -", k["text"], "<-", repr(k["evidence_quote"]))
print("next step:", o["next_step"] and o["next_step"]["text"])'
refresh
curl -fsS "${auth[@]}" -X DELETE "$GW/v1/audio/call-1" -o /dev/null -w 'recording deleted: HTTP %{http_code}\n'
