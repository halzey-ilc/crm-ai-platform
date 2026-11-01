# Эксплуатация

Статусы — [STATUS_LEGEND.md](STATUS_LEGEND.md).

## 1. Требования

Go 1.27, Docker и Compose, GNU Make; для Toolkit — uv. GPU и платные сервисы не нужны.
Стек AI Platform полностью отделён от стека CRM: свой compose-проект
`crm-ai-platform`, своя сеть, свой PostgreSQL на `127.0.0.1:55432`.

## 2. Локальный запуск ✅

```bash
make env                 # .env с случайными секретами (не перезаписывает существующий)
make up                  # postgres → migrate → gateway (127.0.0.1:8090) + orchestrator
./scripts/smoke.sh       # создать задачу, дождаться результата, проверить изоляцию
make token TENANT=demo   # dev-токен для ручных запросов
make down                # остановить (данные в volume сохраняются)
```

Ручной запрос:

```bash
TOK=$(make -s token TENANT=demo)
curl -s -X POST localhost:8090/v1/jobs -H "Authorization: Bearer $TOK" \
  -H "Idempotency-Key: demo-0001" \
  -d '{"type":"call.summary.v1","input":{"transcript":"Добрый день. Перезвоню завтра.","language":"ru"}}'
```

### С локальной моделью вместо mock ✅

```bash
make up-llm   # скачивает модель (3 ГБ, sha256 проверяется), поднимает llama.cpp + orchestrator
make up       # вернуться на mock-провайдер
python3 -I evaluation/run_call_summary_eval.py --out=evaluation/reports/run.json
```

Нужно ~4 ГБ свободной RAM. На ноутбуке резюме занимает 2–4 минуты. Другие тяжёлые
процессы (minikube, второй стек) замедляют модель в разы.

### Вместе с CRM ✅

```bash
./scripts/e2e_with_crm.sh    # резюме звонка: нужен ../crm-platform с модулем ai_insights
./scripts/e2e_operations.sh  # правила CRM → объяснение AI (модули operations + ai_insights)
```

Скрипт поднимает этот стек, переподключает `api` и `worker` CRM через
`crm-platform/compose.ai.yaml` к сети `crm-ai-link`, где находится **только gateway**,
и передаёт общий ключ. Затем применяет миграции компаний CRM, создаёт локального
пользователя `ai-e2e` в компании `demo`, логирует звонок, запрашивает резюме и ждёт
результат.

## 3. Проверки

| Команда | Что проверяет | Внешние сервисы |
|---|---|---|
| `make lint` | gofmt, go vet (включая integration-код) | нет |
| `make test` | unit-тесты Go с `-race` | нет |
| `make test-integration` | PostgreSQL: идемпотентность, повторная обработка, ретраи, отмена, конкурентный claim, RLS, привилегии | PostgreSQL из `make up` |
| `make docker-build` | сборка трёх образов | Docker |
| `make toolkit-test` | ruff + pytest Toolkit | нет |
| `make toolkit-experiment` | воспроизводимый синтетический эксперимент | нет |

Интеграционные тесты создают временную базу `crmai_it_*` и удаляют её. Если
PostgreSQL недоступен, они **падают**, а не пропускаются.

## 4. Конфигурация

Все переменные — в `internal/config/config.go`; ошибки валидируются при старте
и перечисляются разом.

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `AI_ENV` | local | local / test / production |
| `AI_DATABASE_URL` | — | DSN роли `ai_app` |
| `AI_DATABASE_OWNER_URL`, `AI_DB_APP_PASSWORD` | — | только для `migrate` |
| `AI_AUTH_HS256_KEY` (`_PREVIOUS`) | — | ключ проверки токенов (≥ 32 байт) |
| `AI_AUTH_ISSUER` / `AI_AUTH_AUDIENCE` | crm-platform / crm-ai-platform | |
| `AI_AUTH_MAX_TOKEN_TTL` | 10m | максимум 1h |
| `AI_REQUEST_TIMEOUT` | 10s | |
| `AI_MAX_BODY_BYTES` | 1 MiB | |
| `AI_RATE_LIMIT_RPS` / `_BURST` | 20 / 40 | на пару (tenant, subject), в рамках одной реплики |
| `AI_MAX_UPLOAD_BYTES` | 50 MiB | записи звонков и файлы базы знаний; максимум 100 MiB |
| `AI_MODEL_PROVIDERS` | mock | `mock`, `llamacpp`, `whispercpp` через запятую; первый подходящий обслуживает задачу; в production mock запрещён |
| `AI_LLAMACPP_URL` / `_MODEL` | — / local-gguf | основная модель |
| `AI_LLAMACPP_LARGE_URL` / `_MODEL` / `_TASKS` | — | вторая, большая модель и список задач для неё через запятую (например `agent_plan_v3,deal_coach,risk_review`) |
| `AI_WHISPER_URL` / `_MODEL` | — / whisper-large-v3-turbo-q5_0 | распознавание речи (ru, en) |
| `AI_WHISPER_KY_URL` / `_KY_LANGUAGE` | — / auto | отдельная модель для кыргызского; без неё задачи `ky` завершаются ошибкой |
| `AI_EMBED_URL` / `_MODEL` | — / multilingual-e5-small-q8_0 | поиск по смыслу в базе знаний; без него — только по словам |
| `AI_AUDIO_RETENTION_HOURS` | 24 | записи звонков удаляются через столько часов |
| `AI_WORKER_CONCURRENCY` | 4 | |
| `AI_WORKER_POLL_INTERVAL` | 1s | |
| `AI_WORKER_LEASE` | 15m | ≥ максимальный тайм-аут задачи + 30 с |
| `AI_METRICS_ADDR` | — (выключено) | отдельный порт `/metrics` gateway; не может совпадать с API-портом |
| `AI_RETENTION_DAYS` | 30 | срок хранения завершённых задач; 0 — не удалять |

Любой секрет можно передать как `<NAME>_FILE`.

## 5. Мониторинг

- `GET /health/live` и `/health/ready` на gateway (:8090) и orchestrator (:8091).
  Readiness проверяет БД и версию схемы. ✅
- Логи JSON со `correlation_id`, `job_id`, `tenant`, `duration_ms`. ✅
- Метрики Prometheus ✅. Отдаёт их собственный экспортёр (`internal/metrics`), без внешних
  библиотек. В метках нет tenant и содержимого задач, пути нормализуются (`/v1/jobs/:id`,
  остальное — `other`).
  - gateway: `AI_METRICS_ADDR`, локально `127.0.0.1:9464/metrics`. Серии
    `ai_http_requests_total{method,route,status}` и `ai_http_request_duration_seconds{route}`.
    На публичном порту :8090 метрик нет.
  - orchestrator: `127.0.0.1:8091/metrics`. Серии:
    - `ai_jobs_finished_total{job_type,outcome,error_code}` и `ai_job_execution_seconds{job_type}`;
    - `ai_model_calls_total{task,provider,outcome}` и `ai_model_call_seconds{task,provider}`;
    - `ai_queue_jobs{status}` и `ai_queue_oldest_queued_seconds` (раз в 30 с через `ai_queue_stats()`);
    - `ai_retention_jobs_deleted_total` и `ai_retention_runs_total{outcome}`.
  - Что стоит настроить в алертах: `ai_queue_oldest_queued_seconds` > 600 (очередь не
    разбирается); рост `ai_jobs_finished_total{outcome="failed"}`;
    `ai_retention_runs_total{outcome="error"}` > 0.
- Трассировки OpenTelemetry: 📐.

## 6. Масштабирование и восстановление

- Gateway без состояния, масштабируется репликами (лимиты считаются на реплику).
- Orchestrator масштабируется репликами: claim через `FOR UPDATE SKIP LOCKED` безопасен
  (✅ `TestConcurrentClaimsAreDisjoint`).
- Падение воркера: задачи возвращаются в работу после истечения аренды (✅).
  При остановке по SIGTERM воркер перестаёт брать задачи и дожидается текущих.
- Миграции идемпотентны и сериализуются advisory lock; данные не удаляются (✅).
- Резервное копирование PostgreSQL AI Platform: 📐 (тот же подход, что в CRM:
  pgBackRest или снапшоты с **проверенным** восстановлением).
