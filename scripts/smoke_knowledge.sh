#!/usr/bin/env bash
# Knowledge Assistant against the running stack (no CRM needed): put two
# documents with different access tags, ask as a sales rep and as HR, and
# check that each sees only their own document. Synthetic documents only.
set -euo pipefail
cd "$(dirname "$0")/.."
GW="${GW:-http://127.0.0.1:8090}"
TENANT="smoke-kb-$(date +%s)"
set -a; source .env; set +a   # the dev signing key; never printed
token() { go run ./tools/devtoken -tenant "$TENANT" -sub user:smoke -scopes "$1"; }
WRITE=$(token "ai.knowledge.write")
ASK=$(token "ai.jobs.create ai.jobs.read")
req() { curl -fsS -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-$RANDOM$RANDOM" "${@:2}"; }

req "$WRITE" -X PUT "$GW/v1/knowledge/documents/sales-policy" -d '{"title":"Скидки","language":"ru","access_tags":["role:sales_rep","role:manager"],
  "text":"Скидка для новых клиентов составляет не более 10 процентов.\n\nСкидку утверждает руководитель отдела продаж."}' >/dev/null
req "$WRITE" -X PUT "$GW/v1/knowledge/documents/hr-policy" -d '{"title":"Отпуска","language":"ru","access_tags":["role:hr"],
  "text":"Отпуск согласуется за две недели. Скидка сотрудникам на продукцию не предусмотрена."}' >/dev/null

ask_as() {
  local id status body
  id=$(req "$ASK" -X POST "$GW/v1/knowledge/ask" -d "{\"question\":\"Какая скидка для новых клиентов?\",\"language\":\"ru\",\"access_tags\":[\"$1\"]}" |
    python3 -I -c 'import json,sys; print(json.load(sys.stdin)["id"])')
  for _ in $(seq 1 600); do
    body=$(req "$ASK" "$GW/v1/jobs/$id")
    status=$(echo "$body" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["status"])')
    case "$status" in succeeded|failed) break ;; esac
    sleep 1
  done
  echo "$body" | ROLE="$1" python3 -I -c '
import json, os, sys
d = json.load(sys.stdin)
out = d["result"]["output"]
refs = sorted({s["passage_ref"].split("#")[0] for s in out["statements"]})
print(os.environ["ROLE"], "->", d["status"], "| documents cited:", refs or "none", "| insufficient_data:", out["insufficient_data"])
for s in out["statements"]:
    print("   ", s["text"], "<-", s["passage_ref"])
print(refs, file=sys.stderr)'
}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
ask_as role:sales_rep 2>"$TMP/rep"
ask_as role:hr 2>"$TMP/hr"
grep -q "sales-policy" "$TMP/rep" && ! grep -q "hr-policy" "$TMP/rep" || { echo "sales rep saw the wrong documents"; exit 1; }
! grep -q "sales-policy" "$TMP/hr" || { echo "HR saw the sales policy"; exit 1; }
echo "smoke OK: each role sees only the documents it may open"
