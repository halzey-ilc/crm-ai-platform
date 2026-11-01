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

### Тип задачи `call.summary.v2` (AI Call Intelligence) ✅

Вход тот же (`call_summary.input.v1`). Выход `call_summary.output.v2` = v1 + `insufficient_data`.

- Если в тексте меньше 3 слов или 20 букв, модель не вызывается: `insufficient_data: true`,
  `key_points: []`, `next_step: null`, `summary` — фиксированное уведомление.
- Если модель не нашла ни одного пункта с подтверждённой цитатой, ставится тот же флаг,
  а свободный текст `summary` заменяется уведомлением. Резюме без фактов не выдаётся.

Проверка отклоняет выход, если:
- текст слишком короткий, а флаг не стоит;
- флаг не совпадает с пустотой пунктов;
- при флаге `summary` не уведомление.

`call.summary.v1` остаётся: опубликованная версия не меняется.
CRM запрашивает v2 и принимает результаты v1 по задачам, отправленным до перехода.

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

### Тип задачи `deal.coach.v1` (AI Sales Coach) 🟠

Вход (`deal_coach.input.v1`):
- `today`;
- сделка `{ref, title, status, stage, stage_position, stage_count, probability, amount,
  currency, expected_close_date, age_days}`;
- до 40 записей истории `{ref, kind: note|task|call|meeting, date, title, text, status}`.

CRM собирает всё это с правами запросившего пользователя и сама считает статусы,
например просроченные задачи. Телефонов во входе нет.

Выход (`deal_coach.output.v1`): `situation[]`, `risks[]`, `questions[]` и `next_step`,
каждый пункт — `{text, refs}`; плюс `insufficient_data`, `language`, `generator`.

Проверка отклоняет выход, если:
- ссылка указывает не на запись из входа;
- в тексте есть число, которого нет во входе: в значениях сделки, датах или текстах истории;
- `insufficient_data` не совпадает со входом (нет истории — значит нет рисков, вопросов и шага).

Провайдер `llamacpp`:
- ограничивает `refs` грамматикой;
- отбрасывает пункты с выдуманными числами;
- при пустой истории модель не вызывает.

### Тип задачи `risk.review.v1` (AI Risk & Anomaly Detection) 🟠

Вход (`risk_review.input.v1`): `window_days`, `counts` и сигналы `{ref, kind, value, threshold}`.
Сигналы посчитали правила CRM. Ссылки только вида `deal:` и `user:`, без имён и текстов.

Выход (`risk_review.output.v1`): `headline`, `items[]` (`kind`, `text`, `signal_refs`),
`checks[]` (что проверить), `insufficient_data`, `is_evidence: false` (всегда), `language`,
`generator`.

Проверка отклоняет выход, если:
- ссылка не относится к сигналу этого вида;
- в тексте есть число, которого нет во входе;
- текст обвиняет или предполагает нарушение («мошенничество», «нарушает», fraud, steal…):
  сигнал — повод проверить, а не доказательство (ADR-005).

Провайдер `llamacpp` такие тексты отбрасывает, а обвиняющий заголовок заменяет нейтральным.

### Тип задачи `agent.plan.v1` (AI Agent Runtime) 🟠

Вход (`agent_plan.input.v1`):
- `today`;
- `request` — текст пользователя;
- `tools` — инструменты, разрешённые **этому** пользователю: `create_task`, `log_note`,
  `request_deal_brief`;
- `subjects` — записи, которые пользователь назвал. Для сделки передаётся название, клиент
  передаётся только как `customer`, без имени.

Выход (`agent_plan.output.v1`): `summary`, `actions[]` (поля зависят от инструмента, у
каждого действия есть `reason`), `insufficient_data`, `requires_confirmation: true`
(всегда), `language`, `generator`.

**AI Platform ничего не выполняет.** CRM запускает каждое действие только после
подтверждения пользователя, через сервис модуля и с правами этого пользователя.

Проверка отклоняет выход, если:
- инструмент не был предложен;
- запись не из входа;
- бриф запрошен не для сделки;
- срок задачи в прошлом или дальше чем через год.

`llamacpp` ограничивает грамматикой инструменты и записи, а неверные действия отбрасывает.

### Тип задачи `agent.plan.v2` 🟠 (на стороне AI; CRM ещё не подключена)

`agent.plan.v1` плюс два инструмента:
- `schedule_meeting {subject_ref, title, start: "YYYY-MM-DDTHH:MM" (местное время),
  duration_minutes: 15..480}`;
- `move_deal_stage {subject_ref, stage}`.

Для сделок CRM передаёт текущий `stage` и `stages` её воронки.

Проверка отклоняет:
- встречу раньше сегодняшнего дня или позже чем через год;
- длительность вне диапазона;
- этап не из воронки сделки или текущий этап.

`llamacpp` ограничивает этапы грамматикой и просит не угадывать дату, если пользователь её
не назвал. v1 не меняется. Чтобы CRM предлагала и выполняла эти инструменты, нужна доработка
CRM (модули meetings и sales_pipeline), см. «Отложено» в roadmap.

### Тип задачи `agent.plan.v3` 🟠 (диалог; на стороне AI, CRM ещё не подключена)

`agent.plan.v2` плюс `history` — до 10 предыдущих реплик того же разговора, от старых к новым:
`{role: "user"|"assistant", text}`. Так пользователь уточняет предложение: «назначь встречу в
понедельник в 15:00» → «лучше во вторник, в то же время» → встреча на вторник 15:00.

- **Разговор хранит CRM**, AI Platform его не сохраняет: история приходит в каждом запросе.
  Права, срок хранения и удаление переписки — на стороне CRM.
- Для реплик ассистента CRM пишет, что было предложено: **имя инструмента и его значения**,
  например `Предложено: schedule_meeting 2026-11-02T15:00, 60 мин`. По ним провайдер
  отбрасывает повтор уже предложенного (тот же инструмент и то же значение); перенос на
  другое время — новое предложение и остаётся.
- Действия предлагаются **только для последней просьбы**; история лишь объясняет «её»,
  «то же время», «эту сделку». Ничего не выполняется без подтверждения, как в v2.
- Слова про дни («завтра», «в пятницу», «tomorrow», «шейшемби») переводит в даты код, не
  модель. Заметку и справку агент предлагает, только если о них есть слова в просьбе
  («запиши», «заметку», «бриф», «справку», «note», «brief», «эскертме»…). Это действует и
  для v2.
- Выход — тот же `agent_plan.output.v2`, с теми же проверками и отказом `can_do`.

### Knowledge Assistant: документы и `knowledge.answer.v1` 🟠 (на стороне AI; CRM ещё не подключена)

```
PUT    /v1/knowledge/documents/{ref}   scope ai.knowledge.write
       {title, language: ru|ky|en, access_tags: [...], text}  -> {ref, passages, changed}
DELETE /v1/knowledge/documents/{ref}   scope ai.knowledge.write  -> 204 | 404
POST   /v1/knowledge/ask               scope ai.jobs.create, Idempotency-Key
       {question, language, access_tags: [...]}  -> 202 job (knowledge.answer.v1); poll GET /v1/jobs/{id}
```

**Документы.**
- Текст до 200 000 символов. Режется на фрагменты до 800 символов по абзацам и предложениям,
  каждый фрагмент — дословный кусок документа.
- Повторная отправка того же текста не переиндексирует документ, но метки доступа обновляет
  сразу.

**Метки доступа:** `all`, `role:<роль>`, `dept:<id>`, `user:<id>`.
- Метки документа и метки спрашивающего задаёт CRM: она знает роли и отделы.
- Поиск идёт **только** по документам, у которых есть хотя бы одна общая метка со
  спрашивающим. Фильтр — часть того же SQL-запроса, и модель не видит фрагментов, которые
  человек не мог бы открыть.
- Документы других компаний недоступны (RLS).

**Поиск:** полнотекстовый в PostgreSQL (`simple`). Слова вопроса обрезаются до префикса —
грубая замена стемминга для ru/ky. Семантический поиск (эмбеддинги) — 📐, нужен GPU.

**Ответ** (`knowledge_answer.output.v1`) — только `statements[]`: `{text, passage_ref, quote}`.
Свободного текста нет. Проверка отклоняет:
- ссылку на фрагмент не из входа;
- цитату, которой нет в фрагменте;
- `insufficient_data`, не совпадающий с пустотой ответа.

Если ничего не найдено, модель не вызывается и возвращается `insufficient_data: true`.

### Записи звонков и `call.transcribe.v1` 🟠 (на стороне AI; CRM ещё не подключена)

```
PUT    /v1/audio/{ref}   scope ai.audio.write, тело — сам файл, Content-Type: audio/*
       (wav, mpeg/mp3, ogg, mp4/m4a, flac, webm)  -> {ref, bytes, sha256, changed}
DELETE /v1/audio/{ref}   scope ai.audio.write  -> 204 | 404
POST   /v1/jobs          {"type":"call.transcribe.v1","input":{
                           "audio_ref": "<ref>", "language": "ru"|"ky"|"en",
                           "channels": "mono"|"manager_left"|"manager_right", "call_ref"?}}
```

**Записи.**
- До 50 МБ (`AI_MAX_AUDIO_BYTES`, не больше 100 МБ); у остальных запросов свой, маленький лимит.
- Хранятся в PostgreSQL с изоляцией по компаниям (RLS) и **удаляются через 24 часа**
  (`AI_AUDIO_RETENTION_HOURS`) — их нужно только расшифровать. CRM может удалить раньше.
- В задаче — только ссылка на запись, сам звук в задачу не попадает.

**Каналы.** В телефонии обычно стерео: менеджер в одном канале, клиент в другом. Тогда
`manager_left` или `manager_right`, и у каждой реплики будет `speaker: manager|customer`.
Для моно-записи `speaker` всегда `unknown`: разделение голосов в одном канале — 📐.

**Ответ** (`call_transcript.output.v1`): `segments[]` `{start_s, end_s, speaker, text}` по
порядку, `text` — все реплики подряд (готовый вход для `call.summary.v2`), `duration_s`,
`insufficient_data` (речи нет — сегментов нет). Проверка отклоняет: другой язык, чем
запрошен; реплики не по порядку или за пределами записи; говорящего в моно-записи; `text`,
не совпадающий с репликами. Тишина и теги вида `[BLANK_AUDIO]` отбрасываются.

**Языки.** Модель — Whisper (whisper.cpp). **В Whisper нет кыргызского**: для `ky` нужен
отдельный сервер с дообученной моделью (`AI_WHISPER_KY_URL`); без него задача сразу
завершается ошибкой `provider_error`, а не возвращает текст на другом языке.

Это машинная расшифровка: в ней бывают ошибки, и сама по себе она не доказательство.

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
