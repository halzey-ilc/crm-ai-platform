#!/usr/bin/env bash
# End-to-end smoke test against the running local stack (make up):
# create a job, wait for the orchestrator, check the result and isolation.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; source .env; set +a
BASE=${AI_GATEWAY_URL:-http://127.0.0.1:8090}

curl -fsS "$BASE/health/ready" >/dev/null
TOK=$(go run ./tools/devtoken -tenant smoke-a)
OTHER=$(go run ./tools/devtoken -tenant smoke-b)
KEY="smoke-$(date +%s%N)"
BODY='{"type":"call.summary.v1","input":{"transcript":"Здравствуйте. Клиенту нужна интеграция с 1С. Договорились, что я перезвоню в пятницу.","language":"ru"}}'

ID=$(curl -fsS -X POST "$BASE/v1/jobs" -H "Authorization: Bearer $TOK" -H "Idempotency-Key: $KEY" \
       -H 'Content-Type: application/json' -d "$BODY" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["id"])')
REPLAY=$(curl -fsS -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/jobs" -H "Authorization: Bearer $TOK" \
       -H "Idempotency-Key: $KEY" -d "$BODY")
[ "$REPLAY" = 200 ] || { echo "idempotent replay returned $REPLAY"; exit 1; }

for _ in $(seq 1 900); do
  STATUS=$(curl -fsS "$BASE/v1/jobs/$ID" -H "Authorization: Bearer $TOK" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["status"])')
  [ "$STATUS" = succeeded ] && break
  sleep 0.2
done
[ "$STATUS" = succeeded ] || { echo "job did not succeed: $STATUS"; exit 1; }

curl -fsS "$BASE/v1/jobs/$ID" -H "Authorization: Bearer $TOK" | python3 -I -c '
import json, sys
j = json.load(sys.stdin)
r = j["result"]
# Provenance must be consistent whichever provider served the job.
assert r["output"]["generator"]["provider"] == r["provider"]
assert r["output"]["generator"]["is_mock"] is (r["provider"] == "mock")
print("provider:", r["provider"], "| model:", r["model"])
print("result:", json.dumps(r["output"], ensure_ascii=False, indent=2))'

CROSS=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/jobs/$ID" -H "Authorization: Bearer $OTHER")
[ "$CROSS" = 404 ] || { echo "cross-tenant read returned $CROSS"; exit 1; }
echo "smoke OK: job $ID succeeded, replay=200, cross-tenant=404"
