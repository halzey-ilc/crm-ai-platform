#!/usr/bin/env bash
# End-to-end: a deal is won with no recorded contact; the CRM's risk rules flag
# it and the AI Platform explains the signals neutrally (no accusations).
set -euo pipefail
source "$(dirname "$0")/lib_crm_e2e.sh"

if [ "$(api "$CRM_URL/api/v1/sales/pipelines/" | json 'len(d)')" = 0 ]; then
  api -X POST "$CRM_URL/api/v1/sales/pipelines/" -d '{"name":"E2E","is_default":true,"stages":[{"name":"Новая","probability":10},{"name":"Выиграна","probability":100}]}' >/dev/null
fi
DEAL_ID=$(api -X POST "$CRM_URL/api/v1/sales/deals/" -d '{"title":"E2E quiet win","amount":"1000.00"}' | json 'd["id"]')
api -X POST "$CRM_URL/api/v1/sales/deals/$DEAL_ID/win/" >/dev/null

echo "--- CRM risk rules (no AI):"
api "$CRM_URL/api/v1/risk/signals/" | json '"counts: " + json.dumps(d["counts"]) + " | is_evidence: " + str(d["is_evidence"])'

STATUS=$(api -o /dev/null -w '%{http_code}' -X POST "$CRM_URL/api/v1/ai/risk/review/" -d '{"language":"ru"}')
case "$STATUS" in 200|202) ;; *) echo "risk review request returned $STATUS"; exit 1 ;; esac

for _ in $(seq 1 900); do
  BODY=$(api "$CRM_URL/api/v1/ai/risk/review/")
  case "$(echo "$BODY" | json 'd["status"]')" in succeeded|failed) break ;; esac
  sleep 1
done
echo "--- AI risk review stored in the CRM:"
echo "$BODY" | DEAL_ID="$DEAL_ID" python3 -I -c '
import json, os, sys
d = json.load(sys.stdin)
print("review:", d["status"], "| provider:", d["provider"], "| is_mock:", d["is_mock"])
if d["status"] != "succeeded":
    print("error_code:", d["error_code"]); sys.exit(1)
r = d["result"]
print(r["headline"])
for it in r["items"]:
    print(" -", it["text"], "->", ", ".join(it["signal_refs"]))
for c in r["checks"]:
    print(" ?", c["text"])
assert r["is_evidence"] is False
refs = {ref for it in r["items"] for ref in it["signal_refs"]}
assert "deal:" + os.environ["DEAL_ID"] in refs, "the quiet win must be cited"'
echo "e2e OK: CRM risk rules -> neutral AI review -> CRM"
