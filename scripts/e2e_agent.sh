#!/usr/bin/env bash
# End-to-end: a user asks the CRM assistant to create a task on a deal. The AI
# proposes it; nothing exists until the user confirms; the CRM then creates the
# task through the activities module with the user's rights.
set -euo pipefail
source "$(dirname "$0")/lib_crm_e2e.sh"

if [ "$(api "$CRM_URL/api/v1/sales/pipelines/" | json 'len(d)')" = 0 ]; then
  api -X POST "$CRM_URL/api/v1/sales/pipelines/" -d '{"name":"E2E","is_default":true,"stages":[{"name":"Новая","probability":10},{"name":"Выиграна","probability":100}]}' >/dev/null
fi
DEAL_ID=$(api -X POST "$CRM_URL/api/v1/sales/deals/" -d '{"title":"E2E assistant deal","amount":"1000.00"}' | json 'd["id"]')
tasks() {
  api "$CRM_URL/api/v1/activities/?subject_type=deal&subject_id=$DEAL_ID&kind=task" |
    json 'len(d if isinstance(d, list) else (d.get("results") or d.get("items") or []))'
}

RUN_ID=$(api -X POST "$CRM_URL/api/v1/ai/agent/runs/" -d "{\"request\":\"Поставь задачу перезвонить клиенту завтра\",\"deal_id\":\"$DEAL_ID\",\"language\":\"ru\"}" | json 'd["id"]')
for _ in $(seq 1 900); do
  BODY=$(api "$CRM_URL/api/v1/ai/agent/runs/$RUN_ID/")
  case "$(echo "$BODY" | json 'd["status"]')" in succeeded|failed) break ;; esac
  sleep 1
done
echo "--- proposed plan:"
echo "$BODY" | python3 -I -c '
import json, sys
d = json.load(sys.stdin)
print("run:", d["status"], "| provider:", d["provider"], "| requires_confirmation:", d["requires_confirmation"])
if d["status"] != "succeeded":
    print("error_code:", d["error_code"]); sys.exit(1)
print(d["summary"])
for a in d["actions"]:
    print(" [%s] %s %s -> %s" % (a["index"], a["tool"], a["args"], a["status"]))
assert d["actions"] and d["actions"][0]["tool"] == "create_task"'
[ "$(tasks)" = 0 ] || { echo "a task exists before confirmation!"; exit 1; }

RESULT=$(api -X POST "$CRM_URL/api/v1/ai/agent/runs/$RUN_ID/actions/0/confirm/")
echo "confirmed: $(echo "$RESULT" | json 'd["status"] + " " + str(d["result_ref"])')"
api -X POST "$CRM_URL/api/v1/ai/agent/runs/$RUN_ID/actions/0/confirm/" >/dev/null  # again: no second task
[ "$(tasks)" = 1 ] || { echo "expected exactly one task after confirmation, got $(tasks)"; exit 1; }
echo "e2e OK: request -> AI plan -> user confirmation -> task created once by the CRM"
