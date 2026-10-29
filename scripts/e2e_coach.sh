#!/usr/bin/env bash
# End-to-end: a deal with a note and an overdue task; the CRM asks the AI
# Platform for a brief of the next conversation and stores it. Every point must
# cite a record of this deal.
set -euo pipefail
source "$(dirname "$0")/lib_crm_e2e.sh"

if [ "$(api "$CRM_URL/api/v1/sales/pipelines/" | json 'len(d)')" = 0 ]; then
  api -X POST "$CRM_URL/api/v1/sales/pipelines/" -d '{"name":"E2E","is_default":true,"stages":[{"name":"Новая","probability":10},{"name":"Переговоры","probability":50},{"name":"Выиграна","probability":100}]}' >/dev/null
fi
DEAL_ID=$(api -X POST "$CRM_URL/api/v1/sales/deals/" -d '{"title":"E2E внедрение CRM","amount":"450000.00","expected_close_date":"2026-10-01"}' | json 'd["id"]')
api -X POST "$CRM_URL/api/v1/activities/" -d "{\"kind\":\"note\",\"subject_type\":\"deal\",\"subject_id\":\"$DEAL_ID\",\"body\":\"Клиент просит скидку 10% и интеграцию с 1С.\"}" >/dev/null
TASK_ID=$(api -X POST "$CRM_URL/api/v1/activities/" -d "{\"kind\":\"task\",\"subject_type\":\"deal\",\"subject_id\":\"$DEAL_ID\",\"title\":\"Отправить КП\"}" | json 'd["id"]')
# Make the task overdue (local test data only).
crm_shell "
from datetime import UTC, datetime, timedelta
from platform_core.infrastructure.tenancy.services import system_tenant_scope
from platform_core.application.context import require_tenant
from modules.activities.infrastructure.models import ActivityRecord
with system_tenant_scope('$TENANT'):
    ActivityRecord.objects.using(require_tenant().db_alias).filter(id='$TASK_ID').update(
        due_date=datetime.now(UTC).date() - timedelta(days=3))
" >/dev/null

STATUS=$(api -o /dev/null -w '%{http_code}' -X POST "$CRM_URL/api/v1/ai/deals/$DEAL_ID/brief/" -d '{"language":"ru"}')
case "$STATUS" in 200|202) ;; *) echo "brief request returned $STATUS"; exit 1 ;; esac

for _ in $(seq 1 900); do
  BODY=$(api "$CRM_URL/api/v1/ai/deals/$DEAL_ID/brief/")
  case "$(echo "$BODY" | json 'd["status"]')" in succeeded|failed) break ;; esac
  sleep 1
done
echo "--- AI brief stored in the CRM:"
echo "$BODY" | DEAL_ID="$DEAL_ID" TASK_ID="$TASK_ID" python3 -I -c '
import json, os, sys
d = json.load(sys.stdin)
print("brief:", d["status"], "| provider:", d["provider"], "| is_mock:", d["is_mock"])
if d["status"] != "succeeded":
    print("error_code:", d["error_code"]); sys.exit(1)
r = d["result"]
for section in ("situation", "risks", "questions"):
    for p in r[section]:
        print(f" [{section}]", p["text"], "->", ", ".join(p["refs"]))
if r["next_step"]:
    print(" [next_step]", r["next_step"]["text"])
refs = {ref for s in ("situation", "risks", "questions") for p in r[s] for ref in p["refs"]}
assert "deal:" + os.environ["DEAL_ID"] in refs, "the deal must be cited"
if d["is_mock"]:
    assert "activity:" + os.environ["TASK_ID"] in refs, "the overdue task must be flagged"'
echo "e2e OK: CRM deal -> AI brief -> CRM"
