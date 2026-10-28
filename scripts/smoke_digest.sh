#!/usr/bin/env bash
# Live check of operations.digest.v1 against the running stack (make up).
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; source .env; set +a
BASE=${AI_GATEWAY_URL:-http://127.0.0.1:8090}
TOK=$(go run ./tools/devtoken -tenant smoke-ops)
BODY='{"type":"operations.digest.v1","input":{"language":"ru",
 "thresholds":{"stale_lead_days":3,"stalled_deal_days":14},
 "counts":{"stale_lead":2,"overdue_task":1,"deal_without_next_step":1,"stalled_deal":0},
 "findings":[{"ref":"lead-1","kind":"stale_lead","age_days":12},{"ref":"lead-2","kind":"stale_lead","age_days":4},
             {"ref":"task-1","kind":"overdue_task","age_days":3},{"ref":"deal-1","kind":"deal_without_next_step","age_days":6}]}}'
ID=$(curl -fsS -X POST "$BASE/v1/jobs" -H "Authorization: Bearer $TOK" -H "Idempotency-Key: ops-$(date +%s%N)" \
  -d "$BODY" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["id"])')
for _ in $(seq 1 50); do
  OUT=$(curl -fsS "$BASE/v1/jobs/$ID" -H "Authorization: Bearer $TOK")
  [ "$(echo "$OUT" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["status"])')" = succeeded ] && break
  sleep 0.2
done
echo "$OUT" | python3 -I -c 'import json,sys; d=json.load(sys.stdin); print(d["status"]); print(json.dumps(d["result"]["output"], ensure_ascii=False, indent=2))'
