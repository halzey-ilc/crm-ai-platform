# Архитектура CRM AI Platform

Статусы — см. [STATUS_LEGEND.md](STATUS_LEGEND.md). Решения — в [DECISIONS/](DECISIONS/).

## 1. Назначение и границы

AI Platform — отдельный проект, интеллектуальная часть CRM-экосистемы. Она
**не** является источником истины для бизнес-данных: клиенты, лиды, сделки,
звонки, права доступа и бизнес-правила остаются в CRM Backend
(`/home/user/crm-platform`, Django). AI Platform получает от CRM разрешённые
данные, выполняет AI-задачи и возвращает структурированные результаты.
Любые изменения бизнес-данных выполняет CRM через свои интерфейсы.

## 2. Компоненты

```
             CRM Backend (Django)                       AI Platform
 ┌──────────────────────────────────┐      ┌──────────────────────────────────────────┐
 │ пользователь → JWT + X-Tenant    │      │  AI Gateway (Go)                          │
 │ проверка членства и RBAC         │─────▶│   токен HS256: tenant, sub, scopes        │
 │ выпуск короткого сервис-токена   │ HTTP │   лимиты, тайм-ауты, correlation id       │
 │ (📐 требует изменения CRM)       │      │   валидация контракта входа               │
 └──────────────────────────────────┘      │        │ jobs.Service (идемпотентность)    │
                                           │        ▼                                  │
                                           │  PostgreSQL (метаданные AI)               │
                                           │   ai_job / ai_job_result + RLS по tenant  │
                                           │        ▲  ai_claim_jobs (SKIP LOCKED)     │
                                           │        │                                  │
                                           │  AI Orchestrator (Go)                     │
                                           │   аренда, тайм-ауты, ретраи, отмена       │
                                           │        │                                  │
                                           │  Model Gateway (Go)                       │
                                           │   task → provider; проверка схемы выхода  │
                                           │   и привязки к входу (grounding)          │
                                           │        │                                  │
                                           │  Providers: mock ✅ │ локальный runtime 📐 │
                                           │             внешний LLM API 📐 (по согл.) │
                                           └──────────────────────────────────────────┘
  Training Toolkit (Python, изолирован): датасеты → проверки → обучение → отчёт → candidate
```

| Компонент | Язык | Где | Статус |
|---|---|---|---|
| AI Gateway | Go | `services/gateway`, `internal/httpapi`, `internal/auth` | ✅ |
| AI Job System (домен, use cases) | Go | `internal/jobs` | ✅ |
| AI Orchestrator (воркер) | Go | `services/orchestrator`, `internal/orchestrator` | ✅ |
| Model Gateway + mock provider | Go | `internal/modelgw`, `internal/modelgw/mock` | ✅ |
| Каталог типов задач | Go | `internal/jobtypes` | ✅ (1 тип: `call.summary.v1`) |
| Контракты (JSON Schema) | JSON | `contracts/` | ✅ |
| Хранилище (PostgreSQL + RLS) | SQL/Go | `internal/store/postgres` | ✅ |
| Миграции | Go | `services/migrate` | ✅ |
| Training Toolkit | Python | `training/toolkit` | ✅ (синтетический эксперимент) |
| Model Registry | — | — | 🟠 метаданные запуска в `manifest.json`; реестр как сервис 📐 |
| Model Serving (C++ runtime) | C++ | — | 📐 этап G |
| Audio processing (Rust) | Rust | — | 📐 только при измеримой необходимости |
| AI Data Platform (каталог датасетов) | — | — | 🟠 версия + отчёт качества в Toolkit; каталог 📐 |
| AI Evaluation (регрессии моделей) | — | — | 🟠 метрики эксперимента; регрессионные наборы 📐 |
| 8 AI-направлений | — | — | 📐 см. [AI_ROADMAP.md](AI_ROADMAP.md) |

Отклонение от предложенного дерева каталогов: вместо `services/gateway-go` и
`services/orchestrator-go` с отдельными модулями — **один Go-модуль** в корне
(`go.mod`) с бинарниками `services/{gateway,orchestrator,migrate}` и общими
пакетами `internal/`. Причина: общий домен задач и контракты, одна версия
зависимостей, нет дублирования. Каталоги `runtimes/`, `capabilities/` и
Rust/C++ не созданы, потому что в них пока нечего положить (правило «не
создавать пустые каталоги»). См. [ADR-001](DECISIONS/ADR-001-single-go-module.md).

## 3. Жизненный цикл AI-задачи

```
POST /v1/jobs ──▶ queued ──claim──▶ running ──▶ succeeded (+1 result)
                    │                 │  ├────▶ queued   (временная ошибка, backoff с jitter)
                    │                 │  ├────▶ failed   (нарушение контракта / попытки исчерпаны)
                    │                 │  └────▶ cancelled (отмена запрошена во время работы)
                    └──cancel─────────┴───────▶ cancelled
```

- **Идемпотентность создания.** Ключ `(tenant, создатель, Idempotency-Key)` уникален в БД.
  Повтор с тем же телом возвращает ту же задачу (200, `Idempotent-Replayed: true`). Тот же
  ключ с другим телом даёт 409. Тело сравнивается по каноническому SHA-256: порядок ключей
  JSON и пробелы не влияют. ✅
- **Доставка at-least-once.** Воркер арендует задачу (`lease_expires_at`). Если воркер упал,
  после истечения аренды задачу забирает другой воркер. Exactly-once **не** обещается. ✅
- **Нет дублирования результата.** У `ai_job_result` первичный ключ `job_id`, а завершение
  требует владения арендой (`lease_owner`). Поздний или повторный `Complete` получает
  `ErrLeaseLost`, и результат остаётся один. Доказано тестом
  `TestReprocessingNeverDuplicatesResult`. ✅
- **Тайм-ауты.** Свой для каждого типа задачи. Аренда обязана превышать максимальный тайм-аут
  минимум на 30 с, иначе конфигурация не пройдёт валидацию. ✅
- **Повторы.** Экспоненциальная задержка с full jitter (база 2 с, потолок 5 мин), не больше
  `max_attempts` попыток. Нарушение контракта считается постоянной ошибкой и не повторяется. ✅
- **Dead-letter.** Окончательно упавшая задача остаётся в статусе `failed` с `error_code` и
  доступна для разбора. Отдельной DLQ нет: очередь и есть таблица. ✅
- **Backpressure.** Воркер забирает не больше свободных слотов `AI_WORKER_CONCURRENCY`.
  Gateway ограничивает запросы на пару (tenant, subject) и размер тела. ✅
  Лимит на число задач в очереди на tenant не реализован. 📐

## 4. Model Gateway

Бизнес-возможность (job type) называет **задачу модели** (`call_summary`), а не
поставщика. Провайдер выбирается маршрутизацией (`AI_MODEL_PROVIDERS`). Если
ни один включённый провайдер не поддерживает нужную задачу, воркер не стартует.

Каждый выход модели до сохранения проходит:
1. проверку JSON Schema выхода (`contracts/model-schemas/*.json`);
2. проверку привязки к входу: каждая `evidence_quote` обязана дословно
   встречаться в транскрипте, язык выхода совпадает с языком входа.

Не прошедший проверку выход становится постоянной ошибкой
(`output_contract_violation` / `output_not_grounded`) и не сохраняется. ✅

Mock provider детерминированно извлекает предложения из входа и помечает выход
`is_mock: true`. Это **не** AI-анализ. В `AI_ENV=production` mock запрещён
валидацией конфигурации. ✅

## 5. Хранение

Решение: [ADR-002](DECISIONS/ADR-002-single-database-rls.md).

| Данные | Где | Статус |
|---|---|---|
| Задачи, статусы, попытки, ошибки | PostgreSQL `ai_job` | ✅ |
| Результаты (структурированный JSON) | PostgreSQL `ai_job_result` | ✅ |
| Версии схемы | `schema_migrations` | ✅ |
| Аудио, веса моделей, контрольные точки | объектное хранилище (MinIO локально / S3) | 📐 |
| Датасеты, артефакты экспериментов | вне git, `training/toolkit/runs/`; далее объектное хранилище | 🟠 |
| Каталог моделей (registry) | PostgreSQL + объектное хранилище | 📐 |

Большие файлы не передаются в JSON и не хранятся в PostgreSQL. В контракте
`call.summary.v1` передаётся только текст транскрипта (≤ 100 000 символов).

## 6. Технологии: что и почему

| Технология | Где используется | Почему |
|---|---|---|
| Go 1.27 | gateway, orchestrator, migrate | stdlib HTTP, простая конкурентность, статические бинарники в distroless |
| pgx v5 | доступ к PostgreSQL | де-факто стандарт для Go и PostgreSQL |
| golang-jwt v5 | проверка токенов | проверенная библиотека, без самописной криптографии |
| santhosh-tekuri/jsonschema v6 | контракты | JSON Schema 2020-12 |
| x/time/rate | лимиты | официальный пакет Go |
| PostgreSQL 17 | метаданные и очередь | уже используется экосистемой; очередь на `SKIP LOCKED` без нового брокера ([ADR-003](DECISIONS/ADR-003-postgres-job-queue.md)) |
| Python 3.13 + uv, numpy, scikit-learn | Training Toolkit | ML-экосистема; PyTorch добавится, когда появится нейросетевая задача |
| Rust, C++ | — | установлены, но не используются: нет компонента, где они дали бы измеримое преимущество ([ADR-004](DECISIONS/ADR-004-languages-by-need.md)) |

Не добавлены (нет измеримой необходимости): Kafka, Kubernetes, ClickHouse,
RabbitMQ (для AI), gRPC/Protobuf (нет межъязыкового вызова между процессами).
