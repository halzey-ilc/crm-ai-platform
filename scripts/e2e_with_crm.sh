#!/usr/bin/env bash
# End-to-end check: CRM (../crm-platform) asks the AI Platform for a call summary.
#
# Needs both local stacks. It:
#   1. starts this stack (make up) and reconnects the CRM api/worker to it
#      through crm-platform/compose.ai.yaml, sharing the HS256 key;
#   2. applies tenant migrations in the CRM (adds the ai_insights table);
#   3. ensures a local e2e user (owner of the "demo" tenant) and mints its token;
#   4. logs a call with notes, requests an AI summary, waits for the result.
# Secrets are passed through the environment and never printed.
set -euo pipefail
AI_DIR="$(cd "$(dirname "$0")/.." && pwd)"
CRM_DIR="${CRM_DIR:-$AI_DIR/../crm-platform}"
CRM_URL="${CRM_URL:-http://127.0.0.1:8000}"
TENANT="${TENANT:-demo}"

cd "$AI_DIR"
make -s up >/dev/null
KEY=$(grep '^AI_AUTH_HS256_KEY=' .env | cut -d= -f2-)

cd "$CRM_DIR"
COMPOSE=(docker compose -f compose.yaml -f compose.ai.yaml)
AI_PLATFORM_TOKEN_KEY="$KEY" "${COMPOSE[@]}" up -d --build --wait api worker >/dev/null
AI_PLATFORM_TOKEN_KEY="$KEY" "${COMPOSE[@]}" exec -T api python manage.py migrate_tenants >/dev/null

TOKEN=$(AI_PLATFORM_TOKEN_KEY="$KEY" "${COMPOSE[@]}" exec -T api python manage.py shell -c "
import secrets
from django.contrib.auth import get_user_model
from rest_framework_simplejwt.tokens import AccessToken
from modules.organizations.infrastructure.models import Membership, Organization
user, created = get_user_model().objects.get_or_create(
    username='ai-e2e', defaults={'email': 'ai-e2e@example.test'})
if created:
    user.set_password(secrets.token_urlsafe(32)); user.save()
org = Organization.objects.get(slug='$TENANT')
Membership.objects.get_or_create(user=user, organization=org, defaults={'role': 'owner'})
print(AccessToken.for_user(user))
" | tail -1)

api() { curl -fsS -H "Authorization: Bearer $TOKEN" -H "X-Tenant: $TENANT" -H 'Content-Type: application/json' "$@"; }
json() { python3 -I -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

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
  STATE=$(echo "$BODY" | json 'd["status"]')
  case "$STATE" in succeeded|failed) break ;; esac
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
