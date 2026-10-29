#!/usr/bin/env bash
# End-to-end: the CRM computes KPIs for this month and the previous one, the AI
# Platform explains them, the CRM stores the explanation. Every number in the
# AI's texts must be one of the CRM's numbers (checked again here).
#
# Seeds a few synthetic KPI facts into the local demo tenant (marked e2e).
set -euo pipefail
source "$(dirname "$0")/lib_crm_e2e.sh"

crm_shell "
import uuid
from datetime import UTC, datetime, timedelta
from decimal import Decimal
from platform_core.infrastructure.tenancy.services import system_tenant_scope
from platform_core.application.context import require_tenant
from modules.sales_kpi.infrastructure.models import MetricFact
now = datetime.now(UTC)
this_month = now.replace(day=5, hour=10, minute=0, second=0, microsecond=0)
last_month = (this_month.replace(day=1) - timedelta(days=1)).replace(day=5)
facts = [('revenue_won', '400000', last_month), ('revenue_won', '600000', last_month),
         ('revenue_won', '1250000', this_month),
         ('deal_won', '1', last_month), ('deal_won', '1', last_month), ('deal_closed', '1', last_month),
         ('deal_closed', '1', last_month), ('deal_closed', '1', last_month),
         ('deal_won', '1', this_month), ('deal_closed', '1', this_month), ('deal_closed', '1', this_month),
         ('deal_closed', '1', this_month), ('deal_closed', '1', this_month)]
with system_tenant_scope('$TENANT'):
    alias = require_tenant().db_alias
    if not MetricFact.objects.using(alias).filter(metric='revenue_won', value=Decimal('1250000')).exists():
        for metric, value, when in facts:
            MetricFact.objects.using(alias).create(source_event_id=uuid.uuid4(), metric=metric,
                                                   value=Decimal(value), occurred_at=when)
" >/dev/null

STATUS=$(api -o /dev/null -w '%{http_code}' -X POST "$CRM_URL/api/v1/ai/analytics/kpi-explanation/" \
  -d '{"period":"month","language":"ru"}')
case "$STATUS" in 200|202) ;; *) echo "kpi explanation request returned $STATUS"; exit 1 ;; esac

for _ in $(seq 1 900); do
  BODY=$(api "$CRM_URL/api/v1/ai/analytics/kpi-explanation/")
  case "$(echo "$BODY" | json 'd["status"]')" in succeeded|failed) break ;; esac
  sleep 1
done
echo "$BODY" | python3 -I "$(dirname "$0")/print_kpi_report.py"
echo "e2e OK: CRM KPIs -> AI explanation -> CRM"
