#!/usr/bin/env bash
# End-to-end: a customer writes to the company on WhatsApp; the CRM asks the AI
# Platform to classify the message and draft a reply, and stores the result.
# Nothing is sent to the customer.
set -euo pipefail
source "$(dirname "$0")/lib_crm_e2e.sh"

# A synthetic inbound conversation through the CRM's own ingest use case.
CONV_ID=$(crm_shell "
from datetime import UTC, datetime
from uuid import uuid4
from platform_core.infrastructure.tenancy.services import system_tenant_scope
from modules.communications.application.service import InboundMessage
from modules.communications.composition import conversation_service, conversation_repository
from modules.communications.domain.conversation import Channel
contact = 'e2e-' + uuid4().hex[:8]
with system_tenant_scope('$TENANT'):
    svc = conversation_service()
    for i, text in enumerate(['Здравствуйте! Интересует тариф Бизнес на 10 пользователей.',
                              'Хочу оформить заказ, что нужно сделать?']):
        svc.ingest_inbound(InboundMessage(channel=Channel.WHATSAPP, external_contact_id=contact,
            external_message_id=f'{contact}-{i}', text=text, occurred_at=datetime.now(UTC)))
    print(conversation_repository().get_by_contact(Channel.WHATSAPP, contact).id)
" | tail -1)
count_messages() {
  api "$CRM_URL/api/v1/communications/conversations/$CONV_ID/messages/" |
    json 'len(d if isinstance(d, list) else (d.get("items") or d.get("results") or []))'
}
COUNT_BEFORE=$(count_messages)

STATUS=$(api -o /dev/null -w '%{http_code}' -X POST "$CRM_URL/api/v1/ai/conversations/$CONV_ID/reply/" -d '{"language":"ru"}')
case "$STATUS" in 200|202) ;; *) echo "reply request returned $STATUS"; exit 1 ;; esac

for _ in $(seq 1 900); do
  BODY=$(api "$CRM_URL/api/v1/ai/conversations/$CONV_ID/reply/")
  case "$(echo "$BODY" | json 'd["status"]')" in succeeded|failed) break ;; esac
  sleep 1
done
echo "--- AI suggestion stored in the CRM:"
echo "$BODY" | python3 -I -c '
import json, sys
d = json.load(sys.stdin)
print("suggestion:", d["status"], "| provider:", d["provider"], "| is_mock:", d["is_mock"], "| auto_send:", d["auto_send"])
if d["status"] != "succeeded":
    print("error_code:", d["error_code"]); sys.exit(1)
r = d["result"]
print("intent:", r["intent"], "| urgency:", r["urgency"], "| needs_human:", r["needs_human"])
print("evidence:", r["evidence"])
print("draft:", r["draft_reply"])
assert d["auto_send"] is False'
COUNT_AFTER=$(count_messages)
[ "$COUNT_BEFORE" = "$COUNT_AFTER" ] || { echo "a message was added to the conversation!"; exit 1; }
echo "e2e OK: CRM conversation -> AI suggestion -> CRM (nothing sent to the customer)"
