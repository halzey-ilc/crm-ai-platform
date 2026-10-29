# Контракты API

Статусы — [STATUS_LEGEND.md](STATUS_LEGEND.md). Все контракты ниже ✅ (покрыты тестами
`internal/httpapi`, `contracts`, `tests/integration`).

## 1. Правила версионирования

- **HTTP.** Версия в пути (`/v1/`). Несовместимое изменение выходит как `/v2/`, `/v1`
  поддерживается до согласованной даты отключения.
- **Тела ответов** несут поле `schema` (`ai.job.v1`, `ai.error.v1`, `ai.job_types.v1`).
- **Тип задачи** версионирован в имени (`call.summary.v1`).
- **JSON Schema** входа и выхода лежат в `contracts/api/` и `contracts/model-schemas/`.
  Идентификатор схемы — имя файла без `.json` (`call_summary.input.v1`).
- **Опубликованная схема неизменяема.** Совместимо можно только добавить необязательное поле
  в **выход**. Вход закрыт (`additionalProperties: false`), поэтому новое поле во входе
  требует новой версии. Любое удаление или сужение поля — новая версия.
- **Схема ошибок** одна для всех эндпоинтов.

## 2. Аутентификация

`Authorization: Bearer <JWT HS256>`. Claims: `iss`, `aud`, `sub`, `tenant`,
`scope` (через пробел), `iat`, `exp`, `jti`. Срок жизни ≤ 10 мин по умолчанию.
Scopes: `ai.jobs.create`, `ai.jobs.read`, `ai.jobs.cancel`.

Tenant берётся **только из токена**. Необязательный заголовок `X-Tenant` должен
совпадать с ним, иначе 403 `tenant_mismatch`. Поле tenant в теле отклоняется
как неизвестное.

## 3. Эндпоинты

| Метод и путь | Scope | Успех | Описание |
|---|---|---|---|
| `GET /health/live` | — | 200 | Процесс жив |
| `GET /health/ready` | — | 200 / 503 | БД доступна и схема на ожидаемой версии |
| `POST /v1/jobs` | `ai.jobs.create` | 202 (создана) / 200 (повтор) | Создать задачу |
| `GET /v1/jobs/{id}` | `ai.jobs.read` | 200 | Статус и результат |
| `POST /v1/jobs/{id}/cancel` | `ai.jobs.cancel` | 202 | Отменить |
| `GET /v1/job-types` | `ai.jobs.read` | 200 | Каталог типов задач |

### POST /v1/jobs

Заголовки: `Idempotency-Key` (обязателен, 8–128 символов `[A-Za-z0-9._:-]`),
`X-Correlation-ID` (необязателен; принимается, только если безопасен для логов, иначе
генерируется новый).

```json
{ "type": "call.summary.v1",
  "input": { "transcript": "Здравствуйте. … Перезвоню в пятницу.", "language": "ru", "call_ref": "c0ffee-01" } }
```

Ответ `ai.job.v1` (вход **никогда** не возвращается):

```json
{ "schema": "ai.job.v1", "id": "4f41ba89-…", "type": "call.summary.v1", "tenant": "acme",
  "status": "queued", "attempts": 0, "max_attempts": 3, "cancel_requested": false,
  "correlation_id": "…", "created_at": "…", "updated_at": "…", "started_at": null, "finished_at": null,
  "error": null, "result": null }
```

После выполнения `result`:

```json
{ "schema_id": "call_summary.output.v1", "provider": "mock", "model": "mock-extractive-v1",
  "created_at": "…",
  "output": { "schema": "call_summary.output.v1", "summary": "…",
              "key_points": [{ "text": "…", "evidence_quote": "…" }],
              "next_step": { "text": "…", "evidence_quote": "…" } ,
              "language": "ru",
              "generator": { "provider": "mock", "model": "mock-extractive-v1", "is_mock": true } } }
```

`error` заполняется только у задач в статусе `failed`: `{code, message}`.
Сообщение не содержит фрагментов входа.

### Тип задачи `operations.digest.v1` (AI Operations) ✅

Объясняет сводку нарушений, которые CRM нашла **своими детерминированными правилами**
(`GET /api/v1/operations/findings/` в CRM). На вход идут только идентификаторы, виды
нарушений, возраст в днях и пороги компании, без имён, сумм и текста:

```json
{ "type": "operations.digest.v1",
  "input": { "language": "ru",
             "thresholds": { "stale_lead_days": 3, "stalled_deal_days": 14 },
             "counts": { "stale_lead": 2, "overdue_task": 1, "deal_without_next_step": 1, "stalled_deal": 0 },
             "findings": [ { "ref": "lead-1", "kind": "stale_lead", "age_days": 12 } ] } }
```

Выход `operations_digest.output.v1`: `headline`, `items[]` (`kind`, `text`,
`finding_refs`), `recommendations[]` (`text`, `finding_refs`), `insufficient_data`,
`language`, `generator`. Проверка привязки отклоняет выход, если он:
- ссылается на нарушение, которого нет во входе;
- ссылается на нарушение под чужим видом;
- описывает вид с нулевым количеством;
- при пустом входе не ставит `insufficient_data: true` (принцип 7, ADR-005).

### Тип задачи `kpi.explanation.v1` (AI Business Analyst) ✅

Вход (`kpi_explanation.input.v1`): период, прошлый период и показатели
`{code, name, unit: count|percent|money, current, previous, change, change_pct}`. Все
значения и изменения **посчитала CRM**, модель ничего не вычисляет (принцип 2).

Выход (`kpi_explanation.output.v1`): `headline`, `observations[]` и `recommendations[]`
(у каждого `metric_codes`, `text`), `insufficient_data`, `language`, `generator`.

Проверка отклоняет выход, если:
- в любом тексте есть число, которого нет во входе (допускаются округление, модуль
  значения и числа из дат периода);
- указан неизвестный показатель;
- флаг `insufficient_data` не совпадает со входом (данных нет — значит текста быть не должно).

Провайдер `llamacpp` ограничивает коды показателей грамматикой и отбрасывает пункты
с посторонними числами.

### Тип задачи `message.reply.v1` (AI Communication Assistant) 🟠

Вход (`message_reply.input.v1`): `language`, `channel` и до 20 последних сообщений
переписки `{ref, direction: inbound|outbound, text}`, от старых к новым; последнее — от
клиента. Телефон, ник и имя клиента в контракт **не входят**.

Выход (`message_reply.output.v1`): `intent` (question, price_request, order,
meeting_request, complaint, cancellation, gratitude, other), `urgency` (low, normal, high),
`evidence {message_ref, quote}`, `draft_reply` (или `null`), `needs_human`, `language`,
`generator`.

Черновик **никогда не отправляется автоматически**: его показывает CRM, отправляет человек.

Проверка отклоняет выход, если:
- цитата не является точным фрагментом указанного сообщения, или сообщение не клиентское;
- намерение указано без доказательства (кроме `other`);
- жалоба или отказ помечены как не требующие человека;
- в черновике есть число, ссылка или e-mail, которых нет в переписке (цены, сроки, скидки
  — только от человека или из CRM).

Провайдер `llamacpp` ограничивает `message_ref` грамматикой клиентскими сообщениями, а
черновик с посторонними фактами не отдаёт (`draft_reply: null`, `needs_human: true`).

## 4. Ошибки (`ai.error.v1`)

```json
{ "schema": "ai.error.v1",
  "error": { "code": "invalid_input", "message": "…", "correlation_id": "…", "details": ["/language: enum"] } }
```

`details` указывает место и нарушенное ключевое слово схемы, но **не значения**.

| HTTP | code | Когда |
|---|---|---|
| 400 | `idempotency_key_required`, `invalid_json`, `invalid_request` | Неверный запрос |
| 401 | `unauthenticated` | Нет токена, токен неверный или истёк (причина только в логах сервера) |
| 403 | `forbidden`, `tenant_mismatch` | Нет scope или `X-Tenant` не совпадает с токеном |
| 404 | `not_found` | Нет задачи **или задача другой компании** (ответ одинаковый) |
| 409 | `idempotency_conflict`, `not_cancellable` | Ключ уже использован с другим телом; задача уже завершена |
| 413 | `payload_too_large` | Тело больше `AI_MAX_BODY_BYTES` |
| 422 | `unknown_job_type`, `invalid_input` | Тип или вход не соответствует контракту |
| 429 | `rate_limited` | Превышен лимит (tenant, subject) |
| 500 | `internal_error` | Без деталей |
| 503 | `timeout` | Тайм-аут запроса |

Коды ошибок задачи (`job.error.code`): `output_contract_violation`,
`output_not_grounded`, `provider_error`, `unknown_job_type`,
`attempts_exhausted`, `internal_error`.

## 5. События

📐 Событийных контрактов пока нет: внешнего канала событий нет ни у CRM, ни у
AI Platform. Каталог `contracts/events/` будет создан вместе с первым событием
(см. [CRM_INTEGRATION.md §3.3](CRM_INTEGRATION.md)).
