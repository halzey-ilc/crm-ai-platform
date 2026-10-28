#!/usr/bin/env bash
# End-to-end: CRM operations rules find a forgotten lead and a deal without a
# next step; the CRM asks the AI Platform to explain them and stores the result.
set -euo pipefail
source "$(dirname "$0")/lib_crm_e2e.sh"

LEAD_ID=$(api -X POST "$CRM_URL/api/v1/leads/" -d '{"name":"E2E forgotten lead","email":"e2e-lead@example.test"}' | json 'd["id"]')
# Make the lead look untouched for 9 days (local test data only).
crm_shell "
from datetime import UTC, datetime, timedelta
from platform_core.infrastructure.tenancy.services import system_tenant_scope
from platform_core.application.context import require_tenant
from modules.leads.infrastructure.models import LeadRecord
with system_tenant_scope('$TENANT'):
    LeadRecord.objects.using(require_tenant().db_alias).filter(id='$LEAD_ID').update(
        updated_at=datetime.now(UTC) - timedelta(days=9))
" >/dev/null

echo "--- CRM rules (no AI):"
api "$CRM_URL/api/v1/operations/findings/" | json '"counts: " + json.dumps(d["counts"]) + " | thresholds: " + json.dumps(d["thresholds"])'

STATUS=$(api -o /dev/null -w '%{http_code}' -X POST "$CRM_URL/api/v1/ai/operations/digest/" -d '{"language":"ru"}')
case "$STATUS" in 200|202) ;; *) echo "digest request returned $STATUS"; exit 1 ;; esac

for _ in $(seq 1 900); do
  BODY=$(api "$CRM_URL/api/v1/ai/operations/digest/")
  case "$(echo "$BODY" | json 'd["status"]')" in succeeded|failed) break ;; esac
  sleep 1
done
echo "--- AI explanation stored in the CRM:"
echo "$BODY" | LEAD_ID="$LEAD_ID" python3 -I -c '
import json, os, sys
d = json.load(sys.stdin)
print("digest:", d["status"], "| provider:", d["provider"], "| is_mock:", d["is_mock"])
if d["status"] != "succeeded":
    print("error_code:", d["error_code"]); sys.exit(1)
r = d["result"]
print(r["headline"])
for item in r["items"]:
    print(" -", item["text"], "->", ", ".join(item["finding_refs"]))
for rec in r["recommendations"]:
    print(" *", rec["text"])
refs = {ref for item in r["items"] for ref in item["finding_refs"]}
assert "lead:" + os.environ["LEAD_ID"] in refs, "the forgotten lead must be cited"'
echo "e2e OK: CRM rules -> AI explanation -> CRM"
