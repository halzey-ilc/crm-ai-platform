#!/usr/bin/env bash
# End-to-end: the CRM (../crm-platform) asks the AI Platform for a call summary.
set -euo pipefail
source "$(dirname "$0")/lib_crm_e2e.sh"

CALL_ID=$(api -X POST "$CRM_URL/api/v1/telephony/calls/" -d '{
  "direction":"inbound","from_number":"+77011112233","to_number":"+77019998877",
  "status":"completed","started_at":"2026-10-28T09:00:00Z","duration_seconds":180,
  "notes":"Здравствуйте, это Айбек. Клиенту нужна интеграция с 1С и обучение сотрудников. Бюджет пока не утверждён. Договорились, что я перезвоню в пятницу и отправлю коммерческое предложение."
}' | json 'd["id"]')
echo "CRM call: $CALL_ID"

STATUS=$(api -o /dev/null -w '%{http_code}' -X POST "$CRM_URL/api/v1/ai/calls/$CALL_ID/summary/")
[ "$STATUS" = 202 ] || { echo "summary request returned $STATUS"; exit 1; }

for _ in $(seq 1 60); do
  BODY=$(api "$CRM_URL/api/v1/ai/calls/$CALL_ID/summary/")
  case "$(echo "$BODY" | json 'd["status"]')" in succeeded|failed) break ;; esac
  sleep 1
done
echo "$BODY" | python3 -I -c '
import json, sys
d = json.load(sys.stdin)
print("insight:", d["status"], "| provider:", d["provider"], "| model:", d["model"], "| is_mock:", d["is_mock"])
if d["status"] != "succeeded":
    print("error_code:", d["error_code"]); sys.exit(1)
print(json.dumps(d["result"], ensure_ascii=False, indent=2))'
echo "e2e OK: CRM -> AI Platform -> CRM"
