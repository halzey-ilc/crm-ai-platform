# Shared setup for the CRM end-to-end scripts (source it, do not run it).
#
# Starts this stack, reconnects the CRM api/worker through
# crm-platform/compose.ai.yaml (gateway-only network crm-ai-link), applies the
# CRM tenant migrations and mints a token for a local e2e user (owner of
# $TENANT). Defines: CRM_URL, TENANT, TOKEN, COMPOSE, KEY, api(), json(), crm_shell().
# Secrets travel through the environment and are never printed.

AI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CRM_DIR="${CRM_DIR:-$AI_DIR/../crm-platform}"
CRM_URL="${CRM_URL:-http://127.0.0.1:8000}"
TENANT="${TENANT:-demo}"

# AI_USE_LLM=1 forces the local model, AI_USE_LLM=0 the mock; unset keeps the
# local model if it is running (plain `make up` would switch back to the mock).
if [ "${AI_USE_LLM:-}" = 1 ] || { [ "${AI_USE_LLM:-}" != 0 ] && docker ps --format '{{.Names}}' | grep -qx crm-ai-platform-llm-1; }; then
  (cd "$AI_DIR" && make -s up-llm >/dev/null)
else
  (cd "$AI_DIR" && make -s up >/dev/null)
fi
KEY=$(grep '^AI_AUTH_HS256_KEY=' "$AI_DIR/.env" | cut -d= -f2-)
COMPOSE=(docker compose -f compose.yaml -f compose.ai.yaml)

crm_compose() { (cd "$CRM_DIR" && AI_PLATFORM_TOKEN_KEY="$KEY" "${COMPOSE[@]}" "$@"); }
crm_shell() { crm_compose exec -T api python manage.py shell -c "$1"; }

crm_compose up -d --build --wait api worker >/dev/null
crm_compose exec -T api python manage.py migrate_tenants >/dev/null

mint_token() {
crm_shell "
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
" | tail -1
}
TOKEN=$(mint_token)
TOKEN_AT=$SECONDS

# CRM access tokens are short-lived on purpose; a slow local model outlives them,
# so re-mint before they expire instead of weakening the CRM's settings.
api() {
  if [ $((SECONDS - TOKEN_AT)) -gt 120 ]; then TOKEN=$(mint_token); TOKEN_AT=$SECONDS; fi
  curl -fsS -H "Authorization: Bearer $TOKEN" -H "X-Tenant: $TENANT" -H 'Content-Type: application/json' "$@"
}
json() { python3 -I -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
